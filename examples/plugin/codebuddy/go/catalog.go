package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	codeBuddyCatalogCacheTTL = time.Minute
	maxCodeBuddyCatalogBytes = 2 * 1024 * 1024
)

type codeBuddyCatalog struct {
	Models       []pluginapi.ModelInfo
	Allowed      map[string]struct{}
	EnterpriseID string
}

type codeBuddyCatalogCacheEntry struct {
	FetchedAt time.Time
	Catalog   codeBuddyCatalog
}

type codeBuddyCatalogResponse struct {
	Code int                   `json:"code"`
	Data *codeBuddyCatalogData `json:"data"`
	Msg  string                `json:"msg,omitempty"`
}

type codeBuddyCatalogData struct {
	Models       []codeBuddyCatalogModel `json:"models"`
	Agents       []codeBuddyCatalogAgent `json:"agents"`
	EnterpriseID string                  `json:"enterpriseId,omitempty"`
}

type codeBuddyCatalogAgent struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

type codeBuddyCatalogModel struct {
	ID                 string                    `json:"id"`
	Name               string                    `json:"name"`
	DescriptionEN      string                    `json:"descriptionEn"`
	DescriptionZH      string                    `json:"descriptionZh"`
	MaxAllowedSize     int64                     `json:"maxAllowedSize"`
	MaxInputTokens     int64                     `json:"maxInputTokens"`
	MaxOutputTokens    int64                     `json:"maxOutputTokens"`
	DisabledMultimodal bool                      `json:"disabledMultimodal"`
	OnlyReasoning      bool                      `json:"onlyReasoning"`
	SupportsImages     bool                      `json:"supportsImages"`
	SupportsReasoning  bool                      `json:"supportsReasoning"`
	SupportsToolCall   bool                      `json:"supportsToolCall"`
	Reasoning          codeBuddyCatalogReasoning `json:"reasoning"`
}

type codeBuddyCatalogReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
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
	catalog, errCatalog := r.catalogForAuth(auth, req.HostCallbackID)
	if errCatalog != nil {
		return pluginapi.ModelResponse{}, errCatalog
	}
	return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: cloneCodeBuddyModels(catalog.Models)}, nil
}

func (r *pluginRuntime) catalogForAuth(auth codeBuddyAuth, callbackID string) (codeBuddyCatalog, error) {
	cfg := r.loadedConfig()
	key := codeBuddyCatalogCacheKey(auth, cfg.CatalogEndpoint, cfg.CatalogUserAgent)
	r.mu.Lock()
	cached, ok := r.catalogCache[key]
	r.mu.Unlock()
	if ok && time.Since(cached.FetchedAt) < codeBuddyCatalogCacheTTL {
		return cloneCodeBuddyCatalog(cached.Catalog), nil
	}
	if strings.TrimSpace(callbackID) == "" {
		if ok {
			return cloneCodeBuddyCatalog(cached.Catalog), nil
		}
		return codeBuddyCatalog{}, newPluginCallError("catalog_unavailable", "CodeBuddy catalog requires a host callback context", http.StatusServiceUnavailable, true)
	}
	catalog, errFetch := fetchCodeBuddyCatalog(r.caller, callbackID, cfg, auth)
	if errFetch != nil {
		if ok {
			return cloneCodeBuddyCatalog(cached.Catalog), nil
		}
		return codeBuddyCatalog{}, errFetch
	}
	r.mu.Lock()
	r.catalogCache[key] = codeBuddyCatalogCacheEntry{FetchedAt: time.Now(), Catalog: cloneCodeBuddyCatalog(catalog)}
	r.mu.Unlock()
	return catalog, nil
}

func fetchCodeBuddyCatalog(caller hostCaller, callbackID string, cfg pluginConfig, auth codeBuddyAuth) (codeBuddyCatalog, error) {
	if caller == nil {
		return codeBuddyCatalog{}, newPluginCallError("catalog_unavailable", "CodeBuddy host HTTP callback is unavailable", http.StatusServiceUnavailable, true)
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("X-API-Key", auth.APIKey)
	headers.Set("X-Product", "SaaS")
	headers.Set("User-Agent", cfg.CatalogUserAgent)
	response, errRequest := doHostHTTP(caller, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            cfg.CatalogEndpoint,
		Headers:        headers,
	})
	if errRequest != nil {
		return codeBuddyCatalog{}, newPluginCallError("catalog_unavailable", "CodeBuddy catalog request failed", http.StatusBadGateway, true)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return codeBuddyCatalog{}, newPluginCallError("catalog_upstream_error", fmt.Sprintf("CodeBuddy catalog returned HTTP %d", status), status, status == http.StatusTooManyRequests || status >= 500)
	}
	if len(response.Body) == 0 || len(response.Body) > maxCodeBuddyCatalogBytes {
		return codeBuddyCatalog{}, newPluginCallError("catalog_invalid_response", "CodeBuddy catalog response is empty or oversized", http.StatusBadGateway, true)
	}
	return parseCodeBuddyCatalog(response.Body)
}

