package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexClientModelsResponse_InputModalitiesFromRegistry(t *testing.T) {
	modelID := "mimo-v2.5-pro-codex-test"
	textOnlyModelID := "mimo-text-only-codex-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("codex-input-modalities-test", "openai-compatibility", []*registry.ModelInfo{
		{
			ID:                       modelID,
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              modelID,
			SupportedInputModalities: []string{"text", "image"},
		},
		{
			ID:                       textOnlyModelID,
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              textOnlyModelID,
			SupportedInputModalities: []string{"text"},
		},
		{
			ID:                       "mimo-mixed-modalities-codex-test",
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              "mimo-mixed-modalities-codex-test",
			SupportedInputModalities: []string{"text", "image", "audio", "video", "TEXT", "IMAGE"},
		},
		{
			ID:      "compat-image-only-codex-test",
			Object:  "model",
			OwnedBy: "mimo",
			Type:    registry.OpenAIImageModelType,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("codex-input-modalities-test")
	})

	openaiModels := modelRegistry.GetAvailableModels("openai")
	resp := CodexClientModelsResponse(openaiModels)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var visionEntry map[string]any
	var textOnlyEntry map[string]any
	var mixedEntry map[string]any
	var imageEntry map[string]any
	for _, entry := range models {
		slug := stringModelValue(entry, "slug")
		switch slug {
		case modelID:
			visionEntry = entry
		case textOnlyModelID:
			textOnlyEntry = entry
		case "mimo-mixed-modalities-codex-test":
			mixedEntry = entry
		case "compat-image-only-codex-test":
			imageEntry = entry
		}
	}
	if visionEntry == nil {
		t.Fatalf("expected codex entry for %q", modelID)
	}
	modalities, ok := visionEntry["input_modalities"].([]any)
	if !ok || len(modalities) != 2 {
		t.Fatalf("input_modalities = %#v, want [text image]", visionEntry["input_modalities"])
	}
	if got, _ := modalities[0].(string); got != "text" {
		t.Fatalf("input_modalities[0] = %q, want text", got)
	}
	if got, _ := modalities[1].(string); got != "image" {
		t.Fatalf("input_modalities[1] = %q, want image", got)
	}
	if got, ok := visionEntry["supports_image_detail_original"].(bool); !ok || !got {
		t.Fatalf("supports_image_detail_original = %#v, want true", visionEntry["supports_image_detail_original"])
	}

	if textOnlyEntry == nil {
		t.Fatalf("expected codex entry for %q", textOnlyModelID)
	}
	textOnlyModalities, ok := textOnlyEntry["input_modalities"].([]any)
	if !ok || len(textOnlyModalities) != 1 {
		t.Fatalf("text-only input_modalities = %#v, want [text]", textOnlyEntry["input_modalities"])
	}
	if got, _ := textOnlyModalities[0].(string); got != "text" {
		t.Fatalf("text-only input_modalities[0] = %q, want text", got)
	}
	if _, exists := textOnlyEntry["supports_image_detail_original"]; exists {
		t.Fatalf("text-only model should not expose supports_image_detail_original: %#v", textOnlyEntry["supports_image_detail_original"])
	}

	if mixedEntry == nil {
		t.Fatal("expected codex entry for mixed-modalities model")
	}
	mixedModalities, ok := mixedEntry["input_modalities"].([]any)
	if !ok || len(mixedModalities) != 2 {
		t.Fatalf("mixed input_modalities = %#v, want [text image]", mixedEntry["input_modalities"])
	}
	if got, _ := mixedModalities[0].(string); got != "text" {
		t.Fatalf("mixed input_modalities[0] = %q, want text", got)
	}
	if got, _ := mixedModalities[1].(string); got != "image" {
		t.Fatalf("mixed input_modalities[1] = %q, want image", got)
	}
	if got, ok := mixedEntry["supports_image_detail_original"].(bool); !ok || !got {
		t.Fatalf("mixed supports_image_detail_original = %#v, want true", mixedEntry["supports_image_detail_original"])
	}

	if imageEntry == nil {
		t.Fatal("expected codex entry for image-only compat model")
	}
	if got, _ := imageEntry["visibility"].(string); got != "hide" {
		t.Fatalf("image model visibility = %q, want hide", got)
	}
	if _, exists := imageEntry["input_modalities"]; exists {
		t.Fatalf("image endpoint model should not expose input_modalities from registry: %#v", imageEntry["input_modalities"])
	}
}

