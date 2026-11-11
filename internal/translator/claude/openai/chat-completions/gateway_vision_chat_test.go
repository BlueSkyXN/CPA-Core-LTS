package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// 网关识图（4_5v_mcp）配对在 Chat 出站的端到端形状：校验层放行合法配对后，
// 翻译层跳过服务端块，最终回答照常输出（评审 I2 回归）。
func TestGatewayVisionPairConvertsToChatResponse(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_v\",\"model\":\"glm-5.3\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"mcp_tool_use\",\"id\":\"call_v\",\"name\":\"analyze_image\",\"input\":{\"imageSource\":\"https://glm-file.bigmodel.cn/ccproxy/file-input/abc?Signature=x\",\"prompt\":\"Describe\"},\"server_name\":\"4_5v_mcp\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"mcp_tool_result\",\"tool_use_id\":\"call_v\",\"content\":[{\"type\":\"text\",\"text\":\"Red square top-left.\"}],\"is_error\":false}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"It is a red square.\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":2}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n" +
		"data: {\"type\":\"message_stop\"}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "glm-5.3", nil, nil, rawJSON, nil)
	if !strings.Contains(string(out), "It is a red square.") {
		t.Fatal("final answer lost in chat conversion", string(out))
	}
	if strings.Contains(string(out), "mcp_tool_use") || strings.Contains(string(out), "analyze_image") {
		t.Fatal("gateway vision pair leaked into chat response", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatal("finish_reason wrong:", got)
	}
}
