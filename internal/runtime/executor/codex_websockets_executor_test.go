package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestBuildCodexWebsocketRequestBodyPreservesPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if got := gjson.GetBytes(wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %s, want response.create", got)
	}
	if got := gjson.GetBytes(wsReqBody, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %s, want resp-1", got)
	}
	if gjson.GetBytes(wsReqBody, "input.0.id").String() != "msg-1" {
		t.Fatalf("input item id mismatch")
	}
	if got := gjson.GetBytes(wsReqBody, "type").String(); got == "response.append" {
		t.Fatalf("unexpected websocket request type: %s", got)
	}
}

func TestBuildCodexWebsocketRequestBodyShortensOverlongInputItemIDs(t *testing.T) {
	longCallItemID := strings.Repeat("grok-call-item-", 6)
	longOutputItemID := strings.Repeat("grok-output-item-", 6)
	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"function_call","id":"` + longCallItemID + `","call_id":"call-1","name":"lookup"},{"type":"function_call_output","id":"` + longOutputItemID + `","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)

	first := buildCodexWebsocketRequestBody(body)
	second := buildCodexWebsocketRequestBody(body)

	shortCallItemID := gjson.GetBytes(first, "input.0.id").String()
	shortOutputItemID := gjson.GetBytes(first, "input.1.id").String()
	if len([]rune(shortCallItemID)) > 64 || shortCallItemID == longCallItemID {
		t.Fatalf("input.0.id was not shortened to at most 64 characters: %q", shortCallItemID)
	}
	if len([]rune(shortOutputItemID)) > 64 || shortOutputItemID == longOutputItemID {
		t.Fatalf("input.1.id was not shortened to at most 64 characters: %q", shortOutputItemID)
	}
	if shortCallItemID == shortOutputItemID {
		t.Fatalf("distinct long IDs produced the same shortened ID: %q", shortCallItemID)
	}
	if got := gjson.GetBytes(second, "input.0.id").String(); got != shortCallItemID {
		t.Fatalf("input item ID shortening is not deterministic: first=%q second=%q", shortCallItemID, got)
	}
	if got := gjson.GetBytes(first, "input.0.call_id").String(); got != "call-1" {
		t.Fatalf("function call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(first, "input.1.call_id").String(); got != "call-1" {
		t.Fatalf("function call output call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(first, "input.2.id").String(); got != "msg-1" {
		t.Fatalf("valid input item ID changed: %q", got)
	}
}

func TestCodexWebsocketsExecuteRestoresClaudeAgentReasoningReplay(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningEncryptedContentForTestSeed(31)
	cacheCodexReasoningReplayFromCompleted(codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:ws-replay-session:agent:agent-a",
	}, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"previous answer"}]}`+
		`]}}`))

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Fatalf("upgrade websocket: %v", errUpgrade)
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read upstream websocket message: %v", errRead)
		}
		capturedPayload <- bytes.Clone(payload)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-ws-replay","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"next answer"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"messages":[
				{"role":"user","content":"first"},
				{"role":"assistant","content":"previous answer"},
				{"role":"user","content":"next"}
			]
		}`),
	}
	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "ws-replay-session")
	headers.Set("X-Claude-Code-Agent-Id", "agent-a")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Headers: headers}

	if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	select {
	case payload := <-capturedPayload:
		input := gjson.GetBytes(payload, "input").Array()
		if len(input) != 4 {
			t.Fatalf("upstream input length = %d, want 4; payload=%s", len(input), payload)
		}
		if input[1].Get("type").String() != "reasoning" || input[1].Get("encrypted_content").String() != encryptedContent {
			t.Fatalf("websocket reasoning replay missing before assistant message: %s", payload)
		}
		if input[2].Get("role").String() != "assistant" {
			t.Fatalf("input.2.role = %q, want assistant; payload=%s", input[2].Get("role").String(), payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestClearCodexReasoningReplayOnWebsocketInvalidSignature(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{modelName: "gpt-5.4", sessionKey: "claude:ws-invalid:agent:main"}
	encryptedContent := validCodexReasoningEncryptedContentForTestSeed(32)
	if !internalcache.CacheCodexReasoningReplayItem(scope.modelName, scope.sessionKey, []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}`)) {
		t.Fatal("failed to seed websocket replay cache")
	}
	payload := []byte(`{"type":"error","status":400,"body":{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}}`)
	if errClear := clearCodexReasoningReplayOnWebsocketError(context.Background(), scope, payload); errClear != nil {
		t.Fatalf("clear websocket replay error: %v", errClear)
	}
	if _, ok := internalcache.GetCodexReasoningReplayItem(scope.modelName, scope.sessionKey); ok {
		t.Fatal("websocket invalid signature did not clear replay state")
	}
}

func TestCodexWebsocketsExecuteResponsesLiteDoesNotInjectImageGenerationTool(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read upstream websocket message: %v", errRead)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},{"role":"user","content":"hello"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if tools := gjson.GetBytes(payload, "tools"); tools.Exists() {
			t.Fatalf("unexpected tools in responses-lite upstream payload: %s", tools.Raw)
		}
		if got := gjson.GetBytes(payload, "input.0.type").String(); got != "additional_tools" {
			t.Fatalf("input.0.type = %q, want additional_tools; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").String(); got != "true" {
			t.Fatalf("responses-lite metadata = %q, want true; payload=%s", got, payload)
		}
		parallelToolCalls := gjson.GetBytes(payload, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
			t.Fatalf("responses-lite parallel_tool_calls should be false: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamResponsesLiteForcesParallelToolCallsFalse(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},{"role":"user","content":"hello"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	streamComplete := false
	for !streamComplete {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				streamComplete = true
				continue
			}
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for websocket stream completion")
		}
	}

	select {
	case payload := <-capturedPayload:
		parallelToolCalls := gjson.GetBytes(payload, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
			t.Fatalf("responses-lite parallel_tool_calls should be false: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecutePreservesPreviousResponseIDUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("request path = %s, want /responses", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("upstream type = %s, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-1" {
			t.Fatalf("upstream previous_response_id = %s, want resp-1; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamPassesThroughUpstreamWebsocketPayloadForDownstreamWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	delta := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)
		if errWrite := conn.WriteMessage(websocket.TextMessage, delta); errWrite != nil {
			t.Errorf("write delta websocket message: %v", errWrite)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"prolite/gpt-5-codex","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},{"type":"message","role":"user","content":"hello"}],"parallel_tool_calls":true}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before first chunk")
		}
		if chunk.Err != nil {
			t.Fatalf("first chunk error = %v", chunk.Err)
		}
		if !bytes.Equal(bytes.TrimSpace(chunk.Payload), delta) {
			t.Fatalf("first chunk = %q, want raw upstream websocket payload %q", chunk.Payload, delta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first stream chunk")
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "model").String(); got != "gpt-5-codex" {
			t.Fatalf("upstream model = %s, want gpt-5-codex; payload=%s", got, payload)
		}
		parallelToolCalls := gjson.GetBytes(payload, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || !parallelToolCalls.Bool() {
			t.Fatalf("non-lite parallel_tool_calls should be preserved: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamPropagatesUpstreamErrorForDownstreamWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errorPayload := []byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
			t.Errorf("write error websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before error chunk")
		}
		if len(bytes.TrimSpace(chunk.Payload)) != 0 {
			t.Fatalf("error chunk payload = %q, want empty", chunk.Payload)
		}
		if chunk.Err == nil {
			t.Fatal("error chunk Err = nil, want upstream error")
		}
		statusErr, ok := chunk.Err.(interface{ StatusCode() int })
		if !ok {
			t.Fatalf("error type %T does not expose StatusCode", chunk.Err)
		}
		if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want %d", got, http.StatusTooManyRequests)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for error stream chunk")
	}
}

func TestCodexWebsocketsExecuteStreamMapsMessageTooBigClose(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		deadline := time.Now().Add(time.Second)
		closeMessage := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
		if errWrite := conn.WriteControl(websocket.CloseMessage, closeMessage, deadline); errWrite != nil {
			t.Errorf("write close websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before error chunk")
		}
		if chunk.Err == nil {
			t.Fatal("error chunk Err = nil, want message-too-big error")
		}
		statusErr, ok := chunk.Err.(interface{ StatusCode() int })
		if !ok {
			t.Fatalf("error type %T does not expose StatusCode", chunk.Err)
		}
		if got := statusErr.StatusCode(); got != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", got, http.StatusRequestEntityTooLarge)
		}
		if got := gjson.Get(chunk.Err.Error(), "error.code").String(); got != "message_too_big" {
			t.Fatalf("error code = %q, want message_too_big; err=%v", got, chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for error stream chunk")
	}
}

func TestCodexWebsocketHandshakeFailureReleasesExecutionSession(t *testing.T) {
	tests := []struct {
		name            string
		handshakeStatus int
	}{
		{name: "upgrade-required-fallback", handshakeStatus: http.StatusUpgradeRequired},
		{name: "upstream-status-error", handshakeStatus: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if websocket.IsWebSocketUpgrade(r) {
					w.WriteHeader(tt.handshakeStatus)
					_, _ = w.Write([]byte(`{"error":{"message":"websocket rejected"}}`))
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"http fallback stopped"}}`))
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			auth := &cliproxyauth.Auth{
				ID:         "auth-handshake-" + tt.name,
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL},
			}
			sessionID := "session-handshake-" + tt.name
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, _ := exec.ExecuteStream(ctx, auth, req, opts)
			if result != nil {
				for range result.Chunks {
				}
			}

			sess := exec.getOrCreateSession(sessionID)
			acquired := make(chan struct{})
			go func() {
				sess.reqMu.Lock()
				close(acquired)
				sess.reqMu.Unlock()
			}()
			select {
			case <-acquired:
			case <-time.After(2 * time.Second):
				t.Fatal("websocket handshake failure left the execution session locked")
			}
			exec.CloseExecutionSession(sessionID)
		})
	}
}

