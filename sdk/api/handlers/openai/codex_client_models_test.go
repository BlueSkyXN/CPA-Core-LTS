package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestCodexClientModelsResponseMultiAgentV2FollowsConfig(t *testing.T) {
	modelID := "codex-client-multi-agent-v2-test"
	clientID := "codex-client-multi-agent-v2-test-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)
	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled", enabled: false},
		{name: "enabled", enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base.Cfg.CodexOptimizeMultiAgentV2 = tt.enabled
			response := handler.codexClientModelsResponse()
			models, ok := response["models"].([]map[string]any)
			if !ok {
				t.Fatalf("models type = %T, want []map[string]any", response["models"])
			}
			var entry map[string]any
			for _, model := range models {
				slug, _ := model["slug"].(string)
				if slug == modelID {
					entry = model
					break
				}
			}
			if entry == nil {
				t.Fatalf("missing synthesized model %q", modelID)
			}
			value, exists := entry["multi_agent_version"]
			if tt.enabled {
				if !exists || value != "v2" {
					t.Fatalf("multi_agent_version = %#v, want v2", value)
				}
				return
			}
			if !exists || value != nil {
				t.Fatalf("multi_agent_version = %#v, want preserved null", value)
			}
		})
	}
}

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

func TestCodexClientModelsResponse_AppliesDisplayNameToTemplateModel(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{{
		"id":           "gpt-5.5",
		"display_name": "Configured Codex Name",
	}})
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}
	if got := stringModelValue(models[0], "display_name"); got != "Configured Codex Name" {
		t.Fatalf("display_name = %q, want Configured Codex Name", got)
	}
}

func TestCodexClientModelsResponse_GPT56UltraReasoningMetadata(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{
		{"id": "gpt-5.6-sol", "display_name": "Configured Sol"},
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
	if got := stringModelValue(bySlug["gpt-5.6-sol"], "display_name"); got != "Configured Sol" {
		t.Fatalf("gpt-5.6-sol display_name = %q, want Configured Sol", got)
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

func TestCodexClientModelsResponse_DisablesSearchToolForSynthesizedModels(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{
		{"id": "custom-openai-compatible-model"},
		{"id": "gpt-5.5"},
	})
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}
	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	custom := bySlug["custom-openai-compatible-model"]
	if custom == nil {
		t.Fatal("expected synthesized custom model entry")
	}
	if got, ok := custom["supports_search_tool"].(bool); !ok || got {
		t.Fatalf("custom supports_search_tool = %#v, want false", custom["supports_search_tool"])
	}

	official := bySlug["gpt-5.5"]
	if official == nil {
		t.Fatal("expected official template model entry")
	}
	if got, ok := official["supports_search_tool"].(bool); !ok || !got {
		t.Fatalf("official supports_search_tool = %#v, want true", official["supports_search_tool"])
	}
}

func TestCodexClientModelsResponse_RequiresTemplateAndCodexProvidersForSearchTool(t *testing.T) {
	providers := map[string][]string{
		"new-codex-model": {"codex"},
		"gpt-5.5":         {"openai-compatible-deepseek"},
		"gpt-5.4":         {"codex", "xai"},
		"gpt-5.6-sol":     {"codex"},
	}
	resp := codexClientModelsResponse([]map[string]any{
		{"id": "new-codex-model"},
		{"id": "gpt-5.5"},
		{"id": "gpt-5.4"},
		{"id": "gpt-5.6-sol"},
	}, func(id string) []string {
		return providers[id]
	})
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	if got, ok := bySlug["gpt-5.6-sol"]["supports_search_tool"].(bool); !ok || !got {
		t.Errorf("gpt-5.6-sol supports_search_tool = %#v, want true", bySlug["gpt-5.6-sol"]["supports_search_tool"])
	}
	for _, slug := range []string{"new-codex-model", "gpt-5.5", "gpt-5.4"} {
		if got, ok := bySlug[slug]["supports_search_tool"].(bool); !ok || got {
			t.Errorf("%s supports_search_tool = %#v, want false", slug, bySlug[slug]["supports_search_tool"])
		}
	}
}

func TestCodexClientModelsResponse_PreservesUltraReasoningEffort(t *testing.T) {
	resp := CodexClientModelsResponse([]map[string]any{{"id": "gpt-5.6-sol"}})
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var sol map[string]any
	for _, entry := range models {
		if stringModelValue(entry, "slug") == "gpt-5.6-sol" {
			sol = entry
			break
		}
	}
	if sol == nil {
		t.Fatal("expected codex client entry for gpt-5.6-sol")
	}
	levels, ok := sol["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("supported_reasoning_levels = %T, want []any", sol["supported_reasoning_levels"])
	}
	for _, rawLevel := range levels {
		level, ok := rawLevel.(map[string]any)
		if ok && stringModelValue(level, "effort") == "ultra" {
			return
		}
	}
	t.Fatalf("supported_reasoning_levels = %#v, want ultra", levels)
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

func TestLoadCodexClientModelTemplatesRefreshesOnRevision(t *testing.T) {
	codexClientModelTemplatesMu.Lock()
	previousLoaded := codexClientModelTemplatesLoaded
	previousRevision := codexClientModelTemplatesRevision
	previousTemplates := codexClientModelTemplates
	previousDefault := codexClientDefaultTemplate
	previousErr := codexClientModelTemplatesErr
	codexClientModelTemplatesLoaded = false
	codexClientModelTemplatesMu.Unlock()
	t.Cleanup(func() {
		codexClientModelTemplatesMu.Lock()
		codexClientModelTemplatesLoaded = previousLoaded
		codexClientModelTemplatesRevision = previousRevision
		codexClientModelTemplates = previousTemplates
		codexClientDefaultTemplate = previousDefault
		codexClientModelTemplatesErr = previousErr
		codexClientModelTemplatesMu.Unlock()
	})

	first := []byte(`{"models":[{"slug":"gpt-5.5","display_name":"First"}]}`)
	templates, defaultTemplate, err := loadCodexClientModelTemplatesSnapshot(first, 100)
	if err != nil {
		t.Fatalf("load first snapshot: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "First" {
		t.Fatalf("first display_name = %q, want First", got)
	}
	if got := stringModelValue(defaultTemplate, "display_name"); got != "First" {
		t.Fatalf("first default display_name = %q, want First", got)
	}

	second := []byte(`{"models":[{"slug":"gpt-5.5","display_name":"Second"}]}`)
	templates, defaultTemplate, err = loadCodexClientModelTemplatesSnapshot(second, 101)
	if err != nil {
		t.Fatalf("load second snapshot: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "Second" {
		t.Fatalf("second display_name = %q, want Second", got)
	}
	if got := stringModelValue(defaultTemplate, "display_name"); got != "Second" {
		t.Fatalf("second default display_name = %q, want Second", got)
	}

	templates, _, err = loadCodexClientModelTemplatesSnapshot(first, 101)
	if err != nil {
		t.Fatalf("reload cached revision: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "Second" {
		t.Fatalf("cached display_name = %q, want Second", got)
	}
}
