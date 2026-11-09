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
