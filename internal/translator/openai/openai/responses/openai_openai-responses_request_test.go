package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func prettyJSONForTest(raw []byte) string {
	if !gjson.ValidBytes(raw) {
		return string(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergeConsecutiveFunctionCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"exec_command:0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"exec_command:1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"exec_command:0","output":"ok0"},
			{"type":"function_call_output","call_id":"exec_command:1","output":"ok1"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages should be an array")
	}
	if got := len(msgs.Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d", got, 2)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "exec_command:0" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != "exec_command:1" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "exec_command:1")
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "exec_command:0" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "exec_command:1" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "exec_command:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitFunctionCallsWhenInterrupted(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"next"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_a")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_calls.0.id").String(); got != "call_b" {
		t.Fatalf("messages.2.tool_calls.0.id = %q, want %q", got, "call_b")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilToolOutput(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_x","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},
			{"type":"message","role":"user","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_x","output":"ok"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 4 {
		t.Fatalf("messages count = %d, want %d", got, 4)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_x" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_x")
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "Approved command prefix saved" {
		t.Fatalf("messages.2.content = %q, want %q", got, "Approved command prefix saved")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "next" {
		t.Fatalf("messages.3.content = %q, want %q", got, "next")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToAssistantMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_1",
				"summary": [
					{"type": "summary_text", "text": "first line\n"},
					{"type": "summary_text", "text": "second line"}
				]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "answer"}]
			},
			{"type": "message", "role": "user", "content": "next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "first line\nsecond line" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q; output=%s", got, "first line\nsecond line", out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
		t.Fatalf("messages.0.content.0.text = %q, want answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToToolCallMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_tool",
				"summary": [{"type": "summary_text", "text": "tool reasoning"}]
			},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "tool reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want tool reasoning; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want call_1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want tool; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsReasoningBeforeUserMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type": "reasoning", "id": "rs_empty", "summary": []},
			{"type": "message", "role": "user", "content": "continue"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want placeholder; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceTools(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Use add_numbers."}
		],
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__test_mcp__",
				"description": "Tools in the mcp__test_mcp__ namespace.",
				"tools": [
					{
						"type": "function",
						"name": "add_numbers",
						"description": "Add two numbers",
						"parameters": {
							"type": "object",
							"properties": {
								"a": { "type": "number" },
								"b": { "type": "number" }
							},
							"required": ["a", "b"]
						}
					}
				]
			}
		],
		"tool_choice": "auto"
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "mcp__test_mcp__add_numbers" {
		t.Fatalf("tools.0.function.name = %q, want mcp__test_mcp__add_numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Add two numbers" {
		t.Fatalf("tools.0.function.description = %q, want Add two numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "a" {
		t.Fatalf("tools.0.function.parameters.required.0 = %q, want a; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_QualifiesNamespaceFunctionCallHistory(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_get_me","name":"get_me","namespace":"mcp__github","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_get_me","output":"ok"}
		],
		"tools": [
			{
				"type":"namespace",
				"name":"mcp__github",
				"tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	gotHistoryName := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String()
	gotDeclaredName := gjson.GetBytes(out, "tools.0.function.name").String()
	if gotHistoryName != "mcp__github__get_me" {
		t.Fatalf("history function name = %q, want mcp__github__get_me; output=%s", gotHistoryName, out)
	}
	if gotHistoryName != gotDeclaredName {
		t.Fatalf("history function name = %q, declared function name = %q; output=%s", gotHistoryName, gotDeclaredName, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceCustomTools(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "top-level tools",
			raw: []byte(`{
				"tools":[{
					"type":"namespace",
					"name":"terminal",
					"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
				}]
			}`),
		},
		{
			name: "additional tools",
			raw: []byte(`{
				"input":[{
					"type":"additional_tools",
					"tools":[{
						"type":"namespace",
						"name":"terminal",
						"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
					}]
				}]
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)

			if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
				t.Fatalf("tools count = %d, want 1; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "terminal__exec" {
				t.Fatalf("tool name = %q, want terminal__exec; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Run a command" {
				t.Fatalf("tool description = %q, want Run a command; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.type").String(); got != "object" {
				t.Fatalf("parameters type = %q, want object; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.input.type").String(); got != "string" {
				t.Fatalf("input type = %q, want string; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "input" {
				t.Fatalf("required parameter = %q, want input; output=%s", got, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesStructuredToolChoice(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Run command."}
		],
		"tools": [
			{
				"type": "function",
				"name": "run_command",
				"parameters": {"type": "object"}
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {
				"name": "run_command"
			}
		}
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "run_command" {
		t.Fatalf("tool_choice.function.name = %q, want run_command; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OmitsToolSettingsWithoutTools(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "empty tools",
			raw: []byte(`{
				"input": [{"role":"user","content":"say ok"}],
				"tools": [],
				"tool_choice": "auto",
				"parallel_tool_calls": false
			}`),
		},
		{
			name: "unconvertible tools",
			raw: []byte(`{
				"tools": [{"type":"unsupported"}],
				"tool_choice": "auto",
				"parallel_tool_calls": false
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("grok-4.5", tt.raw, false)

			for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
				if got := gjson.GetBytes(out, field); got.Exists() {
					t.Fatalf("%s should be omitted without tools; output=%s", field, out)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesParallelToolCallsWithTools(t *testing.T) {
	raw := []byte(`{
		"tools": [
			{
				"type": "function",
				"name": "run_command",
				"parameters": {"type": "object"}
			}
		],
		"parallel_tool_calls": false
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("grok-4.5", raw, false)

	if got := gjson.GetBytes(out, "parallel_tool_calls"); !got.Exists() || got.Bool() {
		t.Fatalf("parallel_tool_calls = %v, want false; output=%s", got.Value(), out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesInputImageDetail(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_image",
						"image_url": "https://example.com/image.png",
						"detail": "high"
					}
				]
			}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("messages.0.content.0.image_url.url = %q, want https://example.com/image.png; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.detail").String(); got != "high" {
		t.Fatalf("messages.0.content.0.image_url.detail = %q, want high; output=%s", got, out)
	}
}

// messageShapesForTest renders the converted messages as compact shape tokens
// such as assistant[call_a,call_b]{rc}, assistant(text), tool(call_a), user.
func messageShapesForTest(out []byte) []string {
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

func assertMessageShapesForTest(t *testing.T, out []byte, want []string) {
	t.Helper()
	got := messageShapesForTest(out)
	if len(got) != len(want) {
		t.Fatalf("message shapes = %v, want %v; output=%s", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message shapes = %v, want %v; output=%s", got, want, out)
		}
	}
}

// One assistant turn of parallel calls must stay grouped in a single Chat
// Completions assistant message even when Codex history interleaves assistant
// texts between the calls; strict providers (Kimi) otherwise reject the
// adjacent assistant(tool_calls) messages with HTTP 400.
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
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	assertMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"assistant(text)",
		"user",
	})
}

// Reasoning items between calls must not split the assistant turn either, and
// the pending reasoning content stays attached to the merged tool_calls
// message.
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
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	assertMessageShapesForTest(t, out, []string{
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
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	assertMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"assistant(text)",
	})
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String(); got != `{"input":"ls"}` {
		t.Fatalf("messages.0.tool_calls.0.function.arguments = %q, want wrapped input; output=%s", got, out)
	}
}

// Codex Desktop multi-agent sub-agent calls use harness style ids
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
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	assertMessageShapesForTest(t, out, []string{
		"assistant[call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0,call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1]",
		"tool(call-583ab1f9-5593-419e-bd7f-3e2e234b0711-0)",
		"tool(call-583ab1f9-5593-419e-bd7f-3e2e234b0711-1)",
		"assistant(text)",
	})
}

// Shapes that were already correct must stay byte-stable: grouping only
// changes the previously broken split-turn cases.
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_StableMessageShapes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name: "single call pair",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: []string{"assistant[call_a]", "tool(call_a)"},
		},
		{
			name: "adjacent calls stay merged",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"function_call_output","call_id":"call_b","output":"ok"}
			]`,
			want: []string{"assistant[call_a,call_b]", "tool(call_a)", "tool(call_b)"},
		},
		{
			name: "message after completed call",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"message","role":"user","content":"next"}
			]`,
			want: []string{"assistant[call_a]", "tool(call_a)", "user"},
		},
		{
			name: "message before call",
			input: `[
				{"type":"message","role":"user","content":"go"},
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: []string{"user", "assistant[call_a]", "tool(call_a)"},
		},
		{
			name: "reasoning before call",
			input: `[
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]},
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: []string{"assistant[call_a]{rc}", "tool(call_a)"},
		},
		{
			name: "two sequential turns",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"message","role":"user","content":"next"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_b","output":"ok"}
			]`,
			want: []string{"assistant[call_a]", "tool(call_a)", "user", "assistant[call_b]", "tool(call_b)"},
		},
		{
			name: "user interrupt without outputs keeps turn split",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"message","role":"user","content":"hold on"},
				{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
			]`,
			want: []string{"assistant[call_a]", "user", "assistant[call_b]"},
		},
		{
			name: "dangling call does not trap assistant text",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"note"}]}
			]`,
			want: []string{"assistant[call_a]", "assistant(text)"},
		},
		{
			name: "agent_message item is skipped",
			input: `[
				{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_a","output":"ok"},
				{"type":"agent_message","role":"assistant","content":[{"type":"output_text","text":"sub"}]},
				{"type":"message","role":"user","content":"next"}
			]`,
			want: []string{"assistant[call_a]", "tool(call_a)", "user"},
		},
		{
			name: "custom tool single pair",
			input: `[
				{"type":"custom_tool_call","call_id":"call_a","name":"exec","input":"ls"},
				{"type":"custom_tool_call_output","call_id":"call_a","output":"ok"}
			]`,
			want: []string{"assistant[call_a]", "tool(call_a)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"input": ` + tc.input + `}`)
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
			assertMessageShapesForTest(t, out, tc.want)
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
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	assertMessageShapesForTest(t, out, []string{
		"assistant[call_a]{rc}",
		"tool(call_a)",
	})
}
