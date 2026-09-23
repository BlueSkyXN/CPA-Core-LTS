package main

import (
	"bytes"
	"encoding/base64"
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

// 本地 HTTP 上游 + Core callback 线格式，覆盖真实 HTTP 分片和取消。
type nativeFixtureHost struct {
	mu          sync.Mutex
	client      *http.Client
	streams     map[string]io.ReadCloser
	requests    []hostHTTPRequest
	emitted     [][]byte
	closed      chan pluginStreamCloseRequest
	closeCounts map[string]int
	next        int
}

func (h *nativeFixtureHost) Call(method string, payload any) (json.RawMessage, error) {
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

func nativeFixture(t *testing.T, handler http.HandlerFunc) (*pluginRuntime, *nativeFixtureHost) {
	t.Helper()
	server := httptest.NewServer(handler)
	host := &nativeFixtureHost{client: &http.Client{Timeout: 5 * time.Second}, streams: make(map[string]io.ReadCloser), closeCounts: make(map[string]int), closed: make(chan pluginStreamCloseRequest, 10)}
	r := newPluginRuntime(host)
	r.config.Transport = "direct_openai"
	r.config.DirectModelsEndpoint = server.URL + "/models"
	r.config.DirectEndpoint = server.URL + "/model/v1/chat/completions"
	r.config.OpenAPIEndpoint = server.URL
	r.config.DirectModels = []directModelConfig{{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"}}
	t.Cleanup(func() { r.shutdown(); server.Close() })
	return r, host
}

func nativeReq(id string, stream bool) rpcExecutorRequest {
	return rpcExecutorRequest{HostCallbackID: "fixture-callback", StreamID: "downstream-" + id, ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: id, ExecutionSessionID: "session", CallerScope: "caller", AuthID: "auth", AuthIndex: "index", Model: "qmodel_38max", Format: "chat-completions", Stream: stream,
		StorageJSON: json.RawMessage(`{"type":"qoder","auth_mode":"pat","pat":"pt-fixture","transport":"direct_openai"}`),
		Payload:     []byte(`{"messages":[{"role":"system","content":"keep system"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required","temperature":0.25}`),
	}}
}

func exchangeFixture(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"token":"jt-fixture","refresh_token":"jrt-fixture","expires_in":86400000}`)
}

func waitNativeClose(t *testing.T, host *nativeFixtureHost) pluginStreamCloseRequest {
	t.Helper()
	select {
	case result := <-host.closed:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("native stream did not finish")
		return pluginStreamCloseRequest{}
	}
}

func TestNativeDirectPreservesRequestAndLateUsageWithoutRunner(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var chats atomic.Int32
			r, host := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v1/jobToken/exchange" {
					exchangeFixture(w)
					return
				}
				chats.Add(1)
				if request.Header.Get("Authorization") != "Bearer jt-fixture" {
					t.Error("wrong upstream credential")
				}
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				messages := body["messages"].([]any)
				if len(messages) != 2 || messages[0].(map[string]any)["content"] != "keep system" || len(messages[1].(map[string]any)["content"].([]any)) != 2 || body["tool_choice"] != "required" || body["temperature"] != 0.25 || body["stream"] != true {
					t.Error("request semantics changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"思考\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"完成\"},\"finish_reason\":\"length\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"raw_usage\":{\"input_tokens\":100,\"output_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":60,\"cache_creation_tokens\":5},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			})
			req := nativeReq("one", stream)
			raw, _ := json.Marshal(req)
			var payload []byte
			if stream {
				if _, err := r.executeStream(raw); err != nil {
					t.Fatal(err)
				}
				if result := waitNativeClose(t, host); result.Error != "" {
					t.Fatal(result.Error)
				}
				host.mu.Lock()
				payload = bytes.Join(host.emitted, []byte("\n"))
				host.mu.Unlock()
			} else {
				response, err := r.execute(raw)
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
			if chats.Load() != 1 {
				t.Fatal("generation repeated")
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if len(host.streams) != 0 || host.closeCounts["1"] != 1 {
				t.Fatal("upstream stream was not closed exactly once")
			}
		})
	}
}

func TestNativeDecoderToolFragmentsAndPartialUsage(t *testing.T) {
	d := newNativeDecoder("r", "m", nil)
	for _, raw := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"New "}}]}}],"usage":{"prompt_tokens":10}}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"York\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":2}}`,
		`[DONE]`,
	} {
		if _, err := d.data([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.finish(); err != nil {
		t.Fatal(err)
	}
	response, err := d.projection.nonStreamResponse()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`New York`, `"name":"lookup"`, `"id":"call-1"`, `"total_tokens":12`} {
		if !bytes.Contains(response.Payload, []byte(expected)) {
			t.Errorf("missing %s", expected)
		}
	}
}

func TestNativeTokenSingleFlightRotationAndMillis(t *testing.T) {
	var exchanges, refreshes atomic.Int32
	r, _ := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/refresh") {
			refreshes.Add(1)
			fmt.Fprint(w, `{"token":"jt-refreshed","refresh_token":"jrt-refreshed","expires_in":86400000}`)
			return
		}
		exchanges.Add(1)
		time.Sleep(20 * time.Millisecond)
		exchangeFixture(w)
	})
	auth := qoderAuth{AuthMode: "pat", PAT: "pt-fixture"}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := r.nativeToken(auth, "fixture-callback", r.loadedConfig(), "")
			if err != nil || state.Token != "jt-fixture" {
				t.Error("concurrent token acquisition failed")
			}
			remaining := time.Until(state.ExpiresAt)
			if remaining < 23*time.Hour || remaining > 25*time.Hour {
				t.Error("wrong expiry unit")
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := r.nativeToken(auth, "fixture-callback", r.loadedConfig(), "jt-fixture")
			if err != nil || state.Token != "jt-refreshed" {
				t.Error("concurrent refresh failed")
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes=%d", refreshes.Load())
	}
	auth.PAT = "pt-replaced"
	if _, err := r.nativeToken(auth, "fixture-callback", r.loadedConfig(), ""); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatal("replaced credential reused stale token")
	}
}

