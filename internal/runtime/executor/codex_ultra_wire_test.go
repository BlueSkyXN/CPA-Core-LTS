package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type gpt56UltraCapturedRequest struct {
	path string
	body []byte
}

func TestCodexExecutorExecuteCanonicalizesGPT56UltraFinalWire(t *testing.T) {
	server, captured := newGPT56UltraHTTPServer(t)
	defer server.Close()

	executor := NewCodexExecutor(gpt56UltraTestConfig())
	_, err := executor.Execute(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"effort":"ultra"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	request := waitForGPT56UltraHTTPRequest(t, captured)
	if request.path != "/responses" {
		t.Fatalf("path = %q, want /responses", request.path)
	}
	assertGPT56UltraWireEffort(t, request.body, "max")
}

func TestCodexExecutorExecutePreservesGPT56MaxFinalWire(t *testing.T) {
	models := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	for _, model := range models {
		model := model
		for _, fromSuffix := range []bool{false, true} {
			fromSuffix := fromSuffix
			name := "body"
			requestedModel := model
			effort := "max"
			if fromSuffix {
				name = "suffix"
				requestedModel = model + "(max)"
				effort = "low"
			}
			t.Run(model+" "+name, func(t *testing.T) {
				server, captured := newGPT56UltraHTTPServer(t)
				defer server.Close()

				executor := NewCodexExecutor(gpt56UltraTestConfig())
				_, err := executor.Execute(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
					Model: requestedModel,
					Payload: []byte(fmt.Sprintf(
						`{"model":%q,"input":"hello","reasoning":{"effort":%q}}`,
						model,
						effort,
					)),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}

				request := waitForGPT56UltraHTTPRequest(t, captured)
				assertGPT56UltraWireEffort(t, request.body, "max")
			})
		}
	}
}

func TestCodexExecutorExecuteStreamCanonicalizesGPT56UltraSuffixFinalWire(t *testing.T) {
	server, captured := newGPT56UltraHTTPServer(t)
	defer server.Close()

	executor := NewCodexExecutor(gpt56UltraTestConfig())
	result, err := executor.ExecuteStream(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol(ultra)",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"effort":"low"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	request := waitForGPT56UltraHTTPRequest(t, captured)
	if request.path != "/responses" {
		t.Fatalf("path = %q, want /responses", request.path)
	}
	assertGPT56UltraWireEffort(t, request.body, "max")
}

func TestCodexExecutorCompactCanonicalizesPayloadOverrideUltraFinalWire(t *testing.T) {
	server, captured := newGPT56UltraHTTPServer(t)
	defer server.Close()

	executor := NewCodexExecutor(gpt56UltraOverrideConfig("gpt-5.6-terra"))
	_, err := executor.Execute(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-terra",
		Payload: []byte(`{"model":"gpt-5.6-terra","input":"hello","reasoning":{"effort":"max"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute(compact) error = %v", err)
	}

	request := waitForGPT56UltraHTTPRequest(t, captured)
	if request.path != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", request.path)
	}
	assertGPT56UltraWireEffort(t, request.body, "max")
}

func TestCodexExecutorFinalGuardRejectsLunaUltraPayloadOverride(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	executor := NewCodexExecutor(gpt56UltraOverrideConfig("gpt-5.6-luna"))
	_, err := executor.Execute(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"hello","reasoning":{"effort":"max"}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err == nil {
		t.Fatal("Execute() error = nil, want LEVEL_NOT_SUPPORTED")
	}
	var thinkingErr *thinking.ThinkingError
	if !errors.As(err, &thinkingErr) || thinkingErr.Code != thinking.ErrLevelNotSupported {
		t.Fatalf("error = %T %v, want LEVEL_NOT_SUPPORTED", err, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestCodexExecutorUnknownCustomModelPreservesLiteralUltraFinalWire(t *testing.T) {
	server, captured := newGPT56UltraHTTPServer(t)
	defer server.Close()

	executor := NewCodexExecutor(gpt56UltraTestConfig())
	_, err := executor.Execute(context.Background(), gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "custom-codex-ultra-wire",
		Payload: []byte(`{"model":"custom-codex-ultra-wire","input":"hello","reasoning":{"effort":"ultra"}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	request := waitForGPT56UltraHTTPRequest(t, captured)
	assertGPT56UltraWireEffort(t, request.body, "ultra")
}

func TestCodexExecutorCountTokensRunsGPT56WireValidation(t *testing.T) {
	executor := NewCodexExecutor(gpt56UltraTestConfig())
	payload := func(model, effort string) []byte {
		return []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"reasoning":{"effort":%q}}`, model, effort))
	}

	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if _, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
			Model: model, Payload: payload(model, "max"),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); err != nil {
			t.Fatalf("CountTokens(%s max) error = %v", model, err)
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
		if _, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
			Model: model, Payload: payload(model, "ultra"),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); err != nil {
			t.Fatalf("CountTokens(%s ultra) error = %v", model, err)
		}
	}
	_, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model: "gpt-5.6-luna", Payload: payload("gpt-5.6-luna", "ultra"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	var thinkingErr *thinking.ThinkingError
	if !errors.As(err, &thinkingErr) || thinkingErr.Code != thinking.ErrLevelNotSupported {
		t.Fatalf("CountTokens(luna ultra) error = %T %v, want LEVEL_NOT_SUPPORTED", err, err)
	}
	if _, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model: "custom-codex-ultra-count", Payload: payload("custom-codex-ultra-count", "ultra"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); err != nil {
		t.Fatalf("CountTokens(custom ultra) error = %v", err)
	}
}

func TestCodexWebsocketsExecuteCanonicalizesUltraAcrossReusedConnection(t *testing.T) {
	server, captured, handshakes := newGPT56UltraWebsocketServer(t)
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(gpt56UltraTestConfig())
	sessionID := "gpt56-ultra-wire-reuse"
	defer executor.CloseExecutionSession(sessionID)
	auth := gpt56UltraTestAuth(server.URL)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}

	requests := []cliproxyexecutor.Request{
		{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":"first","reasoning":{"effort":"max"}}`)},
		{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":"second","reasoning":{"effort":"ultra"}}`)},
	}
	for i := range requests {
		if _, err := executor.Execute(context.Background(), auth, requests[i], opts); err != nil {
			t.Fatalf("Execute(request %d) error = %v", i+1, err)
		}
	}

	for i := 0; i < 2; i++ {
		payload := waitForGPT56UltraWebsocketPayload(t, captured)
		assertGPT56UltraWireEffort(t, payload, "max")
	}
	if got := handshakes.Load(); got != 1 {
		t.Fatalf("websocket handshakes = %d, want 1 reused connection", got)
	}
}

func TestCodexWebsocketsExecuteStreamCanonicalizesPayloadOverrideUltraFinalWire(t *testing.T) {
	server, captured, _ := newGPT56UltraWebsocketServer(t)
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(gpt56UltraOverrideConfig("gpt-5.6-terra"))
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	result, err := executor.ExecuteStream(ctx, gpt56UltraTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-terra",
		Payload: []byte(`{"model":"gpt-5.6-terra","input":"hello","reasoning":{"effort":"max"}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	payload := waitForGPT56UltraWebsocketPayload(t, captured)
	assertGPT56UltraWireEffort(t, payload, "max")
}

func newGPT56UltraHTTPServer(t *testing.T) (*httptest.Server, <-chan gpt56UltraCapturedRequest) {
	t.Helper()
	captured := make(chan gpt56UltraCapturedRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		captured <- gpt56UltraCapturedRequest{path: r.URL.Path, body: bytes.Clone(body)}
		if r.URL.Path == "/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		model := gjson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":%q,\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", model)
	}))
	return server, captured
}

func newGPT56UltraWebsocketServer(t *testing.T) (*httptest.Server, <-chan []byte, *atomic.Int32) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	captured := make(chan []byte, 8)
	handshakes := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			captured <- bytes.Clone(payload)
			completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				return
			}
		}
	}))
	return server, captured, handshakes
}

func gpt56UltraTestConfig() *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
}

func gpt56UltraOverrideConfig(model string) *config.Config {
	cfg := gpt56UltraTestConfig()
	cfg.Payload.Override = []config.PayloadRule{
		{
			Models: []config.PayloadModelRule{{Name: model}},
			Params: map[string]any{"reasoning.effort": "ultra"},
		},
	}
	return cfg
}

func gpt56UltraTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "gpt56-ultra-wire-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  baseURL,
			"api_key":   "test",
			"auth_kind": "oauth",
		},
	}
}

func waitForGPT56UltraHTTPRequest(t *testing.T, captured <-chan gpt56UltraCapturedRequest) gpt56UltraCapturedRequest {
	t.Helper()
	select {
	case request := <-captured:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
		return gpt56UltraCapturedRequest{}
	}
}

func waitForGPT56UltraWebsocketPayload(t *testing.T, captured <-chan []byte) []byte {
	t.Helper()
	select {
	case payload := <-captured:
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket payload")
		return nil
	}
}

func assertGPT56UltraWireEffort(t *testing.T, body []byte, want string) {
	t.Helper()
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != want {
		t.Fatalf("reasoning.effort = %q, want %q; body=%s", got, want, body)
	}
}
