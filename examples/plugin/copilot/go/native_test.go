package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本地 HTTP 假上游 + Core callback 线格式，覆盖真实 HTTP 分片与取消。
type copilotFixtureHost struct {
	mu          sync.Mutex
	client      *http.Client
	streams     map[string]io.ReadCloser
	requests    []hostHTTPRequest
	emitted     [][]byte
	closed      chan pluginStreamCloseRequest
	closeCounts map[string]int
	next        int
}

func (h *copilotFixtureHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream:
		req := payload.(hostHTTPRequest)
		if req.HostCallbackID != "fixture-callback" {
			return nil, fmt.Errorf("missing callback ownership")
		}
		h.mu.Lock()
		h.requests = append(h.requests, req)
		h.mu.Unlock()
		request, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			return nil, err
		}
		request.Header = req.Headers.Clone()
		response, err := h.client.Do(request)
		if err != nil {
			return nil, err
		}
		if method == pluginabi.MethodHostHTTPDo {
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			return json.Marshal(hostHTTPResponse{StatusCode: response.StatusCode, Headers: response.Header, Body: body})
		}
		h.mu.Lock()
		h.next++
		id := fmt.Sprint(h.next)
		h.streams[id] = response.Body
		h.mu.Unlock()
		return json.Marshal(hostHTTPStreamResponse{StatusCode: response.StatusCode, Headers: response.Header, StreamID: id})
	case pluginabi.MethodHostHTTPStreamRead:
		id := payload.(map[string]string)["stream_id"]
		h.mu.Lock()
		body := h.streams[id]
		h.mu.Unlock()
		if body == nil {
			return json.Marshal(hostHTTPStreamReadResponse{Done: true})
		}
		buffer := make([]byte, 17) // 故意把 UTF-8、JSON 和 SSE 分隔符拆开。
		n, err := body.Read(buffer)
		chunk := hostHTTPStreamReadResponse{Payload: buffer[:n], Done: err == io.EOF}
		if err != nil && err != io.EOF {
			chunk.Error = "fixture stream closed"
		}
		return json.Marshal(chunk)
	case pluginabi.MethodHostHTTPStreamClose:
		id := payload.(map[string]string)["stream_id"]
		h.mu.Lock()
		body := h.streams[id]
		delete(h.streams, id)
		h.closeCounts[id]++
		h.mu.Unlock()
		if body != nil {
			_ = body.Close()
		}
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamEmit:
		value := payload.(pluginStreamEmitRequest)
		h.mu.Lock()
		h.emitted = append(h.emitted, bytes.Clone(value.Payload))
		h.mu.Unlock()
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamClose:
		h.closed <- payload.(pluginStreamCloseRequest)
		return json.RawMessage(`{}`), nil
	default:
		return nil, fmt.Errorf("unexpected callback")
	}
}

func (h *copilotFixtureHost) recordedRequests() []hostHTTPRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hostHTTPRequest(nil), h.requests...)
}

func copilotFixture(t *testing.T, handler http.HandlerFunc) (*pluginRuntime, *copilotFixtureHost) {
	t.Helper()
	server := httptest.NewServer(handler)
	host := &copilotFixtureHost{client: &http.Client{Timeout: 5 * time.Second}, streams: make(map[string]io.ReadCloser), closeCounts: make(map[string]int), closed: make(chan pluginStreamCloseRequest, 10)}
	runtime := newPluginRuntime(host)
	runtime.config.GitHubEndpoint = server.URL
	runtime.config.GitHubAPIEndpoint = server.URL
	runtime.config.CopilotAPIEndpoint = server.URL
	t.Cleanup(func() { runtime.shutdown(); server.Close() })
	return runtime, host
}

func copilotTokenFixture(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"token":"cc-fixture","refresh_in":1800,"expires_at":4102444800}`)
}

func serveCopilotModels(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, copilotModelsPayload())
}

func copilotReq(id string, stream bool) rpcExecutorRequest {
	return rpcExecutorRequest{HostCallbackID: "fixture-callback", StreamID: "downstream-" + id, ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: id, ExecutionSessionID: "session", CallerScope: "caller", AuthID: "auth", AuthIndex: "index", Model: "gpt-5-codex", Format: "chat-completions", Stream: stream,
		StorageJSON: json.RawMessage(`{"type":"copilot","auth_mode":"github_token","github_token":"ghp-fixture"}`),
		Payload:     []byte(`{"messages":[{"role":"system","content":"keep system"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required","temperature":0.25}`),
	}}
}