func TestCodexWebsocketReconnectHandshakePreservesTypedQuotaError(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upgradeAttempts atomic.Int32
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Path; got != "/responses" {
					t.Errorf("request path = %q, want /responses", got)
				}
				if upgradeAttempts.Add(1) == 1 {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Errorf("upgrade stale websocket: %v", err)
						return
					}
					defer func() { _ = conn.Close() }()
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "9")
				w.Header().Set("X-Request-ID", "req-reconnect-quota")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"quota","resets_in_seconds":7}}`))
			}))
			defer server.Close()

			wsURL, err := buildCodexResponsesWebsocketURL(strings.TrimSuffix(server.URL, "/") + "/responses")
			if err != nil {
				t.Fatalf("build websocket URL: %v", err)
			}
			staleConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial stale websocket: %v", err)
			}
			if errClose := staleConn.Close(); errClose != nil {
				t.Fatalf("close stale websocket: %v", errClose)
			}

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{
				ID:         "auth-reconnect-quota-" + tt.name,
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL},
			}
			const model = "gpt-5.4"
			sessionID := "session-reconnect-quota-" + tt.name
			sess := exec.getOrCreateSession(sessionID)
			connectionKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, model, resolveCodexModelHeaderProfile(model).digest)
			sess.connMu.Lock()
			sess.conn = staleConn
			sess.connGen = 1
			sess.connKey = connectionKey
			sess.wsURL = wsURL
			sess.authID = auth.ID
			sess.readerConn = staleConn
			sess.readerGen = 1
			sess.connMu.Unlock()
			defer exec.CloseExecutionSession(sessionID)

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var executeErr error
			if tt.stream {
				result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
				if result != nil {
					t.Fatalf("ExecuteStream() result = %#v, want nil on reconnect handshake rejection", result)
				}
				executeErr = errStream
			} else {
				_, executeErr = exec.Execute(ctx, auth, req, opts)
			}
			if executeErr == nil {
				t.Fatal("reconnect handshake error = nil, want typed usage-limit error")
			}
			classified, ok := executeErr.(interface{ ModelFallbackReason() string })
			if !ok || classified.ModelFallbackReason() != config.CodexModelFallbackTriggerUsageLimit {
				t.Fatalf("fallback reason = %T %v, want %q", executeErr, executeErr, config.CodexModelFallbackTriggerUsageLimit)
			}
			retryable, ok := executeErr.(interface{ RetryAfter() *time.Duration })
			if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
				t.Fatalf("RetryAfter = %v, want 7s", retryable.RetryAfter())
			}
			withHeaders, ok := executeErr.(interface{ Headers() http.Header })
			if !ok {
				t.Fatalf("reconnect error type %T does not expose headers", executeErr)
			}
			if got := withHeaders.Headers().Get("X-Request-ID"); got != "req-reconnect-quota" {
				t.Fatalf("X-Request-ID = %q, want req-reconnect-quota", got)
			}
			if got := upgradeAttempts.Load(); got != 2 {
				t.Fatalf("upgrade attempts = %d, want stale connection plus one reconnect", got)
			}
		})
	}
}

func TestCodexWebsocketsUpstreamDisconnectChanSignalsOnInvalidate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-1"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}

	sess := exec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session")
	}
	sess.connMu.Lock()
	sess.conn = conn
	sess.connGen = 1
	sess.authID = "auth-1"
	sess.wsURL = "ws://example.test/responses"
	sess.readerConn = conn
	sess.readerGen = 1
	sess.connMu.Unlock()

	upstreamErr := errors.New("upstream gone")
	exec.invalidateUpstreamConn(sess, codexWebsocketConnectionRef{conn: conn, generation: 1}, "test_invalidate", upstreamErr)

	select {
	case errRead, ok := <-disconnectCh:
		if !ok {
			t.Fatal("expected disconnect channel to deliver error before closing")
		}
		if errRead == nil || errRead.Error() != upstreamErr.Error() {
			t.Fatalf("disconnect error = %v, want %v", errRead, upstreamErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disconnect signal")
	}
}

func TestCodexWebsocketStaleReaderCannotCloseNewActiveChannel(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverReady := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		serverReady <- struct{}{}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	oldConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	select {
	case <-serverReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket server")
	}

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sess := &codexWebsocketSession{sessionID: "stale-reader-generation"}
	oldConnection := codexWebsocketConnectionRef{conn: oldConn, generation: 1}
	newConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 2}

	sess.connMu.Lock()
	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	sess.readerConn = oldConnection.conn
	sess.readerGen = oldConnection.generation
	sess.connMu.Unlock()

	readerDone := make(chan struct{})
	go func() {
		exec.readUpstreamLoop(sess, oldConnection)
		close(readerDone)
	}()

	readCh := make(chan codexWebsocketRead, 4)
	sess.connMu.Lock()
	sess.conn = newConnection.conn
	sess.connGen = newConnection.generation
	sess.readerConn = newConnection.conn
	sess.readerGen = newConnection.generation
	sess.connMu.Unlock()
	requestSignal, errActive := sess.setActiveConnection(newConnection, readCh)
	if errActive != nil {
		t.Fatalf("install new active connection: %v", errActive)
	}
	if requestSignal == nil {
		t.Fatal("install new active connection returned a nil request signal")
	}

	if errClose := oldConn.Close(); errClose != nil {
		t.Fatalf("close old websocket: %v", errClose)
	}
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stale reader did not exit")
	}

	if activeCh, _, ok := sess.activeFor(newConnection); !ok || activeCh != readCh {
		t.Fatal("stale reader cleared the new connection active channel")
	}
	select {
	case event, ok := <-readCh:
		if !ok {
			t.Fatal("stale reader closed the new connection active channel")
		}
		t.Fatalf("stale reader delivered an event to the new connection: %#v", event)
	default:
	}

	sess.clearActiveConnection(newConnection, readCh)
	sess.connMu.Lock()
	sess.conn = nil
	sess.readerConn = nil
	sess.connMu.Unlock()
}

func TestCodexWebsocketGenerationRetryRebindUsesCurrentConnection(t *testing.T) {
	sess := &codexWebsocketSession{sessionID: "retry-generation-rebind"}
	oldConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 1}
	newConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 2}
	readCh := make(chan codexWebsocketRead, 4)

	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	_, errActive := sess.setActiveConnection(oldConnection, readCh)
	if errActive != nil {
		t.Fatalf("install initial retry connection: %v", errActive)
	}
	sess.connMu.Lock()
	sess.conn = newConnection.conn
	sess.connGen = newConnection.generation
	sess.connMu.Unlock()
	requestSignal, errActive := sess.setActiveConnection(newConnection, readCh)
	if errActive != nil {
		t.Fatalf("rebind retry connection: %v", errActive)
	}

	if delivered := sess.dispatchRead(oldConnection, codexWebsocketRead{err: errors.New("stale retry error")}, true); delivered {
		t.Fatal("stale connection delivered an error after retry rebind")
	}
	wantPayload := []byte(`{"type":"response.completed"}`)
	if delivered := sess.dispatchRead(newConnection, codexWebsocketRead{msgType: websocket.TextMessage, payload: wantPayload}, false); !delivered {
		t.Fatal("current retry connection did not deliver its response")
	}

	msgType, payload, errRead := readCodexWebsocketMessage(context.Background(), sess, newConnection, readCh, requestSignal)
	if errRead != nil {
		t.Fatalf("read current retry response: %v", errRead)
	}
	if msgType != websocket.TextMessage || !bytes.Equal(payload, wantPayload) {
		t.Fatalf("retry response = type %d payload %s, want type %d payload %s", msgType, payload, websocket.TextMessage, wantPayload)
	}
	sess.clearActiveConnection(newConnection, readCh)
}

func TestCodexWebsocketGenerationRetryRebindRejectedAfterSessionClose(t *testing.T) {
	sess := &codexWebsocketSession{sessionID: "retry-rebind-after-close"}
	oldConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 11}
	retryConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 12}
	readCh := make(chan codexWebsocketRead, 1)

	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	_, errActive := sess.setActiveConnection(oldConnection, readCh)
	if errActive != nil {
		t.Fatalf("install initial retry active connection: %v", errActive)
	}

	// Simulate a successful retry dial followed by session shutdown before the
	// request can rebind its active channel to the returned generation.
	sess.connMu.Lock()
	sess.conn = nil
	sess.connGen = retryConnection.generation + 1
	sess.closed = true
	sess.connMu.Unlock()
	sess.cancelActiveConnection(errors.New("test session closed"))

	if _, errActive := sess.setActiveConnection(retryConnection, readCh); errActive == nil {
		t.Fatal("closed session accepted a retry generation rebind")
	}
	if _, _, active := sess.activeFor(oldConnection); active {
		t.Fatal("closed session retained the pre-retry active connection")
	}
	if _, _, active := sess.activeFor(retryConnection); active {
		t.Fatal("closed session installed the retry active connection")
	}
}

func TestCodexWebsocketGenerationSessionCloseCancelsActiveRequest(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	sess := &codexWebsocketSession{
		sessionID:  "generation-session-close",
		conn:       conn,
		connGen:    7,
		readerConn: conn,
		readerGen:  7,
		wsURL:      wsURL,
		authID:     "auth-session-close",
	}
	connection := codexWebsocketConnectionRef{conn: conn, generation: 7}
	readCh := make(chan codexWebsocketRead, 1)
	requestSignal, errActive := sess.setActiveConnection(connection, readCh)
	if errActive != nil {
		t.Fatalf("install session close active connection: %v", errActive)
	}

	closeCodexWebsocketSession(sess, "test_close")

	_, _, errRead := readCodexWebsocketMessage(context.Background(), sess, connection, readCh, requestSignal)
	if errRead == nil || !strings.Contains(errRead.Error(), "test_close") {
		t.Fatalf("session close read error = %v, want generation-scoped close error", errRead)
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("session close left the active request installed")
	}
	sess.connMu.Lock()
	currentConn := sess.conn
	currentGeneration := sess.connGen
	sess.connMu.Unlock()
	if currentConn != nil || currentGeneration == connection.generation {
		t.Fatalf("session close state = conn %p generation %d, want nil and a new generation", currentConn, currentGeneration)
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	connectionKey := newCodexWebsocketConnectionKey("auth-session-close", wsURL, "gpt-5.4", codexModelHeaderProfile{}.digest)
	if _, _, errEnsure := exec.ensureUpstreamConn(context.Background(), nil, sess, connectionKey, http.Header{}); errEnsure == nil {
		t.Fatal("closed session accepted a replacement connection")
	}
	lateReadCh := make(chan codexWebsocketRead, 1)
	if _, errActive := sess.setActiveConnection(connection, lateReadCh); errActive == nil {
		t.Fatal("closed session accepted an active request after connection setup")
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("closed session retained a late active request")
	}
	select {
	case event, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel after delivering the error")
		}
		if event.connection != connection || event.err == nil || !strings.Contains(event.err.Error(), "test_close") {
			t.Fatalf("queued session close event = %#v, want generation-scoped close error", event)
		}
	default:
	}
	select {
	case _, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel")
		}
		t.Fatal("session close delivered more than one terminal event")
	default:
	}
}

func TestCodexWebsocketGenerationSessionCloseDoesNotBlockOnFullActiveChannel(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	connection := codexWebsocketConnectionRef{conn: conn, generation: 17}
	sess := &codexWebsocketSession{
		sessionID:  "generation-session-close-full-channel",
		conn:       conn,
		connGen:    connection.generation,
		readerConn: conn,
		readerGen:  connection.generation,
		wsURL:      wsURL,
		authID:     "auth-session-close-full-channel",
	}
	readCh := make(chan codexWebsocketRead, 1)
	readCh <- codexWebsocketRead{connection: connection, msgType: websocket.TextMessage, payload: []byte(`{"type":"response.output_text.delta"}`)}
	requestSignal, errActive := sess.setActiveConnection(connection, readCh)
	if errActive != nil {
		t.Fatalf("install full-channel active connection: %v", errActive)
	}

	closed := make(chan struct{})
	go func() {
		closeCodexWebsocketSession(sess, "full_channel_close")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session close blocked while the request-owned channel was full")
	}
	select {
	case <-requestSignal.ctx.Done():
		if cause := context.Cause(requestSignal.ctx); cause == nil || !strings.Contains(cause.Error(), "full_channel_close") {
			t.Fatalf("request cancellation cause = %v, want full_channel_close", cause)
		}
	default:
		t.Fatal("session close did not cancel the request signal")
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("session close left the full-channel request active")
	}
	select {
	case _, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel")
		}
	default:
		t.Fatal("session close unexpectedly drained the request-owned channel")
	}
}

func TestCodexWebsocketsEnsureUpstreamConnRedialsForLunaHeaderProfile(t *testing.T) {
	const (
		normalModel = "gpt-5.4"
		lunaModel   = "gpt-5.6-luna"
		normalUA    = "codex-tui/test-normal"
		lunaUA      = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- r.UserAgent()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-model-header-profile-change"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-1"}

	normalHeaders := http.Header{"User-Agent": []string{normalUA}}
	normalProfile := resolveCodexModelHeaderProfile(normalModel)
	normalKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, normalModel, normalProfile.digest)
	firstConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, normalKey, normalHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(normal) error = %v", err)
	}

	lunaHeaders := http.Header{"User-Agent": []string{normalUA}}
	lunaProfile := resolveCodexModelHeaderProfile(lunaModel)
	applyModelHeaderOverrides(lunaHeaders, lunaProfile)
	lunaKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, lunaModel, lunaProfile.digest)
	secondConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, lunaKey, lunaHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(luna) error = %v", err)
	}
	if secondConn == firstConn {
		t.Fatal("Luna header profile reused the normal-model websocket; want a fresh connection")
	}

	for i, wantUA := range []string{normalUA, lunaUA} {
		select {
		case gotUA := <-handshakes:
			if gotUA != wantUA {
				t.Fatalf("handshake %d User-Agent = %q, want %q", i+1, gotUA, wantUA)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for handshake %d", i+1)
		}
	}

	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("planned connection profile change signaled upstream disconnect: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCodexWebsocketsEnsureUpstreamConnUsesAppliedHeaderProfileSnapshot(t *testing.T) {
	const (
		modelID  = "test-codex-websocket-profile-snapshot"
		clientID = "test-codex-websocket-profile-snapshot-client"
	)

	reg := registry.GetGlobalRegistry()
	registerProfile := func(userAgent string) {
		reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
			ID: modelID,
			Config: &registry.ModelConfig{OverrideHeader: map[string]string{
				"user-agent": userAgent,
			}},
		}})
	}
	registerProfile("snapshot-a/1.0")
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	profileA := resolveCodexModelHeaderProfile(modelID)
	headersA := http.Header{}
	applyModelHeaderOverrides(headersA, profileA)

	// Change the live registry after request headers and their connection key
	// profile have been captured. The first dial must still use snapshot A.
	registerProfile("snapshot-b/1.0")

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- r.UserAgent()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-applied-header-profile-snapshot"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-profile-snapshot"}

	keyA := newCodexWebsocketConnectionKey(auth.ID, wsURL, modelID, profileA.digest)
	firstConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, keyA, headersA)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(snapshot A) error = %v", err)
	}

	profileB := resolveCodexModelHeaderProfile(modelID)
	headersB := http.Header{}
	applyModelHeaderOverrides(headersB, profileB)
	keyB := newCodexWebsocketConnectionKey(auth.ID, wsURL, modelID, profileB.digest)
	secondConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, keyB, headersB)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(snapshot B) error = %v", err)
	}
	if secondConn == firstConn {
		t.Fatal("updated applied profile reused the snapshot A websocket; want redial")
	}

	for index, wantUserAgent := range []string{"snapshot-a/1.0", "snapshot-b/1.0"} {
		select {
		case gotUserAgent := <-handshakes:
			if gotUserAgent != wantUserAgent {
				t.Fatalf("handshake %d User-Agent = %q, want %q", index+1, gotUserAgent, wantUserAgent)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for handshake %d", index+1)
		}
	}
}

func TestCodexWebsocketsEnsureUpstreamConnReusesMatchingConnectionKey(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- struct{}{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-matching-connection-key"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	headers := http.Header{
		"User-Agent":          []string{"codex-tui/test"},
		"X-Client-Request-Id": []string{"request-1"},
	}

	profile := resolveCodexModelHeaderProfile("gpt-5.4")
	connectionKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, "gpt-5.4", profile.digest)
	firstConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, connectionKey, headers)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(first) error = %v", err)
	}
	secondHeaders := headers.Clone()
	secondHeaders.Set("X-Client-Request-Id", "request-2")
	secondConn, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, connectionKey, secondHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(second) error = %v", err)
	}
	if secondConn != firstConn {
		t.Fatal("matching connection key redialed; want websocket reuse")
	}

	select {
	case <-handshakes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial websocket handshake")
	}
	select {
	case <-handshakes:
		t.Fatal("matching connection key created a second websocket handshake")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestApplyCodexWebsocketHeadersDefaultsToCurrentResponsesBeta(t *testing.T) {
	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, nil, "", nil)

	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
	if got := headers.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %s, want %s", got, codexUserAgent)
	}
	if !strings.HasPrefix(codexUserAgent, codexOriginator+"/") {
		t.Fatalf("default Codex User-Agent = %s, want prefix %s/", codexUserAgent, codexOriginator)
	}
	if !strings.HasPrefix(codexUserAgent, "codex-tui/") {
		t.Fatalf("default Codex User-Agent = %s, want codex-tui prefix", codexUserAgent)
	}
	if !strings.Contains(codexUserAgent, "(codex-tui;") {
		t.Fatalf("default Codex User-Agent = %s, want codex-tui suffix", codexUserAgent)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %s, want %s", got, codexOriginator)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"User-Agent":            "codex_cli_rs/0.1.0",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
		"session-id":            "legacy-session",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := headers.Get("User-Agent"); got != "codex_cli_rs/0.1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "codex_cli_rs/0.1.0")
	}
	if got := headers.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
	if got := headers["session_id"]; len(got) != 1 || got[0] != "legacy-session" {
		t.Fatalf("session_id = %#v, want [legacy-session]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersCanonicalizesLegacyUnderscoreSessionHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator": "Codex Desktop",
		"User-Agent": "codex_cli_rs/0.1.0",
		"Session_id": "legacy-underscore-session",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers["session_id"]; len(got) != 1 || got[0] != "legacy-underscore-session" {
		t.Fatalf("session_id = %#v, want [legacy-underscore-session]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigDefaultsForOAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "my-codex-client/1.0",
			BetaFeatures: "feature-a,feature-b",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "my-codex-client/1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "my-codex-client/1.0")
	}
	if got := headers.Get("x-codex-beta-features"); got != "feature-a,feature-b" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "feature-a,feature-b")
	}
	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
}

func TestApplyCodexWebsocketHeadersPrefersExistingHeadersOverClientAndConfig(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	headers.Set("X-Codex-Beta-Features", "existing-beta")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "existing-ua" {
		t.Fatalf("User-Agent = %s, want %s", gotVal, "existing-ua")
	}
	if gotVal := got.Get("x-codex-beta-features"); gotVal != "existing-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", gotVal, "existing-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesClientHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := headers.Get("x-codex-beta-features"); got != "client-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "client-beta")
	}
}

func TestApplyCodexWebsocketHeadersIgnoresConfigForAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "sk-test", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != "" {
		t.Fatalf("Originator = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPreservesExplicitAPIKeyUserAgent(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test"}}
	ctx := contextWithGinHeaders(map[string]string{"User-Agent": "api-key-client/1.0", "Originator": "explicit-origin"})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "sk-test", nil)

	if got := headers.Get("User-Agent"); got != "api-key-client/1.0" {
		t.Fatalf("User-Agent = %s, want api-key-client/1.0", got)
	}
	if got := headers.Get("Originator"); got != "explicit-origin" {
		t.Fatalf("Originator = %s, want explicit-origin", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesCanonicalAccountHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", nil)

	if got := headerValueCaseInsensitive(headers, "ChatGPT-Account-ID"); got != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID = %s, want acct-1", got)
	}
	values, ok := headers["ChatGPT-Account-ID"]
	if !ok {
		t.Fatalf("expected exact ChatGPT-Account-ID key, got %#v", headers)
	}
	if len(values) != 1 || values[0] != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID values = %#v, want [acct-1]", values)
	}
}

func TestApplyCodexPromptCacheHeadersSetsSessionIDAndLegacyConversation(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"prompt_cache_key":"cache-1"}`)}

	_, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))

	if got := headers["session_id"]; len(got) != 1 || got[0] != "cache-1" {
		t.Fatalf("session_id = %#v, want [cache-1]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
	if got := headers.Get("Conversation_id"); got != "cache-1" {
		t.Fatalf("Conversation_id = %s, want cache-1", got)
	}
}

func TestApplyCodexPromptCacheHeadersOpenAIChatPreservesExplicitKey(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"tenant:explicit"}`),
	}

	body, headers := applyCodexPromptCacheHeaders("openai", req, []byte(`{"model":"gpt-5.6-sol"}`))
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "tenant:explicit" {
		t.Fatalf("prompt_cache_key = %q, want explicit client key; body=%s", got, body)
	}
	if got := headers["session_id"]; len(got) != 1 || got[0] != "tenant:explicit" {
		t.Fatalf("session_id = %#v, want [tenant:explicit]", got)
	}
	if got := headers.Get("Conversation_id"); got != "tenant:explicit" {
		t.Fatalf("Conversation_id = %q, want tenant:explicit", got)
	}
}

func TestApplyCodexPromptCacheHeadersOpenAIChatUsesStableAPIKeyFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", "test-api-key")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol"}`)}

	firstBody, firstHeaders, err := applyCodexPromptCacheHeadersWithContext(ctx, sdktranslator.FromString("openai"), req, []byte(`{"model":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("first prompt cache headers: %v", err)
	}
	secondBody, secondHeaders, err := applyCodexPromptCacheHeadersWithContext(ctx, sdktranslator.FromString("openai"), req, []byte(`{"model":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("second prompt cache headers: %v", err)
	}

	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" || secondKey != firstKey {
		t.Fatalf("stable fallback keys = (%q, %q), want same non-empty key", firstKey, secondKey)
	}
	if got := firstHeaders.Get("Conversation_id"); got != firstKey {
		t.Fatalf("first Conversation_id = %q, want %q", got, firstKey)
	}
	if got := secondHeaders.Get("Conversation_id"); got != secondKey {
		t.Fatalf("second Conversation_id = %q, want %q", got, secondKey)
	}
}