func TestNativeRefreshOnlyExchangesOnExplicitRejection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		wantExchange int32
		wantError    bool
	}{
		{"transient", 503, `{}`, 1, true},
		{"rate_limit", 429, `{}`, 1, true},
		{"forbidden", 403, `{"code":"policy_denied"}`, 1, true},
		{"invalid_json", 200, `invalid`, 1, true},
		{"expired", 401, `{"code":"TOKEN_EXPIRE"}`, 2, false},
		{"retain_refresh", 200, `{"token":"jt-new","expires_in":86400000}`, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var exchanges atomic.Int32
			r, _ := nativeFixture(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/refresh") {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				exchanges.Add(1)
				exchangeFixture(w)
			})
			auth := qoderAuth{AuthMode: "pat", PAT: "pt-fixture"}
			if _, err := r.nativeToken(auth, "fixture-callback", r.loadedConfig(), ""); err != nil {
				t.Fatal(err)
			}
			state, err := r.nativeToken(auth, "fixture-callback", r.loadedConfig(), "jt-fixture")
			if (err != nil) != tc.wantError || exchanges.Load() != tc.wantExchange {
				t.Fatalf("exchanges=%d error=%v", exchanges.Load(), err)
			}
			if tc.name == "retain_refresh" && state.RefreshToken != "jrt-fixture" {
				t.Fatal("refresh token was discarded")
			}
		})
	}
}

func TestNativeCatalogRespectsExplicitDefaultWindow(t *testing.T) {
	for _, tc := range []struct{ format, raw string }{
		{"qoder", `{"chat":[{"key":"qmodel-test","enable":true,"max_input_tokens":1000000,"default_context_window":200000,"available_context_windows":[200000,1000000]}]}`},
		{"openai", `{"data":[{"id":"exact-test","maxInputTokens":1000000,"defaultContextWindow":200000,"availableContextWindows":[200000,1000000]}]}`},
	} {
		entry, err := parseNativeCatalog([]byte(tc.raw), tc.format)
		if err != nil || len(entry.models) != 1 || entry.models[0].ContextLength != 200000 {
			t.Fatalf("catalog default window lost: %v", err)
		}
	}
}

