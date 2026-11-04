package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A plugin response that the host cannot convert must be reported as a
// request-scoped failure with its upstream usage, never as a success.
func TestExecutorAdapterExecuteTranslationFailureIsRequestScoped(t *testing.T) {
	for _, outcome := range []string{"success", "empty", "tool_error"} {
		t.Run(outcome, func(t *testing.T) {
			provider := "plugin-provider"
			plugin := newTestUsageCapturePlugin(provider)
			registerTestUsagePlugin(t, "test-adapter-translation-"+outcome, plugin)

			format := sdktranslator.Format("test-plugin-" + strings.ReplaceAll(uuid.NewString(), "-", ""))
			sdktranslator.Register(format, sdktranslator.FormatOpenAI, nil, sdktranslator.ResponseTransform{
				NonStream: func(_ context.Context, _ string, _, _, raw []byte, param *any) []byte {
					switch outcome {
					case "empty":
						return nil
					case "tool_error":
						state := &translatorcommon.ApplyPatchErrorState{}
						state.SetToolInputError(errors.New("synthetic conversion error"))
						*param = state
					}
					return append([]byte(nil), raw...)
				},
			})
			t.Cleanup(func() { sdktranslator.Unregister(format, sdktranslator.FormatOpenAI) })

			record := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin"})
			host := newHostWithRecords(record)
			exec := &fakeExecutor{
				identifier: provider,
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{
						Payload: []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
						Headers: http.Header{"Content-Type": []string{"application/json"}},
					}, nil
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, record, exec,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
			)
			auth := &coreauth.Auth{ID: "auth-" + outcome, Provider: provider, FileName: "auth.json", Attributes: map[string]string{"type": "oauth"}}
			resp, err := adapter.Execute(context.Background(), auth, coreexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
			}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: format})

			rec := plugin.waitRecord(t, time.Second)
			if rec.Detail.TotalTokens != 30 {
				t.Fatalf("usage detail=%+v, want upstream tokens preserved", rec.Detail)
			}
			if outcome == "success" {
				if err != nil || len(resp.Payload) == 0 || rec.Failed {
					t.Fatalf("success: err=%v bytes=%d failed=%t", err, len(resp.Payload), rec.Failed)
				}
				return
			}
			if err == nil || len(resp.Payload) != 0 {
				t.Fatalf("expected conversion failure, got err=%v bytes=%d", err, len(resp.Payload))
			}
			if scoped, ok := errors.AsType[coreexecutor.RequestScopedError](err); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("conversion failure must be request-scoped: %v", err)
			}
			if !rec.Failed {
				t.Fatal("conversion failure was recorded as a successful request")
			}
			plugin.assertNoRecord(t, 50*time.Millisecond)
		})
	}
}

