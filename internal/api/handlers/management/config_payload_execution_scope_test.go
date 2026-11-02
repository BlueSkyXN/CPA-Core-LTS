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
