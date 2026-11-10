package responses

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// 网关识图（4_5v_mcp）流式折叠：mcp_tool_use/mcp_tool_result 对不产生任何
// Responses item，最终 text 正常输出。契约见仓库
// local/2026-11-10-glm-server-side-vision-mcp-discovery.md 第 3 节。
func TestGatewayVisionBlocksFoldSilently(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_v","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"mcp_tool_use","id":"call_vision","name":"analyze_image","input":{"imageSource":"https://glm-file.bigmodel.cn/ccproxy/file-input/abc?Signature=x","prompt":"Describe"},"server_name":"4_5v_mcp"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"mcp_tool_result","tool_use_id":"call_vision","content":[{"type":"text","text":"Red square top-left."}],"is_error":false}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"It is a red square."}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	var completed ojson.Result
	for _, o := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		if ev, data := parseClaudeResponsesSSEEvent(t, o); ev == "response.completed" {
			completed = data
		}
	}
	if !completed.Exists() {
		t.Fatal("response.completed missing")
	}
	items := completed.Get("response.output").Array()
	// 期望仅 reasoning + message；网关识图工具对折叠为不可见。
	if len(items) != 2 {
		t.Fatalf("expected 2 folded output items, got %d: %s", len(items), completed.Get("response.output").Raw)
	}
	if items[0].Get("type").String() != "reasoning" || items[1].Get("type").String() != "message" {
		t.Fatal("unexpected item types", items[0].Get("type").String(), items[1].Get("type").String())
	}
	if items[1].Get("content.0.text").String() != "It is a red square." {
		t.Fatal("final text lost")
	}
}
