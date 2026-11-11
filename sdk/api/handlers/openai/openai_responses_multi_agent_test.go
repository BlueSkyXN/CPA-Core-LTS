package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestPrepareCodexMultiAgentV2ToolsAtResponsesBoundary(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}}}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{
        "tools":[{"type":"namespace","name":"collaboration","tools":[
            {"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}},
            {"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}
        ]}]
    }`)
	got := handler.prepareCodexMultiAgentV2Tools(ginContext, payload)

	if namespace := ojson.GetBytesNoCopy(got, "tools.0.name").String(); namespace != "collaboration" {
		t.Fatalf("namespace = %q, want collaboration", namespace)
	}
	for _, path := range []string{"tools.0.tools.0", "tools.0.tools.1"} {
		if encrypted := ojson.GetBytesNoCopy(got, path+".parameters.properties.message.encrypted"); encrypted.Exists() {
			t.Fatalf("%s message.encrypted was not removed: %s", path, encrypted.Raw)
		}
	}
	prepared, exists := ginContext.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey)
	if !exists || prepared != true {
		t.Fatalf("prepared marker = %#v, want true", prepared)
	}
}

func TestPrepareCodexRequestPayloadUnchangedReturnsSameSlice(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}}}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	got := handler.prepareCodexRequestPayload(ginContext, payload)
	if len(got) == 0 || &got[0] != &payload[0] {
		t.Fatal("expected prepareCodexRequestPayload to return original payload slice without reallocating when unchanged")
	}
}

func TestResponsesPreparesCodexMultiAgentV2ToolsForHTTPAndSSE(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			executor := &responsesMultiAgentCaptureExecutor{}
			handler, modelID := newResponsesMultiAgentTestHandler(t, executor)
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)

			payload := fmt.Sprintf(`{"model":%q,"stream":%t,"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`, modelID, stream)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
			request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}

			payloads := executor.Payloads()
			if len(payloads) != 1 {
				t.Fatalf("captured payload count = %d, want 1", len(payloads))
			}
			captured := payloads[0]
			if encrypted := ojson.GetBytesNoCopy(captured, "tools.0.tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
				t.Fatalf("message.encrypted was not removed: %s", captured)
			}
			if namespace := ojson.GetBytesNoCopy(captured, "tools.0.name").String(); namespace != "collaboration" {
				t.Fatalf("namespace = %q, want collaboration", namespace)
			}
		})
	}
}

type responsesMultiAgentCaptureExecutor struct {
	websocketDirectCaptureExecutor
	output []byte
}

func (e *responsesMultiAgentCaptureExecutor) response() []byte {
	output := e.output
	if len(output) == 0 {
		output = []byte(`[]`)
	}
	return []byte(fmt.Sprintf(`{"id":"resp-1","object":"response","status":"completed","output":%s}`, output))
}

func (e *responsesMultiAgentCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()
	return coreexecutor.Response{Payload: e.response()}, nil
}

func (e *responsesMultiAgentCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":%s}\n\n", e.response()))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func newResponsesMultiAgentTestHandler(t *testing.T, executor *responsesMultiAgentCaptureExecutor) (*OpenAIResponsesAPIHandler, string) {
	t.Helper()

	modelID := "responses-multi-agent-" + strings.ReplaceAll(t.Name(), "/", "-")
	authID := modelID + "-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}}}, manager)
	return NewOpenAIResponsesAPIHandler(base), modelID
}

func TestResponsesWebsocketPreparesCodexMultiAgentV2Tools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &websocketDirectCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "responses-multi-agent-ws-auth", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	modelID := "responses-multi-agent-ws-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}}}, manager)
	handler := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, http.Header{"User-Agent": []string{"codex_cli_rs/0.144.1"}})
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()

	request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[],"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`, modelID)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("write websocket request: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket response: %v", errRead)
	}

	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	if encrypted := ojson.GetBytesNoCopy(captured, "tools.0.tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
		t.Fatalf("message.encrypted was not removed: %s", captured)
	}
	if namespace := ojson.GetBytesNoCopy(captured, "tools.0.name").String(); namespace != "collaboration" {
		t.Fatalf("namespace = %q, want collaboration", namespace)
	}
}

func TestPrepareCodexMultiAgentV2ToolsAtResponsesBoundarySkipsOtherClients(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}}}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "curl/8.7.1")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{"tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]}`)
	got := handler.prepareCodexMultiAgentV2Tools(ginContext, payload)

	if string(got) != string(payload) {
		t.Fatalf("other client payload changed: %s", got)
	}
	if _, exists := ginContext.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey); exists {
		t.Fatal("other client unexpectedly received prepared marker")
	}
}

