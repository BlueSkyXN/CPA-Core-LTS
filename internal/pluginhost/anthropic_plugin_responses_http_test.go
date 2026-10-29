package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAnthropicPluginOpenAIHTTP(t *testing.T) {
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		t.Run(source.String(), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			for _, stream := range []bool{false, true} {
				for _, invalid := range []bool{false, true} {
					t.Run(fmt.Sprintf("stream=%v/invalid=%v", stream, invalid), func(t *testing.T) {
						var calls atomic.Int32
						adapter := newCurrentExecutorAdapterForTest(New(), "http-fixture", &fakeExecutor{
							execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
								calls.Add(1)
								stop := "end_turn"
								if invalid {
									stop = "unknown_stop"
								}
								return pluginapi.ExecutorResponse{Payload: []byte(`{"type":"message","id":"fixture","role":"assistant","content":[{"type":"text","text":"你好"}],"stop_reason":"` + stop + `","usage":{"input_tokens":3,"output_tokens":7}}`)}, nil
							},
							executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
								calls.Add(1)
								payload := claudePluginFixture
								if invalid {
									payload = "data: broken-json\n\n"
								}
								in := make(chan pluginapi.ExecutorStreamChunk, 1)
								in <- pluginapi.ExecutorStreamChunk{Payload: []byte(payload)}
								close(in)
								return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
							},
						}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
						manager := coreauth.NewManager(nil, nil, nil)
						manager.SetRetryConfig(2, time.Millisecond, 0)
						manager.RegisterExecutor(adapter)
						model := "http-fixture"
						for _, id := range []string{"http-a", "http-b"} {
							auth := &coreauth.Auth{ID: id, Provider: "plugin-provider", Status: coreauth.StatusActive}
							if _, err := manager.Register(context.Background(), auth); err != nil {
								t.Fatal(err)
							}
							registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
							t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						}
						records := make(chan coreusage.Record, 4)
						coreusage.RegisterNamedPlugin("test:anthropic-http", coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
							if record.Provider == "plugin-provider" {
								records <- record
							}
						}))
						t.Cleanup(func() { coreusage.RegisterNamedPlugin("test:anthropic-http", noopFormalPluginUsageSink{}) })
						base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}}, manager)
						router := gin.New()
						route := "/v1/responses"
						body := fmt.Sprintf(`{"model":%q,"input":"hello","stream":%v}`, model, stream)
						if source == tr.FormatOpenAI {
							route = "/v1/chat/completions"
							body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":%v}`, model, stream)
							router.POST(route, openai.NewOpenAIAPIHandler(base).ChatCompletions)
						} else {
							router.POST(route, openai.NewOpenAIResponsesAPIHandler(base).Responses)
						}
						recorder := httptest.NewRecorder()
						request := httptest.NewRequest("POST", route, strings.NewReader(body))
						ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
						defer cancel()
						router.ServeHTTP(recorder, request.WithContext(ctx))
						if invalid {
							if recorder.Code != 502 {
								t.Errorf("status=%d body=%s", recorder.Code, recorder.Body.String())
							}
						} else if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "你好") {
							t.Errorf("response=%d %s", recorder.Code, recorder.Body.String())
						}
						if calls.Load() != 1 {
							t.Errorf("model executions=%d", calls.Load())
						}
						for _, id := range []string{"http-a", "http-b"} {
							auth, _ := manager.GetByID(id)
							if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
								t.Error("conversion changed credential availability")
							}
						}
						select {
						case record := <-records:
							if record.Failed != invalid {
								t.Errorf("record failed=%v", record.Failed)
							}
							if (!invalid || !stream) && (record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 7) {
								t.Error("usage lost")
							}
						case <-ctx.Done():
							t.Fatal("usage missing")
						}
						select {
						case <-records:
							t.Error("duplicate usage")
						default:
						}
					})
				}
			}
		})
	}
}

func TestAnthropicPluginResponsesLocalFailureStopsUpstream(t *testing.T) {
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		for _, oversized := range []bool{false, true} {
			t.Run(fmt.Sprint(oversized), func(t *testing.T) {
				stopped := make(chan struct{})
				cancelled := make(chan struct{}, 1)
				adapter := newCurrentExecutorAdapterForTest(New(), "active-fixture", &fakeExecutor{executeStream: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					in := make(chan pluginapi.ExecutorStreamChunk)
					go func() {
						defer close(stopped)
						defer close(in)
						body := "data: broken-json\n\n"
						if oversized {
							body = "data: " + strings.Repeat("x", maxClaudePluginEventBytes)
						}
						select {
						case in <- pluginapi.ExecutorStreamChunk{Payload: []byte(body)}:
						case <-ctx.Done():
							return
						}
						<-ctx.Done()
					}()
					return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
				}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
				adapter.canceller = executionCancellerFunc(func(context.Context, pluginapi.CancelExecutionRequest) error { cancelled <- struct{}{}; return nil })
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result, err := adapter.ExecuteStream(ctx, nil, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}, coreexecutor.Options{SourceFormat: source, Stream: true, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "active"}})
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						var scoped coreexecutor.RequestScopedError
						if !errors.As(chunk.Err, &scoped) || !scoped.IsRequestScoped() {
							t.Errorf("wrong error=%v", chunk.Err)
						}
					}
				}
				if ctx.Err() != nil {
					t.Fatal("cleanup waited for caller cancellation")
				}
				select {
				case <-stopped:
				case <-ctx.Done():
					t.Fatal("upstream not stopped")
				}
				select {
				case <-cancelled:
				case <-ctx.Done():
					t.Fatal("cancel callback missing")
				}
			})
		}
	}
}

func TestAnthropicPluginResponsesCancelWithoutConsumer(t *testing.T) {
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		stopped := make(chan struct{})
		adapter := newCurrentExecutorAdapterForTest(New(), "slow-fixture", &fakeExecutor{executeStream: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			in := make(chan pluginapi.ExecutorStreamChunk)
			go func() {
				defer close(stopped)
				defer close(in)
				select {
				case in <- pluginapi.ExecutorStreamChunk{Payload: []byte(claudePluginFixture)}:
				case <-ctx.Done():
					return
				}
				<-ctx.Done()
			}()
			return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
		}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
		ctx, cancel := context.WithCancel(context.Background())
		result, err := adapter.ExecuteStream(ctx, nil, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}, coreexecutor.Options{Stream: true, SourceFormat: source})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("producer remained active")
		}
		for range result.Chunks {
		}
	}
}
