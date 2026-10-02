package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	coreapi "github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// TestOAuthDaybreakBlueVirtualAlias locks the documented Daybreak Blue model-name
// recipe: an OAuth codex account serving a `<base>-blue` fork alias plus a
// requested-scope payload rule that injects access_programs.cyber=daybreak_blue.
// The virtual name must reach the upstream as the base model with the program
// injected, while the plain base model stays untouched.
func TestOAuthDaybreakBlueVirtualAlias(t *testing.T) {
	const (
		blueBase    = "gpt-5.6-sol"
		blueVirtual = "gpt-5.6-sol-blue"
		upstreamHit = "synthetic-backing-model"
	)

	var mu sync.Mutex
	captures := make([]capturedUpstream, 0, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captures = append(captures, capturedUpstream{path: r.URL.Path, body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_blue\",\"status\":\"completed\",\"model\":%q,\"access_programs\":{\"cyber\":\"daybreak_blue\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", upstreamHit)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.CommercialMode = true
	cfg.DisableImageGeneration = internalconfig.DisableImageGenerationPassthrough
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.OAuthModelAlias = map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name: blueBase, Alias: blueVirtual, Fork: true,
			DisplayName: "GPT-5.6 Sol (Daybreak Blue)",
		}},
	}
	cfg.Payload.OverrideRaw = []internalconfig.PayloadRule{{
		Models: []internalconfig.PayloadModelRule{{Name: blueVirtual, Protocol: "codex", Scope: "requested"}},
		Params: map[string]any{"access_programs": map[string]any{"cyber": "daybreak_blue"}},
	}}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	// Mirrors builder.go startup: the OAuth alias table is registered separately
	// from SetConfig so execution resolves the alias back to the upstream model.
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{cfg: cfg, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "daybreak-blue-virtual-alias-test",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  upstream.URL,
		},
		Metadata: map[string]any{"access_token": "synthetic-oauth-token"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(context.Background(), auth)
	service.ensureExecutorsForAuth(auth)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	handler := coreapi.NewServer(cfg, manager, nil, "", coreapi.WithRequestLoggerFactory(nil)).Handler()

	post := func(model string) (*httptest.ResponseRecorder, capturedUpstream) {
		mu.Lock()
		before := len(captures)
		mu.Unlock()
		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(fmt.Sprintf(`{"model":%q,"input":"OK"}`, model))))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		if rec.Code != http.StatusOK || len(captures) != before+1 {
			t.Fatalf("model %q dispatch: status=%d dispatches=%d body=%s", model, rec.Code, len(captures)-before, rec.Body.String())
		}
		return rec, captures[before]
	}

	rec, got := post(blueVirtual)
	if model := gjson.GetBytes(got.body, "model").String(); model != blueBase {
		t.Fatalf("virtual name reached upstream as %q, want base model %q (body=%s)", model, blueBase, got.body)
	}
	if program := gjson.GetBytes(got.body, "access_programs.cyber").String(); program != "daybreak_blue" {
		t.Fatalf("upstream program = %q, want daybreak_blue (body=%s)", program, got.body)
	}
	if !strings.Contains(rec.Body.String(), upstreamHit) {
		t.Fatalf("terminal upstream model not retained: %s", rec.Body.String())
	}

	_, got = post(blueBase)
	if model := gjson.GetBytes(got.body, "model").String(); model != blueBase {
		t.Fatalf("plain model changed upstream: %s", got.body)
	}
	if gjson.GetBytes(got.body, "access_programs").Exists() {
		t.Fatalf("plain base model received program injection: %s", got.body)
	}

	modelsReq := httptest.NewRequest("GET", "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	handler.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if body := modelsRec.Body.String(); !strings.Contains(body, blueVirtual) || !strings.Contains(body, blueBase) {
		t.Fatalf("models list missing base or virtual entry: %s", body)
	}
}

type capturedUpstream struct {
	path string
	body []byte
}
