package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type promptConfig struct {
	Mode                 string            `json:"mode"`
	Template             string            `json:"template"`
	MovePosition         string            `json:"move_position"`
	AllowRequestOverride bool              `json:"allow_request_override"`
	Templates            map[string]string `json:"templates"`
}

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
func validatePrompt(p promptConfig) error {
	switch p.Mode {
	case "preserve", "replace", "prepend", "append", "move_to_user":
	default:
		return problem(400, "invalid_prompt", "Unknown prompt mode")
	}
	if p.MovePosition != "" && p.MovePosition != "first_user" && p.MovePosition != "last_user" {
		return problem(400, "invalid_prompt", "Invalid move_position")
	}
	if p.Mode != "preserve" && strings.TrimSpace(p.Templates[p.Template]) == "" {
		return problem(400, "invalid_prompt", "A configured non-empty template is required")
	}
	return nil
}
func applyPrompt(body map[string]any, p promptConfig) error {
	if p.Mode == "" {
		p.Mode = "preserve"
	}
	if ext, exists := body["x_coding_plan"]; exists {
		if !p.AllowRequestOverride {
			return problem(400, "prompt_override_disabled", "Request prompt override is disabled")
		}
		o, ok := ext.(map[string]any)
		if !ok || len(o) != 1 {
			return problem(400, "invalid_prompt", "Invalid x_coding_plan extension")
		}
		sel, ok := o["prompt"].(map[string]any)
		if !ok {
			return problem(400, "invalid_prompt", "Invalid prompt override")
		}
		for k, v := range sel {
			s, ok := v.(string)
			if !ok {
				return problem(400, "invalid_prompt", "Invalid prompt override value")
			}
			switch k {
			case "mode":
				p.Mode = s
			case "template":
				// 即使 preserve 不读取模板，也不能接受未登记路径或 URL 作为模板选择。
				if _, exists := p.Templates[s]; !exists {
					return problem(400, "invalid_prompt", "Select a configured template name")
				}
				p.Template = s
			case "move_position":
				p.MovePosition = s
			default:
				return problem(400, "invalid_prompt", "Unknown prompt override field")
			}
		}
	}
	delete(body, "x_coding_plan")
	if err := validatePrompt(p); err != nil {
		return err
	}
	original, err := textBlocks(body["system"])
	if err != nil {
		return err
	}
	if p.Mode == "preserve" {
		return nil
	}
	template := map[string]any{"type": "text", "text": p.Templates[p.Template]}
	switch p.Mode {
	case "prepend":
		body["system"] = append([]any{template}, original...)
		return nil
	case "append":
		body["system"] = append(original, template)
		return nil
	}
	body["system"] = []any{template}
	if p.Mode == "replace" || len(original) == 0 {
		return nil
	}
	texts := []string{}
	for _, v := range original {
		b := v.(map[string]any)
		for k := range b {
			if k != "type" && k != "text" && k != "cache_control" {
				return problem(400, "invalid_system", "Cannot move system blocks with additional fields")
			}
		}
		texts = append(texts, b["text"].(string))
	}
	text := strings.Join(texts, "\n\n")
	if text == "" {
		return nil
	}
	messages := body["messages"].([]any)
	index := -1
	for i, v := range messages {
		if v.(map[string]any)["role"] == "user" {
			index = i
			if p.MovePosition == "first_user" {
				break
			}
		}
	}
	if index < 0 {
		return problem(400, "invalid_system", "Moving system requires a user message")
	}
	m := messages[index].(map[string]any)
	var content []any
	if s, ok := m["content"].(string); ok {
		content = []any{map[string]any{"type": "text", "text": s}}
	} else {
		content = m["content"].([]any)
	}
	m["content"] = append([]any{map[string]any{"type": "text", "text": "Caller context (moved from system; user-level):\n" + text}}, content...)
	return nil
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
			case "text", "thinking", "redacted_thinking", "tool_use", "tool_result":
			default:
				return nil, problem(400, "unsupported_content", "Unsupported content type")
			}
		}
	}
	if v, exists := b["tools"]; exists {
		list, ok := v.([]any)
		if !ok {
			return nil, problem(400, "unsupported_tools", "Invalid tools")
		}
		for _, v := range list {
			tool, ok := v.(map[string]any)
			if !ok {
				return nil, problem(400, "unsupported_tools", "Invalid tool")
			}
			_, name := tool["name"].(string)
			_, schema := tool["input_schema"].(map[string]any)
			if !name || !schema {
				return nil, problem(400, "unsupported_tools", "Only custom tools are supported")
			}
		}
	}
	if err := applyPrompt(b, c.Prompt); err != nil {
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
