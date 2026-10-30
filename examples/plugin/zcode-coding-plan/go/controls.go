package main

import (
	"encoding/json"
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

// 此嵌套 module 仅依赖标准库，与 Core 校验相同的已观察供应商结果形状。
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
