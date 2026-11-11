package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func transformExecution(req executorRequest, c *config, session string) (map[string]any, error) {
	var body, original map[string]any
	if err := decode(req.Payload, &body); err != nil || body == nil {
		return nil, problem(400, "invalid_request", "Invalid provider request")
	}
	if len(req.OriginalRequest) > 0 && json.Unmarshal(req.OriginalRequest, &original) != nil {
		return nil, problem(400, "invalid_request", "Invalid request context")
	}
	if _, lost := original["x_coding_plan"]; lost {
		return nil, problem(400, "unsupported_prompt_entry", "Prompt overrides are no longer supported")
	}
	for _, source := range []map[string]any{body, original} {
		if _, ok := source["text"]; ok {
			format, _ := source["text"].(map[string]any)
			if f, ok := format["format"].(map[string]any); ok && f["type"] != "text" {
				return nil, problem(400, "unsupported_parameter", "Structured output is not supported")
			}
		}
		if tier, ok := source["service_tier"].(string); ok && tier != "" && tier != "auto" && tier != "default" {
			return nil, problem(400, "unsupported_parameter", "Service tier is not supported")
		}
		if value, ok := source["previous_response_id"].(string); ok && value != "" {
			return nil, problem(400, "unsupported_parameter", "Supply full history instead of an unresolved previous_response_id")
		}
		if source["store"] == true || source["background"] == true {
			return nil, problem(400, "unsupported_parameter", "Server-side storage and background requests are not supported")
		}
	}
	if err := applyThinkingControls(body, original); err != nil {
		return nil, err
	}
	if _, ok := body["speed"]; ok {
		return nil, problem(400, "unsupported_parameter", "Unverified generation control")
	}
	model, _ := body["model"].(string)
	limit := limitsFor(c, model)
	if tokens, ok := body["max_tokens"].(float64); ok && limit.Output > 0 && tokens > float64(limit.Output) {
		return nil, problem(400, "invalid_request", "max_tokens exceeds the configured model limit")
	}
	if err := validateToolHistory(body); err != nil {
		return nil, err
	}
	foldGatewayVisionHistory(body)
	return transform(encode(body), c, session)
}

func validateToolHistory(body map[string]any) error {
	messages, _ := body["messages"].([]any)
	pending := map[string]bool{}
	readers := map[string]bool{}
	for _, v := range messages {
		message, ok := v.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "assistant" && len(pending) > 0 {
			return problem(400, "invalid_request", "Tool calls require paired results before the next assistant turn")
		}
		blocks, _ := message["content"].([]any)
		for _, v := range blocks {
			block, ok := v.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "server_tool_use":
				if block["name"] == "webReader" {
					id, _ := block["id"].(string)
					input, _ := block["input"].(map[string]any)
					url, _ := input["url"].(string)
					if role != "assistant" || id == "" || strings.TrimSpace(url) == "" || readers[id] {
						return problem(400, "invalid_request", "Invalid webReader history")
					}
					readers[id] = true
				}
			case "tool_use":
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				_, input := block["input"].(map[string]any)
				if role != "assistant" || id == "" || name == "" || !input || pending[id] {
					return problem(400, "invalid_request", "Invalid tool call")
				}
				pending[id] = true
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				if role == "assistant" && readers[id] {
					if !validWebReaderHistoryResult(block["content"]) {
						return problem(400, "invalid_request", "Invalid webReader result; retain the original page JSON")
					}
					delete(readers, id)
					continue
				}
				if role != "user" || !pending[id] {
					return problem(400, "invalid_request", "Unpaired tool result")
				}
				delete(pending, id)
				if nested, ok := block["content"].([]any); ok {
					for _, entry := range nested {
						part, ok := entry.(map[string]any)
						if !ok || (part["type"] != "text" && part["type"] != "image") {
							return problem(400, "unsupported_content", "Only text and supported image tool results are allowed")
						}
					}
				}
			case "thinking":
				if _, ok := block["thinking"].(string); !ok {
					return problem(400, "invalid_request", "Invalid thinking content")
				}
			case "text":
				if _, ok := block["text"].(string); !ok {
					return problem(400, "invalid_request", "Invalid text content")
				}
			}
		}
	}
	if len(pending) > 0 {
		return problem(400, "invalid_request", "Unresolved tool calls")
	}
	return nil
}

