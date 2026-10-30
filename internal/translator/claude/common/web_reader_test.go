package claudecommon

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPluginWebReaderResultValidation(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"page", `[{"url":"https://example.com/doc","content":"Page body","title":"Doc"}]`, true},
		{"empty", `[]`, true},
		{"invalid JSON", `[{`, false},
		{"wrong container", `{"url":"https://example.com/doc","content":"Page body"}`, false},
		{"missing URL", `[{"content":"Page body"}]`, false},
		{"missing content", `[{"url":"https://example.com/doc"}]`, false},
		{"non-string content", `[{"url":"https://example.com/doc","content":{}}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := fmt.Sprintf(`{"type":"message","id":"reader","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"call_reader","name":"webReader","input":{"url":"https://example.com/doc"}},{"type":"text","text":"Output:"},{"type":"tool_result","tool_use_id":"call_reader","content":%q}]}`, tc.content)
			if err := ValidatePluginMessage(gjson.Parse(message), false); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
		})
	}
}

func TestPluginWebReaderPairing(t *testing.T) {
	use := `{"type":"server_tool_use","id":"call_reader","name":"webReader","input":{"url":"https://example.com/doc"}}`
	result := `{"type":"tool_result","tool_use_id":"call_reader","content":"[{\"url\":\"https://example.com/doc\",\"content\":\"Page body\"}]"}`
	for _, tc := range []struct {
		name, blocks string
		valid        bool
	}{
		{"paired", use + "," + result, true},
		{"orphan", result, false},
		{"duplicate result", use + "," + result + "," + result, false},
		{"duplicate call", use + "," + use, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := gjson.Parse(`{"type":"message","id":"reader","role":"assistant","stop_reason":"end_turn","content":[` + tc.blocks + `]}`)
			if err := ValidatePluginMessage(msg, false); (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if err := ValidatePluginMessage(msg, true); err == nil {
				t.Fatal("unsupported Chat path accepted server reader")
			}
		})
	}
}