func TestApplyCodexPromptCacheHeadersClaudeUsesClaudeCodeSessionID(t *testing.T) {
	firstReq := cliproxyexecutor.Request{
		Model: "gpt-5-codex-claude-ws-cache-session",
		Payload: []byte(`{
			"metadata":{"user_id":"{\"device_id\":\"device-a\",\"account_uuid\":\"\",\"session_id\":\"ws-cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]
		}`),
	}
	secondReq := cliproxyexecutor.Request{
		Model: "gpt-5-codex-claude-ws-cache-session",
		Payload: []byte(`{
			"metadata":{"user_id":"{\"device_id\":\"device-b\",\"account_uuid\":\"\",\"session_id\":\"ws-cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]
		}`),
	}

	firstBody, firstHeaders := applyCodexPromptCacheHeaders("claude", firstReq, []byte(`{"model":"gpt-5-codex"}`))
	secondBody, secondHeaders := applyCodexPromptCacheHeaders("claude", secondReq, []byte(`{"model":"gpt-5-codex"}`))

	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" {
		t.Fatalf("first prompt_cache_key is empty; body=%s", string(firstBody))
	}
	if secondKey != firstKey {
		t.Fatalf("same Claude Code session_id produced different websocket prompt_cache_key: first=%q second=%q", firstKey, secondKey)
	}
	if got := firstHeaders["session_id"]; len(got) != 1 || got[0] != firstKey {
		t.Fatalf("first session_id = %#v, want [%q]", got, firstKey)
	}
	if got := secondHeaders["session_id"]; len(got) != 1 || got[0] != firstKey {
		t.Fatalf("second session_id = %#v, want [%q]", got, firstKey)
	}
}

