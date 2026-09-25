package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 共用已验证的 Chat 投影语义；事件只存在于 Go 内存，不经过外部 runner。
type nativeDecoder struct {
	projection   *eventProjection
	emit         func([]byte) error
	finishReason string
	sawResult    bool
}

func newNativeDecoder(requestID, model string, emit func([]byte) error) *nativeDecoder {
	return &nativeDecoder{projection: newEventProjection(requestID, model), emit: emit}
}

func (d *nativeDecoder) event(kind pluginapi.AgentEventType, payload any) error {
	raw, _ := json.Marshal(payload)
	event := pluginapi.AgentEventV1{SchemaVersion: pluginapi.AgentEventSchemaVersionV1, Type: kind, RequestID: d.projection.requestID, Provider: pluginIdentifier, Sequence: d.projection.sequence + 1, Timestamp: time.Now().UTC(), Payload: raw}
	if d.emit == nil {
		return d.projection.consume(event)
	}
	chunk, _, err := d.projection.streamChunk(event)
	if err == nil && len(chunk) > 0 {
		return d.emit(chunk)
	}
	return err
}

func (d *nativeDecoder) data(raw []byte) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		if !d.sawResult {
			return false, nativeInvalidResponse()
		}
		return true, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return false, nativeInvalidResponse()
	}
	if errValue, exists := obj["error"]; exists && !bytes.Equal(errValue, []byte("null")) {
		return false, newPluginCallError("direct_upstream_error", "Copilot reported an upstream stream error", http.StatusBadGateway, true)
	}
	usageRaw := obj["usage"]
	if len(usageRaw) > 0 && !bytes.Equal(usageRaw, []byte("null")) {
		var usage map[string]any
		if json.Unmarshal(usageRaw, &usage) != nil {
			return false, nativeInvalidResponse()
		}
		merged := mergeCopilotUsage(d.projection.usage, usage)
		if chatUsage(merged) != nil {
			if err := d.event(pluginapi.AgentEventUsageUpdated, merged); err != nil {
				return false, err
			}
		}
	}
	var choices []struct {
		Index        int             `json:"index"`
		FinishReason string          `json:"finish_reason"`
		Delta        json.RawMessage `json:"delta"`
		Message      json.RawMessage `json:"message"`
	}
	if len(obj["choices"]) == 0 {
		return false, nil
	}
	if json.Unmarshal(obj["choices"], &choices) != nil || len(choices) > 1 {
		return false, nativeInvalidResponse()
	}
	for _, choice := range choices {
		if choice.Index != 0 {
			return false, nativeInvalidResponse()
		}
		if choice.FinishReason != "" {
			d.finishReason = choice.FinishReason
			d.sawResult = true
		}
		rawDelta := choice.Delta
		if len(rawDelta) == 0 || bytes.Equal(rawDelta, []byte("null")) {
			rawDelta = choice.Message
		}
		if len(rawDelta) == 0 {
			continue
		}
		var delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			FinishReason     string `json:"finish_reason"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(rawDelta, &delta) != nil {
			return false, nativeInvalidResponse()
		}
		if delta.FinishReason != "" {
			d.finishReason = delta.FinishReason
			d.sawResult = true
		}
		if delta.Content != "" {
			d.sawResult = true
			if err := d.event(pluginapi.AgentEventMessageDelta, pluginapi.AgentTextDeltaV1{Text: delta.Content}); err != nil {
				return false, err
			}
		}
		reasoning := delta.ReasoningContent
		if reasoning == "" {
			reasoning = delta.Reasoning
		}
		if reasoning != "" {
			d.sawResult = true
			if err := d.event(pluginapi.AgentEventReasoningDelta, pluginapi.AgentTextDeltaV1{Text: reasoning}); err != nil {
				return false, err
			}
		}
		for position, call := range delta.ToolCalls {
			index := position
			if call.Index != nil {
				index = *call.Index
			}
			if index < 0 || index >= 256 {
				return false, nativeInvalidResponse()
			}
			previous := d.projection.toolCalls[index]
			if previous == nil {
				if call.ID == "" && call.Function.Name == "" && call.Function.Arguments == "" {
					continue
				}
				if err := d.event(pluginapi.AgentEventToolStarted, map[string]any{"index": index, "tool_call_id": call.ID, "name": call.Function.Name}); err != nil {
					return false, err
				}
			}
			d.sawResult = true
			update := map[string]any{"index": index, "partial_json": call.Function.Arguments}
			if previous != nil && previous.ID == "" {
				update["tool_call_id"] = call.ID
			}
			if previous != nil && previous.Name == "" {
				update["name"] = call.Function.Name
			}
			if err := d.event(pluginapi.AgentEventToolUpdated, update); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

func (d *nativeDecoder) finish() error {
	if !d.sawResult {
		return nativeInvalidResponse()
	}
	return d.event(pluginapi.AgentEventTurnCompleted, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCompleted, FinishReason: d.finishReason})
}

// 失败前已报告的用量仍交给 Core 统计；不伪造成功终结帧或另行发布账本。
func (d *nativeDecoder) emitFailureUsage() error {
	usage := chatUsage(d.projection.usage)
	if d.emit == nil || usage == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{
		"id": "chatcmpl-" + d.projection.requestID, "object": "chat.completion.chunk",
		"created": d.projection.created, "model": d.projection.model,
		"choices": []any{}, "usage": usage,
	})
	if err != nil {
		return err
	}
	return d.emit(append(append([]byte("data: "), raw...), '\n', '\n'))
}

func mergeCopilotUsage(previous pluginapi.AgentUsageV1, usage map[string]any) pluginapi.AgentUsageV1 {
	read := func(values map[string]any, keys ...string) *int64 {
		for _, key := range keys {
			if v, ok := values[key].(float64); ok && v >= 0 && v < 9e15 && float64(int64(v)) == v {
				n := int64(v)
				return &n
			}
		}
		return nil
	}
	set := func(target **int64, value *int64) {
		if value != nil {
			*target = value
		}
	}
	set(&previous.InputTokens, read(usage, "input_tokens", "prompt_tokens"))
	set(&previous.OutputTokens, read(usage, "output_tokens", "completion_tokens"))
	if total := read(usage, "total_tokens"); total != nil {
		previous.TotalTokens = total
	} else if previous.InputTokens != nil && previous.OutputTokens != nil {
		total := *previous.InputTokens + *previous.OutputTokens
		previous.TotalTokens = &total
	}
	input, _ := usage["prompt_tokens_details"].(map[string]any)
	if input == nil {
		input, _ = usage["input_tokens_details"].(map[string]any)
	}
	output, _ := usage["completion_tokens_details"].(map[string]any)
	if output == nil {
		output, _ = usage["output_tokens_details"].(map[string]any)
	}
	set(&previous.CacheReadTokens, read(usage, "cache_read_input_tokens", "cached_tokens"))
	set(&previous.CacheReadTokens, read(input, "cached_tokens", "cache_read_tokens"))
	set(&previous.CacheCreationTokens, read(usage, "cache_creation_input_tokens", "cache_creation_tokens"))
	set(&previous.CacheCreationTokens, read(input, "cache_creation_tokens", "cache_write_tokens"))
	set(&previous.ReasoningTokens, read(output, "reasoning_tokens"))
	if chatUsage(previous) != nil {
		previous.Provenance = "provider_reported_unverified"
	}
	return previous
}
