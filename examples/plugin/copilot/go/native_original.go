package main

import (
	"bytes"
	"encoding/json"
)

// 宿主的 Chat 转换可以省略异协议的签名。这里仅提取与完整工具批次直接相邻的候选值；
// 候选还须由当前作用域已观察到的上游回放记录确认，不能把任意厂商的签名传给 Copilot。
func nativeOriginalToolStates(exec *nativeExecution, model string, raw []byte) map[string]string {
	if len(raw) == 0 || len(raw) > maxExecutorBodyBytes || (!bytes.Contains(raw, []byte(`"encrypted_content"`)) && !bytes.Contains(raw, []byte(`"signature"`))) {
		return nil
	}
	var source struct{ Input, Messages json.RawMessage }
	if json.Unmarshal(raw, &source) != nil || (source.Input != nil && source.Messages != nil) {
		return nil
	}
	states := make(map[string]string)
	add := func(calls []any, state string) {
		key := nativeReasoningKey(exec, model, calls)
		if key == "" {
			return
		}
		if _, duplicate := states[key]; duplicate || len(state) > nativeReasoningMaxBytes {
			state = ""
		}
		states[key] = state
	}
	if source.Input != nil {
		var items []struct {
			Type, Name, Arguments string
			CallID                string `json:"call_id"`
			State                 string `json:"encrypted_content"`
		}
		if json.Unmarshal(source.Input, &items) != nil {
			return nil
		}
		for i := 0; i < len(items); {
			if items[i].Type != "function_call" {
				i++
				continue
			}
			state := ""
			if i > 0 && items[i-1].Type == "reasoning" && (i < 2 || items[i-2].Type != "reasoning") {
				state = items[i-1].State
			}
			var calls []any
			for ; i < len(items) && items[i].Type == "function_call"; i++ {
				item := items[i]
				calls = append(calls, originalToolCall(item.CallID, item.Name, item.Arguments))
			}
			add(calls, state)
		}
	} else {
		var messages []struct {
			Role    string
			Content json.RawMessage
		}
		if json.Unmarshal(source.Messages, &messages) != nil {
			return nil
		}
		for _, msg := range messages {
			if msg.Role != "assistant" {
				continue
			}
			var blocks []struct {
				Type, ID, Name, Signature string
				Input                     json.RawMessage
			}
			if json.Unmarshal(msg.Content, &blocks) != nil {
				continue
			}
			var calls []any
			var state string
			thinking := 0
			for _, b := range blocks {
				switch b.Type {
				case "thinking":
					thinking++
					state = b.Signature
				case "tool_use":
					calls = append(calls, originalToolCall(b.ID, b.Name, string(b.Input)))
				}
			}
			if thinking != 1 {
				state = ""
			}
			add(calls, state)
		}
	}
	return states
}

func originalToolCall(id, name, args string) map[string]any {
	return map[string]any{"id": id, "function": map[string]any{"name": name, "arguments": args}}
}
