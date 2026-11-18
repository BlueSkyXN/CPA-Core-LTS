package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexDuplexIdleErrorPreservesDiagnostics(t *testing.T) {
	for _, status := range []int{400, 500} {
		for _, kind := range []string{"error", "response.failed"} {
			for _, responseID := range []string{"", "completed-first", "unrelated"} {
				t.Run(fmt.Sprintf("%d/%s/id=%s", status, kind, responseID), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					firstDelivered := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							return
						}
						defer conn.Close()
						_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
						if _, _, err = conn.ReadMessage(); err != nil {
							return
						}
						for _, payload := range []string{
							`{"type":"response.created","response":{"id":"completed-first","output":[]}}`,
							`{"type":"response.completed","response":{"id":"completed-first","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":0,"input_tokens_details":{"cached_tokens":64}}}}`,
						} {
							if conn.WriteMessage(websocket.TextMessage, []byte(payload)) != nil {
								return
							}
						}
						// 下游已收到成功终态，才发送不属于任何活动请求的错误。
						select {
						case <-firstDelivered:
						case <-ctx.Done():
							return
						}
						errorType := "invalid_request_error"
						if status == 500 {
							errorType = "server_error"
						}
						errorBody := map[string]any{"type": errorType, "status": status, "message": "synthetic idle connection error"}
						payload := map[string]any{"type": kind, "status": status, "error": errorBody}
						if kind == "response.failed" {
							response := map[string]any{"status": "failed", "error": errorBody}
							if responseID != "" {
								response["id"] = responseID
							}
							payload = map[string]any{"type": kind, "response": response}
						} else if responseID != "" {
							payload["response_id"] = responseID
						}
						encoded, _ := json.Marshal(payload)
						_ = conn.WriteMessage(websocket.TextMessage, encoded)
						_, _, _ = conn.ReadMessage()
					}))
					defer server.Close()
					requestCtx := exec.WithWebsocketInput(exec.WithDownstreamWebsocket(ctx), make(chan exec.WebsocketInput, 1))
					store := diagnostics.NewStore()
					requestCtx = store.Begin(requestCtx, "request", "caller", "session", "gpt-6-astra", "codex", []byte(`{}`))
					cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}}
					cfg.CodexResponseSteering = true
					engine := NewCodexWebsocketsExecutor(cfg)
					engine.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
					credential := &auth.Auth{ID: "idle-error-account", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL, "websockets": "true"}}
					result, err := engine.ExecuteStream(requestCtx, credential,
						exec.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)},
						exec.Options{SourceFormat: translator.FormatCodex, Metadata: map[string]any{exec.ExecutionSessionMetadataKey: t.Name()}})
					if err != nil {
						t.Fatal(err)
					}
					sawFirst, sawError := false, false
					var completed diagnostics.Attempt
					for chunk := range result.Chunks {
						event := ojson.GetBytes(chunk.Payload, "type").String()
						if event == "response.completed" && !sawFirst {
							sawFirst = true
							trace, _ := store.Get(diagnostics.FromContext(requestCtx).ID())
							if len(trace.Attempts) != 1 || trace.Attempts[0].Outcome != "completed" {
								t.Fatal("initial response did not complete its diagnostic")
							}
							completed = trace.Attempts[0]
							close(firstDelivered)
						}
						if event == kind {
							sawError = true
							cancel()
						}
						if chunk.Err != nil {
							cancel()
						}
					}
					if !sawFirst || !sawError {
						t.Fatalf("missing client event: completed=%t error=%t", sawFirst, sawError)
					}
					trace, _ := store.Get(diagnostics.FromContext(requestCtx).ID())
					if len(trace.Attempts) != 1 || !reflect.DeepEqual(trace.Attempts[0], completed) {
						t.Fatalf("idle error changed completed evidence: %+v", trace.Attempts)
					}
					connectionErrors := 0
					for _, event := range trace.Events {
						if event.Method == "websocket_connection" && event.Result == "upstream_error" && event.Reason == "unassigned_error" && event.ExecutionID == "" {
							connectionErrors++
						}
					}
					if connectionErrors != 1 {
						t.Fatalf("connection error records=%d, want 1", connectionErrors)
					}
				})
			}
		}
	}
}
