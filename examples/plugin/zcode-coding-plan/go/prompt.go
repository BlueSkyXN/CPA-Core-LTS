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
		if !ok || (m["role"] != "user" && m["role"] != "assistant") {
			return nil, problem(400, "invalid_request", "Unsupported message role")
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
			case "server_tool_use", "web_search_tool_result", "web_fetch_tool_result":
				return nil, problem(400, "unsupported_content", "Provider-native tool history is not supported by Coding Plan; use client-executed function or MCP tools and start a new conversation without server-tool history")
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
	return b, nil
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
			if typ != "custom" {
				message := "Provider-native tools are not supported by Coding Plan; use client-executed function or MCP tools with name and input_schema"
				if strings.HasPrefix(typ, "web_search_") {
					message = "Provider-native web_search is not supported by Coding Plan; disable provider-native WebSearch for this model and use a client-executed function or MCP search tool"
				}
				return problem(400, "unsupported_tools", fmt.Sprintf("tools[%d]: %s", i, message))
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
