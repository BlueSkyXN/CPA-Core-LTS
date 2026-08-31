package main

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	usage     pluginapi.AgentUsageV1
	terminal  *pluginapi.AgentTerminalPayloadV1
}

func newEventProjection(requestID, model string) *eventProjection {
	return &eventProjection{requestID: requestID, model: model, created: time.Now().Unix()}
}

func (p *eventProjection) consume(event pluginapi.AgentEventV1) error {
	if errValidate := event.Validate(); errValidate != nil {
		return fmt.Errorf("invalid Qoder AgentEventV1")
	}
	if event.RequestID != p.requestID || event.Sequence != p.sequence+1 {
		return fmt.Errorf("Qoder AgentEventV1 correlation or sequence changed")
	}
	p.sequence = event.Sequence
	switch event.Type {
	case pluginapi.AgentEventMessageDelta:
		var payload pluginapi.AgentTextDeltaV1
		if errDecode := json.Unmarshal(event.Payload, &payload); errDecode != nil {
			return fmt.Errorf("invalid Qoder text event")
		}
		p.text.WriteString(payload.Text)
	case pluginapi.AgentEventUsageUpdated:
		if errDecode := json.Unmarshal(event.Payload, &p.usage); errDecode != nil {
			return fmt.Errorf("invalid Qoder usage event")
		}
	case pluginapi.AgentEventTurnCompleted, pluginapi.AgentEventTurnFailed, pluginapi.AgentEventTurnCancelled, pluginapi.AgentEventSessionClosed:
		var terminal pluginapi.AgentTerminalPayloadV1
		if errDecode := json.Unmarshal(event.Payload, &terminal); errDecode != nil {
			return fmt.Errorf("invalid Qoder terminal event")
		}
		p.terminal = &terminal
	}
	return nil
}

func (p *eventProjection) nonStreamResponse() (pluginapi.ExecutorResponse, error) {
	if p.terminal == nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("runner_lost", "Qoder runner ended without a terminal event", http.StatusBadGateway, true)
	}
	if p.terminal.State != pluginapi.AgentTerminalCompleted {
		message := strings.TrimSpace(p.terminal.Message)
		if message == "" {
			message = "Qoder turn did not complete"
		}
		status := http.StatusBadGateway
		if p.terminal.State == pluginapi.AgentTerminalCancelled {
			status = 499
		} else if p.terminal.State == pluginapi.AgentTerminalPermissionDenied || p.terminal.State == pluginapi.AgentTerminalPermissionUnsupported {
			status = http.StatusForbidden
		}
		return pluginapi.ExecutorResponse{}, newPluginCallError(string(p.terminal.State), message, status, p.terminal.Retryable)
	}
	body := map[string]any{
		"id":      "chatcmpl-" + p.requestID,
		"object":  "chat.completion",
		"created": p.created,
		"model":   p.model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": p.text.String()},
			"finish_reason": "stop",
		}},
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
	case pluginapi.AgentEventTurnCompleted:
		return chatChunk(p.requestID, p.model, p.created, map[string]any{}, "stop", chatUsage(p.usage)), true, nil
	case pluginapi.AgentEventTurnFailed, pluginapi.AgentEventTurnCancelled, pluginapi.AgentEventSessionClosed:
		return nil, true, newPluginCallError(string(p.terminal.State), "Qoder stream did not complete", http.StatusBadGateway, p.terminal.Retryable)
	}
	return nil, false, nil
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
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil {
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
	return result
}
