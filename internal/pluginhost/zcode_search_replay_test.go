package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Codex 的 WebSearchCall serde 模型只保留这些字段；results 不进入后续请求。
func codexSearchItemRoundTrip(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	if ojson.GetBytes(raw, "type").String() != "web_search_call" {
		return raw
	}
	var item struct {
		Type             string          `json:"type"`
		ID               *string         `json:"id,omitempty"`
		Status           *string         `json:"status,omitempty"`
		Action           json.RawMessage `json:"action,omitempty"`
		EncryptedContent *string         `json:"encrypted_content,omitempty"`
		Metadata         json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestZcodeContextCountMatchesCodexReplay(t *testing.T) {
	reader := strings.Replace(zcodeSearchMessage, ojson.Parse(zcodeSearchMessage).Get("content").Raw,
		`[{"type":"server_tool_use","id":"srvtoolu_reader","name":"webReader","input":{"url":"https://example.com/"}},{"type":"tool_result","tool_use_id":"srvtoolu_reader","content":"[{\"url\":\"https://example.com/\",\"content\":\"fixture-content\"}]"},{"type":"text","text":"delivered answer"}]`, 1)
	for _, stream := range []bool{false, true} {
		for _, message := range []string{zcodeSearchMessage, reader} {
			for _, tc := range []struct {
				name                             string
				enabled, session, differentScope bool
			}{
				{"cached", true, true, false}, {"disabled", false, true, false},
				{"no-session", true, false, false}, {"other-session", true, true, true},
			} {
				t.Run(fmt.Sprintf("%s/reader=%t/stream=%t", tc.name, message == reader, stream), func(t *testing.T) {
					cache.ClearClaudeWebSearchResultCache()
					t.Cleanup(cache.ClearClaudeWebSearchResultCache)
					host := New()
					host.runtimeConfig = &config.Config{Codex: config.CodexConfig{ZcodeContextUsage: config.ZcodeContextUsageCorrect}}
					host.runtimeConfig.Client.Codex.WebSearchResultCache.Enabled = tc.enabled
					var countPayload, nextPayload []byte
					generation := 0
					respond := func(req pluginapi.ExecutorRequest) []byte {
						generation++
						if generation == 1 {
							return []byte(message)
						}
						nextPayload = append([]byte(nil), req.Payload...)
						return []byte(strings.Replace(message, ojson.Parse(message).Get("content").Raw, `[{"type":"text","text":"next answer"}]`, 1))
					}
					executor := &fakeExecutor{
						execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							return pluginapi.ExecutorResponse{Payload: respond(req)}, nil
						},
						executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
							chunks <- pluginapi.ExecutorStreamChunk{Payload: zcodeSearchStream(string(respond(req)))}
							close(chunks)
							return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
						},
						countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							countPayload = append([]byte(nil), req.Payload...)
							return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":101100}`)}, nil
						},
					}
					a := newCurrentExecutorAdapterForTest(host, "zcode-replay", executor, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
					a.provider = "zcode-coding-plan"
					info := &registry.ModelInfo{ID: "glm-fixture", IsCompat: true, NativeCapabilities: &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
					auth := &coreauth.Auth{ID: "synthetic-auth", Provider: a.provider}
					req := coreexecutor.Request{Model: info.ID, Payload: []byte(`{"input":[{"role":"user","content":"hello"}]}`), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": info}}
					opts := coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.1"}}}
					if tc.session {
						opts.Headers.Set("Session_id", "synthetic-session")
					}
					run := func() []json.RawMessage {
						var items []json.RawMessage
						if !stream {
							result, err := a.Execute(context.Background(), auth, req, opts)
							if err != nil {
								t.Fatal(err)
							}
							for _, item := range ojson.GetBytes(result.Payload, "output").Array() {
								items = append(items, codexSearchItemRoundTrip(t, []byte(item.Raw)))
							}
						} else {
							result, err := a.ExecuteStream(context.Background(), auth, req, opts)
							if err != nil {
								t.Fatal(err)
							}
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
								for _, line := range strings.Split(string(chunk.Payload), "\n") {
									data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
									if ojson.Get(data, "type").String() == "response.output_item.done" {
										items = append(items, codexSearchItemRoundTrip(t, []byte(ojson.Get(data, "item").Raw)))
									}
								}
							}
						}
						return items
					}
					items := []json.RawMessage{json.RawMessage(`{"role":"user","content":"hello"}`)}
					items = append(items, run()...)
					items = append(items, json.RawMessage(`{"role":"user","content":"next question"}`))
					req.Payload, _ = json.Marshal(map[string]any{"input": items})
					if tc.differentScope {
						opts.Headers.Set("Session_id", "another-session")
					}
					run()
					wantCountResult := tc.enabled && tc.session
					if got := strings.Contains(string(countPayload), "fixture-content"); got != wantCountResult {
						t.Fatalf("count contains result=%t, want %t", got, wantCountResult)
					}
					if got := strings.Contains(string(nextPayload), "fixture-content"); got != (wantCountResult && !tc.differentScope) {
						t.Fatalf("next request contains result=%t", got)
					}
					if !tc.differentScope {
						counted := ojson.GetBytes(countPayload, "messages").Array()
						next := ojson.GetBytes(nextPayload, "messages").Array()
						if len(next) != len(counted)+1 {
							t.Fatal("unexpected next-turn boundary")
						}
						for i, item := range counted {
							if item.Raw != next[i].Raw {
								t.Fatalf("counted message %d differs from client replay", i)
							}
						}
					}
				})
			}
		}
	}
}

func TestZcodeSearchReplayIsolationAndExplicitResults(t *testing.T) {
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)
	host := New()
	cfg := &config.Config{}
	cfg.Client.Codex.WebSearchResultCache.Enabled = true
	a := &executorAdapter{host: host, provider: "zcode-coding-plan", pluginID: "synthetic-plugin"}
	auth := &coreauth.Auth{ID: "synthetic-auth"}
	req := coreexecutor.Request{Model: "glm-fixture", Metadata: map[string]any{coreexecutor.CallerScopeMetadataKey: "caller-a"}}
	opts := coreexecutor.Options{Headers: http.Header{"Session_id": {"session-a"}}}
	info := &registry.ModelInfo{NativeCapabilities: &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
	ctx := context.Background()
	scope := a.zcodeSearchReplayScope(ctx, cfg, auth, req, opts, info)
	item := ojson.Parse(`{"type":"web_search_call","id":"ws_test","results":[{"content":"private-result"}]}`)
	scope.recordItem(ctx, item)
	input := []byte(`{"input":[{"type":"web_search_call","id":"ws_test","action":{"type":"search","query":"q"}}]}`)
	if !strings.Contains(string(scope.restoreItems(ctx, input, "input")), "private-result") {
		t.Fatal("same scope did not restore result")
	}
	cases := map[string]zcodeSearchReplay{}
	otherAuth := *auth
	otherAuth.ID = "other-auth"
	cases["auth"] = a.zcodeSearchReplayScope(ctx, cfg, &otherAuth, req, opts, info)
	otherReq := req
	otherReq.Model = "other-model"
	cases["model"] = a.zcodeSearchReplayScope(ctx, cfg, auth, otherReq, opts, info)
	otherReq = req
	otherReq.Metadata = map[string]any{coreexecutor.CallerScopeMetadataKey: "caller-b"}
	cases["caller"] = a.zcodeSearchReplayScope(ctx, cfg, auth, otherReq, opts, info)
	otherOpts := opts
	otherOpts.Headers = http.Header{"Session_id": {"session-b"}}
	cases["session"] = a.zcodeSearchReplayScope(ctx, cfg, auth, req, otherOpts, info)
	otherAdapter := *a
	otherAdapter.provider = "other-provider"
	cases["provider"] = otherAdapter.zcodeSearchReplayScope(ctx, cfg, auth, req, opts, info)
	cases["unknown-capability"] = a.zcodeSearchReplayScope(ctx, cfg, auth, req, opts, &registry.ModelInfo{})
	for name, other := range cases {
		t.Run(name, func(t *testing.T) {
			if got := other.restoreItems(ctx, input, "input"); strings.Contains(string(got), "private-result") {
				t.Fatal("cached result crossed identity boundary")
			}
		})
	}
	explicit := []byte(`{"input":[{"type":"web_search_call","id":"ws_test","results":[{"content":"client-result"}]}]}`)
	if got := scope.restoreItems(ctx, explicit, "input"); string(got) != string(explicit) {
		t.Fatal("explicit client results were overwritten")
	}
}

func TestZcodeContextCountFollowsReplayEviction(t *testing.T) {
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)
	r := zcodeSearchReplay{scope: "synthetic-eviction", limit: 1}
	output := []byte(`[{"type":"web_search_call","id":"ws_1","results":[{"content":"first-result"}]},{"type":"web_search_call","id":"ws_2","results":[{"content":"second-result"}]}]`)
	for _, item := range util.ParseGJSONBytesNoCopy(output).Array() {
		r.recordItem(context.Background(), item)
	}
	countOutput := r.countOutput(context.Background(), output)
	if strings.Contains(string(countOutput), "first-result") || !strings.Contains(string(countOutput), "second-result") {
		t.Fatal("auxiliary count ignored cache eviction")
	}
}

