package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexResponseCyberProgramTransports(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ name, created, terminal, want string }{
				{"default blue", "", `,"access_programs":{"cyber":"daybreak_blue"}`, "daybreak_blue"},
				{"created only", `,"access_programs":{"cyber":"daybreak_blue"}`, "", "daybreak_blue"},
				{"terminal standard", `,"access_programs":{"cyber":"daybreak_blue"}`, `,"access_programs":null`, "standard"},
				{"terminal unknown", `,"access_programs":{"cyber":"daybreak_blue"}`, `,"access_programs":{"cyber":"future"}`, "unknown"},
				{"missing", "", "", ""},
			} {
				t.Run(fmt.Sprintf("ws=%t/stream=%t/%s", ws, stream, tc.name), func(t *testing.T) {
					capture := &codexResponseModelUsageCapture{alias: t.Name(), records: make(chan usage.Record, 4)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), noopUsagePlugin{}) })
					events := []string{
						`{"type":"response.created","response":{"id":"resp_blue","model":"gpt-5.6-sol"` + tc.created + `}}`,
						`{"type":"response.completed","response":{"id":"resp_blue","status":"completed","model":"gpt-5.6-sol","service_tier":"priority","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]` + tc.terminal + `}}`,
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body []byte
						if ws {
							upgrader := websocket.Upgrader{}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							defer conn.Close()
							_, body, err = conn.ReadMessage()
							if err != nil {
								t.Error(err)
								return
							}
							for _, event := range events {
								if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
									t.Error(err)
									return
								}
							}
						} else {
							body, _ = io.ReadAll(r.Body)
							w.Header().Set("Content-Type", "text/event-stream")
							for _, event := range events {
								fmt.Fprintf(w, "data: %s\n\n", event)
							}
						}
						if gjson.GetBytes(body, "access_programs").Exists() {
							t.Error("omitted request program was injected")
						}
					}))
					defer server.Close()
					ctx := usage.WithRequestedModelAlias(context.Background(), t.Name())
					auth := codexAbnormalReasoningRetryTestAuth(server.URL)
					req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":"OK"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
					var err error
					if stream {
						var result *cliproxyexecutor.StreamResult
						if ws {
							result, err = NewCodexWebsocketsExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
						} else {
							result, err = NewCodexExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
						}
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					} else if ws {
						_, err = NewCodexWebsocketsExecutor(&config.Config{}).Execute(ctx, auth, req, opts)
					} else {
						_, err = NewCodexExecutor(&config.Config{}).Execute(ctx, auth, req, opts)
					}
					if err != nil {
						t.Fatal(err)
					}
					record := capture.await(t)
					if record.ResponseCyberProgram != tc.want || record.EffectiveServiceTier != "priority" || record.Detail.TotalTokens != 0 {
						t.Fatalf("record program/tier/tokens = %q/%q/%d", record.ResponseCyberProgram, record.EffectiveServiceTier, record.Detail.TotalTokens)
					}
				})
			}
		}
	}
}
