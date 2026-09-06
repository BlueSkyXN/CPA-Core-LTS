package registry

import "strings"

var codexGPT56UltraCompatibilityLevels = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

// withCodexCompatibility applies LTS client compatibility metadata to cloned
// Codex model definitions. The remote models catalog remains authoritative for
// server wire capabilities; this overlay only preserves logical client presets
// that CPA-Core-LTS canonicalizes before the request reaches Codex upstream.
func withCodexCompatibility(models []*ModelInfo) []*ModelInfo {
	for _, model := range models {
		applyCodexCompatibility(model)
	}
	return models
}

// applyCodexCompatibility mutates a cloned model definition in place.
func applyCodexCompatibility(model *ModelInfo) *ModelInfo {
	if model == nil || model.UserDefined {
		return model
	}
	if model.ID != "gpt-5.6-sol" && model.ID != "gpt-5.6-terra" && model.ID != "gpt-6-astra" {
		return model
	}

	if model.Thinking == nil {
		model.Thinking = &ThinkingSupport{
			Levels: append([]string(nil), codexGPT56UltraCompatibilityLevels...),
		}
		return model
	}
	if len(model.Thinking.Levels) == 0 {
		model.Thinking.Levels = append([]string(nil), codexGPT56UltraCompatibilityLevels...)
		return model
	}
	for _, level := range model.Thinking.Levels {
		if strings.EqualFold(strings.TrimSpace(level), "ultra") {
			return model
		}
	}
	model.Thinking.Levels = append(model.Thinking.Levels, "ultra")
	return model
}
