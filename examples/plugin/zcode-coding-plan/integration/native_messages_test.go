package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestNativeMessagesSystemHistoryPassthrough(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, "glm-5.3")
	for _, tt := range []struct {
		name, messages string
		betas          []string
	}{
		{
			name:     "compact text",
			messages: `[{"role":"user","content":"Summarize the conversation"},{"role":"system","content":[{"type":"text","text":"保留决定、文件路径和待办事项。","cache_control":{"type":"ephemeral"}}]}]`,
		},
		{
			name:     "compact tool history",
			messages: `[{"role":"user","content":"Read the file"},{"role":"assistant","content":[{"type":"thinking","thinking":"Read first","signature":"synthetic-opaque"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"File contents"}]},{"role":"system","content":"Summarize without calling more tools"}]`,
		},
		{
			name:     "turn scoped history and effort",
			messages: `[{"role":"system","content":[],"output_config":{"effort":"medium"}},{"role":"user","content":"First turn"},{"role":"system","content":"Old reminder","clear_at":"next_user_message"},{"role":"assistant","content":"Done"},{"role":"system","content":[],"output_config":{"effort":"xhigh"}},{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"text","text":"Current reminder"}],"clear_at":"next_user_message"}]`,
			betas:    []string{"mid-conversation-system-clear-at-2026-08-21", "mid-conversation-output-config-2026-07-01", "per-turn-control-2026-07-01"},
		},
		{
			name:     "inline tool changes",
			messages: `[{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"read_file"}},{"type":"text","text":"Only summarize"}]}]`,
			betas:    []string{"mid-conversation-tool-changes-2026-07-01", "inline-tools-2026-09-15"},
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, stream), func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"glm-5.3","max_tokens":1000,"stream":%t,"system":[{"type":"text","text":"Preserve the caller system prompt"}],"messages":%s}`, stream, tt.messages)
				var want map[string]any
				if err := json.Unmarshal([]byte(body), &want); err != nil {
					t.Fatal(err)
				}
				f.transport.lock.Lock()
				before := len(f.transport.captured)
				f.transport.lock.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "synthetic-claude-code")
				req.Header.Set("Authorization", "Bearer synthetic-caller-key")
				req.Header.Set("X-Api-Key", "synthetic-caller-key")
				req.Header.Add("Anthropic-Beta", "mid-conversation-system-2026-04-07,oauth-2025-04-20")
				if len(tt.betas) > 0 {
					req.Header.Add("Anthropic-Beta", strings.Join(tt.betas, ","))
				}
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fixture-ok") {
					t.Fatalf("status=%d response=%s", rec.Code, rec.Body.String())
				}
				if stream {
					if strings.Count(rec.Body.String(), "event: message_stop") != 1 {
						t.Fatal("native stream terminal event lost or duplicated")
					}
				} else if gjson.GetBytes(rec.Body.Bytes(), "type").String() != "message" || gjson.GetBytes(rec.Body.Bytes(), "role").String() != "assistant" {
					t.Fatal("native Messages response was translated")
				}
				f.checkUsage(t, false)
				f.transport.lock.Lock()
				count := len(f.transport.captured)
				wire := append([]byte(nil), f.transport.captured[count-1]...)
				headers := f.transport.headers[count-1].Clone()
				f.transport.lock.Unlock()
				if count != before+1 {
					t.Fatal("native request was retried or not dispatched")
				}
				var got map[string]any
				if err := json.Unmarshal(wire, &got); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"model", "max_tokens", "stream", "system", "messages"} {
					if !reflect.DeepEqual(got[key], want[key]) {
						t.Fatalf("native %s changed", key)
					}
				}
				if got["reasoning_effort"] != nil || got["tools"] != nil {
					t.Fatal("message controls or inline tools were moved to top-level fields")
				}
				wantBetas := append([]string{"mid-conversation-system-2026-04-07"}, tt.betas...)
				if !reflect.DeepEqual(strings.Split(headers.Get("Anthropic-Beta"), ","), wantBetas) {
					t.Fatal("native system beta headers lost or unrelated beta forwarded")
				}
				if headers.Get("User-Agent") != "ZCode/3.14.3 ai-sdk/provider-utils/4.0.27 runtime/node.js/24" || headers.Get("X-App-Id") != "zcode" || headers.Get("Anthropic-Version") != "2023-06-01" {
					t.Fatal("provider identity changed")
				}
			})
		}
	}
	f.transport.fixtureTransport.mu.Lock()
	handshakes := f.transport.handshakes
	f.transport.fixtureTransport.mu.Unlock()
	if handshakes != 1 {
		t.Fatalf("handshake count = %d, want 1", handshakes)
	}
	select {
	case <-f.usage.records:
		t.Fatal("native request usage counted twice")
	default:
	}
}
