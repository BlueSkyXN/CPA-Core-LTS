package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedAstraCatalogRetainsNativeInstructions(t *testing.T) {
	var payload codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &payload); err != nil {
		t.Fatal(err)
	}
	for _, model := range payload.Models {
		if model["slug"] != "gpt-6-astra" {
			continue
		}
		if model["minimal_client_version"] != "0.153.0" || model["context_window"] != float64(272000) || model["max_context_window"] != float64(872000) {
			t.Fatalf("Astra capabilities changed: %v %v %v", model["minimal_client_version"], model["context_window"], model["max_context_window"])
		}
		instructions, ok := model["base_instructions"].(string)
		messages, mok := model["model_messages"].(map[string]any)
		template, tok := messages["instructions_template"].(string)
		if !ok || !mok || !tok || len(instructions) < 1000 || len(template) < 1000 || !strings.Contains(template, "based on GPT-6") {
			t.Fatal("native Astra instructions missing or replaced by an old model template")
		}
		if err := validateCodexClientModel(model); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("offline embedded catalog has no Astra entry")
}

func TestCatalogRefreshRejectsLossOfAstra(t *testing.T) {
	data := testCodexClientCatalog(t, testLTSCodexClientModelsWithout("gpt-6-astra")...)
	if err := ValidateCodexClientModelsLTSCompatibility(data); err == nil || !strings.Contains(err.Error(), "gpt-6-astra") {
		t.Fatalf("catalog missing Astra accepted: %v", err)
	}
}