func TestClientMultiAgentPreparationDoesNotWaitForOAuthCredential(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Client:                             sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{OptimizeMultiAgentV2: true}},
		CodexOrphanDelegationCompatibility: true,
		OAuthOnlyFields:                    map[string]bool{"codex.orphan-delegation-compatibility": true},
	}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	request.Header.Set("X-Openai-Subagent", "collab_spawn")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request
	payload := []byte(`{"input":[{"type":"function_call_output","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"}],"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`)
	got := handler.prepareCodexMultiAgentV2Tools(ginContext, payload)
	got = handler.prepareCodexOrphanDelegation(ginContext, got)
	if ojson.GetBytesNoCopy(got, "tools.0.tools.0.parameters.properties.message.encrypted").Exists() {
		t.Fatalf("client preparation did not remove encryption: %s", got)
	}
	if ojson.GetBytesNoCopy(got, "input.0.type").String() != "message" {
		t.Fatalf("shared orphan preparation waited for credential selection: %s", got)
	}
	if _, exists := ginContext.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey); !exists {
		t.Fatal("client request did not receive prepared marker")
	}
}

func newResponsesOrphanDelegationTestHandler(t *testing.T, executor *responsesMultiAgentCaptureExecutor) (*OpenAIResponsesAPIHandler, string) {
	t.Helper()

	modelID := "responses-orphan-test-model"
	authID := "responses-orphan-test-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOrphanDelegationCompatibility: true}, manager)
	return NewOpenAIResponsesAPIHandler(base), modelID
}

func TestResponsesOrphanCodexDelegationCompatibility(t *testing.T) {
	t.Parallel()

	executor := &responsesMultiAgentCaptureExecutor{}
	handler, modelID := newResponsesOrphanDelegationTestHandler(t, executor)

	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	payload := fmt.Sprintf(`{
		"model": %q,
		"stream": false,
		"input": [
			{
				"type": "function_call_output",
				"name": "create_thread",
				"namespace": "codex_app",
				"output": "<codex_delegation>msg</codex_delegation>"
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "continue"}]
			}
		]
	}`, modelID)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	request.Header.Set("X-Openai-Subagent", "collab_spawn")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	parsed := ojson.GetBytesNoCopy(captured, "@this")
	if itemType := parsed.Get("input.0.type").String(); itemType != "message" {
		t.Fatalf("input.0.type = %q, want message; captured=%s", itemType, captured)
	}
	if role := parsed.Get("input.0.role").String(); role != "user" {
		t.Fatalf("input.0.role = %q, want user", role)
	}
	wantText := "Tool output from codex_app__create_thread:\n<codex_delegation>msg</codex_delegation>"
	if text := parsed.Get("input.0.content.0.text").String(); text != wantText {
		t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
	}

	// Without X-Openai-Subagent header, payload should remain function_call_output
	requestNoHeader := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	recorderNoHeader := httptest.NewRecorder()
	router.ServeHTTP(recorderNoHeader, requestNoHeader)
	if recorderNoHeader.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorderNoHeader.Code, recorderNoHeader.Body.String())
	}
	payloads = executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("captured payload count = %d, want 2", len(payloads))
	}
	capturedNoHeader := payloads[1]
	parsedNoHeader := ojson.GetBytesNoCopy(capturedNoHeader, "@this")
	if itemType := parsedNoHeader.Get("input.0.type").String(); itemType != "function_call_output" {
		t.Fatalf("input.0.type = %q, want function_call_output without header; captured=%s", itemType, capturedNoHeader)
	}
}