// foldGatewayVisionHistory 把历史轮次中的网关识图调用对（mcp_tool_use +
// mcp_tool_result）折叠为等价的 text 快照，再进入 transform 校验。
// 折叠原因：imageSource 是网关 OSS 签名 URL（会过期），且上游不保证接受
// 原样回放的配对块。转写文本本身无损保留。（2026-11-11 回放实测：history
// 里的旧图与折叠 text 快照均被正常消化，旧图不会重复触发转写——网关只对
// 当前 user turn 的图发起 analyze_image。）
// 仅折叠已验证的配对：孤立 mcp_tool_use（不同 ID 无配对结果）保留原块，
// 由 transform 显式拒绝；字符串 content 的消息原样保留不重写。
func foldGatewayVisionHistory(body map[string]any) {
	messages, _ := body["messages"].([]any)
	// 先收集每个 mcp_tool_use 的配对结果文本。
	results := map[string]string{}
	for _, v := range messages {
		message, ok := v.(map[string]any)
		if !ok {
			continue
		}
		blocks, _ := message["content"].([]any)
		for _, w := range blocks {
			block, ok := w.(map[string]any)
			if !ok || block["type"] != "mcp_tool_result" {
				continue
			}
			id, _ := block["tool_use_id"].(string)
			if id == "" {
				continue
			}
			if content, ok := block["content"].([]any); ok {
				var parts []string
				for _, x := range content {
					part, ok := x.(map[string]any)
					if ok && part["type"] == "text" {
						if text, ok := part["text"].(string); ok {
							parts = append(parts, text)
						}
					}
				}
				if len(parts) > 0 {
					results[id] = strings.Join(parts, "\n\n")
				}
			}
		}
	}
	if len(results) == 0 {
		// 没有可折叠的结果对；可能存在孤立 mcp_tool_use（客户端历史被截断等），
		// 交给 transform 拒绝，不在这里静默丢块。
		return
	}
	for _, v := range messages {
		message, ok := v.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			// 字符串 content 与其他非数组形态原样保留；只重写含目标块的消息。
			continue
		}
		folded := false
		kept := make([]any, 0, len(blocks))
		for _, w := range blocks {
			block, ok := w.(map[string]any)
			if !ok {
				kept = append(kept, w)
				continue
			}
			switch block["type"] {
			case "mcp_tool_use":
				id, _ := block["id"].(string)
				if _, paired := results[id]; !paired {
					// 孤立调用（无配对结果）保留原块，transform 显式拒绝。
					kept = append(kept, w)
					continue
				}
				name, _ := block["name"].(string)
				kept = append(kept, map[string]any{
					"type": "text",
					"text": fmt.Sprintf("[gateway vision: %s executed server-side; analysis retained below]", name),
				})
				folded = true
			case "mcp_tool_result":
				id, _ := block["tool_use_id"].(string)
				text := results[id]
				if text == "" {
					kept = append(kept, w)
					continue
				}
				kept = append(kept, map[string]any{"type": "text", "text": text})
				folded = true
			default:
				kept = append(kept, w)
			}
		}
		if folded {
			message["content"] = kept
		}
	}
}

func validWebReaderHistoryResult(value any) bool {
	content, ok := value.(string)
	if !ok || !strings.HasPrefix(strings.TrimSpace(content), "[") {
		return false
	}
	var pages []map[string]any
	if json.Unmarshal([]byte(content), &pages) != nil {
		return false
	}
	for _, page := range pages {
		url, ok := page["url"].(string)
		if !ok || strings.TrimSpace(url) == "" {
			return false
		}
		if _, ok := page["content"].(string); !ok {
			return false
		}
	}
	return true
}

func rpcErrorCode(e *apiError) string {
	if e == nil {
		return "request_scoped"
	}
	switch e.Code {
	case "upstream_error", "connection_lifecycle":
		return e.Code
	default:
		return "request_scoped"
	}
}