func TestCodexClientModelsResponse_GPT56UltraReasoningMetadata(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{
		{"id": "gpt-5.6-sol"},
		{"id": "gpt-5.6-terra"},
		{"id": "gpt-5.6-luna"},
	})
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	for _, slug := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
		model := bySlug[slug]
		if model == nil {
			t.Fatalf("expected codex entry for %q", slug)
		}
		assertCodexClientReasoningEfforts(t, model, []string{"low", "medium", "high", "xhigh", "max", "ultra"})
		if got := codexClientReasoningLevelDescription(model, "ultra"); got != "Maximum reasoning with automatic task delegation" {
			t.Fatalf("%s ultra description = %q, want automatic task delegation description", slug, got)
		}
	}

	luna := bySlug["gpt-5.6-luna"]
	if luna == nil {
		t.Fatal("expected codex entry for gpt-5.6-luna")
	}
	assertCodexClientReasoningEfforts(t, luna, []string{"low", "medium", "high", "xhigh", "max"})
	if got := codexClientReasoningLevelDescription(luna, "ultra"); got != "" {
		t.Fatalf("gpt-5.6-luna unexpectedly exposes ultra description %q", got)
	}
}

func TestCodexClientModelsResponse_SparkKeepsEffortEnabledWithSummaryDefaultOff(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{{"id": "gpt-5.3-codex-spark"}})
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var spark map[string]any
	for _, model := range models {
		if stringModelValue(model, "slug") == "gpt-5.3-codex-spark" {
			spark = model
			break
		}
	}
	if spark == nil {
		t.Fatal("expected codex entry for gpt-5.3-codex-spark")
	}
	if supports, okSupports := spark["supports_reasoning_summaries"].(bool); !okSupports || !supports {
		t.Fatalf("supports_reasoning_summaries = %#v, want true so reasoning.effort remains enabled", spark["supports_reasoning_summaries"])
	}
	if got := stringModelValue(spark, "default_reasoning_summary"); got != "none" {
		t.Fatalf("default_reasoning_summary = %q, want none", got)
	}
	assertCodexClientReasoningEfforts(t, spark, []string{"low", "medium", "high", "xhigh"})
}

func TestCodexClientReasoningDescriptionUltra(t *testing.T) {
	const want = "Maximum reasoning with automatic task delegation"
	if got := codexClientReasoningDescription("ultra"); got != want {
		t.Fatalf("ultra description = %q, want %q", got, want)
	}
}

func assertCodexClientReasoningEfforts(t *testing.T, model map[string]any, want []string) {
	t.Helper()

	rawLevels, ok := model["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("%s supported_reasoning_levels = %#v, want array", stringModelValue(model, "slug"), model["supported_reasoning_levels"])
	}
	if len(rawLevels) != len(want) {
		t.Fatalf("%s supported_reasoning_levels length = %d, want %d: %#v", stringModelValue(model, "slug"), len(rawLevels), len(want), rawLevels)
	}
	for index, wantEffort := range want {
		level, ok := rawLevels[index].(map[string]any)
		if !ok {
			t.Fatalf("%s supported_reasoning_levels[%d] = %#v, want object", stringModelValue(model, "slug"), index, rawLevels[index])
		}
		if got := stringModelValue(level, "effort"); got != wantEffort {
			t.Fatalf("%s supported_reasoning_levels[%d].effort = %q, want %q", stringModelValue(model, "slug"), index, got, wantEffort)
		}
	}
}

func codexClientReasoningLevelDescription(model map[string]any, effort string) string {
	rawLevels, _ := model["supported_reasoning_levels"].([]any)
	for _, rawLevel := range rawLevels {
		level, ok := rawLevel.(map[string]any)
		if !ok || stringModelValue(level, "effort") != effort {
			continue
		}
		return stringModelValue(level, "description")
	}
	return ""
}
