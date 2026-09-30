package chat_completions

import (
	"context"
	"fmt"
	"strings"
	"testing"

	claudecommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/common"
	"github.com/tidwall/gjson"
)

func TestChatMessageJSONAndSeededStream(t *testing.T) {
	blocks := []string{`{"type":"thinking","thinking":"公开思考","signature":"opaque-only"}`, `{"type":"redacted_thinking","data":"hidden-only"}`, `{"type":"text","text":"answer"}`, `{"type":"tool_use","id":"call-1","name":"lookup","input":{"q":"hello"}}`}
	usage := `{"input_tokens":3,"cache_read_input_tokens":5,"cache_creation_input_tokens":2,"output_tokens":7}`
	message := fmt.Sprintf(`{"type":"message","id":"fixture","role":"assistant","model":"glm-fixture","content":[%s],"stop_reason":"tool_use","usage":%s}`, strings.Join(blocks, ","), usage)
	events := []string{`{"type":"message_start","message":{"type":"message","id":"fixture","role":"assistant","model":"glm-fixture","content":[]}}`}
	for i, block := range blocks {
		events = append(events, fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, i, block), fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
	}
	events = append(events, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":`+usage+`}`, `{"type":"message_stop"}`)
	sse := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
	for _, body := range []string{message, sse} {
		state := &claudecommon.PluginResponseState{Chat: true}
		var param any = state
		out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "glm-fixture", nil, nil, []byte(body), &param)
		if state.Err != nil || gjson.GetBytes(out, "choices.0.message.reasoning_content").String() != "公开思考" || gjson.GetBytes(out, "choices.0.message.content").String() != "answer" || gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String() != `{"q":"hello"}` || gjson.GetBytes(out, "usage.prompt_tokens").Int() != 10 || gjson.GetBytes(out, "usage.total_tokens").Int() != 17 {
			t.Fatalf("invalid conversion: %s, %v", out, state.Err)
		}
		if strings.Contains(string(out), "hidden-only") || strings.Contains(string(out), "opaque-only") {
			t.Fatal("opaque payload became visible")
		}
	}
	state := &claudecommon.PluginResponseState{Chat: true}
	var param any = state
	var thought, text, args string
	var usageCount int
	for _, event := range events {
		for _, frame := range ConvertClaudeResponseToOpenAI(context.Background(), "glm-fixture", nil, nil, []byte("data: "+event), &param) {
			thought += gjson.GetBytes(frame, "choices.0.delta.reasoning_content").String()
			text += gjson.GetBytes(frame, "choices.0.delta.content").String()
			args += gjson.GetBytes(frame, "choices.0.delta.tool_calls.0.function.arguments").String()
			if gjson.GetBytes(frame, "choices.#").Int() == 0 && gjson.GetBytes(frame, "usage.total_tokens").Int() == 17 {
				usageCount++
			}
		}
	}
	if state.Err != nil || !state.Terminal || thought != "公开思考" || text != "answer" || args != `{"q":"hello"}` || usageCount != 1 {
		t.Fatalf("stream mismatch: %q %q %q %d %v", thought, text, args, usageCount, state.Err)
	}
}

func TestChatMessageJSONRejectsMalformed(t *testing.T) {
	for _, body := range []string{`broken`, `{}`, `{"type":"error","error":{"message":"synthetic"}}`, `{"type":"message","id":"m","role":"assistant","content":[{"type":"thinking"}],"stop_reason":"end_turn"}`, `{"type":"message","id":"m","role":"assistant","content":[],"stop_reason":"unknown"}`, `data: broken`, `data: {"type":"message_start"}`} {
		state := &claudecommon.PluginResponseState{Chat: true}
		var param any = state
		if out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "m", nil, nil, []byte(body), &param); out != nil || state.Err == nil {
			t.Fatalf("malformed response accepted: %s", out)
		}
	}
	for stop, finish := range map[string]string{"end_turn": "stop", "max_tokens": "length", "refusal": "content_filter", "sensitive": "content_filter"} {
		var param any
		out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "m", nil, nil, []byte(fmt.Sprintf(`{"type":"message","id":"m","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":%q}`, stop)), &param)
		if gjson.GetBytes(out, "choices.0.finish_reason").String() != finish || gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists() {
			t.Fatalf("stop mapping failed: %s", out)
		}
	}
}