func TestResponsesSanitizesOrphanToolOutputsThroughHTTPAndCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelID = "test-responses-orphan-tool-output-model"
	authID := "test-responses-orphan-tool-output-auth"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	coreManager := coreauth.NewManager(nil, nil, nil)
	executor := &responsesMultiAgentCaptureExecutor{}
	coreManager.RegisterExecutor(executor)
	if _, errRegister := coreManager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	baseHandler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreManager)
	responsesHandler := NewOpenAIResponsesAPIHandler(baseHandler)

	router := gin.New()
	router.POST("/v1/responses", responsesHandler.Responses)
	router.POST("/v1/responses/compact", responsesHandler.Compact)

	payload := fmt.Sprintf(`{
		"model": %q,
		"stream": false,
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","id":"fco_1","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"},
			{"type":"custom_tool_call_output","id":"fco_2","name":"custom_ctx","output":[{"type":"input_text","text":"part one"},{"type":"input_text","text":"part two"},{"type":"input_image","image_url":"https://example.com/page.png"}]},
			{"type":"function_call","call_id":"call_ok","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"},
			{"type":"function_call_output","call_id":" ","output":"blank id"},
			{"type":"function_call_output","id":"fco_3","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"},
			{"type":"function_call","call_id":"call_pair","name":"shell","arguments":"{}"},
			{"type":"function_call_output","output":"paired without call_id"}
		]
	}`, modelID)

	for idx, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		payloads := executor.Payloads()
		if len(payloads) != idx+1 {
			t.Fatalf("%s captured payload count = %d, want %d", path, len(payloads), idx+1)
		}
		input := ojson.GetBytesNoCopy(payloads[idx], "input").Array()
		if len(input) != 9 {
			t.Fatalf("%s input len = %d, want 9", path, len(input))
		}
		if got := input[1].Get("type").String(); got != "message" {
			t.Fatalf("%s input[1].type = %q, want message", path, got)
		}
		want1 := "Tool output from codex_apps_open_page:"
		if got := input[1].Get("content.0.text").String(); got != want1 {
			t.Fatalf("%s input[1].content.0.text = %q, want %q", path, got, want1)
		}
		if got := input[2].Get("type").String(); got != "message" {
			t.Fatalf("%s input[2].type = %q, want message", path, got)
		}
		want2 := "Tool output from custom_ctx:"
		if got := input[2].Get("content.0.text").String(); got != want2 {
			t.Fatalf("%s input[2].content.0.text = %q, want %q", path, got, want2)
		}
		if got := input[2].Get("content.1.text").String(); got != "part one" {
			t.Fatalf("%s input[2].content.1.text = %q, want part one", path, got)
		}
		if got := input[2].Get("content.2.text").String(); got != "part two" {
			t.Fatalf("%s input[2].content.2.text = %q, want part two", path, got)
		}
		if got := input[2].Get("content.3.type").String(); got != "input_image" {
			t.Fatalf("%s input[2].content.3.type = %q, want input_image", path, got)
		}
		if got := input[2].Get("content.3.image_url").String(); got != "https://example.com/page.png" {
			t.Fatalf("%s input[2].content.3.image_url = %q, want https://example.com/page.png", path, got)
		}
		if got := input[4].Get("type").String(); got != "function_call_output" {
			t.Fatalf("%s input[4].type = %q, want function_call_output", path, got)
		}
		if got := input[5].Get("type").String(); got != "message" {
			t.Fatalf("%s input[5].type = %q, want message", path, got)
		}
		want5 := "Tool output from tool:"
		if got := input[5].Get("content.0.text").String(); got != want5 {
			t.Fatalf("%s input[5].content.0.text = %q, want %q", path, got, want5)
		}
		if got := input[6].Get("type").String(); got != "function_call_output" {
			t.Fatalf("%s input[6].type = %q, want function_call_output", path, got)
		}
		if got := input[8].Get("type").String(); got != "message" {
			t.Fatalf("%s input[8].type = %q, want message", path, got)
		}
		if input[8].Get("call_id").Exists() {
			t.Fatalf("%s input[8] gained a guessed call_id: %s", path, input[8].Raw)
		}
	}
}

