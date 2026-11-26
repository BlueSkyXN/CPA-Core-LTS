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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConfigV8HomeRejectsAllowlistsAndFlowControlBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := []byte("config-version: 8\nserver:\n  port: 8317\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = config.HomeConfig{Enabled: true, Host: "home.example"}

	t.Run("resource-allowlists rejected with 422 before disk write", func(t *testing.T) {
		h := NewHandler(cfg.CloneForRuntime(), path, nil)
		router := gin.New()
		router.PUT("/v8/config/*path", h.configV8WithCurrentRevisionForTest(t))
		router.PUT("/v8/config.yaml", h.configV8WithCurrentRevisionForTest(t))

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v8/config/access/resource-allowlists", strings.NewReader(`{"auth-files":{"team.json":[]},"models":{"private":[]}}`)))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"invalid_config"`) {
			t.Fatalf("PUT resource-allowlists status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected resource-allowlists overwrote file, readErr=%v", readErr)
		}
		if h.configSnapshot().ResourceAllowlists != nil {
			t.Fatalf("rejected update mutated h.cfg.ResourceAllowlists: %+v", h.configSnapshot().ResourceAllowlists)
		}

		yamlBody := "config-version: 8\nserver:\n  port: 8317\naccess:\n  resource-allowlists:\n    auth-files:\n      team.json: []\n"
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v8/config.yaml", strings.NewReader(yamlBody)))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"invalid_config"`) {
			t.Fatalf("PUT v8 config.yaml status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected v8 config.yaml overwrote file, readErr=%v", readErr)
		}
	})

	t.Run("flow-control rejected with 409 when authManager present", func(t *testing.T) {
		runCfg := cfg.CloneForRuntime()
		manager := coreauth.NewManager(nil, nil, nil)
		defer manager.CloseFlowControl()
		manager.SetConfig(runCfg)
		h := NewHandler(runCfg, path, manager)
		router := gin.New()
		router.PATCH("/v8/config", h.configV8WithCurrentRevisionForTest(t))

		body := `{"flow-control":{"version":3,"enabled":true,"rules":[{"id":"execution","stage":"attempt","scope":"global","model":"public-test","max-concurrent":2,"windows":[{"requests":20,"period-ms":60000}]}]}}`
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v8/config", strings.NewReader(body)))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"flow_control_update_rejected"`) {
			t.Fatalf("PATCH flow-control status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected flow-control overwrote file, readErr=%v", readErr)
		}
	})

	t.Run("flow-control rejected with 422 when authManager nil", func(t *testing.T) {
		h := NewHandler(cfg.CloneForRuntime(), path, nil)
		router := gin.New()
		router.PATCH("/v8/config", h.configV8WithCurrentRevisionForTest(t))

		body := `{"flow-control":{"version":3,"enabled":true,"rules":[{"id":"execution","stage":"attempt","scope":"global","model":"public-test","max-concurrent":2,"windows":[{"requests":20,"period-ms":60000}]}]}}`
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v8/config", strings.NewReader(body)))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"invalid_config"`) {
			t.Fatalf("PATCH flow-control (nil authManager) status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected flow-control overwrote file, readErr=%v", readErr)
		}
	})

	t.Run("nil h.cfg does not panic on ConfigV8", func(t *testing.T) {
		nilPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(nilPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		h := &Handler{configFilePath: nilPath}
		router := gin.New()
		router.PATCH("/v8/config", h.configV8WithCurrentRevisionForTest(t))

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v8/config", strings.NewReader(`{"server":{"port":8318}}`)))
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH with nil h.cfg status=%d body=%s", w.Code, w.Body.String())
		}
		if h.cfg == nil || h.cfg.Port != 8318 {
			t.Fatalf("unexpected h.cfg after save: %+v", h.cfg)
		}
	})
}

func TestPutConfigYAMLHomeRejectsAllowlistsAndFlowControlBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := []byte("port: 8317\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = config.HomeConfig{Enabled: true, Host: "home.example"}

	t.Run("resource-allowlists rejected with 422 before disk write", func(t *testing.T) {
		h := NewHandler(cfg.CloneForRuntime(), path, nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := "port: 8317\nresource-allowlists:\n  auth-files:\n    team.json: []\n"
		c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/yaml")
		h.PutConfigYAML(c)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"invalid_config"`) {
			t.Fatalf("PutConfigYAML resource-allowlists status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected PutConfigYAML overwrote file, readErr=%v", readErr)
		}
		if !h.configSnapshot().Home.Enabled {
			t.Fatal("h.cfg.Home.Enabled was lost")
		}
	})

	t.Run("flow-control rejected with 409 when authManager present", func(t *testing.T) {
		runCfg := cfg.CloneForRuntime()
		manager := coreauth.NewManager(nil, nil, nil)
		defer manager.CloseFlowControl()
		manager.SetConfig(runCfg)
		h := NewHandler(runCfg, path, manager)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := "port: 8317\nflow-control:\n  version: 3\n  enabled: true\n  rules:\n    - id: execution\n      stage: attempt\n      scope: global\n      model: public-test\n      max-concurrent: 2\n      windows:\n        - requests: 20\n          period-ms: 60000\n"
		c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/yaml")
		h.PutConfigYAML(c)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"flow_control_update_rejected"`) {
			t.Fatalf("PutConfigYAML flow-control status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected PutConfigYAML flow-control overwrote file, readErr=%v", readErr)
		}
		if !h.configSnapshot().Home.Enabled {
			t.Fatal("h.cfg.Home.Enabled was lost")
		}
	})

	t.Run("flow-control rejected with 422 when authManager nil", func(t *testing.T) {
		h := NewHandler(cfg.CloneForRuntime(), path, nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := "port: 8317\nflow-control:\n  version: 3\n  enabled: true\n  rules:\n    - id: execution\n      stage: attempt\n      scope: global\n      model: public-test\n      max-concurrent: 2\n      windows:\n        - requests: 20\n          period-ms: 60000\n"
		c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/yaml")
		h.PutConfigYAML(c)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"invalid_config"`) {
			t.Fatalf("PutConfigYAML flow-control (nil authManager) status=%d body=%s", w.Code, w.Body.String())
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("rejected PutConfigYAML flow-control overwrote file, readErr=%v", readErr)
		}
	})

	t.Run("nil h.cfg does not panic on PutConfigYAML", func(t *testing.T) {
		nilPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(nilPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		h := &Handler{configFilePath: nilPath}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader("port: 8319\n"))
		c.Request.Header.Set("Content-Type", "application/yaml")
		h.PutConfigYAML(c)
		if w.Code != http.StatusOK {
			t.Fatalf("PutConfigYAML with nil h.cfg status=%d body=%s", w.Code, w.Body.String())
		}
		if h.cfg == nil || h.cfg.Port != 8319 {
			t.Fatalf("unexpected h.cfg after save: %+v", h.cfg)
		}
	})
}
