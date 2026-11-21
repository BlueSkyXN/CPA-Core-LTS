package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
)

const cacheFixtureAB = `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"query"},{"role":"assistant","tool_calls":[{"id":"A"},{"id":"B"}]},{"role":"tool","tool_call_id":"A","content":"result A"},{"role":"tool","tool_call_id":"B","content":"result B"}]}`

func cachedIndexes(t *testing.T, raw []byte) []int {
	t.Helper()
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var indexes []int
	for i, m := range body.Messages {
		if _, ok := m["copilot_cache_control"]; ok {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func TestNativeChatCacheToolBatches(t *testing.T) {
	abc := strings.TrimSuffix(cacheFixtureAB, "]}") + `,{"role":"assistant","tool_calls":[{"id":"C"}]},{"role":"tool","tool_call_id":"C","content":"result C"}]}`
	for _, tc := range []struct {
		name, body string
		want       []int
	}{
		{"two_tools", cacheFixtureAB, []int{0, 1, 4}},
		{"next_batch", abc, []int{0, 1, 4, 6}},
		{"partial_batch", strings.Replace(cacheFixtureAB, `,{"role":"tool","tool_call_id":"B","content":"result B"}`, "", 1), []int{0, 1}},
		{"duplicate_result", strings.Replace(cacheFixtureAB, `"tool_call_id":"B"`, `"tool_call_id":"A"`, 1), []int{0, 1}},
		{"empty_tail", strings.Replace(cacheFixtureAB, `"result B"`, `""`, 1), []int{0, 1}},
		{"non_message", `{"messages":[null,{"role":"user","content":[]}]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := copilotReq("cache", false).ExecutorRequest
			req.Payload = []byte(tc.body)
			on, err := nativeRequestPayload(req, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := cachedIndexes(t, on); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("markers=%v want %v", got, tc.want)
			}
			off, err := nativeRequestPayload(req, false)
			if err != nil {
				t.Fatal(err)
			}
			var a, b map[string]any
			_ = json.Unmarshal(on, &a)
			_ = json.Unmarshal(off, &b)
			for _, v := range a["messages"].([]any) {
				m, _ := v.(map[string]any)
				delete(m, "copilot_cache_control")
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatal("cache markers changed message content, order or tool ownership")
			}
		})
	}
}

func TestNativeChatCachePreservesClientBudget(t *testing.T) {
	for _, existing := range []int{0, 3, 4, 5} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", existing, enabled), func(t *testing.T) {
				var b map[string]any
				_ = json.Unmarshal([]byte(cacheFixtureAB), &b)
				ms := b["messages"].([]any)
				prefix := make([]any, 0, existing)
				for range existing {
					prefix = append(prefix, map[string]any{"role": "system", "content": "existing", "copilot_cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}})
				}
				b["messages"] = append(prefix, ms...)
				req := copilotReq("budget", false).ExecutorRequest
				req.Payload, _ = json.Marshal(b)
				raw, err := nativeRequestPayload(req, enabled)
				if err != nil {
					t.Fatal(err)
				}
				got := len(cachedIndexes(t, raw))
				if got < existing || (enabled && got > max(existing, chatCacheMarkerBudget)) || (!enabled && got != existing) || (enabled && existing < chatCacheMarkerBudget && got <= existing) {
					t.Fatalf("existing=%d markers=%d", existing, got)
				}
				if strings.Count(string(raw), `"ttl":"1h"`) != existing {
					t.Fatal("caller markers changed")
				}
			})
		}
	}
}

func TestNativeResponsesCacheBoundaryAtUpstream(t *testing.T) {
	const input = `{"model":"gpt-5-codex","input":[{"role":"user","content":"query"},{"type":"function_call","call_id":"A","name":"a","arguments":"{}"},{"type":"function_call","call_id":"B","name":"b","arguments":"{}"},{"type":"function_call_output","call_id":"A","output":[{"type":"input_text","text":"result A","prompt_cache_breakpoint":{"mode":"explicit"}}]},{"type":"function_call_output","call_id":"B","output":"result B"}]}`
	for _, tc := range []struct {
		name         string
		enabled      bool
		withOriginal bool
		mode         string
		want         []int
	}{
		{"off", false, true, "explicit", nil},
		{"no_original", true, false, "explicit", []int{0, 3}},
		{"explicit", true, true, "explicit", []int{0, 2, 3}},
		{"implicit", true, true, "implicit", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := make(chan json.RawMessage, 1)
			r, _ := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					var body json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					wire <- body
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				default:
					t.Errorf("unexpected upstream: %s", req.URL.Path)
				}
			})
			r.config.ChatCacheBreakpoints = tc.enabled
			req := copilotReq("cache-wire", false)
			original := []byte(strings.Replace(input, `"input":[`, fmt.Sprintf(`"prompt_cache_options":{"mode":%q},"input":[`, tc.mode), 1))
			var err error
			req.Payload, err = responses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(req.Model, original, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.withOriginal {
				req.OriginalRequest = original
			}
			if _, err = r.executeNative(req); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-wire:
				if got := cachedIndexes(t, raw); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("markers=%v want %v", got, tc.want)
				}
				if strings.Contains(string(raw), `"prompt_cache_options"`) || strings.Contains(string(raw), `"prompt_cache_breakpoint"`) {
					t.Fatal("Responses-only fields leaked to Chat upstream")
				}
			default:
				t.Fatal("missing chat payload")
			}
		})
	}
}

func TestNativeCacheSkipsUnmappableResponsesBoundary(t *testing.T) {
	const item = `{"type":"function_call_output","call_id":"A","output":[{"type":"input_text","text":"result A","prompt_cache_breakpoint":{"mode":"explicit"}}]}`
	for _, src := range []string{item + "," + item, strings.Replace(item, `"type":"input_text"`, `"type":"input_image"`, 1), strings.Replace(item, `"mode":"explicit"}}]`, `"mode":"explicit"}},{"type":"input_text","text":"later"}]`, 1), strings.Replace(item, `"call_id":"A"`, `"call_id":"other"`, 1)} {
		req := copilotReq("map", false).ExecutorRequest
		req.Payload = []byte(cacheFixtureAB)
		req.OriginalRequest = []byte(`{"input":[` + src + `]}`)
		out, err := nativeRequestPayload(req, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := cachedIndexes(t, out); !reflect.DeepEqual(got, []int{0, 1, 4}) {
			t.Fatalf("guessed source boundary: %v", got)
		}
	}
}