func TestPrepareCodexRequestPayloadPreservesOrphanToolImagesForChatTranslator(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	arrayOutput := `[{"type":"input_text","text":"before image one"},{"type":"image_url","image_url":{"url":"https://example.com/one.png"}},{"type":"input_text","text":"after image one"},{"type":"input_image","image_url":"https://example.com/two.png","detail":"low"}]`
	tests := []struct {
		name   string
		output string
	}{
		{name: "array", output: arrayOutput},
		{name: "json string", output: strconv.Quote(arrayOutput)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"model":"chat-model","input":[{"type":"message","role":"user","content":"hello"},{"type":"function_call_output","id":"fco_image","name":"capture","output":` + tt.output + `}]}`)
			prepared := handler.prepareCodexRequestPayload(ginContext, payload)
			converted, errConvert := openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-model", prepared, false)
			if errConvert != nil {
				t.Fatalf("Convert... returned error: %v", errConvert)
			}

			messages := ojson.GetBytesNoCopy(converted, "messages").Array()
			var content []ojson.Result
			for _, message := range messages {
				if strings.Contains(message.Raw, "Tool output from capture:") {
					content = message.Get("content").Array()
					break
				}
			}
			if len(content) != 5 {
				t.Fatalf("orphan output content len = %d, want 5; converted=%s", len(content), converted)
			}
			if got := content[1].Get("text").String(); got != "before image one" {
				t.Fatalf("content[1].text = %q; converted=%s", got, converted)
			}
			if got := content[2].Get("image_url.url").String(); got != "https://example.com/one.png" {
				t.Fatalf("content[2].url = %q; converted=%s", got, converted)
			}
			if got := content[3].Get("text").String(); got != "after image one" {
				t.Fatalf("content[3].text = %q; converted=%s", got, converted)
			}
			if got := content[4].Get("image_url.url").String(); got != "https://example.com/two.png" {
				t.Fatalf("content[4].url = %q; converted=%s", got, converted)
			}
			if got := content[4].Get("image_url.detail").String(); got != "low" {
				t.Fatalf("content[4].detail = %q; converted=%s", got, converted)
			}
		})
	}
}

func TestPrepareCodexRequestPayloadPreservesOrphanToolFileForClaudeTranslator(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	payload := []byte(`{
		"model":"claude-model",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"function_call_output","id":"fco_file","name":"capture","output":[
				{"type":"input_text","text":"attachment follows"},
				{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}
			]}
		]
	}`)

	prepared := handler.prepareCodexRequestPayload(ginContext, payload)
	converted, errConvert := clauderesponses.ConvertOpenAIResponsesRequestToClaude("claude-model", prepared, false)
	if errConvert != nil {
		t.Fatalf("Convert... returned error: %v", errConvert)
	}

	messages := ojson.GetBytesNoCopy(converted, "messages").Array()
	var content []ojson.Result
	for _, message := range messages {
		if strings.Contains(message.Raw, "Tool output from capture:") {
			content = message.Get("content").Array()
			break
		}
	}
	if len(content) != 4 {
		t.Fatalf("orphan output content len = %d, want 4; converted=%s", len(content), converted)
	}
	if got := content[1].Get("text").String(); got != "Tool output from capture:" {
		t.Fatalf("content[1].text = %q; converted=%s", got, converted)
	}
	if got := content[2].Get("text").String(); got != "attachment follows" {
		t.Fatalf("content[2].text = %q; converted=%s", got, converted)
	}
	if got := content[3].Get("type").String(); got != "document" {
		t.Fatalf("content[3].type = %q, want document; converted=%s", got, converted)
	}
	if got := content[3].Get("source.media_type").String(); got != "application/pdf" {
		t.Fatalf("content[3].source.media_type = %q; converted=%s", got, converted)
	}
	if got := content[3].Get("source.data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("content[3].source.data = %q; converted=%s", got, converted)
	}
}

func TestPrepareCodexRequestPayloadDoesNotGuessAmbiguousToolOutputPairs(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	tests := []struct {
		name          string
		calls         string
		outputs       string
		outputIndexes []int
	}{
		{
			name:          "omitted ids",
			calls:         `{"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},{"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","output":"output X"},{"type":"function_call_output","output":"output Y"}`,
			outputIndexes: []int{3, 4},
		},
		{
			name:          "empty call ids",
			calls:         `{"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},{"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","call_id":"","output":"output X"},{"type":"function_call_output","call_id":"","output":"output Y"}`,
			outputIndexes: []int{3, 4},
		},
		{
			name:          "blank call ids",
			calls:         `{"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},{"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","call_id":" ","output":"output X"},{"type":"function_call_output","call_id":" ","output":"output Y"}`,
			outputIndexes: []int{3, 4},
		},
		{
			name:          "null call ids",
			calls:         `{"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},{"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","call_id":null,"output":"output X"},{"type":"function_call_output","call_id":null,"output":"output Y"}`,
			outputIndexes: []int{3, 4},
		},
		{
			name:          "same name",
			calls:         `{"type":"function_call","call_id":"a","name":"shell","arguments":"{}"},{"type":"function_call","call_id":"b","name":"shell","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","name":"shell","output":"output X"},{"type":"function_call_output","name":"shell","output":"output Y"}`,
			outputIndexes: []int{3, 4},
		},
		{
			name:          "generic item id fallback",
			calls:         `{"type":"function_call","id":"a","name":"tool_a","arguments":"{}"},{"type":"function_call","id":"b","name":"tool_b","arguments":"{}"}`,
			outputs:       `{"type":"function_call_output","output":"output X"}`,
			outputIndexes: []int{3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(`{
				"model":"chat-model",
				"input":[%s,{"type":"message","role":"user","content":"reminder before results"},%s]
			}`, tt.calls, tt.outputs))

			prepared := handler.prepareCodexRequestPayload(ginContext, payload)
			for _, index := range tt.outputIndexes {
				if got := ojson.GetBytesNoCopy(prepared, fmt.Sprintf("input.%d.type", index)).String(); got != "message" {
					t.Fatalf("prepared input[%d].type = %q, want message; prepared=%s", index, got, prepared)
				}
				if ojson.GetBytesNoCopy(prepared, fmt.Sprintf("input.%d.call_id", index)).Exists() {
					t.Fatalf("prepared input[%d] gained a guessed call_id: %s", index, prepared)
				}
			}

			converted, errConvert := openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-model", prepared, false)
			if errConvert != nil {
				t.Fatalf("Convert... returned error: %v", errConvert)
			}
			messages := ojson.GetBytesNoCopy(converted, "messages").Array()
			wantRoles := make([]string, 0, len(tt.outputIndexes)+2)
			wantRoles = append(wantRoles, "assistant", "user")
			for range tt.outputIndexes {
				wantRoles = append(wantRoles, "user")
			}
			if len(messages) != len(wantRoles) {
				t.Fatalf("message len = %d, want %d; converted=%s", len(messages), len(wantRoles), converted)
			}
			for idx, want := range wantRoles {
				if got := messages[idx].Get("role").String(); got != want {
					t.Fatalf("messages[%d].role = %q, want %q; converted=%s", idx, got, want, converted)
				}
			}
			if got := messages[1].Get("content").String(); got != "reminder before results" {
				t.Fatalf("messages[1].content = %q; converted=%s", got, converted)
			}
			for idx, output := range []string{"output X", "output Y"}[:len(tt.outputIndexes)] {
				if got := messages[idx+2].Get("content").String(); !strings.Contains(got, output) {
					t.Fatalf("messages[%d].content missing %s: %s", idx+2, output, messages[idx+2].Raw)
				}
			}
			for _, message := range messages {
				if message.Get("role").String() == "tool" {
					t.Fatalf("ambiguous output became tool message: %s", message.Raw)
				}
			}
		})
	}
}