func waitCopilotClose(t *testing.T, host *copilotFixtureHost) pluginStreamCloseRequest {
	t.Helper()
	select {
	case result := <-host.closed:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("copilot stream did not finish")
		return pluginStreamCloseRequest{}
	}
}

func TestNativeDirectPreservesRequestAndUsageWithoutRunner(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var chats, tokens atomic.Int32
			runtime, host := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/copilot_internal/v2/token":
					tokens.Add(1)
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					chats.Add(1)
					if request.Header.Get("Authorization") != "Bearer cc-fixture" {
						t.Error("wrong upstream credential")
					}
					if request.Header.Get("Copilot-Integration-Id") != "vscode-chat" || request.Header.Get("Openai-Intent") != "conversation-agent" || request.Header.Get("Editor-Version") != "vscode/1.110.1" || request.Header.Get("Editor-Plugin-Version") != "copilot-chat/0.38.2" || request.Header.Get("User-Agent") != "GitHubCopilotChat/0.38.2" || request.Header.Get("X-Github-Api-Version") != "2025-10-01" || request.Header.Get("X-Request-Id") != "one" {
						t.Errorf("identity headers changed: %v", request.Header)
					}
					var body map[string]any
					_ = json.NewDecoder(request.Body).Decode(&body)
					options, _ := body["stream_options"].(map[string]any)
					messages := body["messages"].([]any)
					if body["model"] != "gpt-5-codex" || body["stream"] != true || options["include_usage"] != true || len(messages) != 2 || messages[0].(map[string]any)["content"] != "keep system" || body["tool_choice"] != "required" || body["temperature"] != 0.25 {
						t.Error("request semantics changed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"思考\"}}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"完成\"},\"finish_reason\":\"length\"}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":60,\"cache_creation_tokens\":5},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n")
					fmt.Fprint(w, "data: [DONE]\n\n")
				default:
					t.Errorf("unexpected upstream path %s", request.URL.Path)
				}
			})
			req := copilotReq("one", stream)
			raw, _ := json.Marshal(req)
			var payload []byte
			if stream {
				if _, err := runtime.executeStream(raw); err != nil {
					t.Fatal(err)
				}
				if result := waitCopilotClose(t, host); result.Error != "" {
					t.Fatal(result.Error)
				}
				host.mu.Lock()
				payload = bytes.Join(host.emitted, []byte("\n"))
				host.mu.Unlock()
			} else {
				response, err := runtime.execute(raw)
				if err != nil {
					t.Fatal(err)
				}
				payload = response.Payload
			}
			for _, expected := range []string{"完成", "思考", `"total_tokens":120`, `"prompt_tokens":100`, `"completion_tokens":20`, `"cached_tokens":60`, `"cache_creation_tokens":5`, `"reasoning_tokens":10`, `"finish_reason":"length"`} {
				if !bytes.Contains(payload, []byte(expected)) {
					t.Errorf("missing %s", expected)
				}
			}
			if chats.Load() != 1 || tokens.Load() != 1 {
				t.Fatalf("chats=%d tokens=%d", chats.Load(), tokens.Load())
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if len(host.streams) != 0 || host.closeCounts["1"] != 1 {
				t.Fatal("upstream stream was not closed exactly once")
			}
		})
	}
}

