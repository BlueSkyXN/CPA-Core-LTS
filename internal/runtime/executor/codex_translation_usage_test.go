package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexNonStreamTranslationUsageOutcome(t *testing.T) {
	for _, transport := range []string{"http", "compact", "websocket"} {
		compact := transport == "compact"
		for _, outcome := range []string{"success", "empty", "tool_error"} {
			for _, withUsage := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/usage=%t", transport, outcome, withUsage), func(t *testing.T) {
					alias := uuid.NewString()
					format := sdktranslator.Format("test-usage-" + alias)
					upstream := sdktranslator.FormatCodex
					if compact {
						upstream = sdktranslator.FormatOpenAIResponse
					}
					sdktranslator.Register(format, upstream, nil, sdktranslator.ResponseTransform{
						NonStream: func(_ context.Context, _ string, _, _, _ []byte, param *any) []byte {
							if outcome == "empty" {
								return nil
							}
							if outcome == "tool_error" {
								state := &translatorcommon.ApplyPatchErrorState{}
								state.SetToolInputError(errors.New("synthetic conversion error"))
								*param = state
							}
							return []byte(`{"id":"synthetic","output":[]}`)
						},
					})
					t.Cleanup(func() { sdktranslator.Unregister(format, upstream) })
					fields := ""
					if withUsage {
						fields = `,"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}`
					}
					if transport == "http" {
						fields += `,"tool_usage":{"image_gen":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						payload := `{"id":"synthetic","model":"gpt-5.5","status":"completed","output":[]` + fields + `}`
						if transport == "websocket" {
							upgrader := websocket.Upgrader{}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							defer conn.Close()
							if _, _, err = conn.ReadMessage(); err != nil {
								t.Error(err)
								return
							}
							if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":`+payload+`}`)); err != nil {
								t.Error(err)
							}
						} else if compact {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, payload)
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
						}
					}))
					t.Cleanup(server.Close)
					capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
					coreusage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
					ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
					ctx = coreusage.WithTraceID(ctx, alias)
					auth := codexAPIKeyTestAuth(server.URL)
					auth.ID, auth.Index = "test-auth", "test-index"
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: format}
					if compact {
						opts.Alt = "responses/compact"
					}
					execute := NewCodexExecutor(&config.Config{}).Execute
					if transport == "websocket" {
						execute = NewCodexWebsocketsExecutor(&config.Config{}).Execute
					}
					resp, err := execute(ctx, auth, cliproxyexecutor.Request{
						Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"synthetic"}`),
					}, opts)
					if outcome == "success" {
						if err != nil || len(resp.Payload) == 0 {
							t.Fatalf("successful translation: error=%v, bytes=%d", err, len(resp.Payload))
						}
					} else if coded, ok := err.(interface{ StatusCode() int }); !ok || coded.StatusCode() != http.StatusBadGateway || len(resp.Payload) != 0 {
						t.Fatalf("failed translation must return empty 502: error=%v, bytes=%d", err, len(resp.Payload))
					} else if scoped, okScoped := errors.AsType[cliproxyexecutor.RequestScopedError](err); !okScoped || !scoped.IsRequestScoped() {
						t.Fatalf("failed translation must be request-scoped: error=%v", err)
					}
					// A FIFO marker observes every publication, including deferred failure reporting.
					coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
					record := capture.await(t)
					if record.Model != "gpt-5.5" || record.Failed != (outcome != "success") {
						t.Fatalf("main outcome: model=%q failed=%t", record.Model, record.Failed)
					}
					if record.RequestID == "" || record.TraceID != alias || record.AuthID != auth.ID || record.AuthIndex != auth.Index {
						t.Fatal("main usage lost request or credential attribution")
					}
					want := coreusage.Detail{}
					if withUsage {
						want = coreusage.Detail{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}
					}
					if got := record.Detail; got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.TotalTokens != want.TotalTokens {
						t.Fatalf("main detail=%+v, want %+v", record.Detail, want)
					}
					if transport == "http" {
						image := capture.await(t)
						if image.Model != codexDefaultImageToolModel || image.Failed || image.Detail.TotalTokens != 30 || image.RequestID == record.RequestID || image.TraceID != alias {
							t.Fatal("image tool usage lost its independent successful consumption record")
						}
					}
					if next := capture.await(t); next.Model != "dispatch-barrier" {
						t.Fatalf("duplicate usage before dispatch barrier: model=%q", next.Model)
					}
				})
			}
		}
	}
}
