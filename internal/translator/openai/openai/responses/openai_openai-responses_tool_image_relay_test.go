package responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestResponsesOrphanToolImagesStayInline(t *testing.T) {
	const content = `[{"type":"input_text","text":"first"},{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":"original"},{"type":"input_text","text":"second"},{"type":"image_url","image_url":{"url":"https://example.com/second.png","detail":"low"}}]`
	for _, outputType := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, stringified := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stringified=%t", outputType, stringified), func(t *testing.T) {
				output := content
				if stringified {
					encoded, _ := json.Marshal(content)
					output = string(encoded)
				}
				raw := []byte(fmt.Sprintf(`{"input":[{"type":%q,"call_id":"orphan","output":%s}]}`, outputType, output))
				out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, false)
				if err != nil {
					t.Fatal(err)
				}
				assertToolTurnMessagesJSONEqualForTest(t, out, `[{"role":"user","content":[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"high"}},{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"https://example.com/second.png","detail":"low"}}]}]`)
			})
		}
	}
}

func TestResponsesParallelToolImagesFollowAllResults(t *testing.T) {
	raw := []byte(`{"input":[
		{"type":"function_call","call_id":"a","name":"Read","arguments":"{}"},
		{"type":"custom_tool_call","call_id":"b","name":"view_image","input":"second.png"},
		{"type":"function_call_output","call_id":"a","output":[{"type":"input_text","text":"image A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
		{"type":"custom_tool_call_output","call_id":"b","output":[{"type":"input_image","image_url":"data:image/png;base64,AQ=="},{"type":"input_image","image_url":"https://example.com/third.png","detail":"low"}]},
		{"role":"user","content":[{"type":"input_text","text":"compare"}]}
	]}`)
	out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
	if err != nil {
		t.Fatal(err)
	}
	assertToolTurnMessageShapesForTest(t, out, []string{"assistant[a,b]", "tool(a)", "tool(b)", "user", "user", "user"})
	for _, i := range []int{1, 2} {
		content := ojson.GetBytesNoCopy(out, fmt.Sprintf("messages.%d.content", i))
		for _, part := range content.Array() {
			if part.Get("type").String() != "text" || strings.Contains(part.Get("text").String(), "base64,") {
				t.Fatalf("tool result still carries image data: %s", content.Raw)
			}
		}
	}
	for path, want := range map[string]string{
		"messages.1.content.0.text":             "image A",
		"messages.3.content.1.image_url.url":    "data:image/png;base64,AA==",
		"messages.4.content.1.image_url.url":    "data:image/png;base64,AQ==",
		"messages.4.content.2.image_url.url":    "https://example.com/third.png",
		"messages.4.content.2.image_url.detail": "low",
		"messages.5.content.0.text":             "compare",
	} {
		if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
			t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
		}
	}
}

func TestResponsesMultipleFullParallelToolGroupsFlushMediaPerGroup(t *testing.T) {
	raw := []byte(`{"input":[
		{"type":"function_call","call_id":"g1_a","name":"Read","arguments":"{}"},
		{"type":"custom_tool_call","call_id":"g1_b","name":"view_image","input":"g1.png"},
		{"type":"function_call_output","call_id":"g1_a","output":[{"type":"input_text","text":"g1 A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
		{"type":"custom_tool_call_output","call_id":"g1_b","output":[{"type":"input_image","image_url":"data:image/png;base64,AQ=="}]},
		{"role":"user","content":[{"type":"input_text","text":"next group"}]},
		{"type":"function_call","call_id":"g2_a","name":"Read","arguments":"{}"},
		{"type":"function_call","call_id":"g2_b","name":"Read","arguments":"{}"},
		{"type":"function_call_output","call_id":"g2_a","output":[{"type":"input_image","image_url":"data:image/png;base64,Ag=="}]},
		{"type":"function_call_output","call_id":"g2_b","output":[{"type":"input_image","image_url":"data:image/png;base64,Aw=="}]},
		{"role":"user","content":[{"type":"input_text","text":"done"}]}
	]}`)
	out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
	if err != nil {
		t.Fatal(err)
	}
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[g1_a,g1_b]", "tool(g1_a)", "tool(g1_b)", "user", "user", "user",
		"assistant[g2_a,g2_b]", "tool(g2_a)", "tool(g2_b)", "user", "user", "user",
	})
	for path, want := range map[string]string{
		"messages.3.content.1.image_url.url":  "data:image/png;base64,AA==",
		"messages.4.content.1.image_url.url":  "data:image/png;base64,AQ==",
		"messages.5.content.0.text":           "next group",
		"messages.9.content.1.image_url.url":  "data:image/png;base64,Ag==",
		"messages.10.content.1.image_url.url": "data:image/png;base64,Aw==",
		"messages.11.content.0.text":          "done",
	} {
		if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
			t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
		}
	}
}

func TestResponsesInterleavedMessagesInParallelGroupPreserveImageOrder(t *testing.T) {
	raw := []byte(`{"input":[
		{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
		{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"first result"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"interleaved user note"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"interleaved assistant note"}]},
		{"type":"function_call_output","call_id":"call_b","output":[{"type":"input_text","text":"second result"},{"type":"input_image","image_url":"data:image/png;base64,AQ=="}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"final question"}]}
	]}`)
	out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
	if err != nil {
		t.Fatal(err)
	}
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"user",
		"user",
		"user",
		"assistant(text)",
		"user",
	})
	for path, want := range map[string]string{
		"messages.3.content.1.image_url.url": "data:image/png;base64,AA==",
		"messages.4.content.1.image_url.url": "data:image/png;base64,AQ==",
		"messages.5.content.0.text":          "interleaved user note",
		"messages.6.content.0.text":          "interleaved assistant note",
		"messages.7.content.0.text":          "final question",
	} {
		if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
			t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
		}
	}
}