func TestNativeTokenSingleFlightExpiryMarginAndRotation(t *testing.T) {
	var exchanges atomic.Int32
	runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/copilot_internal/v2/token" {
			t.Errorf("unexpected token path %s", request.URL.Path)
		}
		if !strings.HasPrefix(request.Header.Get("Authorization"), "token ghp-") {
			t.Error("token exchange lost the GitHub credential")
		}
		exchanges.Add(1)
		time.Sleep(20 * time.Millisecond)
		copilotTokenFixture(w)
	})
	auth := copilotAuth{Type: pluginIdentifier, AuthMode: "github_token", GitHubToken: "ghp-fixture"}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := runtime.cachedCopilotToken(auth, "fixture-callback", runtime.loadedConfig(), "")
			if err != nil || state.Token != "cc-fixture" {
				t.Error("concurrent token acquisition failed")
				return
			}
			remaining := time.Until(state.ExpiresAt)
			if remaining < 29*time.Minute || remaining > 31*time.Minute {
				t.Error("refresh_in seconds were misread")
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	// 30 秒提前过期边界：剩余寿命低于 30s 的缓存必须重换。
	runtime.mu.Lock()
	for key, entry := range runtime.tokenCache {
		entry.State.ExpiresAt = time.Now().Add(20 * time.Second)
		runtime.tokenCache[key] = entry
	}
	runtime.mu.Unlock()
	if _, err := runtime.cachedCopilotToken(auth, "fixture-callback", runtime.loadedConfig(), ""); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatalf("near-expiry cache was reused: exchanges=%d", exchanges.Load())
	}
	// rejectedToken 命中新鲜缓存也必须强制重换。
	if _, err := runtime.cachedCopilotToken(auth, "fixture-callback", runtime.loadedConfig(), "cc-fixture"); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 3 {
		t.Fatalf("rejected token did not force re-exchange: exchanges=%d", exchanges.Load())
	}
	// 换凭证后不得复用旧 token。
	auth.GitHubToken = "ghp-second-account"
	if _, err := runtime.cachedCopilotToken(auth, "fixture-callback", runtime.loadedConfig(), ""); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 4 {
		t.Fatalf("replaced credential reused stale token: exchanges=%d", exchanges.Load())
	}
}

func copilotModelsPayload() string {
	return `{"object":"list","data":[
		{"id":"gpt-5-codex","name":"GPT-5 Codex","preview":false,"vendor":"openai","version":"2025-09-01","model_picker_enabled":true,
		 "capabilities":{"family":"gpt","tokenizer":"o200k","type":"chat","limits":{"max_context_window_tokens":272000,"max_output_tokens":128000,"max_prompt_tokens":272000},
		 "supports":{"tool_calls":true,"parallel_tool_calls":true,"streaming":true,"structured_outputs":true,"vision":true,"adaptive_thinking":true,"max_thinking_budget":32000,"min_thinking_budget":0}},
		 "policy":{"state":"enabled","terms":"https://example.invalid/terms"},"supported_endpoints":["/chat/completions"]},
		{"id":"claude-sonnet-4","name":"Claude Sonnet 4","policy":{"state":"disabled"}},
		{"id":"gpt-4.1-mini","name":"GPT-4.1 Mini"},
		{"id":"text-embedding-3-small","name":"Embeddings","capabilities":{"type":"embeddings"}},
		{"id":"gpt-5-codex","name":"duplicate"}
	]}`
}

func TestNativeCatalogPolicyPrefixExclusionAndCache(t *testing.T) {
	var catalogs atomic.Int32
	runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			catalogs.Add(1)
			if request.Header.Get("Authorization") != "Bearer cc-fixture" {
				t.Error("catalog lost the Copilot token")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, copilotModelsPayload())
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	})
	auth := copilotAuth{Type: pluginIdentifier, AuthMode: "github_token", GitHubToken: "ghp-fixture"}
	models, err := runtime.nativeModels(auth, "fixture-callback", runtime.loadedConfig())
	if err != nil {
		t.Fatal(err)
	}
	// policy.state=disabled 与重复 id 被过滤；未配置排除前缀时其余模型全部发布。
	if len(models) != 3 {
		t.Fatalf("models = %#v", models)
	}
	codex := models[0]
	if codex.ID != "gpt-5-codex" || codex.DisplayName != "GPT-5 Codex" || codex.ContextLength != 272000 || codex.InputTokenLimit != 272000 || codex.OutputTokenLimit != 128000 || codex.MaxCompletionTokens != 128000 {
		t.Fatalf("catalog limits lost: %#v", codex)
	}
	if strings.Join(codex.SupportedInputModalities, ",") != "text,image" || codex.Thinking == nil || !codex.Thinking.ZeroAllowed || codex.Thinking.Max != 32000 {
		t.Fatalf("vision/thinking capability lost: %#v", codex)
	}
	// 缓存复用：外部改动返回值不影响缓存。
	models[0].SupportedInputModalities[0] = "corrupted"
	models, err = runtime.nativeModels(auth, "fixture-callback", runtime.loadedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if catalogs.Load() != 1 || models[0].SupportedInputModalities[0] != "text" {
		t.Fatal("catalog cache is mutable or not reused")
	}
	// 不同凭证目录互相隔离。
	other := copilotAuth{Type: pluginIdentifier, AuthMode: "github_token", GitHubToken: "ghp-second-account"}
	if _, err := runtime.nativeModels(other, "fixture-callback", runtime.loadedConfig()); err != nil {
		t.Fatal(err)
	}
	if catalogs.Load() != 2 {
		t.Fatal("account catalog leaked")
	}
}

