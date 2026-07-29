package openai

import (
	"strings"

	"github.com/tidwall/gjson"
)

// responsesWebsocketHasCompleteToolPairs is a final marker preflight. The
// repair path normally removes/inserts orphaned items, but cross-model replay
// must be stricter: an incomplete call/output pair cannot be safely recreated
// after model-private reasoning and source execution state are discarded.
func responsesWebsocketHasCompleteToolPairs(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	calls := make(map[string]struct{})
	outputs := make(map[string]struct{})
	for _, item := range input.Array() {
		callID := strings.TrimSpace(item.Get("call_id").String())
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			if callID == "" {
				return false
			}
			calls[callID] = struct{}{}
		case "function_call_output", "custom_tool_call_output":
			if callID == "" {
				return false
			}
			outputs[callID] = struct{}{}
		}
	}
	for callID := range calls {
		if _, ok := outputs[callID]; !ok {
			return false
		}
	}
	for callID := range outputs {
		if _, ok := calls[callID]; !ok {
			return false
		}
	}
	return true
}

func responsesWebsocketCanAttestContextReset(payload []byte) bool {
	if strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" {
		return false
	}
	return responsesWebsocketHasCompleteToolPairs(payload)
}
