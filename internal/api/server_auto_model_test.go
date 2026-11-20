package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type autoCapture struct {
	serverResponsesWebsocketCaptureExecutor
	provider string
	mu       sync.Mutex
	attempts []string
}

func (e *autoCapture) Identifier() string { return e.provider }

type autoPluginCapture struct{ *autoCapture }

func (*autoPluginCapture) PluginManagedCredentials() {}
func (e *autoCapture) record(operation string, a *auth.Auth, req coreexecutor.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts = append(e.attempts, operation+"/"+a.ID+"/"+req.Model)
}
func (e *autoCapture) Execute(_ context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.record("chat", a, req)
	return coreexecutor.Response{Payload: fmt.Appendf(nil, `{"model":%q,"choices":[]}`, req.Model)}, nil
}
func (e *autoCapture) CountTokens(_ context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.record("count", a, req)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}
func (e *autoCapture) ExecuteStream(ctx context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	method := "sse"
	if coreexecutor.DownstreamWebsocket(ctx) {
		method = "ws"
	}
	e.record(method, a, req)
	if coreexecutor.WebsocketInputFromContext(ctx) != nil {
		return nil, fmt.Errorf("auto requires per-turn routing")
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-fixture","output":[]}}`)
	if method == "sse" {
		payload = fmt.Appendf(nil, "data: %s\n\n", payload)
	}
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestAutoModelPublicRouting(t *testing.T) {
	const key = "fixture-auto-key"
	cfg := &config.Config{}
	cfg.APIKeys = []string{key}
	cfg.CodexResponseSteering = true
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	reg := registry.GetGlobalRegistry()
	load := func(next *config.Config) {
		manager.SetConfig(next)
		manager.SetOAuthModelAlias(next.OAuthModelAlias)
	}
	load(cfg)
	buddy := &autoCapture{provider: "codebuddy"}
	free := &autoCapture{provider: "codex"}
	manager.RegisterExecutor(&autoPluginCapture{buddy})
	manager.RegisterExecutor(free)
	publish := func(a *auth.Auth, models ...*registry.ModelInfo) {
		t.Helper()
		if _, err := manager.Register(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		reg.RegisterClient(a.ID, a.Provider, models)
		manager.RefreshSchedulerEntry(a.ID)
		t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	}
	buddyAuth := &auth.Auth{ID: "limited-buddy", Provider: "codebuddy", FileName: "buddy.json"}
	auth.SetModelPolicyAttribute(buddyAuth, &auth.AccountModelPolicy{Allowlist: &auth.OAuthModelPolicy{Enabled: true, Models: []string{"hy4flash-free", "deepseek-v4.1-flash"}}})
	publish(buddyAuth, &registry.ModelInfo{ID: "hy4-flash", Created: 100}, &registry.ModelInfo{ID: "deepseek-v4.1-flash", Created: 100})
	catalogOnly := &auth.Auth{ID: "plugin-catalog-native-executor", Provider: "codex", FileName: "catalog.json"}
	auth.SetModelPolicyAttribute(catalogOnly, &auth.AccountModelPolicy{Allowlist: &auth.OAuthModelPolicy{Enabled: true, Models: []string{"catalog-only"}}})
	publish(catalogOnly, &registry.ModelInfo{ID: "catalog-only", Created: 200, PluginCatalog: true})
	freeAuth := &auth.Auth{ID: "free-account", Provider: "codex", FileName: "free.json"}
	publish(freeAuth, &registry.ModelInfo{ID: "free"}, &registry.ModelInfo{ID: "b", Created: 42}, &registry.ModelInfo{ID: "a", Created: 42})
	cfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{"codebuddy": {{Name: "hy4flash-free", Alias: "hy4-flash"}}}
	load(cfg)
	post := func(path, model, extra string) *httptest.ResponseRecorder {
		return serveResourceRequest(server, http.MethodPost, path, "Authorization", "Bearer "+key, fmt.Sprintf(`{"model":%q,%s}`, model, extra))
	}
	chat := func(model string) *httptest.ResponseRecorder {
		return post("/v1/chat/completions", model, `"messages":[{"role":"user","content":"fixture"}]`)
	}
	expect := func(code int, model string) {
		t.Helper()
		r := chat("auto")
		if r.Code != code || model != "" && ojson.GetBytes(r.Body.Bytes(), "model").String() != model {
			t.Fatalf("expected %d %s: %d %s", code, model, r.Code, r.Body)
		}
	}
	r := chat("auto")
	if r.Code != 400 || ojson.GetBytes(r.Body.Bytes(), "error.code").String() != "model_not_found" {
		t.Fatalf("default: %d %s", r.Code, r.Body)
	}
	next := cfg.CloneForRuntime()
	next.Routing.AutoModel.Enabled = true
	next.Routing.AutoModel.Candidates = []string{"hy4-flash", "free", "a"}
	load(next)
	expect(200, "free")
	next.Routing.AutoModel.Candidates = []string{"hy4-flash", "absent"}
	load(next)
	expect(503, "")
	next.Routing.AutoModel.Candidates = nil
	load(next)
	expect(200, "a")
	if len(buddy.attempts) != 0 {
		t.Fatalf("restricted account used for auto: %v", buddy.attempts)
	}
	if r := chat("hy4-flash"); r.Code != 200 || ojson.GetBytes(r.Body.Bytes(), "model").String() != "hy4flash-free" {
		t.Fatalf("direct alias: %d %s", r.Code, r.Body)
	}

	// A native auto loses to configured candidates. A configured alias owns all four entry points.
	other := &auth.Auth{ID: "native-account", Provider: "codebuddy", FileName: "native.json"}
	publish(other, &registry.ModelInfo{ID: "auto"})
	next.Routing.AutoModel.Candidates = []string{"free"}
	load(next)
	expect(200, "free")
	next.Routing.AutoModel.Enabled = false
	load(next)
	expect(200, "auto")
	// This other account inherits the same channel config but has only native auto.
	publish(&auth.Auth{ID: "native-on-same-channel", Provider: "codex", Attributes: map[string]string{"priority": "100"}}, &registry.ModelInfo{ID: "auto"})
	next.OAuthModelAlias["codex"] = []config.OAuthModelAlias{{Name: "real", Alias: "auto"}}
	next.Routing.AutoModel.Enabled = true
	next.Routing.AutoModel.Candidates = []string{"unusable"}
	reg.RegisterClient(freeAuth.ID, freeAuth.Provider, []*registry.ModelInfo{{ID: "auto", ConfiguredAlias: true, MetadataModelID: "real"}})
	manager.RefreshSchedulerEntry(freeAuth.ID)
	load(next)
	expect(200, "real")
	if r := chat("auto(high)"); r.Code != 200 || ojson.GetBytes(r.Body.Bytes(), "model").String() != "real(high)" {
		t.Fatalf("suffix: %d %s", r.Code, r.Body)
	}
	if r := post("/v1/messages/count_tokens", "auto", `"messages":[{"role":"user","content":"fixture"}]`); r.Code != 200 {
		t.Fatalf("count: %d %s", r.Code, r.Body)
	}
	if r := post("/v1/responses", "auto", `"input":"fixture","stream":true`); r.Code != 200 {
		t.Fatalf("SSE: %d %s", r.Code, r.Body)
	}
	var active sync.WaitGroup
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Done()
		server.Handler().ServeHTTP(w, r)
	}))
	defer func() { httpServer.Close(); active.Wait() }()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + key}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	send := func() []byte {
		t.Helper()
		if err := ws.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"auto","input":[{"type":"message","role":"user","content":"fixture"}]}`)); err != nil {
			t.Fatal(err)
		}
		_, reply, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if reply := send(); ojson.GetBytes(reply, "type").String() != "response.completed" {
		t.Fatalf("WS: %s", reply)
	}
	free.mu.Lock()
	got := strings.Join(free.attempts, ",")
	free.mu.Unlock()
	for _, want := range []string{"chat/free-account/real", "count/free-account/real", "sse/free-account/real", "ws/free-account/real"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	private := next.CloneForRuntime()
	private.ResourceAllowlists = &config.ResourceAllowlists{AuthFiles: map[string][]string{"free.json": {}}}
	load(private)
	if r := chat("auto"); r.Code != http.StatusForbidden {
		t.Fatalf("unowned alias fell back: %d %s", r.Code, r.Body)
	}
	load(next)
	reg.UnregisterClient(freeAuth.ID)
	if reply := send(); ojson.GetBytes(reply, "type").String() != "error" {
		t.Fatalf("unavailable alias used another WS account: %s", reply)
	}
	expect(503, "")
	buddy.mu.Lock()
	defer buddy.mu.Unlock()
	if len(buddy.attempts) != 2 {
		t.Fatalf("unexpected CodeBuddy dispatches: %v", buddy.attempts)
	}
}

func TestAutoModelHomePassThrough(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Routing.AutoModel = config.AutoModelConfig{Enabled: true, Candidates: []string{"absent"}}
	m := auth.NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	ctx := t.Context()
	routed, model, err := m.ResolveAutoRoute(ctx, "auto(high)", nil)
	if err != nil || routed != ctx || model != "auto(high)" {
		t.Fatalf("Home route = %s, %v", model, err)
	}
}