func TestApplyCodexPromptCacheHeadersClaudeRejectsBareUserID(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex-claude-ws-cache-bare-user",
		Payload: []byte(`{"metadata":{"user_id":"same-user-across-chats"},"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]}`),
	}

	body, headers := applyCodexPromptCacheHeaders("claude", req, []byte(`{"model":"gpt-5-codex"}`))

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket prompt_cache_key, got %q; body=%s", got, string(body))
	}
	if got := headers["session_id"]; len(got) != 0 {
		t.Fatalf("bare metadata.user_id must not create websocket session_id, got %#v", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket Session-Id, got %q", got)
	}
	if got := headers.Get("Conversation_id"); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket Conversation_id, got %q", got)
	}
}

func TestApplyCodexWebsocketHeadersIdentityConfuseRemapsPromptCacheKey(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-ws-1", Provider: "codex"}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"prompt_cache_key":"cache-ws-1","client_metadata":{"x-codex-installation-id":"install-ws-1"}}`),
	}

	body, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))
	body, identityState := applyCodexIdentityConfuseBody(cfg, auth, req.Payload, body)
	ctx := contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1","window_id":"cache-ws-1:0"}`,
		"X-Client-Request-Id":   "client-request-1",
	})
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexIdentityConfuseHeaders(headers, &identityState)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-ws-1", "prompt-cache", "cache-ws-1")
	expectedTurnID := codexIdentityConfuseUUID("auth-ws-1", "turn", "turn-ws-1")
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedPromptCacheKey)
	}
	if gotSession := headers["session_id"]; len(gotSession) != 1 || gotSession[0] != expectedPromptCacheKey {
		t.Fatalf("session_id = %#v, want [%q]", gotSession, expectedPromptCacheKey)
	}
	if gotCanonicalSession := headers.Get("Session-Id"); gotCanonicalSession != "" {
		t.Fatalf("Session-Id = %q, want empty", gotCanonicalSession)
	}
	if gotRequestID := headers.Get("X-Client-Request-Id"); gotRequestID != expectedPromptCacheKey {
		t.Fatalf("X-Client-Request-Id = %q, want %q", gotRequestID, expectedPromptCacheKey)
	}
	if gotThreadID := headers.Get("Thread-Id"); gotThreadID != expectedPromptCacheKey {
		t.Fatalf("Thread-Id = %q, want %q", gotThreadID, expectedPromptCacheKey)
	}
	if gotConversation := headers.Get("Conversation_id"); gotConversation != expectedPromptCacheKey {
		t.Fatalf("Conversation_id = %q, want %q", gotConversation, expectedPromptCacheKey)
	}
	if gotWindowID := headers.Get("X-Codex-Window-Id"); gotWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", gotWindowID, expectedPromptCacheKey+":0")
	}
	gotMetadata := headers.Get("X-Codex-Turn-Metadata")
	if gotMetadataPromptCacheKey := gjson.Get(gotMetadata, "prompt_cache_key").String(); gotMetadataPromptCacheKey != expectedPromptCacheKey {
		t.Fatalf("X-Codex-Turn-Metadata.prompt_cache_key = %q, want %q", gotMetadataPromptCacheKey, expectedPromptCacheKey)
	}
	if gotMetadataTurnID := gjson.Get(gotMetadata, "turn_id").String(); gotMetadataTurnID != expectedTurnID {
		t.Fatalf("X-Codex-Turn-Metadata.turn_id = %q, want %q", gotMetadataTurnID, expectedTurnID)
	}
	if gotMetadataWindowID := gjson.Get(gotMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Turn-Metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":0")
	}
	expectedInstallationID := codexIdentityConfuseUUID("auth-ws-1", "installation", "install-ws-1")
	if gotInstallationID := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); gotInstallationID != expectedInstallationID {
		t.Fatalf("installation id = %q, want %q", gotInstallationID, expectedInstallationID)
	}
}