func TestNativeCatalogRespectsExcludedPrefixConfig(t *testing.T) {
	// 全部命中排除前缀时目录必须显式失败，不允许伪造静态模型。
	if _, err := parseNativeCatalog([]byte(`{"data":[{"id":"o3-mini"},{"id":"gpt-5"}]}`), []string{"o3", "gpt"}); err == nil {
		t.Fatal("excluded-only catalog published models")
	}
	entry, err := parseNativeCatalog([]byte(`{"data":[{"id":"o3-mini"},{"id":"gpt-5"}]}`), []string{"o3"})
	if err != nil || len(entry.models) != 1 || entry.models[0].ID != "gpt-5" {
		t.Fatalf("prefix exclusion changed exact IDs: %#v, %v", entry.models, err)
	}
	if _, err := parseNativeCatalog([]byte(`{"data":[{"id":"disabled","policy":{"state":"unavailable"}}]}`), nil); err == nil {
		t.Fatal("non-enabled policy state was published")
	}
	if _, err := parseNativeCatalog([]byte(`{"data":[]}`), nil); err == nil {
		t.Fatal("empty catalog published")
	}
	if _, err := parseNativeCatalog([]byte(`not-json`), nil); err == nil {
		t.Fatal("invalid catalog schema accepted")
	}
}

func TestNativeExecutionRefreshesTokenOnceOn401(t *testing.T) {
	for _, scenario := range []struct {
		name                string
		status              int
		wantChats, want_tok int32
		code                string
	}{
		{"expired", http.StatusUnauthorized, 2, 2, ""},
		{"forbidden", http.StatusForbidden, 1, 1, "upstream_forbidden"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var chats, tokens atomic.Int32
			runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/copilot_internal/v2/token":
					if tokens.Add(1) == 1 {
						fmt.Fprint(w, `{"token":"cc-stale","refresh_in":1800}`)
						return
					}
					fmt.Fprint(w, `{"token":"cc-fresh","refresh_in":1800}`)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					if chats.Add(1) == 1 && scenario.status != http.StatusOK {
						w.WriteHeader(scenario.status)
						fmt.Fprint(w, `{"message":"rejected"}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				default:
					t.Errorf("unexpected path %s", request.URL.Path)
				}
			})
			raw, _ := json.Marshal(copilotReq("r", false))
			response, err := runtime.execute(raw)
			if scenario.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(response.Payload, []byte("ok")) {
					t.Fatal("retry lost the successful response")
				}
			} else if call, ok := err.(*pluginCallError); !ok || call.code != scenario.code {
				t.Fatalf("wrong error: %v", err)
			}
			if chats.Load() != scenario.wantChats || tokens.Load() != scenario.want_tok {
				t.Fatalf("chats=%d tokens=%d", chats.Load(), tokens.Load())
			}
		})
	}
}

func TestNativeCatalogRefreshesTokenOn401(t *testing.T) {
	var tokens, models atomic.Int32
	runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/copilot_internal/v2/token":
			if tokens.Add(1) == 1 {
				fmt.Fprint(w, `{"token":"cc-stale","refresh_in":1800}`)
				return
			}
			fmt.Fprint(w, `{"token":"cc-fresh","refresh_in":1800}`)
		case "/models":
			if models.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{}`)
				return
			}
			fmt.Fprint(w, copilotModelsPayload())
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	})
	auth := copilotAuth{Type: pluginIdentifier, AuthMode: "github_token", GitHubToken: "ghp-fixture"}
	modelsList, err := runtime.nativeModels(auth, "fixture-callback", runtime.loadedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(modelsList) == 0 || tokens.Load() != 2 || models.Load() != 2 {
		t.Fatalf("catalog 401 retry failed: models=%d tokens=%d catalogs=%d", len(modelsList), tokens.Load(), models.Load())
	}
}

func TestNativeFailedStreamPreservesReportedUsage(t *testing.T) {
	for _, ending := range []string{"", "data: {\"error\":{\"message\":\"upstream exploded\"}}\n\n"} {
		t.Run(fmt.Sprint(len(ending)), func(t *testing.T) {
			runtime, host := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"+ending)
				default:
					t.Errorf("unexpected path %s", request.URL.Path)
				}
			})
			if _, err := runtime.executeNativeStream(copilotReq("failed-usage", true)); err != nil {
				t.Fatal(err)
			}
			if result := waitCopilotClose(t, host); result.Error == "" {
				t.Fatal("failed upstream reported success")
			}
			host.mu.Lock()
			output := bytes.Join(host.emitted, nil)
			host.mu.Unlock()
			if bytes.Count(output, []byte(`"total_tokens":12`)) != 1 || !bytes.Contains(output, []byte(`"choices":[]`)) || bytes.Contains(output, []byte(`"finish_reason":"stop"`)) {
				t.Fatal("failed stream lost or duplicated usage, or fabricated successful completion")
			}
			if bytes.Contains(output, []byte("upstream exploded")) {
				t.Fatal("upstream error body leaked to the downstream stream")
			}
		})
	}
}

