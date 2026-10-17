package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const zcodeSearchMessage = `{"type":"message","id":"synthetic-search","model":"glm-fixture","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search_prime","input":{"search_query":"fixture"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Fixture","url":"https://example.com/","encrypted_content":"fixture-content"}]},{"type":"text","text":"delivered answer"}],"usage":{"input_tokens":400000,"cache_read_input_tokens":105000,"output_tokens":500}}`

func zcodeSearchStream(message string) []byte {
	root := gjson.Parse(message)
	var out strings.Builder
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(&out, "data: %s\n\n", b) }
	emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "synthetic-search", "type": "message", "role": "assistant", "content": []any{}, "usage": json.RawMessage(root.Get("usage").Raw)}})
	for i, block := range root.Get("content").Array() {
		emit(map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(block.Raw)})
		emit(map[string]any{"type": "content_block_stop", "index": i})
	}
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 500}})
	emit(map[string]any{"type": "message_stop"})
	return []byte(out.String())
}

func TestZcodeContextUsageAccountingAndClientScope(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name                     string
			mode                     config.CodexZcodeContextUsageMode
			provider, ua, originator string
			search, fail, replay     bool
			wantCalls                int
			wantTotal                int64
		}{
			{"correct", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "codex_cli_rs/0.1", "", true, false, true, 1, 101100},
			{"observe", config.ZcodeContextUsageObserve, "zcode-coding-plan", "codex_cli_rs/0.1", "", true, false, true, 1, 505500},
			{"disabled", config.ZcodeContextUsageDisabled, "zcode-coding-plan", "codex_cli_rs/0.1", "", true, false, true, 0, 505500},
			{"failed-count", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "codex_cli_rs/0.1", "", true, true, true, 1, 505500},
			{"other-client", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "python", "", true, false, true, 0, 505500},
			{"other-provider", config.ZcodeContextUsageCorrect, "other-provider", "codex_cli_rs/0.1", "", true, false, true, 0, 505500},
			{"no-search", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "codex_cli_rs/0.1", "", false, false, true, 0, 505500},
			{"originator", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "", "Codex Desktop/0.1", true, false, true, 1, 101100},
			{"unknown-replay", config.ZcodeContextUsageCorrect, "zcode-coding-plan", "codex_cli_rs/0.1", "", true, false, false, 0, 505500},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				host := New()
				host.runtimeConfig = &config.Config{Codex: config.CodexConfig{ZcodeContextUsage: tc.mode}}
				message := zcodeSearchMessage
				if !tc.search {
					message = strings.Replace(message, gjson.Parse(message).Get("content").Raw, `[{"type":"text","text":"delivered answer"}]`, 1)
				}
				calls := 0
				var original pluginapi.ExecutorRequest
				generationClosed := false
				executor := &fakeExecutor{
					execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						original = req
						generationClosed = true
						return pluginapi.ExecutorResponse{Payload: []byte(message)}, nil
					},
					executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						original = req
						chunks := make(chan pluginapi.ExecutorStreamChunk)
						go func() {
							chunks <- pluginapi.ExecutorStreamChunk{Payload: zcodeSearchStream(message)}
							generationClosed = true
							close(chunks)
						}()
						return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
					},
					countTokens: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						calls++
						if !generationClosed {
							t.Error("count overlapped generation")
						}
						if req.AuthID != original.AuthID || req.Model != original.Model || string(req.StorageJSON) != string(original.StorageJSON) || req.ExecutionSessionID != original.ExecutionSessionID || req.RequestID == original.RequestID || req.Stream {
							t.Error("count identity mismatch")
						}
						if helps.UsageReporterFromContext(ctx) != nil {
							t.Error("count inherited generation usage")
						}
						for _, key := range []string{"system", "tools", "messages.0"} {
							if gjson.GetBytes(req.Payload, key).Raw != gjson.GetBytes(original.Payload, key).Raw {
								t.Errorf("prompt field %s changed", key)
							}
						}
						blocks := gjson.GetBytes(req.Payload, "messages.1.content").Array()
						if len(blocks) != 3 || blocks[0].Get("type").String() != "server_tool_use" || blocks[1].Get("type").String() != "web_search_tool_result" || blocks[2].Get("text").String() != "delivered answer" {
							t.Errorf("replay missing: %s", req.Payload)
						}
						if tc.fail {
							return pluginapi.ExecutorResponse{}, errors.New("synthetic unsupported count")
						}
						return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":101100}`)}, nil
					},
				}
				adapter := newCurrentExecutorAdapterForTest(host, "zcode-context-fixture", executor, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
				adapter.provider = tc.provider
				auth := &coreauth.Auth{ID: "context-fixture-auth", Provider: tc.provider}
				info := &registry.ModelInfo{ID: "glm-fixture"}
				if tc.replay {
					info.NativeCapabilities = &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}
				}
				req := coreexecutor.Request{Model: "glm-fixture", Payload: []byte(`{"instructions":"synthetic system","input":"hello","tools":[{"type":"function","name":"check","parameters":{"type":"object"}}]}`), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": info, coreexecutor.RequestIDMetadataKey: "generation", coreexecutor.ExecutionSessionMetadataKey: "session"}}
				opts := coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream, Headers: http.Header{"User-Agent": {tc.ua}, "Originator": {tc.originator}}}
				records := make(chan coreusage.Record, 4)
				coreusage.RegisterNamedPlugin("test:zcode-context", coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
					if record.Provider == tc.provider {
						records <- record
					}
				}))
				t.Cleanup(func() { coreusage.RegisterNamedPlugin("test:zcode-context", noopFormalPluginUsageSink{}) })
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var response []byte
				if stream {
					result, err := adapter.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					var all strings.Builder
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						all.Write(chunk.Payload)
					}
					for _, line := range strings.Split(all.String(), "\n") {
						data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
						if gjson.Get(data, "type").String() == "response.completed" {
							response = []byte(gjson.Get(data, "response").Raw)
						}
					}
				} else {
					result, err := adapter.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					response = result.Payload
				}
				if got := gjson.GetBytes(response, "usage.total_tokens").Int(); got != tc.wantTotal || calls != tc.wantCalls {
					t.Errorf("client=%d calls=%d want=%d/%d", got, calls, tc.wantTotal, tc.wantCalls)
				}
				if gjson.GetBytes(response, "usage.input_tokens").Int() != 505000 || gjson.GetBytes(response, "usage.output_tokens").Int() != 500 {
					t.Fatal("client accounting fields changed")
				}
				select {
				case record := <-records:
					if record.Failed || record.Detail.TotalTokens != 505500 || record.Detail.CachedTokens != 105000 {
						t.Fatalf("backend accounting changed: %+v", record.Detail)
					}
				case <-ctx.Done():
					t.Fatal("usage missing")
				}
			})
		}
	}
}

