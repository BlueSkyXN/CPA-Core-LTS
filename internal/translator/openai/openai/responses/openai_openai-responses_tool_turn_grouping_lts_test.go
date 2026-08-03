package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// toolTurnMessageShapesForTest renders converted messages as compact shape tokens
// such as assistant[call_a,call_b]{rc}, assistant(text), tool(call_a), user.
func toolTurnMessageShapesForTest(out []byte) []string {
	msgs := gjson.GetBytes(out, "messages").Array()
	shapes := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		role := msg.Get("role").String()
		switch role {
		case "assistant":
			toolCalls := msg.Get("tool_calls").Array()
			if len(toolCalls) == 0 {
				shapes = append(shapes, "assistant(text)")
				continue
			}
			ids := ""
			for i, tc := range toolCalls {
				if i > 0 {
					ids += ","
				}
				ids += tc.Get("id").String()
			}
			shape := "assistant[" + ids + "]"
			if msg.Get("reasoning_content").String() != "" {
				shape += "{rc}"
			}
			shapes = append(shapes, shape)
		case "tool":
			shapes = append(shapes, "tool("+msg.Get("tool_call_id").String()+")")
		default:
			shapes = append(shapes, role)
		}
	}
	return shapes
}

func assertToolTurnMessageShapesForTest(t *testing.T, out []byte, want []string) {
	t.Helper()
	got := toolTurnMessageShapesForTest(out)
	if len(got) != len(want) {
		t.Fatalf("message shapes = %v, want %v; output=%s", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message shapes = %v, want %v; output=%s", got, want, out)
		}
	}
}

func canonicalToolTurnJSONForTest(t *testing.T, raw []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("canonicalize JSON %q: %v", raw, err)
	}
	return canonical
}

func assertToolTurnMessagesJSONEqualForTest(t *testing.T, out []byte, want string) {
	t.Helper()
	got := gjson.GetBytes(out, "messages")
	if !got.Exists() || !got.IsArray() {
		t.Fatalf("messages should be an array; output=%s", out)
	}
	gotCanonical := canonicalToolTurnJSONForTest(t, []byte(got.Raw))
	wantCanonical := canonicalToolTurnJSONForTest(t, []byte(want))
	if !bytes.Equal(gotCanonical, wantCanonical) {
		t.Fatalf("messages = %s, want %s; output=%s", gotCanonical, wantCanonical, out)
	}
}

// One assistant turn of parallel calls must stay grouped in a single Chat
// Completions assistant message even when Codex history interleaves assistant
// texts between calls. Strict providers otherwise reject the adjacent
// assistant(tool_calls) messages with HTTP 400.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesCallsSplitByAssistantText(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"sub-agent note"}]},
			{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_a","output":"file.txt"},
			{"type":"function_call_output","call_id":"call_b","output":"/tmp"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"assistant(text)",
		"user",
	})
}

// Reasoning items between calls must not split the assistant turn, and the
// pending reasoning content stays attached to the merged tool_calls message.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesCallsSplitByReasoning(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]},
			{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_a","output":"file.txt"},
			{"type":"function_call_output","call_id":"call_b","output":"/tmp"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]{rc}",
		"tool(call_a)",
		"tool(call_b)",
	})
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "think" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q; output=%s", got, "think", out)
	}
}

// Custom (freeform) tool calls get the same grouping treatment.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesCustomToolCallsSplitByAssistantText(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"custom_tool_call","call_id":"call_a","name":"exec","input":"ls"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"note"}]},
			{"type":"custom_tool_call","call_id":"call_b","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","call_id":"call_a","output":"file.txt"},
			{"type":"custom_tool_call_output","call_id":"call_b","output":"/tmp"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"assistant(text)",
	})
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String(); got != `{"input":"ls"}` {
		t.Fatalf("messages.0.tool_calls.0.function.arguments = %q, want wrapped input; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesMixedToolCallsSplitByAssistantText(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_function","name":"lookup","arguments":"{}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"note"}]},
			{"type":"custom_tool_call","call_id":"call_custom","name":"exec","input":"pwd"},
			{"type":"function_call_output","call_id":"call_function","output":"ok"},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"/tmp"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_function,call_custom]",
		"tool(call_function)",
		"tool(call_custom)",
		"assistant(text)",
	})
}

// Codex Desktop multi-agent sub-agent calls use harness-style ids
// (call-<uuid>-<n>); grouping must preserve them verbatim.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesHarnessStyleCallIDsWhenMerging(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0","name":"exec_command","arguments":"{}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"sub-agent note"}]},
			{"type":"function_call","call_id":"call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0","output":"ok"},
			{"type":"function_call_output","call_id":"call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0,call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1]",
		"tool(call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0)",
		"tool(call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1)",
		"assistant(text)",
	})
}