func TestResponsesMissingToolOutputFlushesMediaBeforeNextTurn(t *testing.T) {
	t.Run("followed by user message", func(t *testing.T) {
		raw := []byte(`{"input":[
			{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"only A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue without B"}]}
		]}`)
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
		if err != nil {
			t.Fatal(err)
		}
		assertToolTurnMessageShapesForTest(t, out, []string{
			"assistant[call_a,call_b]",
			"tool(call_a)",
			"user",
			"user",
		})
		for path, want := range map[string]string{
			"messages.2.content.1.image_url.url": "data:image/png;base64,AA==",
			"messages.3.content.0.text":          "continue without B",
		} {
			if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
				t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
			}
		}
	})

	t.Run("followed by next tool turn", func(t *testing.T) {
		raw := []byte(`{"input":[
			{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"only A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"function_call","call_id":"call_c","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_c","output":[{"type":"input_text","text":"result C"},{"type":"input_image","image_url":"data:image/png;base64,AQ=="}]}
		]}`)
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
		if err != nil {
			t.Fatal(err)
		}
		assertToolTurnMessageShapesForTest(t, out, []string{
			"assistant[call_a,call_b]",
			"tool(call_a)",
			"user",
			"assistant[call_c]",
			"tool(call_c)",
			"user",
		})
		for path, want := range map[string]string{
			"messages.2.content.1.image_url.url": "data:image/png;base64,AA==",
			"messages.5.content.1.image_url.url": "data:image/png;base64,AQ==",
		} {
			if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
				t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
			}
		}
	})
}

func TestResponsesMissingToolOutputFollowedByOrphanOrDuplicateOutputFlushesMediaFirst(t *testing.T) {
	t.Run("orphan function_call_output", func(t *testing.T) {
		raw := []byte(`{"input":[
			{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"only A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"function_call_output","call_id":"call_orphan","output":"orphan function output"}
		]}`)
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
		if err != nil {
			t.Fatal(err)
		}
		assertToolTurnMessageShapesForTest(t, out, []string{
			"assistant[call_a,call_b]",
			"tool(call_a)",
			"user",
			"user",
		})
		for path, want := range map[string]string{
			"messages.2.content.1.image_url.url": "data:image/png;base64,AA==",
			"messages.3.content":                 "orphan function output",
		} {
			if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
				t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
			}
		}
	})

	t.Run("orphan custom_tool_call_output", func(t *testing.T) {
		raw := []byte(`{"input":[
			{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
			{"type":"custom_tool_call","call_id":"call_b","name":"view_image","input":"b.png"},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"only A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"custom_tool_call_output","call_id":"call_orphan","output":"orphan custom output"}
		]}`)
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
		if err != nil {
			t.Fatal(err)
		}
		assertToolTurnMessageShapesForTest(t, out, []string{
			"assistant[call_a,call_b]",
			"tool(call_a)",
			"user",
			"user",
		})
		for path, want := range map[string]string{
			"messages.2.content.1.image_url.url": "data:image/png;base64,AA==",
			"messages.3.content":                 "orphan custom output",
		} {
			if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
				t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
			}
		}
	})

	t.Run("duplicate call_id output after missing sibling", func(t *testing.T) {
		raw := []byte(`{"input":[
			{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"only A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"function_call_output","call_id":"call_a","output":"duplicate A output"}
		]}`)
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
		if err != nil {
			t.Fatal(err)
		}
		assertToolTurnMessageShapesForTest(t, out, []string{
			"assistant[call_a,call_b]",
			"tool(call_a)",
			"user",
			"user",
		})
		for path, want := range map[string]string{
			"messages.2.content.1.image_url.url": "data:image/png;base64,AA==",
			"messages.3.content":                 "duplicate A output",
		} {
			if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
				t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
			}
		}
	})
}

func TestResponsesCompleteParallelGroupPreservedDespiteLaterDuplicateCallID(t *testing.T) {
	raw := []byte(`{"input":[
		{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},
		{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"A"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
		{"type":"function_call_output","call_id":"call_b","output":[{"type":"input_text","text":"B"},{"type":"input_image","image_url":"data:image/png;base64,AQ=="}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next turn"}]},
		{"type":"function_call","call_id":"dup_id","name":"Read","arguments":"{}"},
		{"type":"function_call_output","call_id":"dup_id","output":"first dup"},
		{"type":"function_call_output","call_id":"dup_id","output":"second dup"}
	]}`)
	out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gemini-4-argon", raw, true)
	if err != nil {
		t.Fatal(err)
	}
	assertToolTurnMessageShapesForTest(t, out, []string{
		"assistant[call_a,call_b]",
		"tool(call_a)",
		"tool(call_b)",
		"user",
		"user",
		"user",
		"assistant[dup_id]",
		"tool(dup_id)",
		"user",
	})
	for path, want := range map[string]string{
		"messages.3.content.1.image_url.url": "data:image/png;base64,AA==",
		"messages.4.content.1.image_url.url": "data:image/png;base64,AQ==",
		"messages.5.content.0.text":          "next turn",
		"messages.8.content":                 "second dup",
	} {
		if got := ojson.GetBytesNoCopy(out, path).String(); got != want {
			t.Fatalf("%s = %q, want %q; output=%s", path, got, want, out)
		}
	}
}
