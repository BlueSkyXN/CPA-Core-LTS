package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestCPAInlineAuthAccount 走单文件内联账号：无私有配置文件、无挂载、无环境变量，
// 仅凭 auth JSON 内联凭据 + 管理面 host_logging_disabled 确认完成注册、目录与执行。
func TestCPAInlineAuthAccount(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	library := os.Getenv("CP_PLUGIN_LIBRARY")
	if library == "" {
		t.Skip("CP_PLUGIN_LIBRARY required")
	}
	dir := t.TempDir()
	lib, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "zcode-coding-plan"+filepath.Ext(library)), lib, 0700)
	cfg, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n      host_logging_disabled: true\nrequest-log: false\n", dir)))
	if err != nil {
		t.Fatal(err)
	}
	h := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer h.ShutdownAll()
	h.ApplyConfig(ctx, cfg)
	storage, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "label": "inline", "api_key": "synthetic-key.synthetic-secret", "device_id": "synthetic-device", "request_retry": 0})
	auth, handled, err := h.ParseAuth(ctx, pluginapi.AuthParseRequest{RawJSON: storage, FileName: "inline.json"})
	if err != nil || !handled {
		t.Fatalf("auth %v %v", err, handled)
	}
	auth.ID = "inline-auth"
	auth.Index = "inline-index"
	models := h.ModelsForAuth(ctx, auth)
	if models.Err != nil {
		t.Fatal(models.Err)
	}
	if len(models.Models) != 2 {
		t.Fatalf("default models missing: %d", len(models.Models))
	}
	var flash *registry.ModelInfo
	for _, m := range models.Models {
		if m.ID == "glm-5.3-flash" {
			flash = m
		}
		if m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 || m.Thinking == nil || len(m.Thinking.Levels) != 3 {
			t.Fatalf("builtin metadata missing for %s", m.ID)
		}
	}
	if flash == nil || len(flash.SupportedInputModalities) != 2 {
		t.Fatal("glm-5.3-flash image modality missing")
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h.RegisterExecutors(manager, nil)
	executor, ok := manager.Executor("zcode-coding-plan")
	if !ok {
		t.Fatal("executor missing")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	transport := &fixtureTransport{pub: pub, priv: priv}
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
	sink := &fixtureUsageSink{records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin("coding-plan-inline", sink)
	req := coreexecutor.Request{Model: "glm-5.3", Payload: []byte(`{"model":"glm-5.3","max_tokens":10,"system":"caller","messages":[{"role":"user","content":"hello"}]}`)}
	opts := coreexecutor.Options{Stream: false, SourceFormat: sdktranslator.FormatClaude, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "inline-exec"}}
	result, err := executor.Execute(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload) == 0 {
		t.Fatal("empty response")
	}
	select {
	case record := <-sink.records:
		if record.Failed || record.AuthIndex != "inline-index" {
			t.Fatal("usage attribution mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host did not publish usage")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.handshakes != 1 || transport.models != 1 {
		t.Fatalf("unexpected calls %d %d", transport.handshakes, transport.models)
	}
}