// Ignored assistant-side Responses items are transparent to the remaining
// function-call turn. They must not recreate adjacent assistant(tool_calls)
// messages after their own protocol-specific details are omitted.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesCallsAcrossIgnoredAssistantItems(t *testing.T) {
	tests := []struct {
		name string
		item string
	}{
		{name: "agent message", item: `{"type":"agent_message","content":[{"type":"input_text","text":"sub-agent event"}]}`},
		{name: "web search call", item: `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"input":[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				` + tt.item + `,
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok_a"},
				{"type":"function_call_output","call_id":"call_b","output":"ok_b"}
			]}`)
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("openai-compat-model", raw, true)
			assertToolTurnMessageShapesForTest(t, out, []string{
				"assistant[call_a,call_b]",
				"tool(call_a)",
				"tool(call_b)",
			})
		})
	}
}

// Unaffected message sequences retain their complete converted structure and
// content, not merely their roles or tool-call ids. The cases use several
// model names to make the protocol-wide (rather than model-gated) scope clear.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesCompleteMessagesForUnaffectedShapes(t *testing.T) {
	tests := []struct {
		name  string
		model string
		input string
		want  string
	}{
		{
			name: "single call pair", model: "gpt-compat",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{\"value\":1}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{\"value\":1}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok"}
			]`,
		},
		{
			name: "adjacent calls stay merged", model: "deepseek-v4-flash",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{\"q\":\"x\"}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok_a"},
				{"type":"function_call_output","call_id":"call_b","output":"ok_b"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[
					{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}},
					{"id":"call_b","type":"function","function":{"name":"tool_b","arguments":"{\"q\":\"x\"}"}}
				]},
				{"role":"tool","tool_call_id":"call_a","content":"ok_a"},
				{"role":"tool","tool_call_id":"call_b","content":"ok_b"}
			]`,
		},
		{
			name: "message after completed call", model: "longcat-2.0",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"message","role":"user","content":"next"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok"},
				{"role":"user","content":"next"}
			]`,
		},
		{
			name: "message before call", model: "kimi-k3",
			input: `[
				{"type":"message","role":"user","content":"go"},
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: `[
				{"role":"user","content":"go"},
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok"}
			]`,
		},
		{
			name: "reasoning before call", model: "openai-compat-model",
			input: `[
				{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]},
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}],"reasoning_content":"think"},
				{"role":"tool","tool_call_id":"call_a","content":"ok"}
			]`,
		},
		{
			name: "two sequential turns", model: "openai-compat-model",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok_a"},
				{"type":"message","role":"user","content":"next"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_b","output":"ok_b"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok_a"},
				{"role":"user","content":"next"},
				{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"tool_b","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_b","content":"ok_b"}
			]`,
		},
		{
			name: "user interrupt keeps turn split", model: "openai-compat-model",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"message","role":"user","content":"hold on"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"user","content":"hold on"},
				{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"tool_b","arguments":"{}"}}]}
			]`,
		},
		{
			name: "dangling call does not trap assistant text", model: "openai-compat-model",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"note"}]}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"assistant","content":[{"type":"text","text":"note"}]}
			]`,
		},
		{
			name: "agent message remains omitted", model: "openai-compat-model",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"agent_message","content":[{"type":"input_text","text":"sub"}]},
				{"type":"message","role":"user","content":"next"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok"},
				{"role":"user","content":"next"}
			]`,
		},
		{
			name: "custom tool single pair", model: "openai-compat-model",
			input: `[
				{"type":"custom_tool_call","call_id":"call_a","name":"exec","input":"ls"},
				{"type":"custom_tool_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: `[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"exec","arguments":"{\"input\":\"ls\"}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"ok"}
			]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"input":` + tt.input + `}`)
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions(tt.model, raw, true)
			assertToolTurnMessagesJSONEqualForTest(t, out, tt.want)
		})
	}
}

// Accepted delta: reasoning emitted between a call and its output now attaches
// to the assistant tool_calls message instead of trailing as a separate
// reasoning-only assistant message at the end.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesMidCallReasoningToToolCallMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]},
			{"type":"function_call_output","call_id":"call_a","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	assertToolTurnMessagesJSONEqualForTest(t, out, `[
		{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}}],"reasoning_content":"think"},
		{"role":"tool","tool_call_id":"call_a","content":"ok"}
	]`)
}
