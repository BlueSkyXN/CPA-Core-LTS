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

func TestSaveConfigPreserveCommentsFailureRollsBackHandlerConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping read-only config file permission test as root")
	}
	gin.SetMode(gin.TestMode)

	original := []byte(`port: 8317
debug: false
gemini-api-key:
  - api-key: original-gemini-key
plugins:
  configs:
    demo-plugin:
      enabled: false
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(path, 0o600)
	})

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = config.HomeConfig{Enabled: true, Host: "runtime.home.example"}
	cfg.AuthDir = "/runtime/auths"

	h := NewHandler(cfg, path, nil)
	router := gin.New()
	router.PUT("/v0/management/debug", h.PutDebug)
	router.PUT("/v0/management/request-retry", h.PutRequestRetry)
	router.PUT("/v0/management/gemini-api-key", h.PutGeminiKeys)
	router.PATCH("/v0/management/plugins/:id/enabled", h.PatchPluginEnabled)

	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}

	assertRuntimePreserved := func(t *testing.T) {
		t.Helper()
		snap := h.configSnapshot()
		if !snap.Home.Enabled || snap.Home.Host != "runtime.home.example" {
			t.Fatalf("runtime Home was lost during rollback: %+v", snap.Home)
		}
		if snap.AuthDir != "/runtime/auths" {
			t.Fatalf("runtime AuthDir = %q, want /runtime/auths", snap.AuthDir)
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(original, after) {
			t.Fatalf("read-only config file changed or failed read: %v", readErr)
		}
	}

	t.Run("scalar PutDebug rolls back on 500", func(t *testing.T) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v0/management/debug", strings.NewReader(`{"value":true}`)))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("PutDebug status=%d body=%s", w.Code, w.Body.String())
		}
		if h.configSnapshot().Debug {
			t.Fatal("h.cfg.Debug stayed true after 500 save failure")
		}
		assertRuntimePreserved(t)
	})

	t.Run("slice PutGeminiKeys rolls back on 500", func(t *testing.T) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v0/management/gemini-api-key", strings.NewReader(`[{"api-key":"mutated-gemini-key"}]`)))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("PutGeminiKeys status=%d body=%s", w.Code, w.Body.String())
		}
		keys := h.configSnapshot().GeminiKey
		if len(keys) != 1 || keys[0].APIKey != "original-gemini-key" {
			t.Fatalf("h.cfg.GeminiKey leaked mutation after 500: %+v", keys)
		}
		assertRuntimePreserved(t)
	})

	t.Run("map PatchPluginEnabled rolls back on 500", func(t *testing.T) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/v0/management/plugins/demo-plugin/enabled", strings.NewReader(`{"enabled":true}`)))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("PatchPluginEnabled status=%d body=%s", w.Code, w.Body.String())
		}
		pluginCfg := h.configSnapshot().Plugins.Configs["demo-plugin"]
		if pluginCfg.Enabled == nil || *pluginCfg.Enabled {
			t.Fatalf("h.cfg.Plugins.Configs[demo-plugin] leaked mutation after 500: %+v", pluginCfg)
		}
		assertRuntimePreserved(t)
	})

	// Restore writable permissions and execute a subsequent valid write;
	// none of the earlier rolled-back mutations may be persisted.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v0/management/request-retry", strings.NewReader(`{"value":5}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("subsequent valid write failed: status=%d body=%s", w.Code, w.Body.String())
	}
	persisted, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RequestRetry != 5 {
		t.Fatalf("persisted RequestRetry = %d, want 5", persisted.RequestRetry)
	}
	if persisted.Debug {
		t.Fatal("subsequent save persisted rolled-back Debug=true")
	}
	if len(persisted.GeminiKey) != 1 || persisted.GeminiKey[0].APIKey != "original-gemini-key" {
		t.Fatalf("subsequent save persisted rolled-back GeminiKey: %+v", persisted.GeminiKey)
	}
	persistedPlugin := persisted.Plugins.Configs["demo-plugin"]
	if persistedPlugin.Enabled == nil || *persistedPlugin.Enabled {
		t.Fatalf("subsequent save persisted rolled-back plugin enabled: %+v", persistedPlugin)
	}
}

func TestRestorePersistedConfigLockedHandlesNilConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{configFilePath: path}
	h.mu.Lock()
	h.restorePersistedConfigLocked()
	h.mu.Unlock()
	if h.cfg == nil || h.cfg.Port != 8317 {
		t.Fatalf("restorePersistedConfigLocked with nil h.cfg got %+v", h.cfg)
	}
}

func TestFailedConfigEditWhileFileUnavailableDoesNotSurviveRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\ndebug: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, path, nil)
	router := gin.New()
	router.PUT("/debug", h.PutDebug)
	router.PUT("/retry", h.PutRequestRetry)
	router.GET("/debug", h.GetDebug)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	// 先成功保存一次，回滚目标必须是最近一次生效的配置。
	if w := request("PUT", "/retry", `{"value":3}`); w.Code != 200 {
		t.Fatalf("initial save: %d", w.Code)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", "/debug", `{"value":true}`); w.Code != 500 {
		t.Fatalf("failed edit: %d", w.Code)
	}
	if w := request("GET", "/debug", ""); w.Body.String() != `{"debug":false}` {
		t.Errorf("debug after failed save: %s", w.Body.String())
	}
	if h.configSnapshot().RequestRetry != 3 {
		t.Fatal("rollback discarded the earlier successful edit")
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", "/retry", `{"value":5}`); w.Code != 200 {
		t.Fatalf("subsequent save: %d", w.Code)
	}
	saved, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Debug || saved.RequestRetry != 5 {
		t.Fatalf("persisted debug=%t retry=%d", saved.Debug, saved.RequestRetry)
	}
}

func TestLegacyConfigReadsDuringSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	router := gin.New()
	router.GET("/config", h.GetConfig)
	router.GET("/debug", h.GetDebug)
	router.GET("/keys", h.GetAPIKeys)
	router.PUT("/debug", h.PutDebug)

	// 留住读者取得的版本，并发保存与重载均不能更改这个版本。
	first := h.readOnlyConfig()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for range 32 {
			for _, route := range []string{"/config", "/debug", "/keys"} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
				if w.Code != http.StatusOK {
					t.Errorf("GET %s: %d", route, w.Code)
					return
				}
			}
		}
	}()
	<-started
	for range 8 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/debug", strings.NewReader(`{"value":true}`)))
		if w.Code != http.StatusOK {
			t.Errorf("PUT /debug: %d", w.Code)
			break
		}
		h.SetConfig(&config.Config{Port: 9000})
	}
	<-done
	if first.Debug || first.Port != 8317 || h.readOnlyConfig().Port != 9000 {
		t.Fatal("reader did not retain its configuration across saves and reloads")
	}
}
