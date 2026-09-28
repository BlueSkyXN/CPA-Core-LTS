package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeResponsesRequestBasicControls(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude("fixture", []byte(`{"input":"hello","temperature":0.2,"top_p":0.7,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"parallel_tool_calls":false,"tool_choice":"none"}`), false)
	if gjson.GetBytes(out, "messages.0.content").String() != "hello" {
		t.Error("string input lost")
	}
	if gjson.GetBytes(out, "temperature").Float() != 0.2 || gjson.GetBytes(out, "top_p").Float() != 0.7 {
		t.Error("sampling controls lost")
	}
	if gjson.GetBytes(out, "tool_choice.type").String() != "none" {
		t.Error("none must not become auto")
	}
	out = ConvertOpenAIResponsesRequestToClaude("fixture", []byte(`{"input":[{"role":"user","content":"hello"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"parallel_tool_calls":false}`), false)
	if !gjson.GetBytes(out, "tool_choice.disable_parallel_tool_use").Bool() || gjson.GetBytes(out, "tool_choice.type").String() != "auto" {
		t.Error("serial tool intent lost")
	}
}
