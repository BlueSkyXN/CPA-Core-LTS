package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type multiAccountTransport struct {
	mu         sync.Mutex
	transports map[string]*v2Transport
}

func (m *multiAccountTransport) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	m.mu.Lock()
	defer m.mu.Unlock()
	if auth != nil {
		key, _ := auth.Metadata["api_key"].(string)
		if storage, ok := auth.Storage.(interface{ RawJSON() []byte }); ok {
			key = gjson.GetBytes(storage.RawJSON(), "api_key").String()
		}
		if transport := m.transports[key]; transport != nil {
			return transport
		}
	}
	return rejectedAccountTransport{}
}

type rejectedAccountTransport struct{}

func (rejectedAccountTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unregistered synthetic credential; network access blocked")
}

func (m *multiAccountTransport) add(t *testing.T, key string) *v2Transport {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	transport := &v2Transport{fixtureTransport: &fixtureTransport{pub: pub, priv: priv, apiKey: key}}
	m.mu.Lock()
	m.transports[key] = transport
	m.mu.Unlock()
	return transport
}

func newMultiAccountFixture(t *testing.T) (*v2Fixture, *multiAccountTransport) {
	t.Helper()
	f := newV2Fixture(t, "glm-5.3", "glm-5.3-flash")
	transports := &multiAccountTransport{transports: map[string]*v2Transport{"synthetic-key.synthetic-secret": f.transport}}
	f.manager.SetRoundTripperProvider(transports)
	mgmt := f.management
	mgmt.SetPostAuthPersistHook(func(ctx context.Context, auth *coreauth.Auth) error {
		if auth.Disabled {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			f.manager.RefreshSchedulerEntry(auth.ID)
			return nil
		}
		models := f.host.ModelsForAuth(ctx, auth)
		if models.Err != nil {
			return models.Err
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models.Models)
		f.manager.RefreshSchedulerEntry(auth.ID)
		return nil
	})
	f.router.PATCH("/v0/management/auth-files/status", mgmt.PatchAuthFileStatus)
	f.router.GET("/v0/management/auth-files/models", mgmt.GetAuthFileModels)
	saveMultiAccount(t, f, "synthetic.json", "synthetic-key.synthetic-secret", "synthetic-device")
	return f, transports
}

func saveMultiAccount(t *testing.T, f *v2Fixture, name, key, device string) *coreauth.Auth {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "api_key": key, "device_id": device, "request_retry": 0})
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name="+name, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("account upload status = %d", rec.Code)
	}
	auth, ok := f.manager.GetByID(name)
	if !ok {
		t.Fatal("uploaded account missing from Core")
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(name) })
	return auth
}

func multiAccountOptions(request, authID string, stream bool) coreexecutor.Options {
	metadata := map[string]any{coreexecutor.RequestIDMetadataKey: request, coreexecutor.ExecutionSessionMetadataKey: "shared-conversation", coreexecutor.CallerScopeMetadataKey: "shared-caller", coreexecutor.WorkspaceIdentityMetadataKey: "shared-workspace"}
	if authID != "" {
		metadata[coreexecutor.PinnedAuthMetadataKey] = authID
	}
	return coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatClaude, Metadata: metadata}
}