func TestApplyCodexWebsocketHeadersCanonicalMetadataBypassesLegacyIdentityConfuse(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex: config.CodexConfig{
			IdentityConfuse: true,
			ClientMetadata: config.CodexClientMetadataConfig{
				Mode:            config.CodexClientMetadataModeRepair,
				WorkspacePolicy: config.CodexClientMetadataWorkspacePolicyDrop,
			},
		},
	}
	auth := &cliproxyauth.Auth{ID: "auth-ws-1", Provider: "codex", Metadata: map[string]any{"account_id": "acct-ws-1"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"input":"hello"}`),
	}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"installation_id\":\"install-ws-1\",\"session_id\":\"thread-ws-1\",\"thread_id\":\"thread-ws-1\",\"turn_id\":\"turn-ws-1\",\"window_id\":\"thread-ws-1:2\",\"request_kind\":\"turn\",\"workspaces\":{\"/private/project\":{\"associated_remote_urls\":{\"origin\":\"https://token@example.com/org/repo.git\"}}}}","thread_id":"wrong-thread","x-codex-window-id":"wrong:0"}}`)
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	ctx := contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"thread_id":"header-conflict"}`,
		"X-Client-Request-Id":   "client-request-1",
	})

	upstreamBody, state, err := prepareCodexOutboundMetadata(ctx, cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexOutboundMetadataHeaders(headers, &state)

	if state.enabled || !state.clientMetadata.CanonicalPresent {
		t.Fatalf("canonical websocket state unexpectedly used legacy identity mapping: %+v", state)
	}
	metadata := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-turn-metadata").String()
	if strings.Contains(metadata, `"workspaces"`) || strings.Contains(metadata, "token@example.com") {
		t.Fatalf("drop policy did not remove websocket workspace metadata: %s", metadata)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.thread_id").String(); got != "thread-ws-1" {
		t.Fatalf("flat thread_id = %q", got)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-window-id").String(); got != "thread-ws-1:2" {
		t.Fatalf("flat window_id = %q", got)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != "thread-ws-1:2" {
		t.Fatalf("X-Codex-Window-Id = %q", got)
	}
	if got := codexSessionHeaderValue(headers); got != "thread-ws-1" {
		t.Fatalf("session_id = %q, want canonical session", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != state.clientMetadata.TurnMetadata {
		t.Fatal("websocket canonical header does not match normalized body metadata")
	}
}

func TestApplyCodexWebsocketHeadersOffModeProjectsCanonicalSessionWithoutMutatingBody(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}
	auth := &cliproxyauth.Auth{ID: "auth-ws-off", Provider: "codex"}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-ws-session\",\"thread_id\":\"off-ws-session\"}","thread_id":"legacy-conflict"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawBody}
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	headers.Set("User-Agent", "Codex Desktop (Mac OS)")

	upstreamBody, state, err := prepareCodexOutboundMetadata(context.Background(), cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	if !bytes.Equal(upstreamBody, rawBody) {
		t.Fatalf("off mode mutated websocket body: got %s want %s", upstreamBody, rawBody)
	}
	headers = applyCodexWebsocketHeaders(context.Background(), headers, auth, "oauth-token", cfg)
	if fallback := codexSessionHeaderValue(headers); fallback == "" || fallback == "off-ws-session" {
		t.Fatalf("expected pre-projection random fallback, got %q", fallback)
	}
	applyCodexOutboundMetadataHeaders(headers, &state)
	if got := codexSessionHeaderValue(headers); got != "off-ws-session" {
		t.Fatalf("session_id = %q, want off-ws-session", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("off mode rebuilt X-Codex-Turn-Metadata = %q", got)
	}
}

func TestApplyCodexWebsocketHeadersOffModeBodyCanonicalSuppressesConflictingDirectHeader(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}
	auth := &cliproxyauth.Auth{ID: "auth-ws-off-conflict", Provider: "codex"}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-ws-body-session\"}"}}`)
	direct := `{"request_kind":"turn","session_id":"off-ws-header-session"}`
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawBody}
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	ctx := contextWithGinHeaders(map[string]string{"X-Codex-Turn-Metadata": direct})

	upstreamBody, state, err := prepareCodexOutboundMetadata(ctx, cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	if !bytes.Equal(upstreamBody, rawBody) {
		t.Fatalf("off mode mutated websocket body: got %s want %s", upstreamBody, rawBody)
	}
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexOutboundMetadataHeaders(headers, &state)
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("conflicting direct canonical header survived body precedence: %q", got)
	}
	if got := codexSessionHeaderValue(headers); got != "off-ws-body-session" {
		t.Fatalf("session_id = %q, want off-ws-body-session", got)
	}
}