func TestNativeRetriesOnlyAuthRejectionBeforeOutput(t *testing.T) {
	for _, scenario := range []struct {
		name                   string
		status                 int
		body                   string
		wantChats, wantRefresh int32
		code                   string
	}{
		{"expired", 401, `{"code":"TOKEN_EXPIRE"}`, 2, 1, ""},
		{"queued", 403, `{"code":10605}`, 1, 0, "model_queued"},
		{"forbidden", 403, `{"code":"model_denied"}`, 1, 0, "upstream_forbidden"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var chats, refreshes atomic.Int32
			r, _ := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/exchange") {
					exchangeFixture(w)
					return
				}
				if strings.HasSuffix(request.URL.Path, "/refresh") {
					refreshes.Add(1)
					exchangeFixture(w)
					return
				}
				if chats.Add(1) == 1 {
					w.WriteHeader(scenario.status)
					fmt.Fprint(w, scenario.body)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
			})
			raw, _ := json.Marshal(nativeReq("r", false))
			_, err := r.execute(raw)
			if scenario.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if call, ok := err.(*pluginCallError); !ok || call.code != scenario.code {
				t.Fatalf("wrong error: %v", err)
			}
			if chats.Load() != scenario.wantChats || refreshes.Load() != scenario.wantRefresh {
				t.Fatal("incorrect retry policy")
			}
		})
	}
}

func TestNativeCatalogCOSYNamesFilteringAndIsolation(t *testing.T) {
	var catalogs atomic.Int32
	r, _ := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/jobToken/exchange":
			exchangeFixture(w)
		case "/api/v1/userinfo":
			fmt.Fprint(w, `{"id":"fixture-user","name":"Fixture","user_type":"personal_standard"}`)
		case "/algo/api/v2/model/list":
			catalogs.Add(1)
			if request.URL.Query().Get("Encode") != "1" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer COSY.") || request.Header.Get("Cosy-User") != "fixture-user" {
				t.Error("missing COSY catalog identity")
			}
			wrapped, err := base64.StdEncoding.DecodeString(request.Header.Get("Cosy-Key"))
			if err != nil || len(wrapped) != 128 {
				t.Error("invalid RSA wrapped key")
			}
			fmt.Fprint(w, `{"chat":[{"key":"qmodel_38max","enable":true,"is_vl":true,"is_reasoning":true,"max_input_tokens":180000,"max_output_tokens":32768,"context_config":[{"tokenCount":1000000}]},{"key":"future-model","display_name":"Future","enable":true},{"key":"hidden","enable":false}],"developer":[{"key":"not-chat","enable":true}]}`)
		default:
			t.Error("unexpected inference request")
		}
	})
	r.config.DirectModels = nil
	r.config.DirectCatalogFormat = "qoder"
	r.config.DirectModelsEndpoint = r.config.OpenAPIEndpoint + "/algo/api/v2/model/list"
	auth := qoderAuth{AuthMode: "pat", PAT: "pt-fixture"}
	models, err := r.nativeModels(auth, "fixture-callback", r.loadedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].DisplayName != "Qwen3.8-Max" || models[0].ContextLength != 180000 || len(models[0].SupportedInputModalities) != 2 {
		t.Fatal("wrong model metadata")
	}
	models[0].SupportedInputModalities[0] = "corrupted"
	models, err = r.nativeModels(auth, "fixture-callback", r.loadedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if catalogs.Load() != 1 || models[0].SupportedInputModalities[0] != "text" {
		t.Fatal("catalog cache is mutable or not reused")
	}
	auth.PAT = "pt-second-account"
	if _, err := r.nativeModels(auth, "fixture-callback", r.loadedConfig()); err != nil {
		t.Fatal(err)
	}
	if catalogs.Load() != 2 {
		t.Fatal("account catalog leaked")
	}
	r.config.DirectModels = []directModelConfig{{ID: "manual-only"}}
	models, err = r.nativeModels(auth, "", r.loadedConfig())
	if err != nil || len(models) != 1 || models[0].ID != "manual-only" || catalogs.Load() != 2 {
		t.Fatal("manual model override changed")
	}
}

