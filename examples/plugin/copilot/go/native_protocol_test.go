package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudeapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	openaiapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
)

const matrixToolAnswer = `{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_opaque":"fixture-matrix-state","tool_calls":[{"index":0,"id":"call-A","type":"function","function":{"name":"lookup","arguments":"{\"n\":1}"}},{"index":1,"id":"call-B","type":"function","function":{"name":"lookup","arguments":"{\"n\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
const matrixFinalAnswer = `{"choices":[{"delta":{"content":"matrix-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":6}}}`

type copilotProtocolWire struct {
	t     *testing.T
	calls atomic.Int32
}

func (f *copilotProtocolWire) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	var req struct {
		Messages []struct {
			Role   string
			Cache  *struct{ Type string } `json:"copilot_cache_control"`
			Opaque string                 `json:"reasoning_opaque"`
			Calls  []struct {
				ID       string
				Function struct{ Name, Arguments string }
			} `json:"tool_calls"`
			Content any
			CallID  string `json:"tool_call_id"`
		}
		Stream bool
		Tools  []any
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Error(err)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || !req.Stream || len(req.Tools) != 1 {
		f.t.Error("unexpected upstream endpoint, stream setting or tools")
	}
	switch f.calls.Add(1) {
	case 1:
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", matrixToolAnswer)
	case 2:
		var sawCalls bool
		var results []string
		for _, m := range req.Messages {
			if m.Role == "assistant" && len(m.Calls) > 0 {
				sawCalls = true
				if len(m.Calls) != 2 || m.Opaque != "fixture-matrix-state" {
					f.t.Error("tool continuation lost its batch or reasoning state")
					continue
				}
				for i, c := range m.Calls {
					var a struct{ N int }
					if c.ID != []string{"call-A", "call-B"}[i] || c.Function.Name != "lookup" || json.Unmarshal([]byte(c.Function.Arguments), &a) != nil || a.N != i+1 {
						f.t.Error("tool call identity or arguments changed")
					}
				}
			}
			if m.Role == "tool" {
				results = append(results, m.CallID)
				if m.Content != "result:"+m.CallID {
					f.t.Error("tool result payload changed")
				}
				if (m.CallID == "call-A" && m.Cache != nil) || (m.CallID == "call-B" && (m.Cache == nil || m.Cache.Type != "ephemeral")) {
					f.t.Error("Chat cache marker is not at the end of the tool batch")
				}
			}
		}
		if !sawCalls || strings.Join(results, ",") != "call-A,call-B" {
			f.t.Error("tool batch or paired outputs missing")
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", matrixFinalAnswer)
	case 3:
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n")
	case 4:
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	case 5:
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"total_tokens\":0}}\n\n")
	case 6:
		fmt.Fprintf(w, "data: %s\n\n", `{"error":{"message":"upstream failed"},"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	case 7:
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"choices":[],"usage":{"prompt_tokens":10}}`)
	case 8:
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	default:
		f.t.Error("unexpected upstream replay")
		http.Error(w, "unexpected request", 502)
	}
}

type matrixObject = map[string]any

func matrixPost(t *testing.T, h http.Handler, path, session string, body matrixObject) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Session-Id", session)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func matrixEvents(t *testing.T, raw []byte) (events []matrixObject, done int) {
	t.Helper()
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		text := strings.TrimSpace(line[5:])
		if text == "[DONE]" {
			done++
			continue
		}
		var v matrixObject
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			t.Fatal(err)
		}
		events = append(events, v)
	}
	return
}

// 像客户端一样从终结 JSON / SSE 分片还原助手消息，不能从假上游夹具抄 ID 或参数构造续调。
func matrixFollow(t *testing.T, path string, body matrixObject, stream bool, raw []byte) {
	t.Helper()
	var result matrixObject
	var events []matrixObject
	var done int
	if stream {
		events, done = matrixEvents(t, raw)
	} else if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	switch path {
	case "/v1/responses":
		if stream {
			n := 0
			for _, e := range events {
				if e["type"] == "response.completed" {
					result = e["response"].(matrixObject)
					n++
				}
			}
			if n != 1 {
				t.Fatalf("Responses completions=%d", n)
			}
		}
		items := result["output"].([]any)
		input := append(body["input"].([]any), items...)
		count := 0
		for _, v := range items {
			item := v.(matrixObject)
			if item["type"] == "function_call" {
				id := item["call_id"].(string)
				input = append(input, matrixObject{"type": "function_call_output", "call_id": id, "output": "result:" + id})
				count++
			}
		}
		if count != 2 {
			t.Fatalf("Responses tool count=%d", count)
		}
		body["input"] = input
	case "/v1/messages":
		var blocks []any
		if !stream {
			if result["stop_reason"] != "tool_use" {
				t.Fatal("Messages missing tool stop reason")
			}
			blocks = result["content"].([]any)
		} else {
			partial := map[int]string{}
			stops := 0
			toolStop := false
			for _, e := range events {
				switch e["type"] {
				case "content_block_start":
					i := int(e["index"].(float64))
					if i != len(blocks) {
						t.Fatal("out-of-order message block")
					}
					blocks = append(blocks, e["content_block"])
				case "content_block_delta":
					d := e["delta"].(matrixObject)
					if d["type"] == "input_json_delta" {
						partial[int(e["index"].(float64))] += d["partial_json"].(string)
					}
				case "message_delta":
					toolStop = e["delta"].(matrixObject)["stop_reason"] == "tool_use"
				case "message_stop":
					stops++
				}
			}
			if stops != 1 || !toolStop {
				t.Fatal("Messages tool stream did not terminate exactly once")
			}
			for i, s := range partial {
				var args matrixObject
				if err := json.Unmarshal([]byte(s), &args); err != nil {
					t.Fatal(err)
				}
				blocks[i].(matrixObject)["input"] = args
			}
		}
		var outputs []any
		for _, v := range blocks {
			b := v.(matrixObject)
			if b["type"] == "tool_use" {
				id := b["id"].(string)
				outputs = append(outputs, matrixObject{"type": "tool_result", "tool_use_id": id, "content": "result:" + id})
			}
		}
		if len(outputs) != 2 {
			t.Fatal("Messages missing tools")
		}
		body["messages"] = append(body["messages"].([]any), matrixObject{"role": "assistant", "content": blocks}, matrixObject{"role": "user", "content": outputs})
	case "/v1/chat/completions":
		var msg matrixObject
		if !stream {
			choice := result["choices"].([]any)[0].(matrixObject)
			if choice["finish_reason"] != "tool_calls" {
				t.Fatal("Chat missing tool finish reason")
			}
			msg = choice["message"].(matrixObject)
		} else {
			msg = matrixObject{"role": "assistant", "content": nil}
			var calls []any
			term := 0
			for _, e := range events {
				choices, _ := e["choices"].([]any)
				if len(choices) == 0 {
					continue
				}
				c := choices[0].(matrixObject)
				if c["finish_reason"] == "tool_calls" {
					term++
				}
				d, _ := c["delta"].(matrixObject)
				if opaque, ok := d["reasoning_opaque"].(string); ok {
					msg["reasoning_opaque"] = opaque
				}
				frags, _ := d["tool_calls"].([]any)
				for _, v := range frags {
					f := v.(matrixObject)
					i := int(f["index"].(float64))
					if i == len(calls) {
						calls = append(calls, matrixObject{"type": "function", "function": matrixObject{"arguments": ""}})
					}
					if i < 0 || i >= len(calls) {
						t.Fatal("invalid tool index")
					}
					call := calls[i].(matrixObject)
					if id, ok := f["id"].(string); ok {
						call["id"] = id
					}
					fn := call["function"].(matrixObject)
					chunk := f["function"].(matrixObject)
					if name, ok := chunk["name"].(string); ok {
						fn["name"] = name
					}
					if args, ok := chunk["arguments"].(string); ok {
						fn["arguments"] = fn["arguments"].(string) + args
					}
				}
			}
			if term != 1 || done != 1 {
				t.Fatal("Chat tool stream did not terminate exactly once")
			}
			msg["tool_calls"] = calls
		}
		if msg["reasoning_opaque"] != "fixture-matrix-state" {
			t.Fatal("Chat lost explicit opaque")
		}
		calls := msg["tool_calls"].([]any)
		if len(calls) != 2 {
			t.Fatal("Chat missing tools")
		}
		history := append(body["messages"].([]any), msg)
		for _, v := range calls {
			id := v.(matrixObject)["id"].(string)
			history = append(history, matrixObject{"role": "tool", "tool_call_id": id, "content": "result:" + id})
		}
		body["messages"] = history
	}
}

func testCopilotProtocolMatrix(t *testing.T, base *handlers.BaseAPIHandler, slot *atomic.Pointer[copilotProtocolWire], sink *copilotUsageSink, authIndex string) {
	t.Helper()
	router := gin.New()
	router.POST("/v1/chat/completions", openaiapi.NewOpenAIAPIHandler(base).ChatCompletions)
	router.POST("/v1/responses", openaiapi.NewOpenAIResponsesAPIHandler(base).Responses)
	router.POST("/v1/messages", claudeapi.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", strings.TrimPrefix(path, "/v1/"), stream), func(t *testing.T) {
				f := &copilotProtocolWire{t: t}
				slot.Store(f)
				defer slot.Store(nil)
				user := matrixObject{"role": "user", "content": "run two lookups"}
				body := matrixObject{"model": "gemini-4-argon", "stream": stream}
				schema := matrixObject{"type": "object", "properties": matrixObject{"n": matrixObject{"type": "integer"}}}
				tool := matrixObject{"type": "function", "name": "lookup", "parameters": schema}
				switch path {
				case "/v1/responses":
					body["input"], body["tools"] = []any{user}, []any{tool}
				case "/v1/messages":
					body["max_tokens"], body["messages"], body["tools"] = 128, []any{user}, []any{matrixObject{"name": "lookup", "input_schema": schema}}
				default:
					body["messages"], body["tools"] = []any{user}, []any{matrixObject{"type": "function", "function": matrixObject{"name": "lookup", "parameters": schema}}}
				}
				session := fmt.Sprintf("matrix-%s-%t", strings.ReplaceAll(path, "/", "-"), stream)
				const reported = "provider_reported_unverified"
				seen := make(map[string]bool)
				usage := func(failed bool, wantTokens, wantCached int64, provenance string) {
					t.Helper()
					select {
					case record := <-sink.records:
						if record.RequestID == "" || seen[record.RequestID] || record.UsageProvenance != provenance {
							t.Errorf("missing/duplicate request identity or provenance=%q, want %q", record.UsageProvenance, provenance)
						}
						seen[record.RequestID] = true
						if record.Failed != failed || record.AuthIndex != authIndex || record.Detail.TotalTokens != wantTokens || record.Detail.CacheReadTokens != wantCached {
							t.Errorf("wrong formal usage: failed=%v tokens=%d/%d cacheRead=%d/%d authMatches=%v", record.Failed, record.Detail.TotalTokens, wantTokens, record.Detail.CacheReadTokens, wantCached, record.AuthIndex == authIndex)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("no usage record")
					}
				}
				first := matrixPost(t, router, path, session, body)
				if first.Code != 200 {
					t.Fatalf("first status=%d: %s", first.Code, first.Body.String())
				}
				matrixFollow(t, path, body, stream, first.Body.Bytes())
				usage(false, 12, 0, reported)
				second := matrixPost(t, router, path, session, body)
				if second.Code != 200 || !strings.Contains(second.Body.String(), "matrix-ok") {
					t.Fatalf("follow-up status=%d: %s", second.Code, second.Body.String())
				}
				if stream {
					end := map[string]string{"/v1/chat/completions": "data: [DONE]", "/v1/responses": "event: response.completed", "/v1/messages": "event: message_stop"}[path]
					if strings.Count(second.Body.String(), end) != 1 {
						t.Fatal("second stream missing unique success terminal")
					}
				}
				usage(false, 12, 6, reported)
				bad := matrixPost(t, router, path, session, body)
				if stream {
					s := bad.Body.String()
					if !strings.Contains(s, "partial") || !(strings.Contains(s, `"error"`) || strings.Contains(s, "response.failed")) || strings.Contains(s, "event: message_stop") || strings.Contains(s, "event: response.completed") {
						t.Fatalf("missing stream failure: %s", s)
					}
				} else if bad.Code < 400 || !strings.Contains(bad.Body.String(), `"error"`) {
					t.Fatalf("invalid JSON error: status=%d", bad.Code)
				}
				usage(true, 12, 3, reported)
				wantCalls := int32(3)
				if !stream {
					for _, failure := range []struct {
						name, provenance string
						tokens           int64
						attempts         int32
					}{
						{"no usage", "unavailable", 0, 1},
						{"explicit zero", reported, 0, 1},
						{"usage in error frame", reported, 6, 1},
						{"partial usage across retry", reported, 16, 2},
					} {
						bad := matrixPost(t, router, path, session, body)
						if bad.Code != http.StatusBadGateway || !strings.Contains(bad.Body.String(), `"error"`) || strings.Contains(bad.Body.String(), "partial_usage") {
							t.Fatalf("%s: unexpected non-stream error status=%d", failure.name, bad.Code)
						}
						usage(true, failure.tokens, 0, failure.provenance)
						wantCalls += failure.attempts
					}
				}
				if f.calls.Load() != wantCalls {
					t.Fatalf("expected %d Chat inference requests, got %d", wantCalls, f.calls.Load())
				}
				select {
				case <-sink.records:
					t.Error("duplicate formal usage")
				default:
				}
			})
		}
	}
}
