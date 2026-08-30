package main

import (
	"context"
	"net/http"
	"strings"
	"time"

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

type runnerModel struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description,omitempty"`
	Source           string   `json:"source,omitempty"`
	IsDefault        bool     `json:"is_default,omitempty"`
	IsEnabled        *bool    `json:"is_enabled,omitempty"`
	MaxInputTokens   int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens  int64    `json:"max_output_tokens,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

type runnerModelsResponse struct {
	Models []runnerModel `json:"models"`
}

type cachedModels struct {
	expires time.Time
	models  []pluginapi.ModelInfo
}

func canonicalQoderModels() []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(canonicalQoderModelIDs))
	for _, id := range canonicalQoderModelIDs {
		display := canonicalQoderModelDisplayNames[id]
		if display == "" {
			display = id
		}
		models = append(models, pluginapi.ModelInfo{
			ID: id, Name: id, DisplayName: display, Object: "model", OwnedBy: pluginIdentifier, Type: "agent",
			SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"},
			SupportedOutputModalities: []string{"text"}, UserDefined: true,
		})
	}
	return models
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
	cacheKey := authCacheKey(req.AuthID, req.AuthProvider, auth)
	r.mu.Lock()
	cached, ok := r.modelCache[cacheKey]
	r.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: cloneModels(cached.models)}, nil
	}

	cfg := r.loadedConfig()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()
	client, errStart := r.startRunner(ctx, auth)
	if errStart != nil {
		return pluginapi.ModelResponse{}, errStart
	}
	defer client.shutdown()
	var result runnerModelsResponse
	if errCall := client.call(ctx, "models", map[string]any{
		"auth": auth.runnerAuth(), "cache_ttl_ms": cfg.ModelCacheTTL.Milliseconds(),
	}, &result); errCall != nil {
		return pluginapi.ModelResponse{}, errCall
	}
	models := make([]pluginapi.ModelInfo, 0, len(result.Models))
	for _, model := range result.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" || model.IsEnabled != nil && !*model.IsEnabled {
			continue
		}
		display := strings.TrimSpace(model.DisplayName)
		if display == "" {
			display = id
		}
		models = append(models, pluginapi.ModelInfo{
			ID: id, Name: id, DisplayName: display, Description: model.Description, Object: "model", OwnedBy: pluginIdentifier,
			Type: "agent", InputTokenLimit: model.MaxInputTokens, OutputTokenLimit: model.MaxOutputTokens,
			SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"},
			SupportedOutputModalities: []string{"text"}, UserDefined: true,
		})
	}
	if len(models) == 0 {
		return pluginapi.ModelResponse{}, newPluginCallError("models_unavailable", "Qoder live model discovery returned no enabled canonical IDs", http.StatusServiceUnavailable, true)
	}
	r.mu.Lock()
	r.modelCache[cacheKey] = cachedModels{expires: time.Now().Add(cfg.ModelCacheTTL), models: cloneModels(models)}
	r.mu.Unlock()
	return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: models}, nil
}

func (auth qoderAuth) runnerAuth() map[string]any {
	if auth.AuthMode == "pat" {
		return map[string]any{"mode": "pat", "env_var": runnerPATEnv, "account_id": auth.AccountID}
	}
	return map[string]any{"mode": "local_cli", "profile_id": auth.ProfileID}
}

func authCacheKey(authID, provider string, auth qoderAuth) string {
	return sessionDigest([]string{"qoder-models", strings.TrimSpace(provider), strings.TrimSpace(authID), auth.AuthMode, auth.AccountID, auth.ProfileID, auth.ConfigDir})
}

func cloneModels(input []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	return append([]pluginapi.ModelInfo(nil), input...)
}