func parseCodeBuddyCatalog(raw []byte) (codeBuddyCatalog, error) {
	var response codeBuddyCatalogResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return codeBuddyCatalog{}, newPluginCallError("catalog_invalid_response", "CodeBuddy catalog returned malformed JSON", http.StatusBadGateway, true)
	}
	if response.Code != 0 {
		return codeBuddyCatalog{}, newPluginCallError("catalog_upstream_error", "CodeBuddy catalog rejected the request", http.StatusBadGateway, true)
	}
	if response.Data == nil {
		return codeBuddyCatalog{}, newPluginCallError("catalog_invalid_response", "CodeBuddy catalog response has no data", http.StatusBadGateway, true)
	}

	modelByID := make(map[string]codeBuddyCatalogModel, len(response.Data.Models))
	modelOrder := make([]string, 0, len(response.Data.Models))
	for _, model := range response.Data.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := modelByID[id]; exists {
			continue
		}
		model.ID = id
		modelByID[id] = model
		modelOrder = append(modelOrder, id)
	}

	allowed := make(map[string]struct{}, len(modelByID))
	selectedAgent := ""
	for _, preferredAgent := range []string{"cli", "craft"} {
		for _, agent := range response.Data.Agents {
			if !strings.EqualFold(strings.TrimSpace(agent.Name), preferredAgent) {
				continue
			}
			selectedAgent = preferredAgent
			for _, rawID := range agent.Models {
				if id := strings.TrimSpace(rawID); id != "" {
					allowed[id] = struct{}{}
				}
			}
		}
		if selectedAgent != "" {
			break
		}
	}
	if selectedAgent == "" {
		return codeBuddyCatalog{}, newPluginCallError("catalog_empty", "CodeBuddy catalog returned no CLI or craft model set", http.StatusServiceUnavailable, true)
	}
	if len(allowed) == 0 {
		return codeBuddyCatalog{}, newPluginCallError("catalog_empty", fmt.Sprintf("CodeBuddy %s catalog returned no authorized models", selectedAgent), http.StatusServiceUnavailable, true)
	}

	models := make([]pluginapi.ModelInfo, 0, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, id := range modelOrder {
		if _, ok := allowed[id]; !ok {
			continue
		}
		models = append(models, codeBuddyModelInfo(modelByID[id]))
		seen[id] = struct{}{}
	}
	missingIDs := make([]string, 0)
	for id := range allowed {
		if _, ok := seen[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}
	sort.Strings(missingIDs)
	for _, id := range missingIDs {
		models = append(models, minimalCodeBuddyModel(id))
	}
	if len(models) == 0 {
		return codeBuddyCatalog{}, newPluginCallError("catalog_empty", "CodeBuddy catalog returned no authorized models", http.StatusServiceUnavailable, true)
	}
	return codeBuddyCatalog{Models: models, Allowed: allowed, EnterpriseID: strings.TrimSpace(response.Data.EnterpriseID)}, nil
}

func codeBuddyModelInfo(model codeBuddyCatalogModel) pluginapi.ModelInfo {
	displayName := strings.TrimSpace(model.Name)
	if displayName == "" {
		displayName = model.ID
	}
	description := strings.TrimSpace(model.DescriptionZH)
	if description == "" {
		description = strings.TrimSpace(model.DescriptionEN)
	}
	contextLength := model.MaxInputTokens
	if model.MaxAllowedSize > contextLength {
		contextLength = model.MaxAllowedSize
	}
	inputModalities := []string{"text"}
	if model.SupportsImages && !model.DisabledMultimodal {
		inputModalities = append(inputModalities, "image")
	}
	var thinking *pluginapi.ThinkingSupport
	if model.SupportsReasoning || model.OnlyReasoning || strings.TrimSpace(model.Reasoning.Effort) != "" {
		levels := []string{}
		if effort := strings.TrimSpace(model.Reasoning.Effort); effort != "" {
			levels = append(levels, effort)
		}
		thinking = &pluginapi.ThinkingSupport{Levels: levels, ZeroAllowed: !model.OnlyReasoning}
	}
	return pluginapi.ModelInfo{
		ID:                         model.ID,
		Name:                       model.ID,
		Object:                     "model",
		OwnedBy:                    pluginIdentifier,
		DisplayName:                displayName,
		Description:                description,
		Type:                       "chat",
		InputTokenLimit:            model.MaxInputTokens,
		OutputTokenLimit:           model.MaxOutputTokens,
		ContextLength:              contextLength,
		MaxCompletionTokens:        model.MaxOutputTokens,
		SupportedGenerationMethods: []string{"chat"},
		SupportedInputModalities:   inputModalities,
		SupportedOutputModalities:  []string{"text"},
		Thinking:                   thinking,
		UserDefined:                true,
	}
}

func minimalCodeBuddyModel(id string) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:                         id,
		Name:                       id,
		Object:                     "model",
		OwnedBy:                    pluginIdentifier,
		DisplayName:                id,
		SupportedGenerationMethods: []string{"chat"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
		UserDefined:                true,
	}
}

func (r *pluginRuntime) codeBuddyModelAllowed(auth codeBuddyAuth, model, callbackID string) error {
	catalog, errCatalog := r.catalogForAuth(auth, callbackID)
	if errCatalog != nil {
		return errCatalog
	}
	if _, ok := catalog.Allowed[strings.TrimSpace(model)]; !ok {
		return newPluginCallError("unsupported_model", "CodeBuddy model is not authorized for the selected credential", http.StatusBadRequest, false)
	}
	return nil
}

func codeBuddyCatalogCacheKey(auth codeBuddyAuth, endpoint, userAgent string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{endpoint, userAgent, auth.APIKey}, "\x00")))
	return hex.EncodeToString(hash[:])
}

func cloneCodeBuddyCatalog(input codeBuddyCatalog) codeBuddyCatalog {
	return codeBuddyCatalog{
		Models:       cloneCodeBuddyModels(input.Models),
		Allowed:      cloneCodeBuddyAllowed(input.Allowed),
		EnterpriseID: input.EnterpriseID,
	}
}

func cloneCodeBuddyModels(input []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	return append([]pluginapi.ModelInfo(nil), input...)
}

func cloneCodeBuddyAllowed(input map[string]struct{}) map[string]struct{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(input))
	for key := range input {
		out[key] = struct{}{}
	}
	return out
}