func TestCodexIdentityConfuseResponsePayloadHidesUpstreamAndRestoresClient(t *testing.T) {
	state := codexIdentityConfuseState{
		enabled:                true,
		authID:                 "auth-ws-1",
		originalPromptCacheKey: "cache-ws-1",
		promptCacheKey:         codexIdentityConfuseUUID("auth-ws-1", "prompt-cache", "cache-ws-1"),
	}
	expectedTurnID := state.confuseTurnID("turn-ws-1")
	rawPayload := []byte(`{"type":"response.completed","response":{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"},"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"}`)

	upstreamPayload := applyCodexIdentityConfuseResponsePayload(rawPayload, state)
	if bytes.Contains(upstreamPayload, []byte(`cache-ws-1`)) {
		t.Fatalf("upstream payload still contains original prompt_cache_key: %s", string(upstreamPayload))
	}
	if bytes.Contains(upstreamPayload, []byte(`turn-ws-1`)) {
		t.Fatalf("upstream payload still contains original turn_id: %s", string(upstreamPayload))
	}
	if !bytes.Contains(upstreamPayload, []byte(state.promptCacheKey)) {
		t.Fatalf("upstream payload missing confused prompt_cache_key: %s", string(upstreamPayload))
	}
	if !bytes.Contains(upstreamPayload, []byte(expectedTurnID)) {
		t.Fatalf("upstream payload missing confused turn_id: %s", string(upstreamPayload))
	}

	clientPayload := applyCodexIdentityExposeResponsePayload(upstreamPayload, state)
	if bytes.Contains(clientPayload, []byte(state.promptCacheKey)) {
		t.Fatalf("client payload still contains confused prompt_cache_key: %s", string(clientPayload))
	}
	if bytes.Contains(clientPayload, []byte(expectedTurnID)) {
		t.Fatalf("client payload still contains confused turn_id: %s", string(clientPayload))
	}
	if !bytes.Contains(clientPayload, []byte(`cache-ws-1`)) {
		t.Fatalf("client payload missing original prompt_cache_key: %s", string(clientPayload))
	}
	if !bytes.Contains(clientPayload, []byte(`turn-ws-1`)) {
		t.Fatalf("client payload missing original turn_id: %s", string(clientPayload))
	}

	rawSSE := []byte(`data: {"type":"response.completed","response":{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"}}`)
	upstreamSSE := applyCodexIdentityConfuseResponsePayload(rawSSE, state)
	if bytes.Contains(upstreamSSE, []byte(`cache-ws-1`)) {
		t.Fatalf("upstream SSE still contains original prompt_cache_key: %s", string(upstreamSSE))
	}
	if bytes.Contains(upstreamSSE, []byte(`turn-ws-1`)) {
		t.Fatalf("upstream SSE still contains original turn_id: %s", string(upstreamSSE))
	}
	clientSSE := applyCodexIdentityExposeResponsePayload(upstreamSSE, state)
	if !bytes.Contains(clientSSE, []byte(`cache-ws-1`)) || bytes.Contains(clientSSE, []byte(state.promptCacheKey)) {
		t.Fatalf("client SSE prompt_cache_key was not restored: %s", string(clientSSE))
	}
	if !bytes.Contains(clientSSE, []byte(`turn-ws-1`)) || bytes.Contains(clientSSE, []byte(expectedTurnID)) {
		t.Fatalf("client SSE turn_id was not restored: %s", string(clientSSE))
	}
}

