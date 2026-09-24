package main

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var canonicalQoderModelIDs = []string{
	"auto",
	"qmodel_38max",
	"qfmodel",
	"qmodel_latest",
	"qmodel",
	"q37fmodel",
	"dmodel",
	"dfmodel",
	"gmodel",
	"gfmodel",
	"gm51model",
	"kmodel",
	"mmodel",
}

var canonicalQoderModelDisplayNames = map[string]string{
	"auto":          "Auto",
	"qmodel_38max":  "Qwen3.8-Max",
	"qfmodel":       "Qwen3.8-Flash",
	"qmodel_latest": "Qwen3.7-Max",
	"qmodel":        "Qwen3.7-Plus",
	"q37fmodel":     "Qwen3.7-Flash",
	"dmodel":        "DeepSeek-V4-Pro",
	"dfmodel":       "DeepSeek-V4-Flash",
	"gmodel":        "GLM-5.3",
	"gfmodel":       "GLM-5.3-Flash",
	"gm51model":     "GLM-5.2",
	"kmodel":        "Kimi-K2.7-Code",
	"mmodel":        "MiniMax-M2.7",
}

type qoderCatalogModel struct {
	ID                      string   `json:"id"`
	DisplayName             string   `json:"display_name"`
	Description             string   `json:"description,omitempty"`
	Source                  string   `json:"source,omitempty"`
	IsDefault               bool     `json:"is_default,omitempty"`
	IsEnabled               *bool    `json:"is_enabled,omitempty"`
	IsReasoning             bool     `json:"is_reasoning,omitempty"`
	IsVL                    bool     `json:"is_vl,omitempty"`
	MaxInputTokens          int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens         int64    `json:"max_output_tokens,omitempty"`
	ReasoningEfforts        []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort  string   `json:"default_reasoning_effort,omitempty"`
	SupportsDisabled        bool     `json:"supports_disabled,omitempty"`
	AvailableContextWindows []int64  `json:"available_context_windows,omitempty"`
	DefaultContextWindow    int64    `json:"default_context_window,omitempty"`
}

func configuredDirectModels(models []directModelConfig) []pluginapi.ModelInfo {
	result := make([]pluginapi.ModelInfo, 0, len(models))
	for _, model := range models {
		inputModalities := []string{"text"}
		if model.IsVL {
			inputModalities = append(inputModalities, "image")
		}
		var thinking *pluginapi.ThinkingSupport
		if model.IsReasoning || len(model.ReasoningEfforts) > 0 || model.SupportsDisabled {
			thinking = &pluginapi.ThinkingSupport{Levels: append([]string(nil), model.ReasoningEfforts...), ZeroAllowed: model.SupportsDisabled}
		}
		contextLength := model.MaxInputTokens
		for _, value := range model.AvailableContextWindows {
			if value > contextLength {
				contextLength = value
			}
		}
		result = append(result, pluginapi.ModelInfo{
			ID: model.ID, Name: model.ID, DisplayName: model.DisplayName, Description: model.Description,
			Object: "model", OwnedBy: pluginIdentifier, Type: "agent", InputTokenLimit: model.MaxInputTokens,
			OutputTokenLimit: model.MaxOutputTokens, ContextLength: contextLength, MaxCompletionTokens: model.MaxOutputTokens,
			SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: inputModalities,
			SupportedOutputModalities: []string{"text"}, Thinking: thinking, UserDefined: true,
		})
	}
	return result
}

func (r *pluginRuntime) modelsForAuth(raw []byte) (pluginapi.ModelResponse, error) {
	var req rpcAuthModelRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ModelResponse{}, errDecode
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return pluginapi.ModelResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusBadRequest, false)
	}
	cfg := r.loadedConfig()
	models, err := r.nativeModels(auth, req.HostCallbackID, cfg)
	if isQoderCosyInferenceEndpoint(cfg.DirectEndpoint) {
		for i := range models {
			models[i].SupportedInputModalities = []string{"text"}
		}
	}
	return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: models}, err
}

func qoderModelInfo(model qoderCatalogModel) pluginapi.ModelInfo {
	inputModalities := []string{"text"}
	if model.IsVL {
		inputModalities = append(inputModalities, "image")
	}
	contextLength := model.MaxInputTokens
	for _, value := range model.AvailableContextWindows {
		if value > contextLength {
			contextLength = value
		}
	}
	var thinking *pluginapi.ThinkingSupport
	if model.IsReasoning || len(model.ReasoningEfforts) > 0 || model.SupportsDisabled {
		thinking = &pluginapi.ThinkingSupport{
			Levels:      append([]string(nil), model.ReasoningEfforts...),
			ZeroAllowed: model.SupportsDisabled,
		}
	}
	return pluginapi.ModelInfo{
		ID: model.ID, Name: model.ID, DisplayName: model.DisplayName, Description: model.Description,
		Object: "model", OwnedBy: pluginIdentifier, Type: "agent",
		InputTokenLimit: model.MaxInputTokens, OutputTokenLimit: model.MaxOutputTokens,
		ContextLength: contextLength, MaxCompletionTokens: model.MaxOutputTokens,
		SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: inputModalities,
		SupportedOutputModalities: []string{"text"}, Thinking: thinking, UserDefined: true,
	}
}

func cloneModels(input []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := append([]pluginapi.ModelInfo(nil), input...)
	for i := range out {
		out[i].SupportedGenerationMethods = append([]string(nil), out[i].SupportedGenerationMethods...)
		out[i].SupportedInputModalities = append([]string(nil), out[i].SupportedInputModalities...)
		out[i].SupportedOutputModalities = append([]string(nil), out[i].SupportedOutputModalities...)
		out[i].SupportedParameters = append([]string(nil), out[i].SupportedParameters...)
		if out[i].Thinking != nil {
			thinking := *out[i].Thinking
			thinking.Levels = append([]string(nil), thinking.Levels...)
			out[i].Thinking = &thinking
		}
	}
	return out
}

func qoderDisplayName(id, supplied string) string {
	if name := strings.TrimSpace(supplied); name != "" {
		return name
	}
	if name := canonicalQoderModelDisplayNames[id]; name != "" {
		return name
	}
	return id
}
