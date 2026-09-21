//go:build cgo && !race && (darwin || linux)

package test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	coreapi "github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// 真正加载 c-shared 插件，验证 JSON RPC、Core 统计和原生 SSE；仅请求 loopback 假上游。
func TestCodeBuddyDynamicPlugin(t *testing.T) {
	dir := t.TempDir()
	library := filepath.Join(dir, "cpa-provider-codebuddy"+pluginhost.PluginExtension(runtime.GOOS))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-o", library, ".")
	build.Dir = "../examples/plugin/codebuddy/go"
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodeBuddyDynamicHelper$")
	helper.Env = append(os.Environ(), "CPA_CODEBUDDY_TEST_LIBRARY="+library)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("dynamic host: %v\n%s", err, output)
	}
}

func TestCodeBuddyDynamicHelper(t *testing.T) {
	library := os.Getenv("CPA_CODEBUDDY_TEST_LIBRARY")
	if library == "" {
		return
	}
	var truncated atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-API-Key") != "fixture-key" {
			t.Error("selected credential lost")
		}
		switch req.URL.Path {
		case "/v3/config":
			if !bytes.Contains([]byte(req.Header.Get("User-Agent")), []byte("CLI/")) {
				t.Error("catalog client profile lost")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3","credits":"x0.00"}],"agents":[{"name":"cli","models":["hy3"]}]}}`)
		case "/v2/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"fixture\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native-ok\",\"reasoning_content\":\"fixture-reasoning\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			if !truncated.Load() {
				fmt.Fprint(w, "data: [DONE]\n\n")
			}
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()
	rawConfig := []byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    cpa-provider-codebuddy:\n      enabled: true\n      endpoint: %s/v2/chat/completions\n      catalog_endpoint: %s/v3/config\n", filepath.Dir(library), upstream.URL, upstream.URL))
	cfg, err := config.ParseConfigBytes(rawConfig)
	if err != nil {
		t.Fatal(err)
	}
	host := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host.ApplyConfig(ctx, cfg)
	defer host.ShutdownAll()
	if !host.PluginRegistered("cpa-provider-codebuddy") {
		t.Fatal("plugin not registered")
	}
	manager := coreauth.NewManager(nil, nil, nil)
	modelRegistry := registry.GetGlobalRegistry()
	host.RegisterExecutors(manager, modelRegistry)
	manager.SetPluginScheduler(host)
	executor, ok := manager.Executor("codebuddy")
	if !ok {
		t.Fatal("executor not registered")
	}
	auth := &coreauth.Auth{ID: "fixture-auth", Index: "fixture-index", Provider: "codebuddy", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "codebuddy", "auth_mode": "pat", "pat": "fixture-key"}}
	auth, err = manager.Register(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || len(models.Models) != 1 || models.Models[0].ID != "hy3" {
		t.Fatalf("catalog: %v", models.Err)
	}
	modelRegistry.RegisterClient(auth.ID, auth.Provider, models.Models)
	defer modelRegistry.UnregisterClient(auth.ID)
	sink := &codeBuddyFixtureUsage{records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin("codebuddy-dynamic-fixture", sink)
	for _, mode := range []string{"json", "sse", "truncated-sse"} {
		truncated.Store(mode == "truncated-sse")
		options := coreexecutor.Options{Stream: mode != "json", SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "fixture-" + mode}}
		request := coreexecutor.Request{Model: "hy3", Payload: []byte(`{"model":"hy3","messages":[{"role":"user","content":"fixture"}]}`)}
		var output []byte
		var failed bool
		if mode == "json" {
			result, err := executor.Execute(ctx, auth, request, options)
			if err != nil {
				t.Fatal(err)
			}
			output = result.Payload
		} else {
			result, err := executor.ExecuteStream(ctx, auth, request, options)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				output = append(output, chunk.Payload...)
				failed = failed || chunk.Err != nil
			}
		}
		if failed != truncated.Load() || !bytes.Contains(output, []byte("native-ok")) || !bytes.Contains(output, []byte(`"total_tokens":12`)) {
			t.Fatalf("%s response lost content, usage or failure", mode)
		}
		select {
		case record := <-sink.records:
			if record.Failed != failed || record.AuthIndex != "fixture-index" || record.Detail.TotalTokens != 12 || record.UsageProvenance != "provider_reported_unverified" {
				t.Fatalf("%s usage attribution failed", mode)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("usage not published")
		}
	}
	select {
	case <-sink.records:
		t.Fatal("usage double counted")
	default:
	}

	// 最后一层使用真正的 CPA HTTP handler，覆盖客户端 API key、模型路由、
	// plugin executor、普通 JSON / SSE 输出和正式 usage 归属。
	truncated.Store(false)
	cfg.APIKeys = []string{"fixture-client-key"}
	cfg.AuthDir = t.TempDir()
	cfg.LoggingToFile = false
	cfg.RequestLog = false
	server := coreapi.NewServer(cfg, manager, sdkaccess.NewManager(), filepath.Join(t.TempDir(), "config.yaml"), coreapi.WithPluginHost(host))
	for _, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":"hy3","messages":[{"role":"user","content":"HTTP API fixture"}],"stream":%t}`, stream)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer fixture-client-key")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("native-ok")) || !bytes.Contains(response.Body.Bytes(), []byte(`"total_tokens":12`)) {
			t.Fatalf("HTTP stream=%t status=%d body=%s", stream, response.Code, response.Body.String())
		}
		if stream && (!bytes.Contains(response.Body.Bytes(), []byte("data:")) || !bytes.Contains(response.Body.Bytes(), []byte("[DONE]"))) {
			t.Fatalf("HTTP streaming response lost SSE framing: %s", response.Body.String())
		}
		select {
		case record := <-sink.records:
			if record.Failed || record.AuthIndex != auth.Index || record.Detail.TotalTokens != 12 {
				t.Fatalf("HTTP stream=%t usage attribution failed: %+v", stream, record)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("HTTP stream=%t did not publish usage", stream)
		}
	}
}

type codeBuddyFixtureUsage struct{ records chan coreusage.Record }

func (s *codeBuddyFixtureUsage) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Provider == "codebuddy" {
		s.records <- record
	}
}
