package openai

import (
	"strings"

	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func (h *OpenAIAPIHandler) codexClientModelsResponse(clientVersion ...string) map[string]any {
	version := ""
	if len(clientVersion) > 0 {
		version = clientVersion[0]
	}
	optimizeMultiAgentV2 := h != nil && h.Cfg != nil && h.Cfg.CodexOptimizeMultiAgentV2
	return preserveLTSMediaModelVisibility(codexmodels.BuildResponseForClient(h.Models(), registry.GetGlobalRegistry().GetModelProviders, optimizeMultiAgentV2, version))
}

// CodexClientModelsResponse builds a Codex client model response.
func CodexClientModelsResponse(models []map[string]any) map[string]any {
	return preserveLTSMediaModelVisibility(codexmodels.BuildResponse(models, nil, false))
}

// CodexClientModelsResponseWithMultiAgentV2 builds a Codex client model response
// and advertises multi-agent v2 for synthesized models when enabled.
func CodexClientModelsResponseWithMultiAgentV2(models []map[string]any, enabled bool) map[string]any {
	return preserveLTSMediaModelVisibility(codexmodels.BuildResponse(models, nil, enabled))
}

// CodexClientModelsResponseForClient builds a Codex client model response
// tailored for a specific client version.
func CodexClientModelsResponseForClient(models []map[string]any, clientVersion string, enabled bool) map[string]any {
	return preserveLTSMediaModelVisibility(codexmodels.BuildResponseForClient(models, nil, enabled, clientVersion))
}

// preserveLTSMediaModelVisibility keeps the removable downstream guard in place
// until its dedicated retirement change lands on an already-merged baseline.
func preserveLTSMediaModelVisibility(response map[string]any) map[string]any {
	models, ok := response["models"].([]map[string]any)
	if !ok {
		return response
	}
	for _, model := range models {
		slug, _ := model["slug"].(string)
		switch strings.TrimSpace(slug) {
		case "grok-imagine-image-quality", "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-2.0", "grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview":
			model["visibility"] = "hide"
		}
	}
	return response
}