func TestNativeRejectsUnknownModelAndBadFrames(t *testing.T) {
	for _, frame := range []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"data: {bad}\n\n",
		"data: [DONE]\n\n",
	} {
		t.Run(fmt.Sprint(len(frame)), func(t *testing.T) {
			var calls atomic.Int32
			runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, frame)
				default:
					t.Errorf("unexpected path %s", request.URL.Path)
				}
			})
			req := copilotReq("bad", false)
			raw, _ := json.Marshal(req)
			if _, err := runtime.execute(raw); err == nil || calls.Load() != 1 {
				t.Fatalf("invalid frame accepted: %v", err)
			}
			req.Model = "not-in-catalog"
			raw, _ = json.Marshal(req)
			_, err := runtime.execute(raw)
			if err == nil || calls.Load() != 1 {
				t.Fatal("unknown model contacted inference")
			}
			if call, ok := err.(*pluginCallError); !ok || call.code != "unsupported_model" {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

func TestNativeRequestPayloadGuards(t *testing.T) {
	req := copilotReq("guard", false).ExecutorRequest
	req.Format = "responses"
	if _, err := nativeRequestPayload(req); err == nil {
		t.Fatal("non chat-completions format accepted")
	}
	req = copilotReq("guard", false).ExecutorRequest
	req.Payload = []byte(`{"messages":[{"role":"user","content":"hi"}],"n":2}`)
	if _, err := nativeRequestPayload(req); err == nil {
		t.Fatal("n>1 accepted")
	}
	req.Payload = []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	raw, err := nativeRequestPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["stream"] != true {
		t.Fatal("stream flag lost")
	}
}

func TestNativeCancelOwnershipQuiesceAndContinuation(t *testing.T) {
	started := make(chan struct{}, 4)
	runtime, host := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			serveCopilotModels(w)
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-request.Context().Done()
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	})
	req := copilotReq("one", true)
	raw, _ := json.Marshal(req)
	if _, err := runtime.executeStream(raw); err != nil {
		t.Fatal(err)
	}
	<-started
	conflict := copilotReq("two", true)
	conflictRaw, _ := json.Marshal(conflict)
	if _, err := runtime.executeStream(conflictRaw); err == nil {
		t.Fatal("same session admitted concurrent turn")
	}
	_ = runtime.cancelExecution(pluginapi.CancelExecutionRequest{RequestID: "one", CallerScope: "wrong"})
	runtime.mu.Lock()
	canceled := runtime.nativeActive["one"].isCanceled()
	runtime.mu.Unlock()
	if canceled {
		t.Fatal("foreign caller canceled request")
	}
	_ = runtime.cancelExecution(pluginapi.CancelExecutionRequest{RequestID: "one", CallerScope: "caller", AuthIndex: "index"})
	if close := waitCopilotClose(t, host); close.ErrorCode != "request_cancelled" {
		t.Fatal("cancellation lost")
	}
	if _, err := runtime.executeStream(conflictRaw); err != nil {
		t.Fatal(err)
	}
	<-started
	runtime.quiesce()
	if close := waitCopilotClose(t, host); close.ErrorCode != "request_cancelled" {
		t.Fatal("quiesce did not cancel")
	}
	if _, err := runtime.executeStream(raw); err == nil {
		t.Fatal("quiesced plugin admitted request")
	}
}