func TestZcodeContextSignedReasoningFallsBack(t *testing.T) {
	previous := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previous) })
	hook := logtest.NewGlobal()
	for _, stream := range []bool{false, true} {
		for _, mode := range []config.CodexZcodeContextUsageMode{config.ZcodeContextUsageCorrect, config.ZcodeContextUsageObserve} {
			for _, signature := range []string{"", strings.Repeat("synthetic", 256)} {
				t.Run(fmt.Sprintf("stream=%t/mode=%s/signed=%t", stream, mode, signature != ""), func(t *testing.T) {
					hook.Reset()
					host := New()
					host.runtimeConfig = &config.Config{Codex: config.CodexConfig{ZcodeContextUsage: mode}}
					calls := 0
					executor := &fakeExecutor{
						execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							return pluginapi.ExecutorResponse{Payload: []byte(zcodeSearchMessage)}, nil
						},
						executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
							chunks <- pluginapi.ExecutorStreamChunk{Payload: zcodeSearchStream(zcodeSearchMessage)}
							close(chunks)
							return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
						},
						countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							calls++
							if !strings.Contains(string(req.Payload), "historical-thinking") {
								t.Error("history lost before native count")
							}
							return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":101100}`)}, nil
						},
					}
					a := newCurrentExecutorAdapterForTest(host, "signed-history-fixture", executor, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
					a.provider = "zcode-coding-plan"
					payload, _ := json.Marshal(map[string]any{"input": []any{
						map[string]any{"role": "user", "content": "first question"},
						map[string]any{"type": "reasoning", "encrypted_content": signature, "summary": []any{map[string]any{"type": "summary_text", "text": "historical-thinking"}}},
						map[string]any{"role": "assistant", "content": "first answer"}, map[string]any{"role": "user", "content": "search again"},
					}})
					info := &registry.ModelInfo{ID: "glm-fixture", IsCompat: true, NativeCapabilities: &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
					req := coreexecutor.Request{Model: info.ID, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": info}}
					opts := coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.1"}}}
					auth := &coreauth.Auth{ID: "synthetic-auth", Provider: a.provider}
					var response []byte
					var headers http.Header
					if stream {
						result, err := a.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						headers = result.Headers
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							for _, line := range strings.Split(string(chunk.Payload), "\n") {
								data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
								if ojson.Get(data, "type").String() == "response.completed" {
									response = []byte(ojson.Get(data, "response").Raw)
								}
							}
						}
					} else {
						result, err := a.Execute(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						response = result.Payload
						headers = result.Headers
					}
					wantCalls := 1
					wantTotal := int64(505500)
					wantStatus := "counted"
					if mode == config.ZcodeContextUsageCorrect {
						if signature != "" {
							wantCalls = 0
							wantStatus = "client_reasoning_unconfirmed"
						} else {
							wantTotal = 101100
							wantStatus = "corrected"
						}
					}
					if calls != wantCalls || ojson.GetBytes(response, "usage.total_tokens").Int() != wantTotal {
						t.Fatalf("calls=%d,total=%s", calls, ojson.GetBytes(response, "usage.total_tokens").Raw)
					}
					if hook.LastEntry() == nil || hook.LastEntry().Data["status"] != wantStatus {
						t.Fatalf("unexpected outcome: %+v", hook.LastEntry())
					}
					if headers.Get("X-Reasoning-Included") != "" {
						t.Fatal("reasoning header was committed without a reliable fallback contract")
					}
				})
			}
		}
	}
}

