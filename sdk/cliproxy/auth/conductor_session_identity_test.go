package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type sessionIdentityTestExecutor struct {
	customStreamMockExecutor
	refreshCalls int
}

func (e *sessionIdentityTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls++
	updated := auth.Clone()
	updated.Metadata["access_token"] = "refreshed-test-token"
	return updated, nil
}

func TestManagerSessionDispatchIdentity(t *testing.T) {
	for strategy, factory := range map[string]func() Selector{
		"round-robin":          func() Selector { return &RoundRobinSelector{} },
		"weighted-round-robin": func() Selector { return &WeightedRoundRobinSelector{} },
		"fill-first":           func() Selector { return &FillFirstSelector{} },
	} {
		for _, path := range []string{"execute", "count", "stream"} {
			for _, mode := range []string{"first-attempt", "unauthorized-refresh"} {
				t.Run(strategy+"/"+path+"/"+mode, func(t *testing.T) {
					selector := NewSessionAffinitySelector(factory())
					t.Cleanup(selector.Stop)
					manager := NewManager(nil, selector, nil)
					manager.SetRetryConfig(0, 0, 0)
					const model = "session-identity-model"
					id := "session-" + strategy + "-" + path
					registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive, Attributes: map[string]string{AttributeWeight: "1"}, Metadata: map[string]any{"access_token": "test-token", "refresh_token": "test-refresh-token"}}); err != nil {
						t.Fatal(err)
					}
					calls := 0
					check := func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						calls++
						actual := logging.GetClientRequestMetadata(ctx).SessionID
						expected, _ := opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
						if !strings.HasPrefix(expected, "lcp:") {
							t.Fatal("test did not enter LCP binding path")
						}
						if actual != expected {
							t.Errorf("context session=%q; execution metadata session=%q", actual, expected)
						}
						parent, _ := opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string)
						if got := logging.GetClientRequestMetadata(ctx).ParentSessionID; got != parent {
							t.Errorf("context parent=%q; execution parent=%q", got, parent)
						}
						if mode == "unauthorized-refresh" && calls == 1 {
							return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "test token expired"}
						}
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
					}
					executor := &sessionIdentityTestExecutor{customStreamMockExecutor: customStreamMockExecutor{identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: check, countFn: check}, streamFn: func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						resp, err := check(ctx, a, r, o)
						if err != nil {
							return nil, err
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, err
					}}}
					manager.RegisterExecutor(executor)
					payload := []byte(`{"messages":[{"role":"system","content":"synthetic session context"},{"role":"user","content":"hello"}]}`)
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "session-caller"}}
					var err error
					switch path {
					case "execute":
						_, err = manager.Execute(context.Background(), []string{"codex"}, req, opts)
					case "count":
						_, err = manager.ExecuteCount(context.Background(), []string{"codex"}, req, opts)
					case "stream":
						opts.Stream = true
						var result *cliproxyexecutor.StreamResult
						result, err = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
						if err == nil {
							for range result.Chunks {
							}
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					wantCalls, wantRefreshes := 1, 0
					if mode == "unauthorized-refresh" {
						wantCalls, wantRefreshes = 2, 1
					}
					if calls != wantCalls || executor.refreshCalls != wantRefreshes {
						t.Fatalf("calls=%d, refreshes=%d; want %d, %d", calls, executor.refreshCalls, wantCalls, wantRefreshes)
					}
				})
			}
		}
	}
}