func TestNativeOpenAICatalogLegacyShapesAndMetadata(t *testing.T) {
	records := `[" qmodel_38max ","",{"id":"custom","displayName":"Custom model","isDefault":true,"isEnabled":true,"isReasoning":true,"isVl":true,"maxInputTokens":32000,"maxOutputTokens":8000,"efforts":["low","high"],"defaultEffort":"high","supportsDisabled":true,"availableContextWindows":[64000,128000],"defaultContextWindow":64000},{"id":"disabled","isEnabled":false}]`
	for _, raw := range []string{records, `{"data":` + records + `}`, `{"models":` + records + `}`} {
		entry, err := parseNativeCatalog([]byte(raw), "openai")
		if err != nil {
			t.Fatal(err)
		}
		if len(entry.models) != 2 || entry.models[0].ID != "qmodel_38max" || entry.models[0].DisplayName != "Qwen3.8-Max" {
			t.Fatal("legacy string catalog or friendly name changed")
		}
		model := entry.models[1]
		if model.DisplayName != "Custom model" || model.InputTokenLimit != 32000 || model.OutputTokenLimit != 8000 || model.ContextLength != 64000 || len(model.SupportedInputModalities) != 2 || model.Thinking == nil || !model.Thinking.ZeroAllowed || strings.Join(model.Thinking.Levels, ",") != "low,high" {
			t.Fatal("legacy camelCase capabilities were lost")
		}
		decoded, err := decodeNativeCatalogModel(entry.rawConfigs["custom"])
		if err != nil || !decoded.IsDefault || decoded.IsEnabled == nil || !*decoded.IsEnabled || decoded.DefaultReasoningEffort != "high" || decoded.DefaultContextWindow != 64000 {
			t.Fatal("legacy model defaults were lost")
		}
	}
	model, err := decodeNativeCatalogModel([]byte(`{"is_vl":false,"isVl":true,"max_input_tokens":16000,"maxInputTokens":32000,"reasoning_efforts":["low"],"efforts":["high"]}`))
	if err != nil || model.IsVL || model.MaxInputTokens != 16000 || strings.Join(model.ReasoningEfforts, ",") != "low" {
		t.Fatal("explicit snake_case fields must take precedence")
	}
}

func TestNativeFailedStreamPreservesReportedUsage(t *testing.T) {
	for _, ending := range []string{"", "data: {\"error\":{\"code\":\"model_queued\"}}\n\n"} {
		t.Run(fmt.Sprint(len(ending)), func(t *testing.T) {
			r, host := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/exchange") {
					exchangeFixture(w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"+ending)
			})
			if _, err := r.executeNativeStream(nativeReq("failed-usage", true)); err != nil {
				t.Fatal(err)
			}
			if result := waitNativeClose(t, host); result.Error == "" {
				t.Fatal("failed upstream reported success")
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			output := bytes.Join(host.emitted, nil)
			if bytes.Count(output, []byte(`"total_tokens":12`)) != 1 || !bytes.Contains(output, []byte(`"choices":[]`)) || bytes.Contains(output, []byte(`"finish_reason":"stop"`)) {
				t.Fatal("failed stream lost or duplicated usage, or fabricated successful completion")
			}
		})
	}
}

