package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return formatUUID(b[:])
}
func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func sessionID(account, caller, workspace, conversation string) string {
	if conversation == "" {
		return uuid()
	}
	h := sha256.Sum256(encode([]string{account, caller, workspace, conversation}))
	h[6] = (h[6] & 15) | 128
	h[8] = (h[8] & 63) | 128
	return formatUUID(h[:16])
}
func textBlocks(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	if s, ok := value.(string); ok {
		if s == "" {
			return []any{}, nil
		}
		return []any{map[string]any{"type": "text", "text": s}}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, problem(400, "invalid_system", "system must be text or text blocks")
	}
	for _, v := range list {
		b, ok := v.(map[string]any)
		if !ok || b["type"] != "text" {
			return nil, problem(400, "invalid_system", "system must contain text blocks")
		}
		if _, ok := b["text"].(string); !ok {
			return nil, problem(400, "invalid_system", "system text is invalid")
		}
	}
	return list, nil
}
func transform(raw []byte, c *config, session string) (map[string]any, error) {
	var b map[string]any
	if err := decode(raw, &b); err != nil || b == nil {
		return nil, problem(400, "invalid_request", "Request must be a JSON object")
	}
	if len(raw) > maxBody {
		return nil, problem(413, "request_too_large", "Request exceeds size limit")
	}
	if value, present := b["system"]; present && value == nil {
		return nil, problem(400, "invalid_system", "system must not be null")
	}
	model, ok := b["model"].(string)
	allowed := false
	for _, m := range c.Models {
		if model == m {
			allowed = true
		}
	}
	if !ok || !allowed {
		return nil, problem(400, "model_not_found", "Model is not in the configured allowlist")
	}
	n, ok := b["max_tokens"].(float64)
	if !ok || n <= 0 || n > 9007199254740991 || n != float64(int64(n)) {
		return nil, problem(400, "invalid_request", "max_tokens must be a positive integer")
	}
	if value, exists := b["stream"]; exists {
		if _, ok := value.(bool); !ok {
			return nil, problem(400, "invalid_request", "stream must be boolean")
		}
	}
	messages, ok := b["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, problem(400, "invalid_request", "messages must be non-empty")
	}
	for _, v := range messages {
		m, ok := v.(map[string]any)
		if !ok || (m["role"] != "user" && m["role"] != "assistant" && m["role"] != "system") {
			return nil, problem(400, "invalid_request", "Unsupported message role")
		}
		if m["role"] == "system" {
			if err := validateSystemMessageContent(m["content"]); err != nil {
				return nil, err
			}
			continue
		}
		if _, ok := m["content"].(string); ok {
			continue
		}
		list, ok := m["content"].([]any)
		if !ok {
			return nil, problem(400, "unsupported_content", "Unsupported message content")
		}
		for _, v := range list {
			block, ok := v.(map[string]any)
			if !ok {
				return nil, problem(400, "unsupported_content", "Invalid block")
			}
			switch block["type"] {
			case "text", "thinking", "redacted_thinking", "tool_use":
			case "server_tool_use":
				name, _ := block["name"].(string)
				if name != "web_search" && name != "web_search_prime" && name != "webReader" {
					return nil, problem(400, "unsupported_content", "Unsupported server tool in Coding Plan history")
				}
			case "mcp_tool_use":
				// 历史里的网关识图调用：其 imageSource 是已过期的网关 OSS 签名 URL，
				// 不能原样回放；折叠为 text 快照（foldGatewayVisionHistory）。
				return nil, problem(400, "unsupported_content", "Gateway vision history must be folded to text before sending; expired gateway image URLs cannot be replayed")
			case "web_search_tool_result":
			case "web_fetch_tool_result":
				return nil, problem(400, "unsupported_content", "web_fetch history is not supported by Coding Plan")
			case "image":
				if m["role"] != "user" {
					return nil, problem(400, "unsupported_content", "Images require a user message")
				}
				if err := validateImage(block, model); err != nil {
					return nil, err
				}
			case "tool_result":
				if content, ok := block["content"].([]any); ok {
					for _, value := range content {
						part, ok := value.(map[string]any)
						if !ok {
							return nil, problem(400, "unsupported_content", "Invalid tool result content")
						}
						if part["type"] == "image" {
							if err := validateImage(part, model); err != nil {
								return nil, err
							}
						}
					}
				}
			default:
				return nil, problem(400, "unsupported_content", "Unsupported content type")
			}
		}
	}
	if err := validateTools(b); err != nil {
		return nil, err
	}
	if _, exists := b["x_coding_plan"]; exists {
		return nil, problem(400, "prompt_override_disabled", "Prompt override is no longer supported")
	}
	if _, err := textBlocks(b["system"]); err != nil {
		return nil, err
	}
	if err := validateCache(b); err != nil {
		return nil, err
	}
	metadata := map[string]any{}
	if v, exists := b["metadata"]; exists {
		var ok bool
		metadata, ok = v.(map[string]any)
		if !ok {
			return nil, problem(400, "invalid_request", "metadata must be an object")
		}
	}
	// 字符串字段顺序也是协议契约，不能用 map 的排序替代 Node 的插入顺序。
	wire := struct {
		Device  string `json:"device_id"`
		Account string `json:"account_uuid"`
		Session string `json:"session_id"`
	}{c.DeviceID, "", session}
	metadata["user_id"] = string(encode(wire))
	b["metadata"] = metadata
	if err := injectGatewayVisionDeclaration(b, model); err != nil {
		return nil, err
	}
	return b, nil
}