func TestNativeDecoderToolFragmentsAndPartialUsage(t *testing.T) {
	decoder := newNativeDecoder("r", "m", nil)
	for _, raw := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"New "}}]}}],"usage":{"prompt_tokens":10}}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"York\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":2}}`,
		`[DONE]`,
	} {
		if _, err := decoder.data([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := decoder.finish(); err != nil {
		t.Fatal(err)
	}
	response, err := decoder.projection.nonStreamResponse()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`New York`, `"name":"lookup"`, `"id":"call-1"`, `"total_tokens":12`} {
		if !bytes.Contains(response.Payload, []byte(expected)) {
			t.Errorf("missing %s", expected)
		}
	}
}

func TestNativeReadinessNeedsNoRunner(t *testing.T) {
	cfg, err := decodePluginConfig([]byte("account_type: business\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CopilotAPIEndpoint != "https://api.business.githubcopilot.com" {
		t.Fatalf("business endpoint = %s", cfg.CopilotAPIEndpoint)
	}
	runtime := newPluginRuntime(nil)
	runtime.config = cfg
	state := runtime.readiness(pluginapi.ReadinessRequest{Purpose: pluginapi.ReadinessPurposeAdmission, StorageJSON: copilotReq("r", false).StorageJSON})
	if !state.Ready {
		t.Fatalf("native configuration rejected without runner: %#v", state)
	}
	invalid := runtime.readiness(pluginapi.ReadinessRequest{Purpose: pluginapi.ReadinessPurposeAdmission, StorageJSON: json.RawMessage(`{"type":"copilot","auth_mode":"github_token"}`)})
	if invalid.Ready {
		t.Fatal("missing token passed readiness")
	}
}

func TestNativeEmbeddingsExecutesThroughCatalog(t *testing.T) {
	var embeddings atomic.Int32
	runtime, _ := copilotFixture(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			serveCopilotModels(w)
		case "/embeddings":
			embeddings.Add(1)
			if request.Header.Get("Authorization") != "Bearer cc-fixture" {
				t.Error("wrong embeddings credential")
			}
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			// 字符串 input 必须归一化为数组后再发上游（Copilot 网关约束）。
			if body["model"] != "text-embedding-3-small" {
				t.Errorf("embeddings model = %#v", body["model"])
			}
			if input, ok := body["input"].([]any); !ok || len(input) != 1 || input[0] != "fixture text" {
				t.Errorf("embeddings input not normalized to array: %#v", body["input"])
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.125]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":5,"total_tokens":5}}`)
		default:
			t.Errorf("unexpected upstream path %q", request.URL.Path)
		}
	})
	// 目录元数据：embeddings 模型声明 embeddings 方法，chat 模型声明 chat。
	auth, errAuth := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"github_token","github_token":"ghp-fixture"}`))
	if errAuth != nil {
		t.Fatal(errAuth)
	}
	models, errModels := runtime.nativeModels(auth, "fixture-callback", runtime.loadedConfig())
	if errModels != nil {
		t.Fatal(errModels)
	}
	methods := map[string]string{}
	for _, model := range models {
		methods[model.ID] = strings.Join(model.SupportedGenerationMethods, ",")
	}
	if methods["text-embedding-3-small"] != "embeddings" || methods["gpt-5-codex"] != "chat" {
		t.Fatalf("generation methods = %#v", methods)
	}
	request := copilotReq("emb-ok", false)
	request.Format = "embeddings"
	request.Model = "text-embedding-3-small"
	request.Payload = []byte(`{"model":"text-embedding-3-small","input":"fixture text"}`)
	raw, _ := json.Marshal(request)
	response, err := runtime.execute(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response.Payload, []byte(`"embedding":[0.125]`)) || !bytes.Contains(response.Payload, []byte(`"total_tokens":5`)) {
		t.Fatalf("embeddings response lost data: %s", response.Payload)
	}
	// input 缺失必须拒绝，且不触达上游。
	request.Payload = []byte(`{"model":"text-embedding-3-small"}`)
	raw, _ = json.Marshal(request)
	if _, err := runtime.execute(raw); err == nil {
		t.Fatal("missing input was accepted")
	}
	// 目录外模型必须拒绝。
	request.Model = "not-in-catalog"
	request.Payload = []byte(`{"model":"not-in-catalog","input":"x"}`)
	raw, _ = json.Marshal(request)
	if _, err := runtime.execute(raw); err == nil {
		t.Fatal("catalog-missing model was accepted")
	}
	if embeddings.Load() != 1 {
		t.Fatalf("embeddings upstream calls = %d", embeddings.Load())
	}
}