func TestExecutorChatStreamToClaudeAcceptsRawAndFramedChunks(t *testing.T) {
	for _, prefix := range []string{"", "data: "} {
		t.Run(prefix, func(t *testing.T) {
			prepared := preparedExecutorCall{
				outputFormat:    sdktranslator.FormatOpenAI,
				requestedFormat: sdktranslator.FormatClaude,
				req:             coreexecutor.Request{Model: "fixture-model"},
				opts:            coreexecutor.Options{OriginalRequest: []byte(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)},
			}
			adapter := &executorAdapter{}
			var param any
			var output []byte
			for _, raw := range []string{
				`{"id":"fixture","model":"fixture-model","choices":[{"index":0,"delta":{"content":"native-ok"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
			} {
				for _, chunk := range adapter.translateExecutorStreamPayload(context.Background(), prepared, []byte(prefix+raw), &param) {
					output = append(output, chunk...)
				}
			}
			for _, chunk := range adapter.translateExecutorStreamPayload(context.Background(), prepared, []byte("data: [DONE]"), &param) {
				output = append(output, chunk...)
			}
			for _, expected := range []string{"native-ok", "message_start", "message_stop", `"output_tokens":2`} {
				if !bytes.Contains(output, []byte(expected)) {
					t.Fatalf("lost %s: %s", expected, output)
				}
			}
		})
	}
}

func TestExecutorChatStreamToClaudeCompletionFollowsOutcome(t *testing.T) {
	for _, prefix := range []string{"", "data: "} {
		for _, content := range []struct {
			name, delta, visible, finish string
		}{
			{"text", `{"content":"live-text"}`, "live-text", "stop"},
			{"tool", `{"tool_calls":[{"index":0,"id":"call-fixture","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"fixture\"}"}}]}`, `"type":"tool_use"`, "tool_calls"},
		} {
			for _, outcome := range []string{"success", "success_no_usage", "error", "error_no_usage", "truncated", "done_then_error"} {
				t.Run(prefix+content.name+"/"+outcome, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					prepared := preparedExecutorCall{
						outputFormat:    sdktranslator.FormatOpenAI,
						requestedFormat: sdktranslator.FormatClaude,
						req:             coreexecutor.Request{Model: "fixture-model"},
						opts:            coreexecutor.Options{OriginalRequest: []byte(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)},
					}
					in := make(chan pluginapi.ExecutorStreamChunk, 8)
					in <- pluginapi.ExecutorStreamChunk{Payload: []byte(prefix + `{"id":"fixture","model":"fixture-model","choices":[{"index":0,"delta":` + content.delta + `}]}`)}
					out := (&executorAdapter{}).translateExecutorStreamChunks(ctx, prepared, in)
					var body []byte
					// 插件保持打开时，正文和工具起始事件必须已经到达客户端。
					for !bytes.Contains(body, []byte(content.visible)) {
						select {
						case chunk, ok := <-out:
							if !ok || chunk.Err != nil {
								t.Fatalf("stream stopped before content: %v", chunk.Err)
							}
							body = append(body, chunk.Payload...)
						case <-ctx.Done():
							t.Fatal("content waited for plugin completion")
						}
					}
					success := strings.HasPrefix(outcome, "success")
					if success || outcome == "truncated" {
						in <- pluginapi.ExecutorStreamChunk{Payload: []byte(prefix + `{"choices":[{"index":0,"delta":{},"finish_reason":"` + content.finish + `"}]}`)}
					}
					if !strings.HasSuffix(outcome, "no_usage") {
						in <- pluginapi.ExecutorStreamChunk{Payload: []byte(prefix + `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)}
					}
					if outcome == "done_then_error" {
						in <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]")}
					}
					upstreamErr := errors.New("synthetic upstream failure")
					if !success {
						in <- pluginapi.ExecutorStreamChunk{Err: upstreamErr}
						in <- pluginapi.ExecutorStreamChunk{Payload: []byte(prefix + `{"choices":[{"index":0,"delta":{"content":"after-error"}}]}`)}
					}
					close(in)
					errorsSeen := 0
					for chunk := range out {
						body = append(body, chunk.Payload...)
						if chunk.Err != nil {
							if !errors.Is(chunk.Err, upstreamErr) {
								t.Fatalf("error identity changed: %v", chunk.Err)
							}
							errorsSeen++
						}
					}
					if ctx.Err() != nil {
						t.Fatal("stream cleanup stalled")
					}
					if success {
						if errorsSeen != 0 || bytes.Count(body, []byte("event: message_delta\n")) != 1 || bytes.Count(body, []byte("event: message_stop\n")) != 1 {
							t.Fatalf("successful completion was lost or duplicated: %s", body)
						}
						if outcome == "success" && !bytes.Contains(body, []byte(`"output_tokens":2`)) {
							t.Fatalf("successful completion lost usage: %s", body)
						}
						if content.name == "tool" && !bytes.Contains(body, []byte("input_json_delta")) {
							t.Fatalf("successful completion lost tool arguments: %s", body)
						}
					} else if errorsSeen != 1 || bytes.Contains(body, []byte("event: message_delta\n")) || bytes.Contains(body, []byte("event: message_stop\n")) || bytes.Contains(body, []byte("after-error")) {
						t.Fatalf("failed stream emitted successful completion or post-error data: errors=%d body=%s", errorsSeen, body)
					}
				})
			}
		}
	}
}
