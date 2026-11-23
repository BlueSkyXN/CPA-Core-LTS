package main

import (
	"encoding/json"
	"strings"
)

// 4 是 Copilot Chat 客户端补标记的预算，不是用户请求的校验上限。
const chatCacheMarkerBudget = 4

func addChatCacheBreakpoints(body map[string]any, messages []any, original []byte) {
	if implicitChatCache(body) {
		return
	}
	remaining := chatCacheMarkerBudget
	for _, value := range messages {
		msg, _ := value.(map[string]any)
		if _, exists := msg["copilot_cache_control"]; exists {
			remaining--
		}
	}
	if remaining <= 0 {
		return
	}
	mark := func(msg map[string]any) {
		if _, exists := msg["copilot_cache_control"]; exists || remaining == 0 || !cacheableChatContent(msg["content"]) {
			return
		}
		msg["copilot_cache_control"] = map[string]any{"type": "ephemeral"}
		remaining--
	}
	var source map[string]any
	if len(original) > 0 {
		if len(original) > maxExecutorBodyBytes || json.Unmarshal(original, &source) != nil || source == nil || implicitChatCache(source) {
			return
		}
		if input, ok := source["input"].([]any); ok {
			markResponsesToolBoundaries(input, messages, mark)
		}
	}

	// 预留稳定前缀和最新完整工具批次，两者不挤占对方的所有槽位。
	for _, value := range messages {
		msg, _ := value.(map[string]any)
		if msg["role"] != "system" && msg["role"] != "developer" {
			break
		}
		if cacheableChatContent(msg["content"]) {
			mark(msg)
			break
		}
	}
	for end := len(messages); end > 0 && remaining > 0; {
		msg, _ := messages[end-1].(map[string]any)
		if msg["role"] == "user" {
			mark(msg)
			break
		}
		if msg["role"] != "tool" {
			end--
			continue
		}
		start := end - 1
		for start > 0 {
			prev, _ := messages[start-1].(map[string]any)
			if prev["role"] != "tool" {
				break
			}
			start--
		}
		if start > 0 && completeChatToolBatch(messages[start-1:end]) {
			mark(msg)
		}
		end = start
	}
}

func implicitChatCache(body map[string]any) bool {
	options, _ := body["prompt_cache_options"].(map[string]any)
	return options["mode"] == "implicit"
}

func cacheableChatContent(content any) bool {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		for _, part := range value {
			block, _ := part.(map[string]any)
			switch block["type"] {
			case "text":
				if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
					return true
				}
			case "image_url", "input_audio", "file":
				return true
			}
		}
	}
	return false
}

func completeChatToolBatch(batch []any) bool {
	producer, _ := batch[0].(map[string]any)
	calls, _ := producer["tool_calls"].([]any)
	if producer["role"] != "assistant" || len(calls) != len(batch)-1 {
		return false
	}
	ids := make(map[string]bool, len(calls))
	for _, value := range calls {
		call, _ := value.(map[string]any)
		id, _ := call["id"].(string)
		if id == "" || ids[id] {
			return false
		}
		ids[id] = true
	}
	for _, value := range batch[1:] {
		msg, _ := value.(map[string]any)
		id, _ := msg["tool_call_id"].(string)
		if !ids[id] {
			return false
		}
		delete(ids, id)
	}
	return true
}

// Responses 的块内断点不能原样放进 Chat。只迁移 call_id 在两侧均唯一、且断点
// 正好位于工具输出末尾的请求；合并、拆分或重复 ID 的边界不按数组下标猜测。
func markResponsesToolBoundaries(input, messages []any, mark func(map[string]any)) {
	byID := make(map[string]map[string]any)
	for _, value := range messages {
		msg, _ := value.(map[string]any)
		id, _ := msg["tool_call_id"].(string)
		if msg["role"] == "tool" && id != "" {
			if _, seen := byID[id]; seen {
				byID[id] = nil
			} else {
				byID[id] = msg
			}
		}
	}
	seen := make(map[string]bool)
	for _, value := range input {
		item, _ := value.(map[string]any)
		if item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output" {
			id, _ := item["call_id"].(string)
			if seen[id] {
				delete(byID, id)
			}
			seen[id] = true
		}
	}
	for i := len(input) - 1; i >= 0; i-- {
		item, _ := input[i].(map[string]any)
		if item["type"] != "function_call_output" && item["type"] != "custom_tool_call_output" {
			continue
		}
		id, _ := item["call_id"].(string)
		msg := byID[id]
		parts, _ := item["output"].([]any)
		if msg == nil || len(parts) == 0 {
			continue
		}
		if markedTextOutput(parts) {
			mark(msg)
		}
	}
}

func markedTextOutput(parts []any) bool {
	// 媒体可能被宿主移动到单独的 user 消息，仅纯文本仍有同一输出边界。
	for _, part := range parts {
		block, _ := part.(map[string]any)
		if _, ok := block["text"].(string); !ok || block["type"] != "input_text" {
			return false
		}
	}
	last := parts[len(parts)-1].(map[string]any)
	marker, _ := last["prompt_cache_breakpoint"].(map[string]any)
	return marker["mode"] == "explicit"
}
