package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type reloadBlockingTransport struct {
	base      *v2Transport
	handshake bool
	body      *accountBlockingBody
	mu        sync.Mutex
	blocked   bool
}

func (b *reloadBlockingTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return b }
func (b *reloadBlockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := b.base.RoundTrip(req)
	if err != nil || strings.HasSuffix(req.URL.Path, "/client") != b.handshake {
		return response, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.blocked {
		b.blocked = true
		b.body.ctx, b.body.source = req.Context(), response.Body
		response.Body = b.body
	}
	return response, nil
}

func TestV2UnrelatedConfigReloadPreservesActiveExecution(t *testing.T) {
	for _, handshake := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("handshake=%t/stream=%t", handshake, stream), func(t *testing.T) {
				if isolateDynamic(t) {
					return
				}
				f := newV2Fixture(t)
				body := &accountBlockingBody{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
				f.manager.SetRoundTripperProvider(&reloadBlockingTransport{base: f.transport, handshake: handshake, body: body})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				done := make(chan struct{})
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("request did not finish during cleanup")
					}
				})
				payload := fmt.Sprintf(`{"model":%q,"input":"hello","stream":%t}`, v2Model, stream)
				response := httptest.NewRecorder()
				go func() {
					defer close(done)
					req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload)).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					f.router.ServeHTTP(response, req)
				}()
				select {
				case <-body.started:
				case <-ctx.Done():
					t.Fatal("upstream fixture was not consumed")
				}
				cfg := f.cfg.CloneForRuntime()
				cfg.Debug = !cfg.Debug
				cfg.Routing.Strategy = "round-robin"
				for range 2 {
					f.host.ApplyConfig(ctx, cfg)
					f.host.RegisterExecutors(f.manager, nil)
					if !f.host.PluginRegistered("zcode-coding-plan") || !f.host.HasAuthProvider("zcode-coding-plan") {
						t.Fatal("unrelated config reload removed plugin registration")
					}
					if _, ok := f.manager.Executor("zcode-coding-plan"); !ok {
						t.Fatal("unrelated config reload removed executor")
					}
					select {
					case <-body.closed:
						t.Fatal("unrelated config reload closed upstream")
					case <-done:
						t.Fatal("unrelated config reload terminated active execution")
					default:
					}
				}
				body.releaseOnce.Do(func() { close(body.release) })
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("execution did not finish after upstream release")
				}
				checkResponse := func(response *httptest.ResponseRecorder) {
					t.Helper()
					text := response.Body.String()
					if response.Code != http.StatusOK || !strings.Contains(text, "fixture-ok") || strings.Contains(text, "response.failed") {
						t.Fatalf("execution failed: HTTP %d %s", response.Code, text)
					}
					if stream && !strings.Contains(text, "response.completed") {
						t.Fatal("stream did not reach a successful terminal event")
					}
				}
				checkResponse(response)
				f.checkUsage(t, false)
				checkResponse(f.post(t, payload))
				f.checkUsage(t, false)
				select {
				case <-f.usage.records:
					t.Fatal("reload published duplicate usage")
				default:
				}
				f.transport.mu.Lock()
				handshakes := f.transport.handshakes
				f.transport.mu.Unlock()
				f.transport.lock.Lock()
				modelCalls := len(f.transport.captured)
				f.transport.lock.Unlock()
				if handshakes != 1 || modelCalls != 2 {
					t.Fatalf("reload caused extra upstream calls: handshakes=%d models=%d", handshakes, modelCalls)
				}
			})
		}
	}
}
