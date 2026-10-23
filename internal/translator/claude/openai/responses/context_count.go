package responses

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AppendResponseOutputForTokenCount retains the already translated prompt and
// appends the delivered Responses output using the same replay rules as the next
// request. Counting keeps trailing thinking/tool calls; it must not invent a
// future user message or a synthetic tool result to make a generation request.
func AppendResponseOutputForTokenCount(model string, nativeRequest, output []byte, modelInfo *registry.ModelInfo) ([]byte, error) {
	if !json.Valid(nativeRequest) || !json.Valid(output) || !gjson.ParseBytes(output).IsArray() || !gjson.GetBytes(nativeRequest, "messages").IsArray() {
		return nil, fmt.Errorf("invalid context count payload")
	}
	for _, item := range gjson.ParseBytes(output).Array() {
		switch item.Get("type").String() {
		case "message":
			if item.Get("role").String() != "assistant" {
				return nil, fmt.Errorf("unsupported response role")
			}
		case "reasoning", "web_search_call", "function_call", "custom_tool_call":
		default:
			return nil, fmt.Errorf("unsupported response item")
		}
	}
	replay, err := sjson.SetRawBytes([]byte(`{}`), "input", output)
	if err != nil {
		return nil, err
	}
	converted, err := convertOpenAIResponsesRequestToClaude(model, replay, false, modelInfo != nil && modelInfo.IsCompat, modelInfo, true)
	if err != nil {
		return nil, err
	}
	messages := make([]json.RawMessage, 0)
	if err = json.Unmarshal([]byte(gjson.GetBytes(nativeRequest, "messages").Raw), &messages); err != nil {
		return nil, err
	}
	for _, message := range gjson.GetBytes(converted, "messages").Array() {
		messages = append(messages, json.RawMessage(message.Raw))
	}
	var items [][]byte
	for _, message := range messages {
		items = append(items, message)
	}
	return sjson.SetRawBytes(nativeRequest, "messages", common.JoinRawArray(items))
}