func TestBuildCodexResponsesWebsocketURLRequiresHTTPURL(t *testing.T) {
	if got, err := buildCodexResponsesWebsocketURL("https://example.com/backend/responses"); err != nil || got != "wss://example.com/backend/responses" {
		t.Fatalf("https URL = %q, %v; want wss URL", got, err)
	}
	if _, err := buildCodexResponsesWebsocketURL("ftp://example.com/responses"); err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
	if _, err := buildCodexResponsesWebsocketURL("https:///responses"); err == nil {
		t.Fatalf("expected empty host error")
	}
}

func TestParseCodexWebsocketErrorMarksConnectionLimitRetryable(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"},"headers":{"retry-after":"1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable websocket connection limit error")
	}
	if got := *retryable.RetryAfter(); got != 0 {
		t.Fatalf("retryAfter = %v, want connection-limit fallback 0", got)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("retry-after") != "1" {
		t.Fatalf("headers = %#v, want retry-after", err)
	}
}

func TestParseCodexWebsocketErrorUsesUsageLimitRetryMetadata(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":7}}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable usage limit websocket error")
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("retryAfter = %v, want 7s", got)
	}
}

func TestCodexWebsocketHandshakeStatusClassifiesFallbackAndPreservesHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Retry-After", "9")
	headers.Set("X-Request-ID", "req-handshake")
	err := newCodexWebsocketHandshakeStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"usage_limit_reached","message":"quota","resets_in_seconds":7}}`), headers)
	classified, ok := err.(interface{ ModelFallbackReason() string })
	if !ok || classified.ModelFallbackReason() != config.CodexModelFallbackTriggerUsageLimit {
		t.Fatalf("fallback reason = %#v, want usage-limit", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
		t.Fatalf("RetryAfter = %#v, want resets_in_seconds", err)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("X-Request-ID") != "req-handshake" {
		t.Fatalf("headers = %#v, want preserved upgrade headers", err)
	}
	transient := newCodexWebsocketHandshakeStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_error","message":"transient"}}`), nil)
	if got := transient.(interface{ ModelFallbackReason() string }).ModelFallbackReason(); got != "" {
		t.Fatalf("bare/transient 429 fallback reason = %q, want empty", got)
	}
}

