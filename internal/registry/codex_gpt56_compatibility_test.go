package registry

import (
	"strings"
	"testing"
)

func TestCodexGPT56UltraCompatibilityCoversStaticTierModels(t *testing.T) {
	tiers := map[string]func() []*ModelInfo{
		"free": GetCodexFreeModels,
		"team": GetCodexTeamModels,
		"plus": GetCodexPlusModels,
		"pro":  GetCodexProModels,
	}
	seen := map[string]bool{}

	for tier, getModels := range tiers {
		for _, model := range getModels() {
			if model == nil {
				continue
			}
			switch model.ID {
			case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra":
				seen[model.ID] = true
				if !modelHasThinkingLevel(model, "ultra") {
					t.Errorf("%s tier %s levels = %v, want logical ultra compatibility", tier, model.ID, thinkingLevels(model))
				}
			case "gpt-5.6-luna":
				seen[model.ID] = true
				if modelHasThinkingLevel(model, "ultra") {
					t.Errorf("%s tier %s levels = %v, want max-only", tier, model.ID, thinkingLevels(model))
				}
			}
		}
	}

	for _, modelID := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra"} {
		if !seen[modelID] {
			t.Fatalf("Codex tier models missing %s", modelID)
		}
	}
}

func TestLookupModelInfoAppliesCodexGPT56UltraCompatibility(t *testing.T) {
	tests := []struct {
		modelID   string
		wantUltra bool
	}{
		{modelID: "gpt-5.6-sol", wantUltra: true},
		{modelID: "gpt-5.6-terra", wantUltra: true},
		{modelID: "gpt-6-astra", wantUltra: true},
		{modelID: "gpt-5.6-luna", wantUltra: false},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			info := LookupModelInfo(tt.modelID, "codex")
			if info == nil {
				t.Fatalf("LookupModelInfo(%q, codex) = nil", tt.modelID)
			}
			if got := modelHasThinkingLevel(info, "ultra"); got != tt.wantUltra {
				t.Fatalf("LookupModelInfo(%q, codex) levels = %v, ultra=%t want %t", tt.modelID, thinkingLevels(info), got, tt.wantUltra)
			}
		})
	}
}

func TestLookupModelInfoAppliesCodexGPT56UltraCompatibilityToDynamicProviderInfo(t *testing.T) {
	registryRef := GetGlobalRegistry()
	clientID := "test-codex-gpt56-ultra-compatibility"
	registryRef.RegisterClient(clientID, "codex", []*ModelInfo{
		maxOnlyGPT56Model("gpt-5.6-sol"),
		maxOnlyGPT56Model("gpt-5.6-terra"),
		maxOnlyGPT56Model("gpt-6-astra"),
		maxOnlyGPT56Model("gpt-5.6-luna"),
	})
	t.Cleanup(func() { registryRef.UnregisterClient(clientID) })

	for _, tt := range []struct {
		modelID   string
		wantUltra bool
	}{
		{modelID: "gpt-5.6-sol", wantUltra: true},
		{modelID: "gpt-5.6-terra", wantUltra: true},
		{modelID: "gpt-6-astra", wantUltra: true},
		{modelID: "gpt-5.6-luna", wantUltra: false},
	} {
		info := LookupModelInfo(tt.modelID, "codex")
		if info == nil {
			t.Fatalf("dynamic LookupModelInfo(%q, codex) = nil", tt.modelID)
		}
		if got := modelHasThinkingLevel(info, "ultra"); got != tt.wantUltra {
			t.Fatalf("dynamic LookupModelInfo(%q, codex) levels = %v, ultra=%t want %t", tt.modelID, thinkingLevels(info), got, tt.wantUltra)
		}
	}
}

func TestLookupModelInfoDoesNotApplyCodexGPT56UltraCompatibilityToOtherProviders(t *testing.T) {
	registryRef := GetGlobalRegistry()
	clientID := "test-openai-gpt56-no-ultra-compatibility"
	registryRef.RegisterClient(clientID, "openai", []*ModelInfo{maxOnlyGPT56Model("gpt-5.6-sol")})
	t.Cleanup(func() { registryRef.UnregisterClient(clientID) })

	info := LookupModelInfo("gpt-5.6-sol", "openai")
	if info == nil {
		t.Fatal("LookupModelInfo(gpt-5.6-sol, openai) = nil")
	}
	if modelHasThinkingLevel(info, "ultra") {
		t.Fatalf("non-Codex provider levels = %v, want no logical ultra overlay", thinkingLevels(info))
	}
}

func maxOnlyGPT56Model(modelID string) *ModelInfo {
	return &ModelInfo{
		ID: modelID,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	}
}

func modelHasThinkingLevel(model *ModelInfo, want string) bool {
	for _, level := range thinkingLevels(model) {
		if strings.EqualFold(strings.TrimSpace(level), want) {
			return true
		}
	}
	return false
}

func thinkingLevels(model *ModelInfo) []string {
	if model == nil || model.Thinking == nil {
		return nil
	}
	return model.Thinking.Levels
}

func TestCodexAstraCompatibilityPreservesUserDefinedModel(t *testing.T) {
	model := maxOnlyGPT56Model("gpt-6-astra")
	model.UserDefined = true
	applyCodexCompatibility(model)
	if modelHasThinkingLevel(model, "ultra") {
		t.Fatal("custom Astra model must retain its own reasoning levels")
	}
}
