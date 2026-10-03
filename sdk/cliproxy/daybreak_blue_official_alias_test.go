package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	coreapi "github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// TestOAuthDaybreakBlueOfficialAlias locks the official Daybreak Blue alias
// route: gpt-daybreak-blue-latest is selected verbatim from the catalog and
// forwarded verbatim to the upstream; CPA adds no default access program,
// keeps an explicit daybreak_blue selection, and relays the upstream backing
// model and program echo without rewriting them to the alias. Catalog
// visibility here only pins routing; it does not imply OAuth approval.
func TestOAuthDaybreakBlueOfficialAlias(t *testing.T) {
	const (
		officialAlias = "gpt-daybreak-blue-latest"
		backingModel  = "synthetic-backing-model"
	)

	var mu sync.Mutex
	captures := make([]capturedUpstream, 0, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captures = append(captures, capturedUpstream{path: r.URL.Path, body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_blue_official\",\"status\":\"completed\",\"model\":%q,\"access_programs\":{\"cyber\":\"daybreak_blue\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", backingModel)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.CommercialMode = true
	cfg.DisableImageGeneration = internalconfig.DisableImageGenerationPassthrough
	cfg.RemoteManagement.DisableControlPanel = true

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "daybreak-blue-official-alias-test",
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

	post := func(requestBody string) (*httptest.ResponseRecorder, capturedUpstream) {
		mu.Lock()
		before := len(captures)
		mu.Unlock()
		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(requestBody)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		if rec.Code != http.StatusOK || len(captures) != before+1 {
			t.Fatalf("official alias dispatch: status=%d dispatches=%d body=%s", rec.Code, len(captures)-before, rec.Body.String())
		}
		return rec, captures[before]
	}

	// Default selection: alias forwarded verbatim, no program auto-injected.
	rec, got := post(fmt.Sprintf(`{"model":%q,"input":"OK"}`, officialAlias))
	if model := gjson.GetBytes(got.body, "model").String(); model != officialAlias {
		t.Fatalf("official alias reached upstream as %q, want verbatim %q (body=%s)", model, officialAlias, got.body)
	}
	if gjson.GetBytes(got.body, "access_programs").Exists() {
		t.Fatalf("default selection must not inject access_programs: %s", got.body)
	}
	assertBackingModelRelayed(t, rec, backingModel)

	// Explicit daybreak_blue selection must survive the outbound translation.
	rec, got = post(fmt.Sprintf(`{"model":%q,"input":"OK","access_programs":{"cyber":"daybreak_blue"}}`, officialAlias))
	if model := gjson.GetBytes(got.body, "model").String(); model != officialAlias {
		t.Fatalf("explicit-program selection changed upstream model: %s", got.body)
	}
	if program := gjson.GetBytes(got.body, "access_programs.cyber").String(); program != "daybreak_blue" {
		t.Fatalf("explicit daybreak_blue selection was stripped: %s", got.body)
	}
	assertBackingModelRelayed(t, rec, backingModel)

	modelsReq := httptest.NewRequest("GET", "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	handler.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	listed := false
	for _, entry := range gjson.Get(modelsRec.Body.String(), "data").Array() {
		if entry.Get("id").String() == officialAlias {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("models list missing official alias entry: %s", modelsRec.Body.String())
	}
}

func assertBackingModelRelayed(t *testing.T, rec *httptest.ResponseRecorder, backingModel string) {
	t.Helper()
	body := rec.Body.String()
	if model := gjson.Get(body, "model").String(); model != backingModel {
		t.Fatalf("relayed response model = %q, want upstream backing model %q: %s", model, backingModel, body)
	}
	if program := gjson.Get(body, "access_programs.cyber").String(); program != "daybreak_blue" {
		t.Fatalf("relayed access program = %q, want daybreak_blue: %s", program, body)
	}
}