func multiAccountRequest(stream bool) coreexecutor.Request {
	return coreexecutor.Request{Model: "glm-5.3", Payload: []byte(fmt.Sprintf(`{"model":"glm-5.3","max_tokens":100,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream))}
}

func takeAccountUsage(t *testing.T, f *v2Fixture) coreusage.Record {
	t.Helper()
	select {
	case record := <-f.usage.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("account usage was not published")
		return coreusage.Record{}
	}
}

func TestMultiAccountManagementModelsSchedulingAndUsage(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f, transports := newMultiAccountFixture(t)
	secondTransport := transports.add(t, "second-key.second-secret")
	second := saveMultiAccount(t, f, "second.json", "second-key.second-secret", "second-device")
	for _, name := range []string{"synthetic.json", "second.json"} {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+name, nil))
		if rec.Code != http.StatusOK || gjson.GetBytes(rec.Body.Bytes(), "models.#").Int() != 2 {
			t.Fatalf("account %s has no independently registered models", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.manager.SetSelector(&coreauth.RoundRobinSelector{})
	seen := map[string]int{}
	for i := range 6 {
		result, err := f.manager.Execute(ctx, []string{"zcode-coding-plan"}, multiAccountRequest(false), multiAccountOptions(fmt.Sprint(i), "", false))
		if err != nil || !strings.Contains(string(result.Payload), "fixture-ok") {
			t.Fatal("multi-account scheduling failed", err)
		}
		record := takeAccountUsage(t, f)
		if record.Failed || record.Detail.TotalTokens != 12 {
			t.Fatal("usage tokens or outcome mismatch")
		}
		seen[record.AuthIndex]++
	}
	if seen["v2-fixture-index"] != 3 || seen[second.Index] != 3 {
		t.Fatal("Core did not distribute calls or attribute usage to both accounts")
	}
	for _, item := range []struct {
		transport *v2Transport
		device    string
	}{{f.transport, "synthetic-device"}, {secondTransport, "second-device"}} {
		item.transport.lock.Lock()
		if item.transport.handshakes != 1 || len(item.transport.captured) != 3 {
			t.Error("account did not reuse its own signer")
		}
		for _, body := range item.transport.captured {
			if !strings.Contains(gjson.GetBytes(body, "metadata.user_id").String(), item.device) {
				t.Error("device identity crossed accounts")
			}
		}
		item.transport.lock.Unlock()
	}
	if f.transport.headers[0].Get("X-Session-Id") == secondTransport.headers[0].Get("X-Session-Id") {
		t.Fatal("upstream session identity crossed accounts")
	}

	rec := httptest.NewRecorder()
	body := `{"name":"synthetic.json","disabled":true}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal("account disable failed")
	}
	for i := range 2 {
		if _, err := f.manager.Execute(ctx, []string{"zcode-coding-plan"}, multiAccountRequest(false), multiAccountOptions(fmt.Sprintf("disabled-%d", i), "", false)); err != nil {
			t.Fatal("disabled peer blocked the enabled account", err)
		}
		if record := takeAccountUsage(t, f); record.Failed || record.AuthIndex != second.Index {
			t.Fatal("disabled account was selected or usage misattributed")
		}
	}

	rotatedTransport := transports.add(t, "rotated-key.rotated-secret")
	rotated := saveMultiAccount(t, f, "second.json", "rotated-key.rotated-secret", "rotated-device")
	if rotated.Index != second.Index {
		t.Fatal("same-account rotation changed statistics identity")
	}
	if _, err := f.manager.Execute(ctx, []string{"zcode-coding-plan"}, multiAccountRequest(false), multiAccountOptions("rotated", "second.json", false)); err != nil {
		t.Fatal("rotated account did not execute", err)
	}
	if record := takeAccountUsage(t, f); record.Failed || record.AuthIndex != second.Index {
		t.Fatal("rotation changed usage attribution")
	}
	if rotatedTransport.handshakes != 1 || !strings.Contains(gjson.GetBytes(rotatedTransport.captured[0], "metadata.user_id").String(), "rotated-device") {
		t.Fatal("rotation did not replace the signer or device identity")
	}
}

func TestMultiAccountDynamicTenInflightAndRemoval(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f, transports := newMultiAccountFixture(t)
	secondTransport := transports.add(t, "second-key.second-secret")
	second := saveMultiAccount(t, f, "second.json", "second-key.second-secret", "second-device")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executor, ok := f.manager.Executor("zcode-coding-plan")
	if !ok {
		t.Fatal("plugin executor missing")
	}
	first, ok := f.manager.GetByID("synthetic.json")
	if !ok {
		t.Fatal("first account missing")
	}
	blocked := &accountBlockingTransport{delegate: transports, bodies: map[string][]*accountBlockingBody{}}
	f.manager.SetRoundTripperProvider(blocked)
	var drains []<-chan error
	for _, auth := range []*coreauth.Auth{first, second} {
		for i := range 10 {
			opts := multiAccountOptions(fmt.Sprintf("%s-%d", auth.ID, i), auth.ID, true)
			callCtx := context.WithValue(ctx, "cliproxy.roundtripper", blocked.RoundTripperFor(auth))
			result, err := executor.ExecuteStream(callCtx, auth, multiAccountRequest(true), opts)
			if err != nil {
				t.Fatalf("account %s request %d was rejected: %v", auth.ID, i+1, err)
			}
			done := make(chan error, 1)
			go func() {
				var failure error
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						failure = chunk.Err
					}
				}
				done <- failure
			}()
			drains = append(drains, done)
		}
		callCtx := context.WithValue(ctx, "cliproxy.roundtripper", blocked.RoundTripperFor(auth))
		if _, err := executor.ExecuteStream(callCtx, auth, multiAccountRequest(true), multiAccountOptions("overflow-"+auth.ID, auth.ID, true)); err == nil {
			t.Fatal("eleventh in-flight request was admitted")
		}
	}
	blocked.mu.Lock()
	firstBodies := append([]*accountBlockingBody(nil), blocked.bodies[first.ID]...)
	secondBodies := append([]*accountBlockingBody(nil), blocked.bodies[second.ID]...)
	blocked.mu.Unlock()
	if len(firstBodies) != 10 || len(secondBodies) != 10 {
		t.Fatal("per-account concurrency limits were not enforced before upstream dispatch")
	}
	for _, body := range append(firstBodies, secondBodies...) {
		select {
		case <-body.started:
		case <-ctx.Done():
			t.Fatal("account stream was not consumed")
		}
	}
	for _, opts := range []pluginapi.CancelExecutionRequest{
		{RequestID: first.ID + "-0", AuthID: second.ID, AuthIndex: second.Index},
		{RequestID: first.ID + "-0", AuthID: first.ID, CallerScope: "wrong"},
	} {
		if err := f.host.CancelProviderExecution(ctx, "zcode-coding-plan", opts); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range firstBodies {
		select {
		case <-body.closed:
			t.Fatal("foreign identity canceled a request")
		default:
		}
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=synthetic.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("active account removal failed: HTTP %d", rec.Code)
	}
	registry.GetGlobalRegistry().UnregisterClient(first.ID)
	for i, body := range firstBodies {
		select {
		case <-body.closed:
		case <-ctx.Done():
			t.Fatal("removed account's upstream stream was not closed")
		}
		select {
		case <-drains[i]:
		case <-ctx.Done():
			t.Fatal("removed account's execution did not finish")
		}
	}
	for _, body := range secondBodies {
		select {
		case <-body.closed:
			t.Fatal("removing account A closed account B")
		default:
		}
	}
	// Re-add the same Core identity while the peer still has ten active requests.
	transports.add(t, "replacement-key.replacement-secret")
	replacement := saveMultiAccount(t, f, "synthetic.json", "replacement-key.replacement-secret", "replacement-device")
	f.manager.SetSelector(&coreauth.FillFirstSelector{})
	if _, err := f.manager.Execute(ctx, []string{"zcode-coding-plan"}, multiAccountRequest(false), multiAccountOptions("after-removal", "", false)); err != nil {
		t.Fatal("full peer or stale removal state blocked the replacement account", err)
	}
	for _, body := range secondBodies {
		body.releaseOnce.Do(func() { close(body.release) })
	}
	for _, done := range drains[10:] {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("peer stream failed after another account's deletion", err)
			}
		case <-ctx.Done():
			t.Fatal("peer stream did not finish")
		}
	}
	counts := map[string]int{}
	failures := 0
	for range 21 {
		record := takeAccountUsage(t, f)
		counts[record.AuthIndex]++
		if record.Failed {
			failures++
		}
	}
	expected := map[string]int{}
	expected[first.Index] += 10
	expected[second.Index] += 10
	expected[replacement.Index]++
	if len(counts) != len(expected) || failures != 10 {
		t.Fatal("multi-account stream usage or failure attribution mismatch")
	}
	for index, count := range expected {
		if counts[index] != count {
			t.Fatal("multi-account usage was attributed to the wrong credential")
		}
	}
	secondTransport.lock.Lock()
	defer secondTransport.lock.Unlock()
	if secondTransport.handshakes != 1 {
		t.Fatal("concurrent account requests did not share their own handshake cache")
	}
}

type accountBlockingTransport struct {
	mu       sync.Mutex
	delegate *multiAccountTransport
	bodies   map[string][]*accountBlockingBody
}

type accountBoundTransport struct {
	parent   *accountBlockingTransport
	authID   string
	delegate http.RoundTripper
}

func (b *accountBlockingTransport) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	return &accountBoundTransport{parent: b, authID: auth.ID, delegate: b.delegate.RoundTripperFor(auth)}
}

func (b *accountBoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := b.delegate.RoundTrip(req)
	if err != nil || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return response, err
	}
	body := &accountBlockingBody{ctx: req.Context(), source: response.Body, started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	response.Body = body
	b.parent.mu.Lock()
	b.parent.bodies[b.authID] = append(b.parent.bodies[b.authID], body)
	b.parent.mu.Unlock()
	return response, nil
}

type accountBlockingBody struct {
	ctx                               context.Context
	source                            io.ReadCloser
	started, release, closed          chan struct{}
	startOnce, releaseOnce, closeOnce sync.Once
}

func (b *accountBlockingBody) Read(out []byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.closed:
		return 0, context.Canceled
	case <-b.release:
		return b.source.Read(out)
	}
}

func (b *accountBlockingBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return b.source.Close()
}
