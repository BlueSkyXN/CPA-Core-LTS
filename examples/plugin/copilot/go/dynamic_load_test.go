package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestDynamicCopilotPluginLoadsWithLoginCatalogExecutionAndCancel(t *testing.T) {
	if raceEnabled {
		t.Skip("c-shared dynamic loading starts a second Go runtime; validate it separately from -race")
	}
	if runtime.GOOS == "windows" {
		t.Skip("c-shared dynamic-load smoke is covered on Unix hosts")
	}
	dir := t.TempDir()
	extension := pluginhost.PluginExtension(runtime.GOOS)
	library := filepath.Join(dir, "copilot-v"+pluginVersion+extension)
	command := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	if output, errBuild := command.CombinedOutput(); errBuild != nil {
		t.Fatalf("c-shared build failed: %v\n%s", errBuild, output)
	}
	helper := exec.Command(os.Args[0], "-test.run=TestDynamicCopilotPluginLoadHelper")
	helper.Env = append(os.Environ(), "GO_WANT_COPILOT_DYNAMIC_LOAD=1", "COPILOT_PLUGIN_LIBRARY="+library)
	if output, errHelper := helper.CombinedOutput(); errHelper != nil {
		t.Fatalf("dynamic plugin load helper failed: %v\n%s", errHelper, output)
	}
}

