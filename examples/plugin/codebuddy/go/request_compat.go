package main

import "strings"

// WorkBuddy 5.8.2 的 MaxTokensFieldRule 优先使用模型 compat，再使用旧模型前缀。
func normalizeCodeBuddyMaxTokens(body map[string]any, model, field string) {
	if field != "max_tokens" && field != "max_completion_tokens" {
		field = ""
		if index := strings.LastIndexAny(model, "/:"); index >= 0 {
			model = model[index+1:]
		}
		for _, prefix := range []string{"o1", "o3", "o4", "gpt-4.1", "gpt-5"} {
			if strings.HasPrefix(model, prefix) {
				field = "max_completion_tokens"
				break
			}
		}
	}
	if field == "" {
		return
	}
	other := "max_tokens"
	if field == other {
		other = "max_completion_tokens"
	}
	// 显式传入目标字段时保留原请求；不替客户端选择两个不同上限中的一个。
	if body[field] != nil || body[other] == nil {
		return
	}
	body[field] = body[other]
	delete(body, other)
}

// normalizeCodeBuddyRoles maps the OpenAI "developer" role onto "system".
// The domestic copilot.tencent.com chat gateway rejects any request that
// carries a developer message with 400/11128 regardless of content (verified
// 2026-11-14 with a neutral prompt; the same text passes as system). Content,
// ordering, and tool-call history are preserved verbatim; messages without a
// developer role are returned untouched.
func normalizeCodeBuddyRoles(body map[string]any) {
	rawMessages, exists := body["messages"]
	if !exists {
		return
	}
	messages, ok := rawMessages.([]any)
	if !ok || len(messages) == 0 {
		return
	}
	hasDeveloper := false
	for _, item := range messages {
		if message, ok := item.(map[string]any); ok {
			if role, _ := message["role"].(string); role == "developer" {
				hasDeveloper = true
				break
			}
		}
	}
	if !hasDeveloper {
		return
	}
	updated := make([]any, 0, len(messages))
	for _, item := range messages {
		if message, ok := item.(map[string]any); ok {
			if role, _ := message["role"].(string); role == "developer" {
				copied := make(map[string]any, len(message))
				for key, value := range message {
					copied[key] = value
				}
				copied["role"] = "system"
				updated = append(updated, copied)
				continue
			}
		}
		updated = append(updated, item)
	}
	body["messages"] = updated
}