func TestPrepareCodexRequestPayloadKeepsOutputItemIDFallbackPairing(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	payload := []byte(`{
		"model":"chat-model",
		"input":[
			{"type":"function_call","call_id":"call_from_id","name":"shell","arguments":"{}"},
			{"type":"function_call_output","id":"call_from_id","output":"paired by generic item id"}
		]
	}`)

	prepared := handler.prepareCodexRequestPayload(ginContext, payload)
	if got := ojson.GetBytesNoCopy(prepared, "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("prepared input.1.type = %q, want function_call_output; prepared=%s", got, prepared)
	}
	if got := ojson.GetBytesNoCopy(prepared, "input.1.call_id").String(); got != "call_from_id" {
		t.Fatalf("prepared input.1.call_id = %q, want call_from_id; prepared=%s", got, prepared)
	}
}

func TestPrepareCodexRequestPayloadPairsSingleOmittedOutput(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	payload := []byte(`{
		"model":"chat-model",
		"input":[
			{"type":"function_call","call_id":"single_pair","name":"shell","arguments":"{}"},
			{"type":"function_call_output","output":"paired without call_id"}
		]
	}`)

	prepared := handler.prepareCodexRequestPayload(ginContext, payload)
	if got := ojson.GetBytesNoCopy(prepared, "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("prepared input.1.type = %q, want function_call_output; prepared=%s", got, prepared)
	}
	if got := ojson.GetBytesNoCopy(prepared, "input.1.call_id").String(); got != "single_pair" {
		t.Fatalf("prepared input.1.call_id = %q, want single_pair; prepared=%s", got, prepared)
	}
}

func TestPrepareCodexRequestPayloadRewritesOrphanGenericItemIDOutput(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	payload := []byte(`{
		"model":"chat-model",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"function_call_output","id":"orphan_id","output":"no matching call"}
		]
	}`)

	prepared := handler.prepareCodexRequestPayload(ginContext, payload)
	if got := ojson.GetBytesNoCopy(prepared, "input.1.type").String(); got != "message" {
		t.Fatalf("prepared input.1.type = %q, want message; prepared=%s", got, prepared)
	}
	if ojson.GetBytesNoCopy(prepared, "input.1.call_id").Exists() {
		t.Fatalf("prepared input.1 gained an invalid call_id: %s", prepared)
	}
}
