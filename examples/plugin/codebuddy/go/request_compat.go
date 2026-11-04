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