func TestZcodeContextInvalidCountAndCancellation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"input_tokens":0}`, `{"input_tokens":-1}`, `{"input_tokens":"10"}`, `{"input_tokens":1.5}`, `{"input_tokens":9223372036854775808}`, `not-json`} {
		a := newCurrentExecutorAdapterForTest(New(), "bad-count", &fakeExecutor{countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(raw)}, nil
		}}, nil, nil)
		if count, status := a.countZcodeContext(context.Background(), pluginapi.ExecutorRequest{}); count != 0 || status != "invalid_count" {
			t.Fatalf("accepted %s: %d %s", raw, count, status)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	a := newCurrentExecutorAdapterForTest(New(), "canceled-count", &fakeExecutor{countTokens: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		<-ctx.Done()
		return pluginapi.ExecutorResponse{}, ctx.Err()
	}}, nil, nil)
	if count, status := a.countZcodeContext(ctx, pluginapi.ExecutorRequest{}); count != 0 || status != "count_canceled_or_timed_out" {
		t.Fatal(count, status)
	}
}

func TestZcodeContextCountBoundsUncooperativePlugin(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	a := newCurrentExecutorAdapterForTest(New(), "slow-count", &fakeExecutor{countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		defer close(finished)
		<-release
		return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":12}`)}, nil
	}}, nil, nil)
	started := time.Now()
	count, status := a.countZcodeContext(ctx, pluginapi.ExecutorRequest{})
	close(release)
	<-finished
	if count != 0 || status != "count_canceled_or_timed_out" || time.Since(started) > time.Second {
		t.Fatalf("slow plugin blocked fallback: %d %s", count, status)
	}
}
