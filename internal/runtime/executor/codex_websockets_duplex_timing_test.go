package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexDuplexRejectedFollowupPreservesDispatchTiming(t *testing.T) {
	for _, rejection := range []struct {
		name    string
		payload string
	}{
		{"error", `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_request","message":"synthetic rejection"}}`},
		{"response_failed", `{"type":"response.failed","response":{"id":"rejected","status":"failed","error":{"type":"invalid_request_error","code":"invalid_request","message":"synthetic rejection"}}}`},
	} {
		t.Run(rejection.name, func(t *testing.T) {
			const delay = 40 * time.Millisecond
			const rejections = 3
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				for attempt := 0; attempt < rejections+2; attempt++ {
					if _, _, err = conn.ReadMessage(); err != nil {
						return
					}
					time.Sleep(delay)
					if attempt > 0 && attempt <= rejections {
						if conn.WriteMessage(websocket.TextMessage, []byte(rejection.payload)) != nil {
							return
						}
						continue
					}
					for _, payload := range []string{
						`{"type":"response.created","response":{"id":"accepted","output":[]}}`,
						`{"type":"response.completed","response":{"id":"accepted","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
					} {
						if conn.WriteMessage(websocket.TextMessage, []byte(payload)) != nil {
							return
						}
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			e := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
			e.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			sink := newTimingUsageSink()
			id := t.Name()
			result, err := e.ExecuteStream(ctx, &auth.Auth{
				ID: id, Provider: "codex", Attributes: map[string]string{
					"api_key": "synthetic-only", "base_url": server.URL, "websockets": "true",
				},
			}, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)},
				core.Options{SourceFormat: tr.FormatCodex, Stream: true, Metadata: map[string]any{core.ExecutionSessionMetadataKey: id}})
			if err != nil {
				t.Fatal(err)
			}
			completed := 0
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				if bytes.Contains(chunk.Payload, []byte(`"type":"response.completed"`)) ||
					bytes.Contains(chunk.Payload, []byte(`"type":"error"`)) ||
					bytes.Contains(chunk.Payload, []byte(`"type":"response.failed"`)) {
					completed++
					if completed == rejections+2 {
						cancel()
						break
					}
					input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":[]}`)}
				}
			}
			if completed != rejections+2 {
				t.Fatalf("terminal events = %d, want %d", completed, rejections+2)
			}
			for attempt := 0; attempt < rejections+2; attempt++ {
				record := waitTimingUsageRecord(t, sink, id)
				wantFailed := attempt > 0 && attempt <= rejections
				if record.Failed != wantFailed {
					t.Fatalf("attempt %d failed = %v, want %v", attempt, record.Failed, wantFailed)
				}
				if record.Latency < delay || record.TTFB < delay {
					t.Errorf("attempt %d lost dispatch timing: latency=%v ttfb=%v, want >= %v", attempt, record.Latency, record.TTFB, delay)
				}
				if wantFailed && record.Size.UpstreamResponseBytes != int64(len(rejection.payload)) {
					t.Errorf("attempt %d response bytes = %d, want %d", attempt, record.Size.UpstreamResponseBytes, len(rejection.payload))
				}
			}
		})
	}
}
