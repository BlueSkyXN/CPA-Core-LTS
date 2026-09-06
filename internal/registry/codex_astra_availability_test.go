package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAstraAsyncQuestionInstructionsRequireAvailableTool(t *testing.T) {
	var catalog struct {
		Models []struct {
			Slug     string `json:"slug"`
			Messages struct {
				Instructions string `json:"instructions_template"`
			} `json:"model_messages"`
		} `json:"models"`
	}
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models {
		if model.Slug != "gpt-6-astra" {
			continue
		}
		if !strings.Contains(model.Messages.Instructions, "When available, you can use the `functions.send_user_message_async` or `functions.request_user_input_async` tool") {
			t.Fatal("Astra must not promise async question tools in sessions which do not register them")
		}
		return
	}
	t.Fatal("Astra missing")
}
