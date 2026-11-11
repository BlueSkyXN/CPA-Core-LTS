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
		// end_turn 终态下孤立调用必须被拒（评审 II3）：Chat 出站会把它静默
		// 折叠成空成功响应。tool_use 终态的跨消息分块豁免见下方专项测试。
		{"orphan call at end_turn rejected", use, false},
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
			// Chat 模式对纯 mcp 对与 Messages 同边界（网关识图触发与入站协议无关）；
			// 混入 server_tool_use 的用例仍按 server 块既有 chat 边界拒绝。
			chatExpect := tc.valid
			if tc.name == "mixed pairs" || tc.name == "mcp result must not satisfy server_tool_use" {
				chatExpect = false
			}
			if err := ValidatePluginMessage(ojson.Parse(message), true); (err == nil) != chatExpect {
				t.Fatalf("chat valid=%t, err=%v", chatExpect, err)
			}
		})
	}
}

func TestPluginServerToolOrphanCallBaseline(t *testing.T) {
	// 基线确认：孤立 server_tool_use（无配对结果）维持既有放行语义——配对
	// 只在结果侧强制。网关识图按评审 II3 收紧为终态必消费（Chat 空成功问
	// 题），server_tool_use 家族不受影响。
	msg := ojson.Parse(`{"type":"message","id":"x","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"call_s","name":"web_search_prime","input":{"search_query":"q"}}]}`)
	if err := ValidatePluginMessage(msg, false); err != nil {
		t.Fatal("baseline shifted: orphan server_tool_use was accepted before", err)
	}
}

// 评审 II3：end_turn 终态的孤立 mcp_tool_use 在 Chat 出站会被折叠成空成功
// （{"role":"assistant","content":""}），必须 fail-loud。tool_use 终态豁免：
// 流式跨消息分块由下一消息补结果（与 server_tool_use 同一边界）。
func TestPluginGatewayVisionOrphanTerminalCheck(t *testing.T) {
	use := `{"type":"mcp_tool_use","id":"call_v","name":"analyze_image","input":{"imageSource":"https://glm-file.bigmodel.cn/x","prompt":"d"},"server_name":"4_5v_mcp"}`
	result := `{"type":"mcp_tool_result","tool_use_id":"call_v","content":[{"type":"text","text":"ok"}],"is_error":false}`
	for _, tc := range []struct {
		name, stop string
		blocks     string
		valid      bool
	}{
		{"orphan at end_turn", "end_turn", use, false},
		{"orphan at tool_use (split stream)", "tool_use", use, true},
		{"paired at end_turn", "end_turn", use + "," + result, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := fmt.Sprintf(`{"type":"message","id":"v","role":"assistant","stop_reason":%q,"content":[%s]}`, tc.stop, tc.blocks)
			for _, chat := range []bool{false, true} {
				if err := ValidatePluginMessage(ojson.Parse(message), chat); (err == nil) != tc.valid {
					t.Fatalf("chat=%v valid=%t, err=%v", chat, tc.valid, err)
				}
			}
		})
	}
}

// 流式路径同样的终态检查：孤立 mcp_tool_use 的流在 message_stop 失败。
func TestPluginGatewayVisionStreamOrphanFails(t *testing.T) {
	state := &PluginResponseState{}
	frames := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant"}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"mcp_tool_use","id":"call_v","name":"analyze_image","server_name":"4_5v_mcp"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	for i, f := range frames {
		if !state.Accept(f) && i < len(frames)-1 {
			t.Fatal("unexpected early failure:", state.Err)
		}
	}
	if state.Err == nil {
		t.Fatal("stream with unconsumed mcp_tool_use must fail at message_stop")
	}
	if state.Terminal {
		t.Fatal("failed stream must not be marked terminal")
	}
}
