package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	resourceOwner = "example-resource-owner"
	resourceGuest = "example-resource-guest"
)

type resourceAccessExecutor struct {
	serverResponsesWebsocketCaptureExecutor
	failPublic    bool
	publicCalls   atomic.Int32
	reservedCalls atomic.Int32
}

func (e *resourceAccessExecutor) Execute(_ context.Context, selected *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if selected.ID == "resource-public.json" {
		e.publicCalls.Add(1)
		if e.failPublic {
			return coreexecutor.Response{}, &auth.Error{Code: "fixture_unavailable", HTTPStatus: http.StatusBadGateway}
		}
	} else {
		e.reservedCalls.Add(1)
	}
	return coreexecutor.Response{Payload: fmt.Appendf(nil, `{"credential":%q,"model":%q,"choices":[]}`, selected.ID, req.Model)}, nil
}

func (e *resourceAccessExecutor) ExecuteStream(ctx context.Context, _ *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if coreexecutor.WebsocketInputFromContext(ctx) != nil {
		return nil, fmt.Errorf("resource rules must authorize each WebSocket turn through Core")
	}
	n := e.calls.Add(1)
	payload := fmt.Appendf(nil, `{"type":"response.completed","response":{"id":"resp-%d","output":[]}}`, n)
	if !coreexecutor.DownstreamWebsocket(ctx) {
		payload = fmt.Appendf(nil, "data: %s\n\n", payload)
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func newResourceAccessServer(t *testing.T) (*Server, *config.Config, *resourceAccessExecutor) {
	t.Helper()
	cfg := &config.Config{
		ResourceAllowlists: &config.ResourceAllowlists{
			AuthFiles: map[string][]string{"resource-team.json": {coresession.CallerScope(resourceOwner)}},
			Models:    map[string][]string{"resource-upstream-private": {coresession.CallerScope(resourceOwner)}},
		},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {{Name: "resource-upstream-private", Alias: "resource-private-alias", Fork: true}},
		},
	}
	cfg.APIKeys = []string{resourceOwner, resourceGuest}
	cfg.CodexResponseSteering = true
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	executor := &resourceAccessExecutor{}
	manager.RegisterExecutor(executor)

	candidates := []struct {
		auth   *auth.Auth
		models []*registry.ModelInfo
	}{
		{
			auth: &auth.Auth{ID: "resource-virtual", Provider: "codex", Attributes: map[string]string{
				auth.AttributeVirtualSource: "/fixture/resource-team.json", "priority": "10",
			}, Metadata: map[string]any{"access_token": "fixture"}},
			models: []*registry.ModelInfo{{ID: "resource-shared"}, {ID: "resource-only-team", Created: 30}},
		},
		{
			auth: &auth.Auth{ID: "resource-public.json", Provider: "codex", Prefix: "fixture", Metadata: map[string]any{"access_token": "fixture"}},
			models: []*registry.ModelInfo{
				{ID: "resource-shared", Created: 10},
				{ID: "resource-upstream-private", Created: 20},
				{ID: "resource-private-alias", Created: 20},
				{ID: "fixture/resource-private-alias", Created: 20},
			},
		},
	}
	models := registry.GetGlobalRegistry()
	for _, row := range candidates {
		if _, err := manager.Register(context.Background(), row.auth); err != nil {
			t.Fatal(err)
		}
		models.RegisterClient(row.auth.ID, row.auth.Provider, row.models)
		t.Cleanup(func() { models.UnregisterClient(row.auth.ID) })
	}
	return server, cfg, executor
}

func serveResourceRequest(server *Server, method, path, header, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set(header, key)
	}
	reply := httptest.NewRecorder()
	server.engine.ServeHTTP(reply, req)
	return reply
}

