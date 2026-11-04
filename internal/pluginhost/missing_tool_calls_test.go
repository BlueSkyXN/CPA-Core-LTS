package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestExecutorMissingToolCallsPreservesFailureUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, validTool := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/valid_tool=%t", stream, validTool), func(t *testing.T) {
				provider := "missing-tools-fixture"
				sink := newTestUsageCapturePlugin(provider)
				registerTestUsagePlugin(t, "missing-tools-fixture", sink)
				record := normalizeTestCapabilityRecord(capabilityRecord{id: "missing-tools-fixture"})
				host := newHostWithRecords(record)
				message := `{"role":"assistant","content":"checking"}`
				delta := `{"content":"checking"}`
				if validTool {
					message = `{"role":"assistant","tool_calls":[{"id":"call-fixture","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`
					delta = `{"tool_calls":[{"index":0,"id":"call-fixture","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`
				}
				usage := `"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}`
				upstream := []byte(`{"id":"fixture","choices":[{"index":0,"message":` + message + `,"finish_reason":"tool_calls"}],` + usage + `}`)
				exec := &fakeExecutor{
					identifier: provider,
					execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						return pluginapi.ExecutorResponse{Payload: upstream}, nil
					},
					executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						chunks := make(chan pluginapi.ExecutorStreamChunk, 2)
						chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(`{"id":"fixture","choices":[{"index":0,"delta":` + delta + `,"finish_reason":"tool_calls"}],` + usage + `}`)}
						close(chunks)
						return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
					},
				}
				adapter := newExecutorAdapterForRecordForTest(host, record, exec, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
				adapter.provider = provider
				auth := &coreauth.Auth{ID: "fixture-auth", Index: "fixture-index", Provider: provider}
				payload := []byte(`{"model":"fixture","messages":[{"role":"user","content":"lookup"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
				request := coreexecutor.Request{Model: "fixture", Payload: payload}
				options := coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var outcome error
				var body []byte
				if stream {
					response, err := adapter.ExecuteStream(ctx, auth, request, options)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range response.Chunks {
						body = append(body, chunk.Payload...)
						if chunk.Err != nil {
							if outcome != nil {
								t.Fatal("duplicate terminal error")
							}
							outcome = chunk.Err
							cancel()
						}
						if bytes.Contains(chunk.Payload, []byte("event: response.completed\n")) {
							cancel()
						}
					}
				} else {
					response, err := adapter.Execute(ctx, auth, request, options)
					body, outcome = response.Payload, err
				}
				if (outcome == nil) != validTool {
					t.Fatalf("valid_tool=%t error=%v body=%s", validTool, outcome, body)
				}
				if !validTool {
					if scoped, ok := errors.AsType[coreexecutor.RequestScopedError](outcome); !ok || !scoped.IsRequestScoped() {
						t.Fatalf("conversion error must be request-scoped: %v", outcome)
					}
					if stream && (bytes.Count(body, []byte("event: response.failed\n")) != 1 || bytes.Contains(body, []byte("event: response.completed\n"))) {
						t.Fatalf("invalid failure terminal: %s", body)
					}
				}
				rec := sink.waitRecord(t, time.Second)
				if rec.Failed == validTool || rec.AuthIndex != "fixture-index" || rec.Detail.TotalTokens != 30 || rec.UsageProvenance != "provider_reported_unverified" {
					t.Fatalf("outcome or upstream usage attribution changed: %+v", rec)
				}
				sink.assertNoRecord(t, 50*time.Millisecond)
			})
		}
	}
}

func TestExecutorTranslatedFailureClosesWithoutSourceClose(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversion=%t", conversion), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			in := make(chan pluginapi.ExecutorStreamChunk, 1)
			defer close(in)
			if conversion {
				in <- pluginapi.ExecutorStreamChunk{Payload: []byte(`{"id":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)}
			} else {
				in <- pluginapi.ExecutorStreamChunk{Err: errors.New("fixture upstream error")}
			}
			prepared := preparedExecutorCall{
				outputFormat: sdktranslator.FormatOpenAI, requestedFormat: sdktranslator.FormatOpenAIResponse,
				req: coreexecutor.Request{Model: "fixture"},
			}
			out := (&executorAdapter{}).translateExecutorStreamChunks(ctx, prepared, in)
			errorsSeen := 0
			for chunk := range out {
				if chunk.Err != nil {
					errorsSeen++
				}
			}
			if ctx.Err() != nil || errorsSeen != 1 {
				t.Fatalf("terminal failure waited for source closure: context=%v errors=%d", ctx.Err(), errorsSeen)
			}
		})
	}
}
