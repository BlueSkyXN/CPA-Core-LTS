package management

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigPayloadExecutionScopeSaveReadback(t *testing.T) {
	router, file := newRevisionTestRouter(t, "config-version: 8\nserver: {port: 8317}\n")
	body := `config-version: 8
server: {port: 8317}
requests:
  payload:
    override:
      - models:
          - name: scope-model
            protocol: codex
            auth-kind: apikey
            request-paths: [/v1/responses, /backend-api/codex/responses]
        params: {service_tier: flex}
`
	view := revisionRequest(router, http.MethodGet, "/v8/config.yaml", "", "")
	response := revisionRequest(router, http.MethodPut, "/v8/config.yaml", body, view.Header().Get("ETag"))
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	view = revisionRequest(router, http.MethodGet, "/v8/config.yaml", "", "")
	if view.Code != http.StatusOK {
		t.Fatal("readback failed")
	}
	cfg, err := config.ParseConfigBytes(view.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	rule := cfg.Payload.Override[0].Models[0]
	if rule.AuthKind != "apikey" || len(rule.RequestPaths) != 2 {
		t.Fatalf("readback lost scope: %+v", rule)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(body, "auth-kind: apikey", "auth-kind: invalid", 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", "[]", 1),
	} {
		response = revisionRequest(router, http.MethodPut, "/v8/config.yaml", bad, view.Header().Get("ETag"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid scope save status=%d body=%s", response.Code, response.Body.String())
		}
		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("rejected scope changed persisted config")
		}
	}
}

func TestConfigPayloadReplaceExecutionScopeSaveReadback(t *testing.T) {
	router, file := newRevisionTestRouter(t, "config-version: 8\nserver: {port: 8317}\n")
	body := `config-version: 8
server: {port: 8317}
requests:
  payload:
    replace:
      - models:
          - name: scope-model
            protocol: codex
            auth-kind: apikey
            request-paths: [/v1/responses, /backend-api/codex/responses]
        target: {role: user, all: true}
        replacements: [{old: new}]
`

	view := revisionRequest(router, http.MethodGet, "/v8/config.yaml", "", "")
	response := revisionRequest(router, http.MethodPut, "/v8/config.yaml", body, view.Header().Get("ETag"))
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	view = revisionRequest(router, http.MethodGet, "/v8/config.yaml", "", "")
	if view.Code != http.StatusOK {
		t.Fatal("readback failed")
	}
	cfg, err := config.ParseConfigBytes(view.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Payload.Replace) != 1 || len(cfg.Payload.Replace[0].Models) != 1 {
		t.Fatalf("unexpected replace rules: %+v", cfg.Payload.Replace)
	}
	rule := cfg.Payload.Replace[0].Models[0]
	if rule.AuthKind != "apikey" || len(rule.RequestPaths) != 2 {
		t.Fatalf("readback lost replace scope: %+v", rule)
	}

	omittedBody := `config-version: 8
server: {port: 8317}
requests:
  payload:
    replace:
      - models:
          - name: scope-model
            protocol: codex
        target: {role: user, all: true}
        replacements: [{old: new}]
`
	response = revisionRequest(router, http.MethodPut, "/v8/config.yaml", omittedBody, view.Header().Get("ETag"))
	if response.Code != http.StatusOK {
		t.Fatalf("omitted scope save status=%d body=%s", response.Code, response.Body.String())
	}
	view = revisionRequest(router, http.MethodGet, "/v8/config.yaml", "", "")
	if view.Code != http.StatusOK {
		t.Fatal("readback after omitted scope save failed")
	}

	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(body, "auth-kind: apikey", "auth-kind: invalid", 1),
		strings.Replace(body, "auth-kind: apikey", "auth-kind: '   '", 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", "[]", 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", "['   ']", 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", `["\t\n"]`, 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", `["\u00a0"]`, 1),
		strings.Replace(body, "[/v1/responses, /backend-api/codex/responses]", "[v1/responses]", 1),
	} {
		response = revisionRequest(router, http.MethodPut, "/v8/config.yaml", bad, view.Header().Get("ETag"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid replace scope save status=%d body=%s", response.Code, response.Body.String())
		}
		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("rejected replace scope changed persisted config")
		}
	}
}
