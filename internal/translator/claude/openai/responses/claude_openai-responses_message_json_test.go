package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeMessageJSONResponses(t *testing.T) {
	for _, tc := range []struct {
		name, content, stop, kind, field, value, status string
	}{
		{"text", `[{"type":"text","text":"hello"}]`, "end_turn", "message", "content.0.text", "hello", "completed"},
		{"thinking", `[{"type":"thinking","thinking":"reason","signature":"synthetic-opaque"}]`, "end_turn", "reasoning", "encrypted_content", "synthetic-opaque", "completed"},
		{"tool", `[{"type":"tool_use","id":"call-1","name":"lookup","input":{"q":"<x>"}}]`, "tool_use", "function_call", "arguments", `{"q":"<x>"}`, "completed"},
		{"truncated", `[{"type":"text","text":"partial"}]`, "max_tokens", "message", "content.0.text", "partial", "incomplete"},
		{"empty", `[]`, "end_turn", "", "", "", "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"type":"message","id":"msg-1","role":"assistant","model":"fixture","content":` + tc.content + `,"stop_reason":"` + tc.stop + `","usage":{"input_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":4,"output_tokens":7}}`)
			out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", []byte(`{"model":"fixture"}`), nil, body, nil)
			root := gjson.ParseBytes(out)
			if root.Get("id").String() != "msg-1" || root.Get("status").String() != tc.status || root.Get("usage.input_tokens").Int() != 9 || root.Get("usage.output_tokens").Int() != 7 || root.Get("usage.total_tokens").Int() != 16 {
				t.Fatalf("message identity, status or usage lost: %s", out)
			}
			if tc.kind != "" && (root.Get("output.0.type").String() != tc.kind || root.Get("output.0."+tc.field).String() != tc.value) {
				t.Fatal("content semantics lost")
			}
		})
	}
}

func TestClaudeMessageJSONResponsesRejectsUnsupported(t *testing.T) {
	for _, body := range []string{
		`{"type":"message","id":"x","role":"assistant","content":[],"stop_reason":"spurious_reason"}`,
		`{"type":"message","id":"x","role":"assistant","content":[],"stop_reason":""}`,
		`{"type":"message","id":"x","role":"assistant","content":[{"type":"unknown_content","text":"lost"}],"stop_reason":"end_turn"}`,
		`{"type":"message","role":"assistant","content":[],"stop_reason":"end_turn"}`,
	} {
		if out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", nil, nil, []byte(body), nil); len(out) != 0 {
			t.Fatalf("unsupported JSON became success: %s", out)
		}
	}
}

// pause_turn, refusal, sensitive and model_context_window_exceeded are valid
// Anthropic stop reasons the plugin validator must accept; the terminal event
// maps each one instead of failing with an unsupported-response error.
func TestClaudeMessageJSONResponsesStopReasonTerminals(t *testing.T) {
	for _, tc := range []struct {
		stop, status, event string
	}{
		{"pause_turn", "completed", "response.completed"},
		{"refusal", "incomplete", "response.incomplete"},
		{"sensitive", "incomplete", "response.incomplete"},
		{"model_context_window_exceeded", "failed", "response.failed"},
	} {
		t.Run(tc.stop, func(t *testing.T) {
			body := []byte(`{"type":"message","id":"msg-stop","role":"assistant","model":"fixture","content":[{"type":"text","text":"partial"}],"stop_reason":"` + tc.stop + `","usage":{"input_tokens":3,"output_tokens":5}}`)
			var param any = &PluginResponseState{}
			out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", nil, nil, body, &param)
			root := gjson.ParseBytes(out)
			if !root.Exists() || root.Get("status").String() != tc.status {
				t.Fatalf("status = %q, want %q: %s", root.Get("status").String(), tc.status, out)
			}
			switch tc.stop {
			case "pause_turn":
				if root.Get("end_turn").Bool() {
					t.Fatal("pause_turn end_turn must be false")
				}
			case "model_context_window_exceeded":
				if root.Get("error.code").String() != "context_length_exceeded" {
					t.Fatalf("error.code = %q", root.Get("error.code").String())
				}
			default:
				if root.Get("incomplete_details.reason").String() != "content_filter" {
					t.Fatalf("incomplete_details.reason = %q", root.Get("incomplete_details.reason").String())
				}
			}
		})
	}
}

func TestClaudeMessageJSONResponsesMixedOrder(t *testing.T) {
	body := []byte(`{"type":"message","id":"mixed","role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"synthetic"},{"type":"text","text":"first"},{"type":"tool_use","id":"tool-1","name":"lookup","input":{}},{"type":"text","text":"last"}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":8}}`)
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", nil, nil, body, nil)
	for i, kind := range []string{"reasoning", "message", "function_call", "message"} {
		items := gjson.GetBytes(out, "output").Array()
		if len(items) != 4 || items[i].Get("type").String() != kind {
			t.Fatal("content order lost")
		}
	}
}