func TestZcodeContextDetectsHistoricalReplayEviction(t *testing.T) {
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)
	r := zcodeSearchReplay{scope: "synthetic-history-eviction", limit: 1}
	ctx := context.Background()
	r.recordItem(ctx, ojson.Parse(`{"type":"web_search_call","id":"ws_1","results":[{"content":"first-result"}]}`))
	r.restoreRequest(ctx, []byte(`{"input":[{"type":"web_search_call","id":"ws_1"}]}`))
	if !r.historyUnchanged(ctx) {
		t.Fatal("unchanged history rejected")
	}
	r.recordItem(ctx, ojson.Parse(`{"type":"web_search_call","id":"ws_2","results":[{"content":"second-result"}]}`))
	if r.historyUnchanged(ctx) {
		t.Fatal("evicted input history still treated as replayable")
	}
	// 同一 ID 后来出现结果时，旧的空历史也不能继续作为计数基线。
	r.restoreRequest(ctx, []byte(`{"input":[{"type":"web_search_call","id":"ws_1"}]}`))
	r.recordItem(ctx, ojson.Parse(`{"type":"web_search_call","id":"ws_1","results":[{"content":"new-result"}]}`))
	if r.historyUnchanged(ctx) {
		t.Fatal("changed history result was ignored")
	}
}
