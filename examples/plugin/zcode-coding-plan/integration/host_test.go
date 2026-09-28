package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginhost"
)

func isolateDynamic(t *testing.T) bool {
	t.Helper()
	if os.Getenv("CP_TEST_CHILD") == t.Name() {
		return false
	}
	parts := strings.Split(t.Name(), "/")
	for i := range parts {
		parts[i] = "^" + regexp.QuoteMeta(parts[i]) + "$"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run="+strings.Join(parts, "/"))
	cmd.Env = append(os.Environ(), "CP_TEST_CHILD="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated dynamic test: %v\n%s", err, out)
	}
	t.Logf("%s", out)
	return true
}

func TestCPADynamicLoadAndAuth(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	library := os.Getenv("CP_PLUGIN_LIBRARY")
	if library == "" {
		t.Skip("CP_PLUGIN_LIBRARY is required")
	}
	raw, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "zcode-coding-plan"+filepath.Ext(library)), raw, 0700); err != nil {
		t.Fatal(err)
	}
	// c-shared 有独立 Go runtime，环境变量必须在动态库初始化前设置。
	t.Setenv("CP_INTEGRATION_KEY", "synthetic-key.synthetic-secret")
	t.Setenv("CP_INTEGRATION_DEVICE", "synthetic-device")
	enabled := true
	host := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer host.ShutdownAll()
	host.ApplyConfig(ctx, pluginhost.RuntimeConfig{Enabled: true, Dir: dir, Configs: map[string]pluginhost.PluginInstanceConfig{"zcode-coding-plan": {Enabled: &enabled}}})
	list := host.RegisteredPlugins()
	if len(list) != 1 {
		t.Fatalf("registered plugin count=%d", len(list))
	}
	if !host.HasAuthProvider("zcode-coding-plan") {
		t.Fatal("auth provider missing")
	}
	configPath := filepath.Join(dir, "private.json")
	config := []byte(`{"credential":{"api_key_env":"CP_INTEGRATION_KEY"},"identity":{"device_id_env":"CP_INTEGRATION_DEVICE","platform":"linux-x64","os_category":"linux","os_version":"test","language":"en","timezone":"UTC"},"models":["test-model"],"host_logging_disabled":true,"prompt":{"mode":"preserve"}}`)
	if err = os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	storage, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "config_file": configPath, "label": "Synthetic", "request_retry": 0})
	auth, handled, err := host.ParseAuth(ctx, pluginapi.AuthParseRequest{RawJSON: storage, FileName: "synthetic.json"})
	if err != nil || !handled || auth == nil {
		t.Fatalf("auth parse handled=%v err=%v", handled, err)
	}
	if n, ok := auth.RequestRetryOverride(); !ok || n != 0 {
		t.Fatal("retry override was not preserved")
	}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || !models.Handled || len(models.Models) != 1 || models.Models[0].ID != "test-model" {
		t.Fatalf("model discovery: err=%v handled=%v count=%d", models.Err, models.Handled, len(models.Models))
	}
	if !host.UnloadPlugin("zcode-coding-plan") {
		t.Fatal("unload failed")
	}
}
