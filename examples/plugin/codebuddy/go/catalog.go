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
	codeBuddyCatalogStaleTTL = 5 * time.Minute
	maxCodeBuddyCatalogBytes = 2 * 1024 * 1024
)

type codeBuddyCatalog struct {
	Models       []pluginapi.ModelInfo
	Allowed      map[string]struct{}
	EnterpriseID string
	Scene        string
	FetchedAt    time.Time
	Stale        bool
	Hints        map[string]codeBuddyModelHints
}

// 供应商扩展只放在插件 Summary，不改变 Core 的公共 ModelInfo/ABI。
type codeBuddyModelHints struct {
	Credits          string  `json:"credits_hint,omitempty"`
	DefaultEffort    string  `json:"default_effort,omitempty"`
	SupportedLengths []int64 `json:"supported_context_lengths,omitempty"`
}

type codeBuddyCatalogCacheEntry struct {
	FetchedAt time.Time
	Catalog   codeBuddyCatalog
}

type codeBuddyCatalogFlight struct {
	done    chan struct{}
	catalog codeBuddyCatalog
	err     error
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
	ID                 string `json:"id"`
	Name               string `json:"name"`
	DescriptionEN      string `json:"descriptionEn"`
	DescriptionZH      string `json:"descriptionZh"`
	MaxAllowedSize     int64  `json:"maxAllowedSize"`
	MaxInputTokens     int64  `json:"maxInputTokens"`
	MaxOutputTokens    int64  `json:"maxOutputTokens"`
	DisabledMultimodal bool   `json:"disabledMultimodal"`
	OnlyReasoning      bool   `json:"onlyReasoning"`
	SupportsImages     bool   `json:"supportsImages"`
	SupportsReasoning  bool   `json:"supportsReasoning"`
	SupportsToolCall   bool   `json:"supportsToolCall"`
	Credits            string `json:"credits"`
	CanDisableThinking *bool  `json:"canDisableThinking"`
	ContextWindow      struct {
		DefaultLength    int64   `json:"defaultLength"`
		SupportedLengths []int64 `json:"supportedLengths"`
	} `json:"contextWindow"`
	Reasoning codeBuddyCatalogReasoning `json:"reasoning"`
}

