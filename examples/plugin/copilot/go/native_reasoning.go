package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	nativeReasoningTTL        = time.Hour
	nativeReasoningMaxEntries = 256
	nativeReasoningMaxBytes   = 64 * 1024
)

type nativeReasoningReplay struct {
	identity  executionIdentity
	opaque    string
	ambiguous bool
	// 只保留同一作用域、同一完整工具批次下的有限摘要；不缓存超限密文本身。
	witnesses [2][sha256.Size]byte
	created   time.Time
}

// Responses 客户端只回传标准工具调用。按账号、调用方、workspace、会话、模型
// 和完整工具调用匹配上次的 opaque 状态，避免依赖客户端保留 Copilot 扩展字段。
func nativeReasoningKey(exec *nativeExecution, model string, calls any) string {
	tools, _ := calls.([]any)
	key, _ := nativeToolBatch(exec, model, tools)
	return key
}

func nativeToolBatch(exec *nativeExecution, model string, tools []any) (string, map[string]nativeToolIdentity) {
	if len(tools) == 0 {
		return "", nil
	}
	parts := []string{exec.identity.key, model}
	batch := make(map[string]nativeToolIdentity, len(tools))
	valid := true
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		id, _ := tool["id"].(string)
		function, _ := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		canonical := canonicalToolArguments(arguments)
		if _, duplicate := batch[id]; id == "" || name == "" || duplicate || canonical == "" {
			valid = false
		}
		batch[id] = nativeToolIdentity{id, name, canonical}
		parts = append(parts, id, name, canonical)
	}
	if !valid {
		return "", batch
	}
	return sessionDigest(parts), batch
}

func canonicalToolArguments(arguments string) string {
	if !json.Valid([]byte(arguments)) {
		return ""
	}
	var value any
	d := json.NewDecoder(strings.NewReader(arguments))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return ""
	}
	canonical, _ := json.Marshal(value)
	return string(canonical)
}

type nativeToolIdentity struct{ id, name, arguments string }

func (r *pluginRuntime) trackNativeTool(exec *nativeExecution, index int, tool nativeToolIdentity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation != exec.generation || r.nativeActive[exec.requestID] != exec || exec.replayClosed || exec.isCanceled() {
		return
	}
	if exec.tools == nil {
		exec.tools = make(map[int]nativeToolIdentity)
	}
	exec.tools[index] = tool
	exec.toolsComplete = false
}

func (r *pluginRuntime) finishNativeTools(exec *nativeExecution, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	exec.toolsComplete = count > 0 && len(exec.tools) == count
}

func (r *pluginRuntime) nativeToolPendingLocked(exec *nativeExecution, model string, batch map[string]nativeToolIdentity, valid bool) bool {
	for _, active := range r.nativeActive {
		if active.replayClosed || active.identity.key != exec.identity.key || active.model != model || active.generation != exec.generation || (valid && active.toolsComplete && len(active.tools) != len(batch)) {
			continue
		}
		overlap, possible := false, true
		for _, call := range active.tools {
			want, ok := batch[call.id]
			if !ok {
				if valid {
					possible = false
					break
				}
				continue
			}
			if (call.name != "" && want.name != "" && call.name != want.name) || (call.arguments != "" && want.arguments != "" && call.arguments != want.arguments) {
				possible = false
				break
			}
			overlap = true
		}
		// 取消后直到移除在途登记前仍要保护公开的批次，不能在这里读取旧回放。
		if overlap && possible {
			return true
		}
	}
	return false
}

// 只有最新 assistant 之后的工具结果依赖在途执行；宿主可能将工具附件另放在尾部 user 消息。
func nativeToolContinuation(messages []any) (producer int, ids map[string]bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		switch message["role"] {
		case "assistant":
			return i, ids
		case "tool":
			if id, _ := message["tool_call_id"].(string); id != "" {
				if ids == nil {
					ids = make(map[string]bool)
				}
				ids[id] = true
			}
		}
	}
	return -1, nil
}

