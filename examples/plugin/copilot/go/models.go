package main

import (
	"net/http"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// copilotCatalogModel 对齐上游 GET /models 的 OpenAI 形状；未知字段整体忽略。
type copilotCatalogModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Vendor      string `json:"vendor"`
	Policy      *struct {
		State string `json:"state"`
		Terms string `json:"terms"`
	} `json:"policy"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Capabilities       *struct {
		Family    string `json:"family"`
		Tokenizer string `json:"tokenizer"`
		Type      string `json:"type"`
		Limits    *struct {
			MaxContextWindowTokens int64 `json:"max_context_window_tokens"`
			MaxOutputTokens        int64 `json:"max_output_tokens"`
			MaxPromptTokens        int64 `json:"max_prompt_tokens"`
		} `json:"limits"`
		Supports *struct {
			ToolCalls         bool     `json:"tool_calls"`
			ParallelToolCalls bool     `json:"parallel_tool_calls"`
			Streaming         bool     `json:"streaming"`
			StructuredOutputs bool     `json:"structured_outputs"`
			Vision            bool     `json:"vision"`
			AdaptiveThinking  bool     `json:"adaptive_thinking"`
			MaxThinkingBudget int64    `json:"max_thinking_budget"`
			MinThinkingBudget int64    `json:"min_thinking_budget"`
			ReasoningEffort   []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
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
	models, err := r.nativeModels(auth, req.HostCallbackID, r.loadedConfig())
	return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: models}, err
}

func copilotGenerationMethods(model copilotCatalogModel) []string {
	// 入口由宿主转换；目录必须与插件真正会调用的 HTTP 出口一致。
	var chat, embeddings bool
	for _, endpoint := range model.SupportedEndpoints {
		switch strings.TrimSpace(endpoint) {
		case "/chat/completions", "/v1/chat/completions":
			chat = true
		case "/embeddings", "/v1/embeddings":
			embeddings = true
		}
	}
	if model.Capabilities != nil {
		switch strings.ToLower(strings.TrimSpace(model.Capabilities.Type)) {
		case "embeddings":
			if len(model.SupportedEndpoints) == 0 || embeddings {
				return []string{"embeddings"}
			}
			return nil
		case "chat", "completion":
			embeddings = false
		}
	}
	var methods []string
	if chat {
		methods = append(methods, "chat")
	}
	if embeddings {
		methods = append(methods, "embeddings")
	}
	return methods
}

func catalogAllowsMethod(models []pluginapi.ModelInfo, id, method string) bool {
	for _, model := range models {
		if model.ID == id {
			return slices.Contains(model.SupportedGenerationMethods, method)
		}
	}
	return false
}

func copilotModelInfo(model copilotCatalogModel) pluginapi.ModelInfo {
	inputModalities := []string{"text"}
	var contextLength, inputLimit, outputLimit int64
	var thinking *pluginapi.ThinkingSupport
	if capabilities := model.Capabilities; capabilities != nil {
		if capabilities.Limits != nil {
			contextLength = capabilities.Limits.MaxContextWindowTokens
			inputLimit = capabilities.Limits.MaxPromptTokens
			outputLimit = capabilities.Limits.MaxOutputTokens
		}
		if supports := capabilities.Supports; supports != nil {
			if supports.Vision {
				inputModalities = append(inputModalities, "image")
			}
			levels := make([]string, 0, len(supports.ReasoningEffort))
			for _, level := range supports.ReasoningEffort {
				if level != "" {
					levels = append(levels, level)
				}
			}
			if supports.AdaptiveThinking || supports.MaxThinkingBudget > 0 || len(levels) > 0 {
				thinking = &pluginapi.ThinkingSupport{
					Min:            int(supports.MinThinkingBudget),
					Max:            int(supports.MaxThinkingBudget),
					ZeroAllowed:    supports.MinThinkingBudget == 0,
					DynamicAllowed: supports.AdaptiveThinking,
					Levels:         levels,
				}
			}
		}
	}
	displayName := strings.TrimSpace(model.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(model.Name)
	}
	if displayName == "" {
		displayName = model.ID
	}
	return pluginapi.ModelInfo{
		ID: model.ID, Object: "model", OwnedBy: pluginIdentifier, Type: "agent",
		DisplayName: displayName, Name: model.ID, Version: strings.TrimSpace(model.Version),
		Description:     strings.TrimSpace(model.Description),
		InputTokenLimit: inputLimit, OutputTokenLimit: outputLimit,
		ContextLength: contextLength, MaxCompletionTokens: outputLimit,
		SupportedGenerationMethods: copilotGenerationMethods(model), SupportedInputModalities: inputModalities,
		SupportedOutputModalities: []string{"text"}, Thinking: thinking,
	}
}
