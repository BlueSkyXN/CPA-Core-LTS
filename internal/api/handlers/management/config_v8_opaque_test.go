package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func TestConfigV8OpaquePluginJSONBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"integer", "table: {1: value}"},
		{"typed", "table:\n        1: numeric\n        \"1\": string"},
		{"composite", "table:\n        ? [a, b]\n        : pair"},
		{"alias", "base: &base {1: numeric}\n      table: *base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "config-version: 8\nserver: {port: 8317}\napi-keys:\n  codex:\n    - name: sample\n      base-url: https://example.invalid\n      keys: [{api-key: synthetic-key}]\nplugins:\n  configs:\n    sample:\n      enabled: false\n      " + tc.body + "\n"
			cfg, err := config.ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err = config.ValidateV8Config([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.GET("/v8/management/config", h.ConfigV8)
			router.GET("/v8/management/config.yaml", h.ConfigV8)
			router.GET("/v8/management/config/*path", h.ConfigV8)
			for _, target := range []struct {
				path   string
				status int
			}{
				{"/config", http.StatusUnprocessableEntity},
				{"/config/plugins/configs/sample", http.StatusUnprocessableEntity},
				{"/config/plugins/configs/sample/table", http.StatusUnprocessableEntity},
				{"/config.yaml", http.StatusOK},
				{"/config/server", http.StatusOK},
				{"/config/api-keys", http.StatusOK},
				{"/config/api-keys/codex", http.StatusOK},
				{"/config/plugins/configs/sample/enabled", http.StatusOK},
			} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management"+target.path, nil))
				if w.Code != target.status {
					t.Errorf("GET %s status = %d, want %d", target.path, w.Code, target.status)
				}
				if w.Header().Get("ETag") != configRevision([]byte(raw)) {
					t.Errorf("GET %s lost persisted revision", target.path)
				}
				if target.status == http.StatusUnprocessableEntity && gjson.GetBytes(w.Body.Bytes(), "error").String() != "config_not_json_compatible" {
					t.Errorf("GET %s did not classify opaque YAML", target.path)
				}
				if target.path == "/config/api-keys/codex" && gjson.GetBytes(w.Body.Bytes(), "0.keys.0.auth_index").String() == "" {
					t.Error("provider subtree lost auth index")
				}
				if target.path == "/config.yaml" && !strings.Contains(w.Body.String(), "table:") {
					t.Error("YAML view lost plugin settings")
				}
			}
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != raw {
				t.Fatal("configuration reads changed the stored document")
			}
		})
	}
}

func TestConfigV8StringMapKeysVisitsSharedAliasesOnce(t *testing.T) {
	// Nine levels of ten-fold alias fan-out expand to 10^9 nodes if aliases are re-walked.
	var b strings.Builder
	b.WriteString("l0: &l0 {k: v}\n")
	for level := 1; level <= 9; level++ {
		fmt.Fprintf(&b, "l%d: &l%d [%s]\n", level, level, strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*l%d, ", level-1), 10), ", "))
	}
	for _, tc := range []struct {
		name string
		leaf string
		want bool
	}{
		{"string keys", "l0: &l0 {k: v}\n", true},
		{"integer key", "l0: &l0 {1: v}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(strings.Replace(b.String(), "l0: &l0 {k: v}\n", tc.leaf, 1)), &doc); err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			go func() { done <- configV8StringMapKeys(&doc) }()
			select {
			case got := <-done:
				if got != tc.want {
					t.Fatalf("configV8StringMapKeys = %v, want %v", got, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("alias fan-out was expanded instead of visited once")
			}
		})
	}
}
