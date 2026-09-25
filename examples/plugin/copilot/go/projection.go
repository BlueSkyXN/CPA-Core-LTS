package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type eventProjection struct {
	requestID string
	model     string
	created   int64
	sequence  uint64
	text      strings.Builder
	reasoning strings.Builder
	usage     pluginapi.AgentUsageV1
	terminal  *pluginapi.AgentTerminalPayloadV1
	toolCalls map[int]*projectedToolCall
}

type projectedToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments strings.Builder
}

func newEventProjection(requestID, model string) *eventProjection {
	return &eventProjection{requestID: requestID, model: model, created: time.Now().Unix(), toolCalls: make(map[int]*projectedToolCall)}
}

func (p *eventProjection) consume(event pluginapi.AgentEventV1) error {
	if errValidate := event.Validate(); errValidate != nil {
		return fmt.Errorf("invalid Copilot AgentEventV1")
	}
	if event.RequestID != p.requestID || event.Sequence != p.sequence+1 {
		return fmt.Errorf("Copilot AgentEventV1 correlation or sequence changed")
	}
	p.sequence = event.Sequence
	switch event.Type {
	case pluginapi.AgentEventMessageDelta:
		var payload pluginapi.AgentTextDeltaV1
		if errDecode := json.Unmarshal(event.Payload, &payload); errDecode != nil {
			return fmt.Errorf("invalid Copilot text event")
		}
		p.text.WriteString(payload.Text)
	case pluginapi.AgentEventReasoningDelta:
		var payload pluginapi.AgentTextDeltaV1
		if errDecode := json.Unmarshal(event.Payload, &payload); errDecode != nil {
			return fmt.Errorf("invalid Copilot reasoning event")
		}
		p.reasoning.WriteString(payload.Text)
	case pluginapi.AgentEventUsageUpdated:
		if errDecode := json.Unmarshal(event.Payload, &p.usage); errDecode != nil {
			return fmt.Errorf("invalid Copilot usage event")
		}
	case pluginapi.AgentEventToolStarted:
		p.consumeToolStarted(event.Payload)
	case pluginapi.AgentEventToolUpdated:
		p.consumeToolUpdated(event.Payload)
	case pluginapi.AgentEventTurnCompleted, pluginapi.AgentEventTurnFailed, pluginapi.AgentEventTurnCancelled, pluginapi.AgentEventSessionClosed:
		var terminal pluginapi.AgentTerminalPayloadV1
		if errDecode := json.Unmarshal(event.Payload, &terminal); errDecode != nil {
			return fmt.Errorf("invalid Copilot terminal event")
		}
		p.terminal = &terminal
	}
	return nil
}

func (p *eventProjection) nonStreamResponse() (pluginapi.ExecutorResponse, error) {
	if p.terminal == nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("runner_lost", "Copilot turn ended without a terminal event", http.StatusBadGateway, true)
	}
	if p.terminal.State != pluginapi.AgentTerminalCompleted {
		return pluginapi.ExecutorResponse{}, p.terminalError()
	}
	body := map[string]any{
		"id":      "chatcmpl-" + p.requestID,
		"object":  "chat.completion",
		"created": p.created,
		"model":   p.model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": p.text.String()},
			"finish_reason": p.finishReason(),
		}},
	}
	message := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if p.reasoning.Len() > 0 {
		message["reasoning_content"] = p.reasoning.String()
	}
	if tools := p.toolCallPayload(); len(tools) > 0 {
		message["tool_calls"] = tools
		if p.text.Len() == 0 {
			message["content"] = nil
		}
	}
	if usage := chatUsage(p.usage); usage != nil {
		body["usage"] = usage
	}
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return pluginapi.ExecutorResponse{}, errMarshal
	}
	return pluginapi.ExecutorResponse{
		Payload:  raw,
		Headers:  http.Header{"Content-Type": {"application/json"}},
		Metadata: map[string]any{"usage_provenance": p.usage.Provenance},
	}, nil
}

