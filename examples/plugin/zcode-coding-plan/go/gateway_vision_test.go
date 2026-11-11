package main

import (
	"strings"
	"testing"
)

// gatewayVisionFixture 是探测捕获的 mcp_tool_use/mcp_tool_result 历史形状
// （local/2026-11-10-glm-server-side-vision-mcp-discovery.md 第 3 节）。
const gatewayVisionCall = `{"type":"mcp_tool_use","id":"call_93643d8af7f242c380b4a34e","name":"analyze_image","input":{"imageSource":"https://glm-file.bigmodel.cn/ccproxy/file-input/abc-3576?OSSAccessKeyId=x&Expires=1794332302&Signature=y","prompt":"Describe this image"},"server_name":"4_5v_mcp"}`

const gatewayVisionResult = `{"type":"mcp_tool_result","tool_use_id":"call_93643d8af7f242c380b4a34e","content":[{"type":"text","text":"# Detailed Image Description\n\nRed square top-left, blue triangle top-right."}],"is_error":false}`

func decodeBlock(t *testing.T, raw string) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := decode([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGatewayVisionDeclarationInjection(t *testing.T) {
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": imageFixture}}

	// glm-5.3 + 图片 → 代注声明。
	body := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": []any{image, map[string]any{"type": "text", "text": "what is this"}}},
	}}
	result, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatal("declaration not injected")
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "image_analysis" || tool["name"] != "image_analysis" {
		t.Fatal("wrong declaration shape", tool)
	}
	if _, hasSchema := tool["input_schema"]; hasSchema {
		t.Fatal("declaration must not carry input_schema")
	}

	// 已有声明（客户端自带）→ 不重复注入。
	body["tools"] = []any{map[string]any{"type": "image_analysis", "name": "image_analysis"}}
	result, err = transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result["tools"].([]any)); got != 1 {
		t.Fatal("duplicate injection", got)
	}

	// 无图片 → 不注入。
	textOnly := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
	}}
	result, err = transformExecution(executorRequest{Payload: encode(textOnly)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if tools, exists := result["tools"]; exists && len(tools.([]any)) != 0 {
		t.Fatal("text-only request must not receive declaration")
	}

	// tool_result 内图片同样触发注入（配一个客户端 tool_use 以满足历史校验）。
	result, err = transformExecution(executorRequest{Payload: encode(map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call_x", "name": "screenshot", "input": map[string]any{"path": "/tmp/x.png"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_x", "content": []any{image}}}},
	}})}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(result["tools"].([]any)) != 1 {
		t.Fatal("tool_result image must trigger injection")
	}
}

func TestGatewayVisionToolsValidation(t *testing.T) {
	// 带声明的请求通过校验（含既有客户端工具共存）。
	body := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
	}, "tools": []any{
		map[string]any{"type": "image_analysis", "name": "image_analysis"},
		map[string]any{"name": "get_weather", "input_schema": map[string]any{"type": "object"}},
	}}
	if err := validateTools(body); err != nil {
		t.Fatal("mixed tools with image_analysis must pass", err)
	}

	// 声明缺 name → 拒绝。
	bad := map[string]any{"messages": []any{}, "tools": []any{map[string]any{"type": "image_analysis"}}}
	if err := validateTools(bad); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatal("image_analysis without name must be rejected", err)
	}
}

func TestGatewayVisionHistoryFolding(t *testing.T) {
	call := decodeBlock(t, gatewayVisionCall)
	result := decodeBlock(t, gatewayVisionResult)

	// 历史含配对：折叠为 text，请求可正常发出（无 mcp 块残留）。
	body := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://images.example.invalid/a.png"}}, map[string]any{"type": "text", "text": "describe"}}},
		map[string]any{"role": "assistant", "content": []any{call, result, map[string]any{"type": "text", "text": "It is a red square."}}},
		map[string]any{"role": "user", "content": "and now?"},
	}}
	foldGatewayVisionHistory(body)
	encoded, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal("folded history must pass validation", err)
	}
	if raw := string(encode(encoded)); strings.Contains(raw, "mcp_tool_use") || strings.Contains(raw, "mcp_tool_result") {
		t.Fatal("mcp blocks must be folded away")
	}
	if !strings.Contains(string(encode(encoded)), "Red square top-left") {
		t.Fatal("analysis text must be retained")
	}
	// 折叠后仍带图片 → 新一轮代注照常发生。
	if len(encoded["tools"].([]any)) != 1 {
		t.Fatal("folding must not suppress new-turn injection")
	}

	// 孤立 mcp_tool_use（无配对结果）→ transform 显式拒绝，不静默丢块。
	orphan := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": []any{call}},
		map[string]any{"role": "user", "content": "go on"},
	}}
	if _, err := transformExecution(executorRequest{Payload: encode(orphan)}, defaultConfig(), "session"); err == nil || !strings.Contains(err.Error(), "Gateway vision history") {
		t.Fatal("orphan mcp_tool_use must be rejected explicitly", err)
	}
}

func TestGatewayVisionHistoryFoldingPreservesStringContent(t *testing.T) {
	call := decodeBlock(t, gatewayVisionCall)
	result := decodeBlock(t, gatewayVisionResult)

	// 一组有效配对 + 字符串 content 消息（含用户最新追问）必须原样保留，
	// 不得被折叠改写成空数组（评审 I1 回归）。
	body := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": "describe previous image"},
		map[string]any{"role": "assistant", "content": []any{call, result, map[string]any{"type": "text", "text": "It is a red square."}}},
		map[string]any{"role": "user", "content": "and now?"},
	}}
	foldGatewayVisionHistory(body)
	messages := body["messages"].([]any)
	if got := messages[0].(map[string]any)["content"]; got != "describe previous image" {
		t.Fatalf("string content rewritten: %v", got)
	}
	if got := messages[2].(map[string]any)["content"]; got != "and now?" {
		t.Fatalf("latest user question rewritten: %v", got)
	}
	encoded, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal("folded history with string messages must pass", err)
	}
	for _, v := range encoded["messages"].([]any) {
		content := v.(map[string]any)["content"]
		if content == "describe previous image" || content == "and now?" {
			continue
		}
		if _, ok := content.(string); ok {
			t.Fatal("unexpected string content mutation")
		}
	}
}

func TestGatewayVisionHistoryFoldingRejectsOrphanWithValidPairPresent(t *testing.T) {
	call := decodeBlock(t, gatewayVisionCall)
	result := decodeBlock(t, gatewayVisionResult)
	orphanCall := decodeBlock(t, `{"type":"mcp_tool_use","id":"call_unpaired","name":"analyze_image","input":{"imageSource":"x","prompt":"y"},"server_name":"4_5v_mcp"}`)

	// 一组有效配对 + 另一个 ID 的孤立调用：孤立块保留原样，transform 必须拒绝
	// （评审 I8 回归——此前实现把孤立调用也折叠放行）。
	body := map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "assistant", "content": []any{call, result, orphanCall}},
		map[string]any{"role": "user", "content": "go on"},
	}}
	foldGatewayVisionHistory(body)
	assistant := body["messages"].([]any)[0].(map[string]any)
	folded := false
	for _, w := range assistant["content"].([]any) {
		if block, ok := w.(map[string]any); ok && block["type"] == "mcp_tool_use" {
			folded = true
		}
	}
	if !folded {
		t.Fatal("orphan mcp_tool_use was folded away instead of preserved for rejection")
	}
	if _, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session"); err == nil || !strings.Contains(err.Error(), "Gateway vision history") {
		t.Fatal("orphan call alongside a valid pair must still be rejected", err)
	}
}
