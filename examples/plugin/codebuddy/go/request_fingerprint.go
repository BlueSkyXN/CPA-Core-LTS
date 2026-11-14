package main

import "strings"

// 已实测会触发 copilot.tencent.com 11128 渠道风控的系统提示词指纹。
// 2026-11-15 用真实 PAT 逐条二分验证；user/assistant 消息中的同串放行，
// 因此清洗只作用于 system/developer 角色。命中的片段做最小中性化替换，
// 未命中任何指纹的文本原样透传，不报错也不拦截。
var codeBuddyBlockedFingerprints = []struct {
	literal     string
	replacement string
}{
	// Claude Code 身份句（须位于 system 开头；实测前缀加一字符即失效，但
	// 整句替换更稳，且与下两条任意位置匹配的指纹共用同一处理路径）。
	{"You are Claude Code, Anthropic's official CLI for Claude", "You are an interactive CLI coding assistant"},
	// Claude Code 默认分支提示（任意位置匹配，句中亦触发）。
	{"Main branch (you will usually use this for PRs)", "Default branch (you will usually use this for PRs)"},
	// Claude Code 反馈指引（任意位置匹配）。
	{"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues", "To provide feedback, users should report issues on the project's public issue tracker"},
	// Claude Code 2.1.37+ 注入的计费归因文本（任意位置匹配，保留字头形态）。
	{"x-anthropic-billing-header: ", "x-attribution-header: "},
	// DeepSeek Harness 身份句（须位于 system 开头）。
	{"You are an AI agent powered by DeepSeek Harness", "You are an AI agent"},
}

// sanitizeCodeBuddyFingerprints 清洗 messages 中 system/developer 角色的已知
// 风控指纹。只在命中时复制对应消息（copy-on-write），其余消息与字段不动；
// 没有任何命中时 body 保持不变。
func sanitizeCodeBuddyFingerprints(body map[string]any) {
	rawMessages, exists := body["messages"]
	if !exists {
		return
	}
	messages, ok := rawMessages.([]any)
	if !ok || len(messages) == 0 {
		return
	}
	changed := false
	updated := make([]any, len(messages))
	for index, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			updated[index] = item
			continue
		}
		role, _ := message["role"].(string)
		if role != "system" && role != "developer" {
			updated[index] = item
			continue
		}
		content, _ := message["content"].(string)
		if content == "" {
			updated[index] = item
			continue
		}
		cleaned := content
		for _, fingerprint := range codeBuddyBlockedFingerprints {
			cleaned = strings.ReplaceAll(cleaned, fingerprint.literal, fingerprint.replacement)
		}
		if cleaned == content {
			updated[index] = item
			continue
		}
		copied := make(map[string]any, len(message))
		for key, value := range message {
			copied[key] = value
		}
		copied["content"] = cleaned
		updated[index] = copied
		changed = true
	}
	if changed {
		body["messages"] = updated
	}
}
