package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const sparkUnsupportedReasoningSummaryError = `{"error":{"message":"Unsupported parameter: 'reasoning.summary' is not supported with the 'gpt-5.3-codex-spark' model.","type":"invalid_request_error","param":"reasoning.summary","code":"unsupported_parameter"}}`

func TestCodexExecutorDropsUnsupportedReasoningSummaryForSpark(t *testing.T) {
	tests := []struct {
		name         string
		sourceFormat sdktranslator.Format
		payload      []byte
	}{
		{
			name:         "native responses payload",
			sourceFormat: sdktranslator.FromString("openai-response"),
			payload:      []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`),
		},
		{
			name:         "chat completions translator default",
			sourceFormat: sdktranslator.FromString("openai"),
			payload:      []byte(`{"model":"gpt-5.3-codex-spark","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hello"}]}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request body: %v", errRead)
					return
				}
				gotBody = bytes.Clone(body)
				if gjson.GetBytes(body, "reasoning.summary").Exists() {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(sparkUnsupportedReasoningSummaryError))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
			}))
			defer server.Close()

			exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.3-codex-spark",
				Payload: tt.payload,
			}, cliproxyexecutor.Options{
				SourceFormat: tt.sourceFormat,
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute() returned the upstream compatibility error: %v", err)
			}
			if gjson.GetBytes(gotBody, "reasoning.summary").Exists() {
				t.Fatalf("reasoning.summary still exists in Spark request: %s", gotBody)
			}
			if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "xhigh" {
				t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, gotBody)
			}
		})
	}
}

func TestCodexExecutorDropsUnsupportedReasoningSummaryAfterPayloadOverride(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = bytes.Clone(body)
		if gjson.GetBytes(body, "reasoning.summary").Exists() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(sparkUnsupportedReasoningSummaryError))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.3-codex-spark", Protocol: "codex"}},
					Params: map[string]any{"reasoning.summary": "auto"},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() returned the upstream compatibility error: %v", err)
	}
	if gjson.GetBytes(gotBody, "reasoning.summary").Exists() {
		t.Fatalf("payload.override restored reasoning.summary in Spark request: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, gotBody)
	}
}

func TestCodexExecutorSparkMaxReasoningEffortRemainsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream request for locally invalid Spark effort")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"max","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported max effort error")
	}
	const want = `level "max" not supported, valid levels: low, medium, high, xhigh`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Execute() error = %q, want %q", err, want)
	}
}

func TestCodexExecutorExecuteStreamDropsUnsupportedReasoningSummaryForSpark(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = bytes.Clone(body)
		if gjson.GetBytes(body, "reasoning.summary").Exists() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(sparkUnsupportedReasoningSummaryError))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() returned the upstream compatibility error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
	if gjson.GetBytes(gotBody, "reasoning.summary").Exists() {
		t.Fatalf("reasoning.summary still exists in Spark stream request: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, gotBody)
	}
}

func TestCodexExecutorCompactDropsUnsupportedReasoningSummaryForSpark(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = bytes.Clone(body)
		if gjson.GetBytes(body, "reasoning.summary").Exists() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(sparkUnsupportedReasoningSummaryError))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute compact returned the upstream compatibility error: %v", err)
	}
	if gjson.GetBytes(gotBody, "reasoning.summary").Exists() {
		t.Fatalf("reasoning.summary still exists in Spark compact request: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, gotBody)
	}
}

func TestCodexWebsocketsExecutorDropsUnsupportedReasoningSummaryForSpark(t *testing.T) {
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
		if gjson.GetBytes(payload, "reasoning.summary").Exists() {
			upstreamErr := []byte(`{"type":"error","status":400,"error":` + sparkUnsupportedReasoningSummaryError[len(`{"error":`):len(sparkUnsupportedReasoningSummaryError)-1] + `}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, upstreamErr); errWrite != nil {
				t.Errorf("write websocket error: %v", errWrite)
			}
			return
		}
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket completion: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() returned the upstream compatibility error: %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if gjson.GetBytes(payload, "reasoning.summary").Exists() {
			t.Fatalf("reasoning.summary still exists in Spark websocket request: %s", payload)
		}
		if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != "xhigh" {
			t.Fatalf("reasoning.effort = %q, want xhigh; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecutorExecuteStreamDropsUnsupportedReasoningSummaryForSpark(t *testing.T) {
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
		if gjson.GetBytes(payload, "reasoning.summary").Exists() {
			upstreamErr := []byte(`{"type":"error","status":400,"error":` + sparkUnsupportedReasoningSummaryError[len(`{"error":`):len(sparkUnsupportedReasoningSummaryError)-1] + `}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, upstreamErr); errWrite != nil {
				t.Errorf("write websocket error: %v", errWrite)
			}
			return
		}
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket completion: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex-spark",
		Payload: []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() returned the upstream compatibility error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-capturedPayload:
		if gjson.GetBytes(payload, "reasoning.summary").Exists() {
			t.Fatalf("reasoning.summary still exists in Spark websocket stream request: %s", payload)
		}
		if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != "xhigh" {
			t.Fatalf("reasoning.effort = %q, want xhigh; payload=%s", got, payload)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for upstream websocket payload: %v", ctx.Err())
	}
}

func TestSanitizeCodexUnsupportedReasoningSummaryRemovesEmptyReasoningObject(t *testing.T) {
	got := sanitizeCodexUnsupportedReasoningSummary([]byte(`{"reasoning":{"summary":"auto"},"input":"hello"}`), "gpt-5.3-codex-spark")
	if gjson.GetBytes(got, "reasoning").Exists() {
		t.Fatalf("empty reasoning object still exists: %s", got)
	}
}

func TestCodexExecutorPreservesReasoningSummaryForSupportedModel(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","reasoning":{"effort":"high","summary":"auto"},"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want auto; body=%s", got, gotBody)
	}
}