func TestDynamicCopilotPluginLoadHelper(t *testing.T) {
	if os.Getenv("GO_WANT_COPILOT_DYNAMIC_LOAD") != "1" {
		return
	}
	library := os.Getenv("COPILOT_PLUGIN_LIBRARY")
	dir := filepath.Dir(library)
	var failStream, hangStream atomic.Bool
	started := make(chan struct{}, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/login/device/code":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":%q,"expires_in":900,"interval":0.01}`, "https://github.com/login/device")
		case "/login/oauth/access_token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"gho-fixture","token_type":"bearer","scope":"read:user"}`)
		case "/user":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"login":"octocat","id":1}`)
		case "/copilot_internal/v2/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"cc-fixture","refresh_in":1800,"expires_at":4102444800}`)
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-5-codex","name":"GPT-5 Codex","capabilities":{"limits":{"max_context_window_tokens":272000,"max_output_tokens":128000},"supports":{"tool_calls":true,"streaming":true}},"policy":{"state":"enabled"}},{"id":"text-embedding-3-small","name":"Embeddings","capabilities":{"type":"embeddings"}}]}`)
		case "/embeddings":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.125]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":5,"total_tokens":5}}`)
		case "/chat/completions":
			if hangStream.Load() {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hanging\"}}]}\n\n")
				w.(http.Flusher).Flush()
				started <- struct{}{}
				<-req.Context().Done()
				return
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
	rawConfig := []byte(fmt.Sprintf(`plugins:
  enabled: true
  dir: %q
  configs:
    copilot:
      enabled: true
      github_endpoint: %s
      github_api_endpoint: %s
      copilot_api_endpoint: %s
`, dir, upstream.URL, upstream.URL, upstream.URL))
	cfg, errConfig := config.ParseConfigBytes(rawConfig)
	if errConfig != nil {
		t.Fatal(errConfig)
	}
	host := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host.ApplyConfig(ctx, cfg)
	defer host.ShutdownAll()
	if !host.PluginRegistered(pluginIdentifier) {
		t.Fatalf("Copilot dynamic plugin did not register; loaded=%v", host.PluginLoaded(pluginIdentifier))
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
	// device 登录全流程：start → 立即 poll 为 pending（不打上游）→ 到点 poll 成功。
	start, handled, errStart := host.StartLogin(ctx, pluginIdentifier, "")
	if errStart != nil || !handled {
		t.Fatalf("StartLogin handled=%t err=%v", handled, errStart)
	}
	if start.Provider != pluginIdentifier || start.State == "" || start.URL == "" {
		t.Fatalf("start = %#v", start)
	}
	if pending, pollHandled, errPoll := host.PollLogin(ctx, pluginIdentifier, start.State); errPoll != nil || !pollHandled || pending.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("early poll = %#v, handled=%t, %v", pending, pollHandled, errPoll)
	}
	time.Sleep(50 * time.Millisecond)
	poll, pollHandled, errPoll := host.PollLogin(ctx, pluginIdentifier, start.State)
	if errPoll != nil || !pollHandled || poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll = %#v, handled=%t, %v", poll, pollHandled, errPoll)
	}
	if poll.Auth.Provider != pluginIdentifier || poll.Auth.Label != "octocat" {
		t.Fatalf("login auth = %#v", poll.Auth)
	}
	// Core 落盘按 FileName 派生 auth 文件；缺失会重现 UAT 抓到的 "missing id" 失败。
	if poll.Auth.FileName != "copilot-octocat.json" {
		t.Fatalf("login auth FileName = %q", poll.Auth.FileName)
	}
	var stored map[string]any
	if errUnmarshal := json.Unmarshal(poll.Auth.StorageJSON, &stored); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if stored["type"] != pluginIdentifier || stored["auth_mode"] != "oauth" || stored["github_token"] != "gho-fixture" {
		t.Fatalf("login storage = %#v", stored)
	}
	// 用登录产物构建运行时凭证，走真实 Core 目录与执行链路。
	manager := coreauth.NewManager(nil, nil, nil)
	host.RegisterExecutors(manager, nil)
	host.SetAuthManager(manager)
	executor, ok := manager.Executor(pluginIdentifier)
	if !ok {
		t.Fatal("native Copilot executor was not registered")
	}
	auth := &coreauth.Auth{ID: "fixture-auth", Index: "fixture-index", Provider: pluginIdentifier, Metadata: stored, Attributes: poll.Auth.Attributes}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || len(models.Models) != 2 || models.Models[0].ID != "gpt-5-codex" || models.Models[0].DisplayName != "GPT-5 Codex" {
		t.Fatalf("dynamic catalog failed: %v, models=%#v", models.Err, models.Models)
	}
	sink := &copilotUsageSink{records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin("native-copilot-fixture", sink)
	for _, stream := range []bool{false, true} {
		options := coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: fmt.Sprintf("fixture-%v", stream)}}
		request := coreexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"fixture"}]}`)}
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
				t.Fatalf("dynamic host usage attribution failed: %#v", record)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("dynamic host did not publish usage")
		}
	}
	// 截断流必须对下游暴露错误，且失败用量只记账一次。
	failStream.Store(true)
	failed, err := executor.ExecuteStream(ctx, auth,
		coreexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"messages":[{"role":"user","content":"fixture failure"}]}`)},
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
			t.Fatalf("dynamic host lost failed stream usage attribution: %#v", record)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic host did not publish failure usage")
	}
	select {
	case <-sink.records:
		t.Fatal("native usage was double counted")
	default:
	}
	// embeddings 请求以同名 format 透传到插件，usage 由 Core 正式解析发布。
	embResult, err := executor.Execute(ctx, auth,
		coreexecutor.Request{Model: "text-embedding-3-small", Payload: []byte(`{"model":"text-embedding-3-small","input":"fixture"}`)},
		coreexecutor.Options{SourceFormat: sdktranslator.FromString("embeddings"), Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "fixture-embeddings"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(embResult.Payload, []byte(`"embedding":[0.125]`)) {
		t.Fatal("dynamic embeddings response lost data")
	}
	select {
	case record := <-sink.records:
		if record.Failed || record.AuthIndex != "fixture-index" || record.Detail.InputTokens != 5 || record.Detail.TotalTokens != 5 || record.UsageProvenance != "provider_reported_unverified" {
			t.Fatalf("dynamic embeddings usage attribution failed: %#v", record)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic host did not publish embeddings usage")
	}
	// 取消：宿主控制面取消后流必须终结并携带错误。
	hangStream.Store(true)
	hangCtx, hangCancel := context.WithCancel(context.Background())
	defer hangCancel()
	hanging, err := executor.ExecuteStream(hangCtx, auth,
		coreexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"messages":[{"role":"user","content":"fixture cancel"}]}`)},
		coreexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "fixture-cancel"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("hanging upstream never started")
	}
	if errCancel := host.CancelProviderExecution(hangCtx, pluginIdentifier, pluginapi.CancelExecutionRequest{
		RequestID: "fixture-cancel", AuthIndex: "fixture-index",
	}); errCancel != nil {
		t.Fatalf("cancel provider execution failed: %v", errCancel)
	}
	cancelTerminated := false
	for chunk := range hanging.Chunks {
		cancelTerminated = cancelTerminated || chunk.Err != nil
	}
	if !cancelTerminated {
		t.Fatal("canceled stream terminated without surfacing the error")
	}
	failStream.Store(false)
}

type copilotUsageSink struct{ records chan coreusage.Record }

func (s *copilotUsageSink) HandleUsage(_ context.Context, record coreusage.Record) {
	if strings.EqualFold(record.Provider, pluginIdentifier) {
		s.records <- record
	}
}
