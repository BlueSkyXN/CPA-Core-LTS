package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestLegacyRawConfigRejectsGroupedLayoutReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, current := range []string{
		"config-version: 8\nserver: {port: 8317}\naccess: {api-keys: [synthetic-client]}\napi-keys: {codex: [{name: fixture, base-url: 'https://example.invalid', keys: [{api-key: synthetic-provider}]}]}\n",
		"port: 8317\naccess: {api-keys: [synthetic-client]}\napi-keys: {codex: [{name: fixture, base-url: 'https://example.invalid', keys: [{api-key: synthetic-provider}]}]}\n",
	} {
		t.Run(current[:8], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(current), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.PUT("/v0/config.yaml", h.PutConfigYAML)
			router.PUT("/v0/request-retry", h.PutRequestRetry)
			for _, body := range []string{"port: 8317\napi-keys: [replacement]\n", current} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v0/config.yaml", strings.NewReader(body)))
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "unsupported_config_layout") {
					t.Fatalf("unsafe raw replacement status=%d", w.Code)
				}
				saved, _ := os.ReadFile(path)
				if !bytes.Equal(saved, []byte(current)) {
					t.Fatal("rejected write changed disk")
				}
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v0/request-retry", strings.NewReader(`{"value":4}`)))
			if w.Code != 200 {
				t.Fatalf("safe legacy setter rejected: %d", w.Code)
			}
			loaded, err := config.LoadConfig(path)
			if err != nil || loaded.RequestRetry != 4 || len(loaded.CodexKey) != 1 || len(loaded.APIKeys) != 1 {
				t.Fatalf("safe setter lost grouped credentials: error=%v retry=%d providers=%d clients=%d", err, loaded.RequestRetry, len(loaded.CodexKey), len(loaded.APIKeys))
			}
		})
	}
}

func TestV8LTSConfigMutationPreservesPolicyAndOpaquePluginYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := `port: 8317
flow-control: {version: 3, enabled: false}
api-request-body-max-bytes: 123456
ampcode: {upstream-url: "https://amp.example"}
codex: {client-metadata: {mode: strict, workspace-policy: drop}}
plugins:
  configs:
    synthetic:
      enabled: false
      permissions: {auth-read: true}
      opaque: {arbitrary-key: [1, two, false]}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/config", h.configV8WithCurrentRevisionForTest(t))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v8/config", strings.NewReader(`{"server":{"port":8318}}`)))
	if w.Code != 200 {
		t.Fatalf("v8 update: %d %s", w.Code, w.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 8318 || loaded.APIRequestBodyMaxBytes != 123456 || loaded.FlowControl.Version != 3 || loaded.AmpCode.UpstreamURL != "https://amp.example" || loaded.ForAPIKey().Codex.ClientMetadata.Mode != "strict" {
		t.Fatal("v8 update lost LTS runtime policy")
	}
	plugin := loaded.Plugins.Configs["synthetic"]
	if plugin.Enabled == nil || *plugin.Enabled || !plugin.Permissions.AuthRead {
		t.Fatal("plugin enablement or permission changed")
	}
}

func TestV8OpaquePluginYAMLRejectsLossyJSONView(t *testing.T) {
	for _, opaque := range []string{"settings: {1: value}", "limit: .inf"} {
		t.Run(opaque, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := "port: 8317\nplugins:\n  configs:\n    fixture:\n      " + opaque + "\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.GET("/v8/config", h.configV8WithCurrentRevisionForTest(t))
			router.GET("/v8/config.yaml", h.configV8WithCurrentRevisionForTest(t))
			router.PATCH("/v8/config", h.configV8WithCurrentRevisionForTest(t))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/config", nil))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("lossy JSON status = %d", w.Code)
			}
			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/config.yaml", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), opaque) {
				t.Fatalf("opaque YAML view changed: %d", w.Code)
			}
			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v8/config", strings.NewReader(`{"server":{"port":8318}}`)))
			if w.Code != 200 {
				t.Fatalf("unrelated patch rejected: %d %s", w.Code, w.Body.String())
			}
			saved, _ := os.ReadFile(path)
			if !strings.Contains(string(saved), opaque) {
				t.Fatal("unrelated patch lost opaque YAML")
			}
		})
	}
}
