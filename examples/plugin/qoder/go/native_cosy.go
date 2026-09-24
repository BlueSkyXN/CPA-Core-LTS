package main

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const qoderCosyInferencePath = "/algo/api/v2/service/pro/sse/agent_chat_generation"

func isQoderCosyInferenceEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Path == qoderCosyInferencePath
}

func qoderCosyInferenceURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("FetchKeys", "llm_model_result")
	query.Set("AgentId", "agent_common")
	query.Set("Encode", "1")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// COSY 的 agent 请求体与 OpenAI Chat 不同；仅映射已确认的文本会话字段。
func nativeCosyRequestPayload(req pluginapi.ExecutorRequest, model pluginapi.ModelInfo, user map[string]any) ([]byte, error) {
	canonical, err := nativeRequestPayload(req)
	if err != nil {
		return nil, err
	}
	var input map[string]any
	if json.Unmarshal(canonical, &input) != nil {
		return nil, nativeInvalidResponse()
	}
	if tools, exists := input["tools"]; exists && tools != nil {
		list, ok := tools.([]any)
		if !ok || len(list) > 0 {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY text execution does not support client tools", 400, false)
		}
	}
	if choice, exists := input["tool_choice"]; exists && choice != nil {
		name, ok := choice.(string)
		if !ok || (name != "none" && name != "auto") {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY text execution does not support client tools", 400, false)
		}
	}
	messages := input["messages"].([]any)
	upstreamMessages := make([]map[string]string, 0, len(messages))
	latestUser := ""
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY requires text messages", 400, false)
		}
		if calls, exists := message["tool_calls"]; exists && calls != nil {
			list, ok := calls.([]any)
			if !ok || len(list) > 0 {
				return nil, newPluginCallError("unsupported_input", "Qoder COSY requires text messages", 400, false)
			}
		}
		if message["tool_call_id"] != nil {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY requires text messages", 400, false)
		}
		role, _ := message["role"].(string)
		if role == "developer" {
			role = "system"
		}
		if role != "system" && role != "user" && role != "assistant" {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY requires text messages", 400, false)
		}
		content, ok := message["content"].(string)
		if !ok {
			return nil, newPluginCallError("unsupported_input", "Qoder COSY requires text messages", 400, false)
		}
		upstreamMessages = append(upstreamMessages, map[string]string{"role": role, "content": content})
		if role == "user" {
			latestUser = content
		}
	}
	if strings.TrimSpace(latestUser) == "" {
		return nil, newPluginCallError("invalid_request", "Qoder COSY requires a user message", 400, false)
	}
	requestSetID, err := cosyUUID()
	if err != nil {
		return nil, newPluginCallError("invalid_request", "Qoder request identity is unavailable", 503, true)
	}
	businessID, err := cosyUUID()
	if err != nil {
		return nil, newPluginCallError("invalid_request", "Qoder request identity is unavailable", 503, true)
	}
	inputLimit := model.InputTokenLimit
	if inputLimit <= 0 {
		inputLimit = 180000
	}
	modelConfig := map[string]any{
		"key": req.Model, "display_name": model.DisplayName, "model": "", "format": "openai",
		"is_vl": false, "is_reasoning": model.Thinking != nil, "api_key": "", "url": "",
		"source": "system", "max_input_tokens": inputLimit,
	}
	if modelConfig["display_name"] == "" {
		modelConfig["display_name"] = req.Model
	}
	parameters := map[string]any{}
	if value, ok := input["max_tokens"]; ok {
		parameters["max_tokens"] = value
	} else if value, ok := input["max_completion_tokens"]; ok {
		parameters["max_tokens"] = value
	} else {
		parameters["max_tokens"] = 4096
	}
	if value, ok := parameters["max_tokens"].(float64); ok {
		if value <= 0 || value > 1_000_000 || math.Trunc(value) != value {
			return nil, newPluginCallError("invalid_request", "Qoder COSY requires a positive max_tokens integer", 400, false)
		}
	} else if _, ok := parameters["max_tokens"].(int); !ok {
		return nil, newPluginCallError("invalid_request", "Qoder COSY requires a positive max_tokens integer", 400, false)
	}
	for _, name := range []string{"reasoning_effort", "temperature", "top_p"} {
		if value, ok := input[name]; ok {
			parameters[name] = value
		}
	}
	businessName := []rune(latestUser)
	if len(businessName) > 30 {
		businessName = businessName[:30]
	}
	body := map[string]any{
		"agent_id": "agent_common", "aliyun_user_type": stringValueFromMap(user, "user_type", "userType"),
		"business": map[string]any{"product": "cli", "version": "0.1.43", "type": "agent", "id": businessID,
			"name": string(businessName), "begin_at": time.Now().UnixMilli(), "stage": "start"},
		"chat_context": map[string]any{"chatPrompt": "", "extra": map[string]any{"context": []any{}, "modelConfig": modelConfig,
			"originalContent": map[string]string{"type": "text", "text": latestUser}},
			"features": []any{}, "imageUrls": nil, "text": map[string]string{"type": "text", "text": latestUser}},
		"chat_prompt": "", "chat_task": "FREE_INPUT", "code_language": "", "image_urls": nil,
		"is_reply": true, "is_retry": false, "messages": upstreamMessages, "model_config": modelConfig,
		"parameters": parameters, "request_id": req.RequestID, "chat_record_id": req.RequestID,
		"request_set_id": requestSetID, "session_id": effectiveExecutionSessionID(req),
		"session_type": "qodercli", "source": 1, "stream": true, "task_id": "common",
		"tools": []any{}, "version": "3",
	}
	if body["aliyun_user_type"] == "" {
		body["aliyun_user_type"] = "personal_professional_trial"
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nativeInvalidResponse()
	}
	encoded := qoderEncode(raw)
	if len(encoded) > maxExecutorBodyBytes {
		return nil, newPluginCallError("direct_request_too_large", "Qoder direct request exceeds the body limit", 413, false)
	}
	return []byte(encoded), nil
}