func TestNativeResourceAccessHTTP(t *testing.T) {
	server, _, executor := newResourceAccessServer(t)
	modelsRequest := func(key string) *httptest.ResponseRecorder {
		return serveResourceRequest(server, http.MethodGet, "/v1/models", "Authorization", "Bearer "+key, "")
	}
	guestModels := modelsRequest(resourceGuest)
	ownerModels := modelsRequest(resourceOwner)
	if guestModels.Code != 200 || ownerModels.Code != 200 || guestModels.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("model lists guest=%d owner=%d headers=%v", guestModels.Code, ownerModels.Code, guestModels.Header())
	}
	for _, model := range []string{"resource-only-team", "resource-upstream-private", "resource-private-alias", "fixture/resource-private-alias"} {
		if strings.Contains(guestModels.Body.String(), model) || !strings.Contains(ownerModels.Body.String(), model) {
			t.Errorf("wrong visibility for %s, guest=%s owner=%s", model, guestModels.Body, ownerModels.Body)
		}
	}
	if !strings.Contains(guestModels.Body.String(), "resource-shared") {
		t.Fatal("shared model was hidden from the guest")
	}
	detail := serveResourceRequest(server, http.MethodGet, "/v1/models/fixture/resource-private-alias", "Authorization", "Bearer "+resourceGuest, "")
	if detail.Code != 404 {
		t.Fatalf("restricted model detail returned %d: %s", detail.Code, detail.Body)
	}

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
		return pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders, Body: []byte(`{"data":[{"id":"resource-upstream-private"},{"id":"resource-shared"}]}`)}
	}})
	if reply := modelsRequest(resourceGuest); reply.Code != 200 || strings.Contains(reply.Body.String(), "resource-upstream-private") {
		t.Fatalf("intercepted model list exposed a restricted entry: %d %s", reply.Code, reply.Body)
	}
	server.handlers.SetPluginHost(nil)

	for _, tc := range []struct {
		name, path, header, key, model, credential string
		status                                     int
	}{
		{"owner-routes-to-team", "/v1/chat/completions", "Authorization", "Bearer " + resourceOwner, "resource-shared", "resource-virtual", 200},
		{"guest-routes-to-public", "/v1/chat/completions", "X-Api-Key", resourceGuest, "resource-shared", "resource-public.json", 200},
		{"auto-skips-restricted", "/v1/chat/completions", "X-Api-Key", resourceGuest, "auto", "resource-public.json", 200},
		{"owner-via-query", "/v1/chat/completions?key=" + resourceOwner, "", "", "fixture/resource-private-alias", "resource-public.json", 200},
		{"guest-model-denied", "/v1/chat/completions", "Authorization", "Bearer " + resourceGuest, "RESOURCE-UPSTREAM-PRIVATE(high)", "", 403},
		{"guest-alias-denied", "/v1/chat/completions", "Authorization", "Bearer " + resourceGuest, "fixture/resource-private-alias", "", 503},
		{"guest-casefolded-prefix-denied", "/v1/chat/completions", "Authorization", "Bearer " + resourceGuest, "FIXTURE/resource-private-alias", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"fixture"}]}`, tc.model)
			reply := serveResourceRequest(server, http.MethodPost, tc.path, tc.header, tc.key, body)
			if reply.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", reply.Code, tc.status, reply.Body)
			}
			if got := ojson.GetBytes(reply.Body.Bytes(), "credential").String(); got != tc.credential {
				t.Fatalf("credential = %q, want %q: %s", got, tc.credential, reply.Body)
			}
		})
	}
	deniedStream := serveResourceRequest(server, http.MethodPost, "/v1/responses", "Authorization", "Bearer "+resourceGuest, `{"model":"resource-upstream-private","input":"fixture","stream":true}`)
	if deniedStream.Code != 403 || executor.calls.Load() != 0 || !strings.Contains(deniedStream.Body.String(), "resource_access_denied") {
		t.Fatalf("denied stream status=%d upstream attempts=%d: %s", deniedStream.Code, executor.calls.Load(), deniedStream.Body)
	}

	allowedStream := serveResourceRequest(server, http.MethodPost, "/v1/responses", "Authorization", "Bearer "+resourceOwner, `{"model":"resource-upstream-private","input":"fixture","stream":true}`)
	if allowedStream.Code != 200 || executor.calls.Load() != 1 || !strings.Contains(allowedStream.Body.String(), "response.completed") {
		t.Fatalf("allowed stream status=%d upstream attempts=%d: %s", allowedStream.Code, executor.calls.Load(), allowedStream.Body)
	}
}

func TestNativeResourceAccessDoesNotRetryOnReservedCredential(t *testing.T) {
	server, _, executor := newResourceAccessServer(t)
	executor.failPublic = true
	body := `{"model":"resource-shared","messages":[{"role":"user","content":"fixture"}]}`
	reply := serveResourceRequest(server, http.MethodPost, "/v1/chat/completions", "Authorization", "Bearer "+resourceGuest, body)
	if reply.Code < 400 || executor.publicCalls.Load() != 1 || executor.reservedCalls.Load() != 0 {
		t.Fatalf("status = %d, public calls = %d, reserved calls = %d", reply.Code, executor.publicCalls.Load(), executor.reservedCalls.Load())
	}
}

func TestNativeResourceAccessUpstreamAPIKeyRotation(t *testing.T) {
	const partner = "example-resource-partner"
	cfg, err := config.ParseConfigBytes([]byte(fmt.Sprintf(`
