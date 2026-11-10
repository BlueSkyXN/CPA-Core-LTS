package claudecommon

import (
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// 网关识图（4_5v_mcp）回包配对校验，契约见仓库
// local/2026-11-10-glm-server-side-vision-mcp-discovery.md 第 3 节。
func TestPluginGatewayVisionPairing(t *testing.T) {
	use := `{"type":"mcp_tool_use","id":"call_vision","name":"analyze_image","input":{"imageSource":"https://glm-file.bigmodel.cn/ccproxy/file-input/abc?Signature=x","prompt":"Describe"},"server_name":"4_5v_mcp"}`
	result := `{"type":"mcp_tool_result","tool_use_id":"call_vision","content":[{"type":"text","text":"Red square top-left."}],"is_error":false}`
	searchUse := `{"type":"server_tool_use","id":"call_search","name":"web_search_prime","input":{"search_query":"q"}}`
	for _, tc := range []struct {
		name, blocks string
		valid        bool
	}{
		{"paired", use + "," + result, true},
		{"orphan result", result, false},
		// 孤立调用与 server_tool_use 基线一致地放行：配对只在结果侧强制
		// （见 TestPluginServerToolOrphanCallBaseline），流式可能跨消息分块。
		{"orphan call follows server_tool baseline", use, true},
		{"duplicate result", use + "," + result + "," + result, false},
		{"duplicate call", use + "," + use, false},
		{"mcp result must not satisfy server_tool_use", searchUse + "," + result, false},
		{"mixed pairs", use + "," + result + "," + searchUse, true}, // 孤立 search 调用同基线放行
		{"non-array content", `{"type":"mcp_tool_result","tool_use_id":"call_vision","content":"Red square."}`, false},
		{"non-text part", `{"type":"mcp_tool_result","tool_use_id":"call_vision","content":[{"type":"image","source":{"type":"base64"}}]}`, false},
		{"empty content array", `{"type":"mcp_tool_result","tool_use_id":"call_vision","content":[]}`, false},
		{"missing id", `{"type":"mcp_tool_use","name":"analyze_image"}`, false},
		{"missing name", `{"type":"mcp_tool_use","id":"call_vision"}`, false},
		{"scalar input", `{"type":"mcp_tool_use","id":"call_vision","name":"analyze_image","input":"x"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := fmt.Sprintf(`{"type":"message","id":"vision","role":"assistant","stop_reason":"end_turn","content":[%s]}`, tc.blocks)
			if err := ValidatePluginMessage(ojson.Parse(message), false); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			// Chat 模式一律拒绝服务端块（与 server_tool_use 同边界）。
			if err := ValidatePluginMessage(ojson.Parse(message), true); tc.valid && err == nil {
				t.Fatal("chat mode must reject gateway vision blocks")
			}
		})
	}
}

func TestPluginServerToolOrphanCallBaseline(t *testing.T) {
	// 基线确认：孤立 server_tool_use（无配对结果）在现有校验下同样放行——
	// 配对只在结果侧强制。网关识图保持同一边界，不收紧也不放松。
	msg := ojson.Parse(`{"type":"message","id":"x","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"call_s","name":"web_search_prime","input":{"search_query":"q"}}]}`)
	if err := ValidatePluginMessage(msg, false); err != nil {
		t.Fatal("baseline shifted: orphan server_tool_use was accepted before", err)
	}
}