func TestNativeCancelOwnershipQuiesceAndContinuation(t *testing.T) {
	started := make(chan struct{}, 4)
	r, host := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/exchange") {
			exchangeFixture(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-request.Context().Done()
	})
	req := nativeReq("one", true)
	raw, _ := json.Marshal(req)
	if _, err := r.executeStream(raw); err != nil {
		t.Fatal(err)
	}
	<-started
	conflict := nativeReq("two", true)
	conflictRaw, _ := json.Marshal(conflict)
	if _, err := r.executeStream(conflictRaw); err == nil {
		t.Fatal("same session admitted concurrent turn")
	}
	_ = r.cancelExecution(pluginapi.CancelExecutionRequest{RequestID: "one", CallerScope: "wrong"})
	r.mu.Lock()
	canceled := r.nativeActive["one"].isCanceled()
	r.mu.Unlock()
	if canceled {
		t.Fatal("foreign caller canceled request")
	}
	_ = r.cancelExecution(pluginapi.CancelExecutionRequest{RequestID: "one", CallerScope: "caller", AuthIndex: "index"})
	if close := waitNativeClose(t, host); close.ErrorCode != "connection_lifecycle" {
		t.Fatal("cancellation lost")
	}
	if _, err := r.executeStream(conflictRaw); err != nil {
		t.Fatal(err)
	}
	<-started
	r.quiesce()
	if close := waitNativeClose(t, host); close.ErrorCode != "connection_lifecycle" {
		t.Fatal("quiesce did not cancel")
	}
	if _, err := r.executeStream(raw); err == nil {
		t.Fatal("quiesced plugin admitted request")
	}
}

func TestNativeRejectsUnknownModelAndTruncatedOrErrorStreams(t *testing.T) {
	for _, frame := range []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"data: {\"error\":{\"code\":\"invalid_token\",\"message\":\"fixture-secret\"}}\n\n",
		"data: {\"statusCodeValue\":429,\"body\":\"{\\\"code\\\":10605}\"}\n\n",
		"data: {bad}\n\n",
		"data: [DONE]\n\n",
	} {
		t.Run(fmt.Sprint(len(frame)), func(t *testing.T) {
			var calls atomic.Int32
			r, _ := nativeFixture(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/exchange") {
					exchangeFixture(w)
					return
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, frame)
			})
			req := nativeReq("bad", false)
			raw, _ := json.Marshal(req)
			_, err := r.execute(raw)
			if err == nil || strings.Contains(err.Error(), "fixture-secret") || calls.Load() != 1 {
				t.Fatalf("invalid failure behavior: %v", err)
			}
			req.Model = "not-listed"
			raw, _ = json.Marshal(req)
			if _, err := r.execute(raw); err == nil || calls.Load() != 1 {
				t.Fatal("unknown model contacted inference")
			}
		})
	}
}

func TestNativeReadinessAndConfigNeedNoRunner(t *testing.T) {
	cfg, err := decodePluginConfig([]byte("transport: direct_openai\ndirect_endpoint: https://gateway.qoder.com.cn/model/v1/chat/completions\nopenapi_endpoint: https://openapi.qoder.com.cn\ndirect_models_endpoint: https://gateway.qoder.com.cn/algo/api/v2/model/list\ndirect_catalog_format: qoder\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := newPluginRuntime(nil)
	r.config = cfg
	state := r.readiness(pluginapi.ReadinessRequest{Purpose: pluginapi.ReadinessPurposeAdmission, StorageJSON: nativeReq("r", false).StorageJSON})
	if !state.Ready {
		t.Fatal("native configuration required runner")
	}
	for _, check := range state.Checks {
		if check.Level == pluginapi.ReadinessLevelRunnerInstalled && check.Version != "native-go" {
			t.Fatal("wrong runtime")
		}
	}
	if _, err := decodePluginConfig([]byte("direct_catalog_format: unknown\n")); err == nil {
		t.Fatal("invalid catalog format accepted")
	}
	if _, err := parseNativeCatalog([]byte(`{"chat":[{"key":"hidden","enable":false}]}`), "qoder"); err == nil {
		t.Fatal("hidden-only catalog advertised")
	}
}