func TestParseCodexWebsocketErrorPreservesWrappedBodyAndHeaders(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"code":"websocket_connection_limit_reached","type":"server_error","message":"too many websocket connections"}},"headers":{"x-request-id":"req-1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("status").Int(); got != http.StatusTooManyRequests {
		t.Fatalf("wrapped status = %d, want 429; payload=%s", got, err.Error())
	}
	if got := parsed.Get("body.error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("wrapped body error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	if got := parsed.Get("error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("surface error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected body.error.code websocket connection limit to be retryable")
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("x-request-id") != "req-1" {
		t.Fatalf("headers = %#v, want x-request-id", err)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForOAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
}

func TestApplyModelHeaderOverridesFromModelConfig(t *testing.T) {
	const wantUA = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent: "config-ua",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)
	applyModelHeaderOverrides(req.Header, resolveCodexModelHeaderProfile("gpt-5.6-luna"))

	if got := req.Header.Get("User-Agent"); got != wantUA {
		t.Fatalf("User-Agent = %q, want %q", got, wantUA)
	}
	if got := codexSessionHeaderValue(req.Header); got == "" {
		t.Fatal("expected Session_id to be set for Mac OS User-Agent override")
	}

	applyModelHeaderOverrides(req.Header, resolveCodexModelHeaderProfile("gpt-5.4"))
	if got := req.Header.Get("User-Agent"); got != wantUA {
		t.Fatalf("User-Agent after no-op override = %q, want %q", got, wantUA)
	}
}

func TestApplyModelHeaderOverridesMultipleHeaders(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-header-override"
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID: "test-override-headers-model",
		Config: &registry.ModelConfig{
			OverrideHeader: map[string]string{
				"user-agent":    "custom-ua/1.0",
				"originator":    "custom-origin",
				"x-test-header": "forced-value",
			},
		},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	headers := http.Header{}
	headers.Set("User-Agent", "old-ua")
	headers.Set("Originator", "old-origin")
	headers.Set("X-Test-Header", "old-value")

	applyModelHeaderOverrides(headers, resolveCodexModelHeaderProfile("test-override-headers-model"))

	if got := headers.Get("User-Agent"); got != "custom-ua/1.0" {
		t.Fatalf("User-Agent = %q, want custom-ua/1.0", got)
	}
	if got := headers.Get("Originator"); got != "custom-origin" {
		t.Fatalf("Originator = %q, want custom-origin", got)
	}
	if got := headers.Get("X-Test-Header"); got != "forced-value" {
		t.Fatalf("X-Test-Header = %q, want forced-value", got)
	}
}

func TestApplyCodexHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if got := req.Header.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := req.Header.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexHeadersDoesNotInjectClientOnlyHeadersByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func contextWithGinHeaders(headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ginCtx.Request.Header = make(http.Header, len(headers))
	for key, value := range headers {
		ginCtx.Request.Header.Set(key, value)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestNewProxyAwareWebsocketDialerDirectDisablesProxy(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
	)

	if dialer.Proxy != nil {
		t.Fatal("expected websocket proxy function to be nil for direct mode")
	}
}
