package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func newCodexUpstreamModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5-sol-2026-0815", 516)))
	}))
}

func newCodexCreatedModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5-sol-2026-0815\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
}

func TestCodexExecutorRecordsUpstreamResponseModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream", stream: false},
		{name: "stream", stream: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-upstream-model-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-upstream-model-test", noopUsagePlugin{})
			})

			server := newCodexUpstreamModelServer(t)
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := codexAbnormalReasoningRetryTestAuth(server.URL)
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.5-sol",
				Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       tc.stream,
			}

			if tc.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for range result.Chunks {
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
				t.Fatalf("Execute: %v", errExec)
			}

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5-sol"
			})
			if record.UpstreamModel != "gpt-5.5-sol-2026-0815" {
				t.Fatalf("record.UpstreamModel = %q, want gpt-5.5-sol-2026-0815", record.UpstreamModel)
			}
		})
	}
}

func TestCodexExecutorRecordsUpstreamResponseModelFromCreatedEvent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream", stream: false},
		{name: "stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-upstream-model-created-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-upstream-model-created-test", noopUsagePlugin{})
			})

			server := newCodexCreatedModelServer(t)
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := codexAbnormalReasoningRetryTestAuth(server.URL)
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.5-sol",
				Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       tc.stream,
			}

			if tc.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk: %v", chunk.Err)
					}
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
				t.Fatalf("Execute: %v", errExec)
			}

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5-sol"
			})
			if record.UpstreamModel != "gpt-5.5-sol-2026-0815" {
				t.Fatalf("record.UpstreamModel = %q, want model from response.created", record.UpstreamModel)
			}
		})
	}
}

func TestCodexExecutorRecordsUpstreamResponseModelOnBootstrapFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event string
	}{
		{name: "overload", event: codexOverloadEvent},
		{name: "empty-incomplete", event: `{"type":"response.incomplete","response":{"status":"incomplete","output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}`},
		{name: "eof"},
	} {
		for _, mode := range []struct {
			name      string
			buffering bool
		}{
			{name: "unbuffered"},
			{name: "buffered", buffering: true},
		} {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				recorder := &codexAbnormalReasoningRetryUsageRecorder{}
				usage.RegisterNamedPlugin("codex-upstream-model-bootstrap-test", recorder)
				t.Cleanup(func() {
					usage.RegisterNamedPlugin("codex-upstream-model-bootstrap-test", noopUsagePlugin{})
				})

				events := []string{`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-sol-2026-0815"}}`}
				if tc.event != "" {
					events = append(events, tc.event)
				}
				server := codexSSEServer(events...)
				defer server.Close()

				executor := NewCodexExecutor(codexBufferingConfig(mode.buffering))
				auth := codexAbnormalReasoningRetryTestAuth(server.URL)
				auth.ID = "codex-upstream-model-bootstrap-" + tc.name + "-" + mode.name
				req := cliproxyexecutor.Request{
					Model:   "gpt-5.5-sol",
					Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai-response"),
					Stream:       true,
				}

				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if mode.buffering {
					if result != nil {
						for range result.Chunks {
						}
						t.Fatal("bootstrap failure must not return a stream")
					}
				} else {
					if err != nil || result == nil {
						t.Fatalf("unbuffered execution must return a stream, got error: %v", err)
					}
					_, err = drainChunks(result)
				}
				if err == nil {
					t.Fatal("expected upstream failure")
				}

				record := recorder.waitForRecord(t, func(record usage.Record) bool {
					return record.AuthID == auth.ID
				})
				if !record.Failed || record.Model != req.Model {
					t.Fatalf("record outcome/model = %v/%q, want failed/%q", record.Failed, record.Model, req.Model)
				}
				if record.UpstreamModel != "gpt-5.5-sol-2026-0815" {
					t.Fatalf("record.UpstreamModel = %q, want model from response.created before failure", record.UpstreamModel)
				}
			})
		}
	}
}

func TestCodexWebsocketsExecutorRecordsUpstreamResponseModelFromCreatedEvent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream", stream: false},
		{name: "stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-upstream-model-websocket-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-upstream-model-websocket-test", noopUsagePlugin{})
			})

			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Errorf("read websocket request: %v", errRead)
					return
				}
				created := []byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-sol-2026-0815"}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, created); errWrite != nil {
					t.Errorf("write websocket created response: %v", errWrite)
					return
				}
				completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					t.Errorf("write websocket response: %v", errWrite)
				}
			}))
			defer server.Close()

			executor := NewCodexWebsocketsExecutor(&config.Config{})
			auth := codexAbnormalReasoningRetryTestAuth(server.URL)
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.5-sol",
				Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       tc.stream,
			}

			if tc.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk: %v", chunk.Err)
					}
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
				t.Fatalf("Execute: %v", errExec)
			}

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5-sol"
			})
			if record.UpstreamModel != "gpt-5.5-sol-2026-0815" {
				t.Fatalf("record.UpstreamModel = %q, want websocket model from response.created", record.UpstreamModel)
			}
		})
	}
}

func TestCodexOpenAIImageRecordsUpstreamResponseModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream", stream: false},
		{name: "stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-upstream-model-image-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-upstream-model-image-test", noopUsagePlugin{})
			})

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4-2026-0815\"}}\n\n"))
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":111,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"AAA\",\"output_format\":\"png\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := newCodexOpenAIImageTestAuth(server.URL)
			req := cliproxyexecutor.Request{
				Model:   "gpt-image-legacy",
				Payload: []byte(`{"model":"gpt-image-legacy","prompt":"draw a cat"}`),
			}
			opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, tc.stream)

			if tc.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk: %v", chunk.Err)
					}
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
				t.Fatalf("Execute: %v", errExec)
			}

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.Model == codexOpenAIImagesMainModel
			})
			if record.UpstreamModel != "gpt-5.4-2026-0815" {
				t.Fatalf("record.UpstreamModel = %q, want image Responses model", record.UpstreamModel)
			}
		})
	}
}