access:
  api-keys: [%q, %q, %q]
  resource-allowlists:
    upstream-api-keys:
      codex-team: [%q, %q]
api-keys:
  codex:
    - name: fixture
      base-url: https://example.invalid
      keys:
        - api-key: example-test-upstream-shared
        - api-key: example-test-upstream-private
          credential-id: codex-team
          priority: 10
`, resourceOwner, partner, resourceGuest, coresession.CallerScope(resourceOwner), coresession.CallerScope(partner))))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.RegisterExecutor(&resourceAccessExecutor{})
	registered := make(map[string]bool)
	register := func(current *config.Config) []*auth.Auth {
		t.Helper()
		auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
			Config: current, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
		})
		if err != nil || len(auths) != 2 {
			t.Fatalf("synthesis returned %d credentials, error = %v", len(auths), err)
		}
		for _, credential := range auths {
			if registered[credential.ID] {
				continue
			}
			registered[credential.ID] = true
			if _, err := manager.Register(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "resource-config-shared"}})
			manager.RefreshSchedulerEntry(credential.ID)
			t.Cleanup(func() { reg.UnregisterClient(credential.ID) })
		}
		return auths
	}
	request := func(key, wantID string) {
		t.Helper()
		body := `{"model":"resource-config-shared","messages":[{"role":"user","content":"fixture"}]}`
		reply := serveResourceRequest(server, http.MethodPost, "/v1/chat/completions", "Authorization", "Bearer "+key, body)
		if got := ojson.GetBytes(reply.Body.Bytes(), "credential").String(); reply.Code != 200 || got != wantID {
			t.Fatalf("expected selected auth %q, got %d %s", wantID, reply.Code, reply.Body)
		}
	}

	manager.SetConfig(cfg)
	before := register(cfg)
	request(resourceOwner, before[1].ID)
	request(partner, before[1].ID)
	request(resourceGuest, before[0].ID)

	rotated := cfg.CloneForRuntime()
	rotated.CodexKey[1].APIKey = "example-test-upstream-rotated"
	rotated.CodexKey[0], rotated.CodexKey[1] = rotated.CodexKey[1], rotated.CodexKey[0]
	manager.SetConfig(rotated)
	// The policy updates before the watcher retires the old credential.
	ownerCtx := coreusage.WithCapturedIdentity(context.Background(), resourceOwner, "config-inline")
	if err := manager.CheckSelectedResourceAccess(ownerCtx, before[1], "resource-config-shared"); !errors.Is(err, auth.ErrResourceAccessDenied) {
		t.Fatalf("stale credential during rotation: %v", err)
	}
	after := register(rotated)
	request(resourceOwner, after[0].ID)
	request(partner, after[0].ID)
	request(resourceGuest, before[0].ID)

	removed := rotated.CloneForRuntime()
	removed.CodexKey = removed.CodexKey[1:]
	removed.ResourceAllowlists = nil
	manager.SetConfig(removed)
	// Both retired credentials remain registered until the watcher removes them.
	if err := manager.CheckSelectedResourceAccess(ownerCtx, after[0], "resource-config-shared"); !errors.Is(err, auth.ErrResourceAccessDenied) {
		t.Fatalf("stale credential when removing the final rule: %v", err)
	}
	request(resourceGuest, before[0].ID)
}

func TestNativeResourceAccessResponsesWebsocketReload(t *testing.T) {
	server, cfg, executor := newResourceAccessServer(t)
	var handlersDone sync.WaitGroup
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlersDone.Add(1)
		defer handlersDone.Done()
		server.engine.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		httpServer.Close()
		handlersDone.Wait()
	})
	endpoint := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, http.Header{"Authorization": {"Bearer " + resourceOwner}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	sendTurn := func() []byte {
		t.Helper()
		payload := []byte(`{"type":"response.create","model":"resource-upstream-private","input":[{"type":"message","role":"user","content":"fixture"}]}`)
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
		_, reply, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if first := sendTurn(); ojson.GetBytes(first, "type").String() != "response.completed" {
		t.Fatalf("first turn = %s", first)
	}
	changed := cfg.CloneForRuntime()
	changed.ResourceAllowlists.Models["resource-upstream-private"] = []string{coresession.CallerScope(resourceGuest)}
	server.handlers.AuthManager.SetConfig(changed)
	if second := sendTurn(); ojson.GetBytes(second, "status").Int() != 403 || executor.calls.Load() != 1 {
		t.Fatalf("second turn = %s, attempts = %d", second, executor.calls.Load())
	}
}