// pending 与已完成回放在同一把锁内判断，不能在新生产者完成前误用旧缓存。
func (r *pluginRuntime) restoreNativeReasoning(exec *nativeExecution, model string, raw, original []byte) ([]byte, error) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return raw, nil
	}
	messages, _ := body["messages"].([]any)
	changed, err := r.applyNativeReasoning(exec, model, messages, nativeOriginalToolStates(exec, model, original))
	if err != nil || !changed {
		return raw, err
	}
	restored, err := json.Marshal(body)
	if err != nil || len(restored) > maxExecutorBodyBytes {
		return nil, newPluginCallError("direct_request_too_large", "Copilot request exceeds the body limit after tool replay", http.StatusRequestEntityTooLarge, false)
	}
	return restored, nil
}

func (r *pluginRuntime) applyNativeReasoning(exec *nativeExecution, model string, messages []any, originalStates map[string]string) (bool, error) {
	producer, resultIDs := nativeToolContinuation(messages)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation != exec.generation {
		return false, nil
	}
	changed := false
	for i, value := range messages {
		message, _ := value.(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		if opaque, _ := message["reasoning_opaque"].(string); opaque != "" {
			continue
		}
		calls, _ := message["tool_calls"].([]any)
		key, batch := nativeToolBatch(exec, model, calls)
		replay, ok := r.nativeReasoning[key]
		if ok && time.Since(replay.created) > nativeReasoningTTL {
			delete(r.nativeReasoning, key)
			replay, ok = nativeReasoningReplay{}, false
		}
		if state := originalStates[key]; ok && replay.recognizes(state) {
			message["reasoning_opaque"] = state
			changed = true
			continue
		}
		continuation := false
		if i == producer {
			for id := range resultIDs {
				if _, exists := batch[id]; exists {
					continuation = true
					break
				}
			}
		}
		if continuation && r.nativeToolPendingLocked(exec, model, batch, key != "") {
			return false, newPluginCallError("tool_continuation_pending", "Copilot tool result arrived before its producing request finished; retry after the stream ends", http.StatusConflict, true)
		}
		if replay.ambiguous || (len(batch) > 0 && (ok || continuation) && r.interruptedNativeBatchLocked(exec, model, batch, key != "")) {
			if continuation {
				return false, newPluginCallError("tool_continuation_ambiguous", "Copilot cannot identify the reasoning state for these tool results; restart from before the tool call without reusing this tool history", http.StatusConflict, false)
			}
			continue
		}
		if replay.opaque != "" {
			message["reasoning_opaque"] = replay.opaque
			changed = true
		}
	}
	return changed, nil
}

func (replay nativeReasoningReplay) recognizes(state string) bool {
	if state == "" || len(state) > nativeReasoningMaxBytes {
		return false
	}
	if !replay.ambiguous {
		return replay.opaque == state
	}
	digest := sha256.Sum256([]byte(state))
	return replay.witnesses[0] == digest || replay.witnesses[1] == digest
}

func (replay *nativeReasoningReplay) witness(state string) {
	if state == "" || len(state) > nativeReasoningMaxBytes {
		return
	}
	digest := sha256.Sum256([]byte(state))
	for i, w := range replay.witnesses {
		if w == digest {
			return
		}
		if w == [sha256.Size]byte{} {
			replay.witnesses[i] = digest
			return
		}
	}
}

func (r *pluginRuntime) rememberNativeReasoning(exec *nativeExecution, model string, projection *eventProjection) {
	opaque := projection.reasoningOpaque
	key := nativeReasoningKey(exec, model, projection.toolCallPayload())
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation != exec.generation || exec.replayClosed || exec.isCanceled() || !r.accepting {
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
	replay := nativeReasoningReplay{identity: exec.identity, created: now}
	if len(opaque) > nativeReasoningMaxBytes {
		replay.ambiguous = true
	} else {
		replay.opaque = opaque
	}
	if prior, exists := r.nativeReasoning[key]; exists {
		if prior.ambiguous || replay.ambiguous || prior.opaque != opaque {
			replay.opaque, replay.ambiguous, replay.witnesses = "", true, prior.witnesses
			replay.witness(prior.opaque)
			replay.witness(opaque)
		}
	} else if len(r.nativeReasoning) >= nativeReasoningMaxEntries {
		delete(r.nativeReasoning, oldestKey)
	}
	r.nativeReasoning[key] = replay
}
