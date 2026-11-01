package main

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	nativeReasoningTTL        = time.Hour
	nativeReasoningMaxEntries = 256
	nativeReasoningMaxBytes   = 64 * 1024
)

type nativeReasoningReplay struct {
	identity executionIdentity
	opaque   string
	created  time.Time
}

// Responses 客户端只回传标准工具调用。按账号、调用方、workspace、会话、模型
// 和完整工具调用匹配上次的 opaque 状态，避免依赖客户端保留 Copilot 扩展字段。
func nativeReasoningKey(exec *nativeExecution, model string, calls any) string {
	tools, ok := calls.([]any)
	if !ok || len(tools) == 0 {
		return ""
	}
	parts := []string{exec.identity.key, model}
	ids := make(map[string]bool, len(tools))
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		id, _ := tool["id"].(string)
		function, _ := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(arguments))
		decoder.UseNumber()
		if id == "" || name == "" || ids[id] || !json.Valid([]byte(arguments)) || decoder.Decode(&decoded) != nil {
			return ""
		}
		ids[id] = true
		canonical, _ := json.Marshal(decoded)
		parts = append(parts, id, name, string(canonical))
	}
	return sessionDigest(parts)
}

func (r *pluginRuntime) restoreNativeReasoning(exec *nativeExecution, model string, raw []byte) []byte {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return raw
	}
	messages, _ := body["messages"].([]any)
	changed := false
	r.mu.Lock()
	if r.generation == exec.generation {
		for _, value := range messages {
			message, _ := value.(map[string]any)
			if message["role"] != "assistant" {
				continue
			}
			if opaque, _ := message["reasoning_opaque"].(string); opaque != "" {
				continue
			}
			key := nativeReasoningKey(exec, model, message["tool_calls"])
			replay, ok := r.nativeReasoning[key]
			if !ok {
				continue
			}
			if time.Since(replay.created) > nativeReasoningTTL {
				delete(r.nativeReasoning, key)
				continue
			}
			message["reasoning_opaque"] = replay.opaque
			changed = true
		}
	}
	r.mu.Unlock()
	if changed {
		if restored, err := json.Marshal(body); err == nil && len(restored) <= maxExecutorBodyBytes {
			return restored
		}
	}
	return raw
}

func (r *pluginRuntime) rememberNativeReasoning(exec *nativeExecution, model string, projection *eventProjection) {
	opaque := projection.reasoningOpaque
	if opaque == "" || len(opaque) > nativeReasoningMaxBytes {
		return
	}
	key := nativeReasoningKey(exec, model, projection.toolCallPayload())
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation != exec.generation || exec.isCanceled() || !r.accepting {
		return
	}
	now := time.Now()
	oldestKey := ""
	oldest := now
	for candidate, replay := range r.nativeReasoning {
		if now.Sub(replay.created) > nativeReasoningTTL {
			delete(r.nativeReasoning, candidate)
		} else if oldestKey == "" || replay.created.Before(oldest) {
			oldestKey, oldest = candidate, replay.created
		}
	}
	if _, exists := r.nativeReasoning[key]; !exists && len(r.nativeReasoning) >= nativeReasoningMaxEntries {
		delete(r.nativeReasoning, oldestKey)
	}
	r.nativeReasoning[key] = nativeReasoningReplay{identity: exec.identity, opaque: opaque, created: now}
}
