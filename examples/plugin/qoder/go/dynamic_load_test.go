package main

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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestDynamicPluginLoadsWithValidMetadataAndSchema5Capabilities(t *testing.T) {
	if raceEnabled {
		t.Skip("c-shared dynamic loading starts a second Go runtime; validate it separately from -race")
	}
	if runtime.GOOS == "windows" {
		t.Skip("c-shared dynamic-load smoke is covered on Unix hosts")
	}
	dir := t.TempDir()
	extension := pluginhost.PluginExtension(runtime.GOOS)
	library := filepath.Join(dir, "qoder-v"+pluginVersion+extension)
	command := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	if output, errBuild := command.CombinedOutput(); errBuild != nil {
		t.Fatalf("c-shared build failed: %v\n%s", errBuild, output)
	}
	helper := exec.Command(os.Args[0], "-test.run=TestDynamicPluginLoadHelper")
	helper.Env = append(os.Environ(), "GO_WANT_QODER_DYNAMIC_LOAD=1", "QODER_PLUGIN_LIBRARY="+library)
	if output, errHelper := helper.CombinedOutput(); errHelper != nil {
		t.Fatalf("dynamic plugin load helper failed: %v\n%s", errHelper, output)
	}
}

func TestDynamicPluginLoadHelper(t *testing.T) {
	if os.Getenv("GO_WANT_QODER_DYNAMIC_LOAD") != "1" {
		return
	}
	library := os.Getenv("QODER_PLUGIN_LIBRARY")
	dir := filepath.Dir(library)
	var failStream atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/jobToken/exchange":
			exchangeFixture(w)
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"qmodel_38max","display_name":"Qwen3.8-Max"}]}`)
		case "/chat":
			if req.Header.Get("Authorization") != "Bearer jt-fixture" {
				t.Error("dynamic bridge lost auth")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"native-ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			if !failStream.Load() {
				fmt.Fprint(w, "data: [DONE]\n\n")
			}
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()
	rawConfig := []byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    qoder:\n      enabled: true\n      transport: direct_openai\n      runner_command: /not-installed\n      direct_catalog_format: openai\n      openapi_endpoint: %s\n      direct_endpoint: %s/chat\n      direct_models_endpoint: %s/models\n", dir, upstream.URL, upstream.URL, upstream.URL))
	cfg, errConfig := config.ParseConfigBytes(rawConfig)
	if errConfig != nil {
		t.Fatal(errConfig)
	}
	host := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host.ApplyConfig(ctx, cfg)
	defer host.ShutdownAll()
	if !host.PluginRegistered("qoder") {
		t.Fatalf("Qoder dynamic plugin did not register; loaded=%v", host.PluginLoaded("qoder"))
	}
	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("registered plugins = %#v", plugins)
	}
	metadata := plugins[0].Metadata
	if metadata.Name != pluginName || metadata.Version != pluginVersion || metadata.Author == "" || metadata.GitHubRepository != "https://github.com/BlueSkyXN/CPA-Core-LTS" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if !plugins[0].SupportsOAuth || plugins[0].OAuthProvider != pluginIdentifier {
		t.Fatalf("auth registration = %#v", plugins[0])
	}
	manager := coreauth.NewManager(nil, nil, nil)
	host.RegisterExecutors(manager, nil)
	executor, ok := manager.Executor(pluginIdentifier)
	if !ok {
		t.Fatal("native Qoder executor was not registered")
	}
	auth := &coreauth.Auth{ID: "fixture-auth", Index: "fixture-index", Provider: pluginIdentifier, Metadata: map[string]any{"type": "qoder", "auth_mode": "pat", "pat": "pt-fixture", "transport": "direct_openai"}}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || len(models.Models) != 1 || models.Models[0].DisplayName != "Qwen3.8-Max" {
		t.Fatalf("dynamic catalog failed: %v", models.Err)
	}
	sink := &nativeUsageSink{records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin("native-qoder-fixture", sink)
	for _, stream := range []bool{false, true} {
		options := coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: fmt.Sprintf("fixture-%v", stream)}}
		request := coreexecutor.Request{Model: "qmodel_38max", Payload: []byte(`{"model":"qmodel_38max","messages":[{"role":"user","content":"fixture"}]}`)}
		var output []byte
		if stream {
			result, err := executor.ExecuteStream(ctx, auth, request, options)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output = append(output, chunk.Payload...)
			}
		} else {
			result, err := executor.Execute(ctx, auth, request, options)
			if err != nil {
				t.Fatal(err)
			}
			output = result.Payload
		}
		if !bytes.Contains(output, []byte("native-ok")) || !bytes.Contains(output, []byte(`"total_tokens":12`)) {
			t.Fatal("dynamic host response lost content or usage")
		}
		select {
		case record := <-sink.records:
			if record.Failed || record.AuthIndex != "fixture-index" || record.Detail.TotalTokens != 12 || record.UsageProvenance != "provider_reported_unverified" {
				t.Fatal("dynamic host usage attribution failed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("dynamic host did not publish usage")
		}
	}
	failStream.Store(true)
	failed, err := executor.ExecuteStream(ctx, auth,
		coreexecutor.Request{Model: "qmodel_38max", Payload: []byte(`{"messages":[{"role":"user","content":"fixture failure"}]}`)},
		coreexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "fixture-failed"}})
	if err != nil {
		t.Fatal(err)
	}
	sawError := false
	for chunk := range failed.Chunks {
		sawError = sawError || chunk.Err != nil
	}
	if !sawError {
		t.Fatal("dynamic host hid the truncated stream failure")
	}
	select {
	case record := <-sink.records:
		if !record.Failed || record.AuthIndex != "fixture-index" || record.Detail.TotalTokens != 12 || record.UsageProvenance != "provider_reported_unverified" {
			t.Fatal("dynamic host lost failed stream usage attribution")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic host did not publish failure usage")
	}
	select {
	case <-sink.records:
		t.Fatal("native usage was double counted")
	default:
	}
}

type nativeUsageSink struct{ records chan coreusage.Record }

func (s *nativeUsageSink) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Provider == pluginIdentifier {
		s.records <- record
	}
}