func (p *eventProjection) streamChunk(event pluginapi.AgentEventV1) ([]byte, bool, error) {
	if errConsume := p.consume(event); errConsume != nil {
		return nil, false, errConsume
	}
	switch event.Type {
	case pluginapi.AgentEventMessageDelta:
		var payload pluginapi.AgentTextDeltaV1
		_ = json.Unmarshal(event.Payload, &payload)
		return chatChunk(p.requestID, p.model, p.created, map[string]any{"content": payload.Text}, nil, nil), false, nil
	case pluginapi.AgentEventReasoningDelta:
		var payload pluginapi.AgentTextDeltaV1
		_ = json.Unmarshal(event.Payload, &payload)
		return chatChunk(p.requestID, p.model, p.created, map[string]any{"reasoning_content": payload.Text}, nil, nil), false, nil
	case pluginapi.AgentEventToolStarted:
		if tool := p.latestTool(event.Payload); tool != nil {
			return chatChunk(p.requestID, p.model, p.created, map[string]any{"tool_calls": []any{map[string]any{
				"index": tool.Index, "id": tool.ID, "type": "function", "function": map[string]any{"name": tool.Name, "arguments": ""},
			}}}, nil, nil), false, nil
		}
	case pluginapi.AgentEventToolUpdated:
		var payload struct {
			Index       *int   `json:"index"`
			PartialJSON string `json:"partial_json"`
			ToolCallID  string `json:"tool_call_id"`
			Name        string `json:"name"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Index != nil && p.toolCalls[*payload.Index] != nil {
			function := map[string]any{"arguments": payload.PartialJSON}
			call := map[string]any{"index": *payload.Index, "function": function}
			if payload.ToolCallID != "" {
				call["id"] = payload.ToolCallID
			}
			if payload.Name != "" {
				function["name"] = payload.Name
			}
			return chatChunk(p.requestID, p.model, p.created, map[string]any{"tool_calls": []any{call}}, nil, nil), false, nil
		}
	case pluginapi.AgentEventTurnCompleted:
		return chatChunk(p.requestID, p.model, p.created, map[string]any{}, p.finishReason(), chatUsage(p.usage)), true, nil
	case pluginapi.AgentEventTurnFailed, pluginapi.AgentEventTurnCancelled, pluginapi.AgentEventSessionClosed:
		return nil, true, p.terminalError()
	}
	return nil, false, nil
}

func (p *eventProjection) terminalError() error {
	if p.terminal == nil {
		return newPluginCallError("runner_lost", "Copilot turn did not produce a terminal result", http.StatusBadGateway, true)
	}
	code := strings.TrimSpace(p.terminal.Code)
	if code == "" {
		code = string(p.terminal.State)
	}
	message := strings.TrimSpace(p.terminal.Message)
	if message == "" {
		message = "Copilot turn did not complete"
	}
	status := http.StatusBadGateway
	switch code {
	case "auth_expired", "auth_invalid":
		status = http.StatusUnauthorized
	case "invalid_request", "unsupported_model", "direct_invalid_request":
		status = http.StatusBadRequest
	case "stream_too_large", "direct_request_too_large":
		status = http.StatusRequestEntityTooLarge
	case "quota_or_rate_limit":
		status = http.StatusTooManyRequests
	case "direct_timeout":
		status = http.StatusGatewayTimeout
	case "request_cancelled":
		status = 499
	}
	if p.terminal.State == pluginapi.AgentTerminalCancelled {
		status = 499
	} else if p.terminal.State == pluginapi.AgentTerminalPermissionDenied || p.terminal.State == pluginapi.AgentTerminalPermissionUnsupported {
		status = http.StatusForbidden
	}
	return newPluginCallError(code, message, status, p.terminal.Retryable)
}

func (p *eventProjection) consumeToolStarted(raw []byte) {
	var payload struct {
		Index      *int   `json:"index"`
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Index == nil {
		// 直连工具事件始终携带 index；缺少 index 的分片按客户端侧调用处理，不投影。
		return
	}
	index := *payload.Index
	tool := p.toolCalls[index]
	if tool == nil {
		tool = &projectedToolCall{Index: index}
		p.toolCalls[index] = tool
	}
	tool.ID = payload.ToolCallID
	tool.Name = payload.Name
}

func (p *eventProjection) consumeToolUpdated(raw []byte) {
	var payload struct {
		Index       *int   `json:"index"`
		PartialJSON string `json:"partial_json"`
		ToolCallID  string `json:"tool_call_id"`
		Name        string `json:"name"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Index == nil {
		return
	}
	if tool := p.toolCalls[*payload.Index]; tool != nil {
		if payload.ToolCallID != "" {
			tool.ID = payload.ToolCallID
		}
		if payload.Name != "" {
			tool.Name = payload.Name
		}
		tool.Arguments.WriteString(payload.PartialJSON)
	}
}

func (p *eventProjection) finishReason() string {
	if p.terminal != nil && p.terminal.FinishReason != "" {
		return p.terminal.FinishReason
	}
	if len(p.toolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (p *eventProjection) latestTool(raw []byte) *projectedToolCall {
	var payload struct {
		Index *int `json:"index"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Index == nil {
		return nil
	}
	return p.toolCalls[*payload.Index]
}

func (p *eventProjection) toolCallPayload() []any {
	if len(p.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(p.toolCalls))
	for index := range p.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := make([]any, 0, len(indexes))
	for _, index := range indexes {
		tool := p.toolCalls[index]
		result = append(result, map[string]any{
			"index":    tool.Index,
			"id":       tool.ID,
			"type":     "function",
			"function": map[string]any{"name": tool.Name, "arguments": tool.Arguments.String()},
		})
	}
	return result
}

func chatChunk(requestID, model string, created int64, delta map[string]any, finishReason any, usage map[string]any) []byte {
	frame := map[string]any{
		"id": "chatcmpl-" + requestID, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
	if usage != nil {
		frame["usage"] = usage
	}
	raw, _ := json.Marshal(frame)
	return raw
}

func chatUsage(usage pluginapi.AgentUsageV1) map[string]any {
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil &&
		usage.CacheReadTokens == nil && usage.CacheCreationTokens == nil && usage.ReasoningTokens == nil {
		return nil
	}
	result := map[string]any{}
	if usage.InputTokens != nil {
		result["prompt_tokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		result["completion_tokens"] = *usage.OutputTokens
	}
	if usage.TotalTokens != nil {
		result["total_tokens"] = *usage.TotalTokens
	}
	inputDetails := map[string]any{}
	if usage.CacheReadTokens != nil {
		inputDetails["cached_tokens"] = *usage.CacheReadTokens
	}
	if usage.CacheCreationTokens != nil {
		inputDetails["cache_creation_tokens"] = *usage.CacheCreationTokens
	}
	if len(inputDetails) > 0 {
		result["prompt_tokens_details"] = inputDetails
	}
	if usage.ReasoningTokens != nil {
		result["completion_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningTokens}
	}
	return result
}
