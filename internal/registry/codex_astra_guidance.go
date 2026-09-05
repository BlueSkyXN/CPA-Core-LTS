package registry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// qualifyAstraAsyncGuidance ports openai/codex#42878 to refreshed catalogs too.
// Only the known legacy sentence in Astra is patched; future wording, other
// models and unknown catalog fields are retained. This does not add a tool.
func qualifyAstraAsyncGuidance(data []byte) ([]byte, error) {
	const legacy = "You can use the `functions.send_user_message_async` or `functions.request_user_input_async` tool (depending on which is available)"
	const qualified = "When available, you can use the `functions.send_user_message_async` or `functions.request_user_input_async` tool (depending on which is available)"
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(catalog["models"], &models); err != nil {
		return nil, err
	}
	for _, model := range models {
		var slug string
		if json.Unmarshal(model["slug"], &slug) != nil || slug != "gpt-6-astra" {
			continue
		}
		var messages map[string]json.RawMessage
		if err := json.Unmarshal(model["model_messages"], &messages); err != nil {
			return data, nil
		}
		var instructions string
		if err := json.Unmarshal(messages["instructions_template"], &instructions); err != nil {
			return data, nil
		}
		if !strings.Contains(instructions, legacy) {
			return data, nil
		}
		instructions = strings.ReplaceAll(instructions, legacy, qualified)
		messages["instructions_template"], _ = json.Marshal(instructions)
		model["model_messages"], _ = json.Marshal(messages)
		var err error
		catalog["models"], err = json.Marshal(models)
		if err != nil {
			return nil, fmt.Errorf("encode Astra model guidance: %w", err)
		}
		return json.Marshal(catalog)
	}
	return data, nil
}