// 网关服务端识图契约（2026-11-10 真凭据探测，bigmodel anthropic 端点）：
// 声明 tools[type=image_analysis, name=image_analysis] 且请求带 system 或
// metadata 任一 → 网关挂载 4_5v_mcp，把模型自主发起的图片分析劫持为
// mcp_tool_use(analyze_image) 服务端代跑并以 mcp_tool_result 回填。zai 上游
// 假设同行为（未实测，翻车时收紧本函数的 upstream 条件即可降级）。
const (
	gatewayVisionToolType = "image_analysis"
	gatewayVisionToolName = "image_analysis"
)

// injectGatewayVisionDeclaration 对 glm-5.3 的图片请求代注网关识图声明。
// 触发后的转写由上游网关完成，插件不 fetch 图片、不本地转写、不增加模型调用。
func injectGatewayVisionDeclaration(b map[string]any, model string) error {
	if modelTemplateID(model) != "glm-5.3" {
		return nil
	}
	images := countImageBlocks(b)
	if images == 0 {
		return nil
	}
	tools, _ := b["tools"].([]any)
	for _, v := range tools {
		tool, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if tool["type"] == gatewayVisionToolType {
			return nil // 客户端已声明（或上一轮已注入），不重复。
		}
	}
	b["tools"] = append(tools, map[string]any{"type": gatewayVisionToolType, "name": gatewayVisionToolName})
	return nil
}

// countImageBlocks 统计消息与 tool_result 内容中的图片块数量。
func countImageBlocks(b map[string]any) int {
	count := 0
	messages, _ := b["messages"].([]any)
	for _, v := range messages {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, w := range blocks {
			block, ok := w.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "image" {
				count++
			}
			if block["type"] == "tool_result" {
				if content, ok := block["content"].([]any); ok {
					for _, x := range content {
						part, ok := x.(map[string]any)
						if ok && part["type"] == "image" {
							count++
						}
					}
				}
			}
		}
	}
	return count
}
func validateTools(body map[string]any) error {
	value, exists := body["tools"]
	if !exists {
		return nil
	}
	tools, ok := value.([]any)
	if !ok {
		return problem(400, "invalid_request", "tools must be an array")
	}
	for i, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			return problem(400, "invalid_request", fmt.Sprintf("tools[%d] must be an object", i))
		}
		if value, exists := tool["type"]; exists {
			typ, ok := value.(string)
			if !ok || strings.TrimSpace(typ) == "" {
				return problem(400, "invalid_request", fmt.Sprintf("tools[%d].type must be a non-empty string when present", i))
			}
			switch typ {
			case "custom":
			case "web_search_20250305", "web_search_20260209":
				// Native web search executes upstream; 2026-10-01 real-credential
				// probes confirmed both versions run and answer via web_search_prime
				// server_tool_use plus bare tool_result blocks. They carry no
				// client name/input_schema contract, so skip those checks.
				continue
			case gatewayVisionToolType:
				// 网关服务端识图（2026-11-10 探测实证）：上游要求声明带 name、无
				// input_schema；模型自主发起的图片分析会被网关劫持为 mcp_tool_use
				// 并由 4_5v_mcp 服务端代跑。插件对图片请求会代注同款声明。
				if _, ok := tool["name"].(string); !ok || strings.TrimSpace(tool["name"].(string)) == "" {
					return problem(400, "invalid_request", fmt.Sprintf("tools[%d].name must be a non-empty string for image_analysis", i))
				}
				continue
			default:
				return problem(400, "unsupported_tools", fmt.Sprintf("tools[%d]: Provider-native tools are not supported by Coding Plan; use client-executed function or MCP tools with name and input_schema", i))
			}
		}
		name, ok := tool["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return problem(400, "invalid_request", fmt.Sprintf("tools[%d].name must be a non-empty string", i))
		}
		if _, ok := tool["input_schema"].(map[string]any); !ok {
			return problem(400, "invalid_request", fmt.Sprintf("tools[%d].input_schema must be an object", i))
		}
	}
	return nil
}

func validateImage(block map[string]any, model string) error {
	if !builtinModelImageInput(model) {
		return problem(400, "unsupported_content", "This model does not support image input")
	}
	source, ok := block["source"].(map[string]any)
	if !ok {
		return problem(400, "invalid_request", "Image source must be an object")
	}
	switch source["type"] {
	case "base64":
		mime, _ := source["media_type"].(string)
		switch mime {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return problem(400, "unsupported_content", "Unsupported image media type")
		}
		data, _ := source["data"].(string)
		decoded, err := base64.StdEncoding.Strict().DecodeString(data)
		if err != nil || len(decoded) == 0 {
			return problem(400, "invalid_request", "Image data must be valid base64")
		}
	case "url":
		value, _ := source["url"].(string)
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || strings.ContainsAny(value, "\r\n\x00") {
			return problem(400, "invalid_request", "Image URL must be an HTTP or HTTPS URL without credentials")
		}
	default:
		return problem(400, "unsupported_content", "Unsupported image source")
	}
	return nil
}
func validateCache(body map[string]any) error {
	blocks, _ := textBlocks(body["system"])
	if tools, ok := body["tools"].([]any); ok {
		blocks = append(blocks, tools...)
	}
	for _, v := range body["messages"].([]any) {
		if list, ok := v.(map[string]any)["content"].([]any); ok {
			blocks = append(blocks, list...)
		}
	}
	count := 0
	for _, v := range blocks {
		b, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if value, exists := b["cache_control"]; exists {
			cc, ok := value.(map[string]any)
			if !ok || len(cc) != 1 || cc["type"] != "ephemeral" {
				return problem(400, "unsupported_cache", "Only default ephemeral cache_control is supported")
			}
			count++
		}
	}
	if count > 4 {
		return problem(400, "unsupported_cache", "At most four cache breakpoints are supported")
	}
	return nil
}