type codeBuddyCatalogReasoning struct {
	Effort             string   `json:"effort"`
	Summary            string   `json:"summary"`
	DefaultEffort      string   `json:"defaultEffort"`
	SupportedEfforts   []string `json:"supportedEfforts"`
	CanDisableThinking *bool    `json:"canDisableThinking"`
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
	r.mu.Lock()
	cfg, generation := r.config, r.generation
	key := fmt.Sprintf("%d:%s", generation, codeBuddyCatalogCacheKey(auth, cfg.CatalogEndpoint, cfg.CatalogUserAgent))
	cached, ok := r.catalogCache[key]
	r.mu.Unlock()
	if ok && time.Since(cached.FetchedAt) < codeBuddyCatalogCacheTTL {
		return cloneCodeBuddyCatalog(cached.Catalog), nil
	}
	if strings.TrimSpace(callbackID) == "" {
		if ok && time.Since(cached.FetchedAt) < codeBuddyCatalogStaleTTL {
			result := cloneCodeBuddyCatalog(cached.Catalog)
			result.Stale = true
			return result, nil
		}
		return codeBuddyCatalog{}, newPluginCallError("catalog_unavailable", "CodeBuddy catalog requires a host callback context", http.StatusServiceUnavailable, true)
	}
	r.mu.Lock()
	if flight := r.catalogFlights[key]; flight != nil {
		r.mu.Unlock()
		<-flight.done
		return cloneCodeBuddyCatalog(flight.catalog), flight.err
	}
	// 前一次请求可能在两次加锁之间已经完成。
	if latest, exists := r.catalogCache[key]; exists && time.Since(latest.FetchedAt) < codeBuddyCatalogCacheTTL {
		r.mu.Unlock()
		return cloneCodeBuddyCatalog(latest.Catalog), nil
	}
	flight := &codeBuddyCatalogFlight{done: make(chan struct{})}
	r.catalogFlights[key] = flight
	r.mu.Unlock()
	catalog, errFetch := fetchCodeBuddyCatalog(r.caller, callbackID, cfg, auth)
	if errFetch != nil {
		// 明确拒绝不能使用旧权限名单掩盖；仅短暂网络/服务故障允许有界降级。
		callErr, typed := errFetch.(*pluginCallError)
		if ok && typed && callErr.retryable && (callErr.code == "catalog_unavailable" || callErr.code == "catalog_upstream_error") && time.Since(cached.FetchedAt) < codeBuddyCatalogStaleTTL {
			catalog = cloneCodeBuddyCatalog(cached.Catalog)
			catalog.Stale = true
			errFetch = nil
		}
	}
	if errFetch == nil && !catalog.Stale {
		catalog.FetchedAt = time.Now().UTC()
	}
	r.mu.Lock()
	if r.generation == generation {
		if errFetch == nil && !catalog.Stale {
			r.catalogCache[key] = codeBuddyCatalogCacheEntry{FetchedAt: catalog.FetchedAt, Catalog: cloneCodeBuddyCatalog(catalog)}
		}
		if errFetch != nil {
			delete(r.catalogCache, key)
		}
	}
	flight.catalog, flight.err = cloneCodeBuddyCatalog(catalog), errFetch
	delete(r.catalogFlights, key)
	close(flight.done)
	r.mu.Unlock()
	return catalog, errFetch
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
		return codeBuddyCatalog{}, newPluginCallError("catalog_rejected", "CodeBuddy catalog rejected the request", http.StatusBadGateway, false)
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
	hints := make(map[string]codeBuddyModelHints, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, id := range modelOrder {
		if _, ok := allowed[id]; !ok {
			continue
		}
		models = append(models, codeBuddyModelInfo(modelByID[id]))
		model := modelByID[id]
		hints[id] = codeBuddyModelHints{Credits: strings.TrimSpace(model.Credits), DefaultEffort: codeBuddyDefaultEffort(model.Reasoning), SupportedLengths: append([]int64(nil), model.ContextWindow.SupportedLengths...)}
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
	return codeBuddyCatalog{Models: models, Allowed: allowed, EnterpriseID: strings.TrimSpace(response.Data.EnterpriseID), Scene: selectedAgent, Hints: hints}, nil
}

func codeBuddyDefaultEffort(reasoning codeBuddyCatalogReasoning) string {
	if value := strings.TrimSpace(reasoning.DefaultEffort); value != "" {
		return value
	}
	return strings.TrimSpace(reasoning.Effort)
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
	if model.ContextWindow.DefaultLength > 0 {
		contextLength = model.ContextWindow.DefaultLength
	}
	inputModalities := []string{"text"}
	if model.SupportsImages && !model.DisabledMultimodal {
		inputModalities = append(inputModalities, "image")
	}
	var thinking *pluginapi.ThinkingSupport
	if model.SupportsReasoning || model.OnlyReasoning || codeBuddyDefaultEffort(model.Reasoning) != "" || len(model.Reasoning.SupportedEfforts) > 0 {
		levels := []string{}
		seen := make(map[string]bool)
		for _, effort := range model.Reasoning.SupportedEfforts {
			effort = strings.TrimSpace(effort)
			if effort != "" && !seen[effort] {
				levels = append(levels, effort)
				seen[effort] = true
			}
		}
		if len(levels) == 0 {
			if effort := codeBuddyDefaultEffort(model.Reasoning); effort != "" {
				levels = append(levels, effort)
			}
		}
		zeroAllowed := !model.OnlyReasoning
		if model.CanDisableThinking != nil {
			zeroAllowed = *model.CanDisableThinking
		}
		if model.Reasoning.CanDisableThinking != nil {
			zeroAllowed = *model.Reasoning.CanDisableThinking
		}
		thinking = &pluginapi.ThinkingSupport{Levels: levels, ZeroAllowed: zeroAllowed}
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
	hints := make(map[string]codeBuddyModelHints, len(input.Hints))
	for id, hint := range input.Hints {
		hint.SupportedLengths = append([]int64(nil), hint.SupportedLengths...)
		hints[id] = hint
	}
	return codeBuddyCatalog{
		Models:       cloneCodeBuddyModels(input.Models),
		Allowed:      cloneCodeBuddyAllowed(input.Allowed),
		EnterpriseID: input.EnterpriseID,
		Scene:        input.Scene, FetchedAt: input.FetchedAt, Stale: input.Stale, Hints: hints,
	}
}

func cloneCodeBuddyModels(input []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	output := append([]pluginapi.ModelInfo(nil), input...)
	for i := range output {
		output[i].SupportedGenerationMethods = append([]string(nil), input[i].SupportedGenerationMethods...)
		output[i].SupportedInputModalities = append([]string(nil), input[i].SupportedInputModalities...)
		output[i].SupportedOutputModalities = append([]string(nil), input[i].SupportedOutputModalities...)
		if input[i].Thinking != nil {
			value := *input[i].Thinking
			value.Levels = append([]string(nil), value.Levels...)
			output[i].Thinking = &value
		}
	}
	return output
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
