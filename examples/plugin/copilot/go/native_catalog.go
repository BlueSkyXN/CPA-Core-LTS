package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	maxNativeCatalogBytes = 2 * 1024 * 1024
	maxNativeCatalogItems = 256
)

type nativeCatalogEntry struct {
	models  []pluginapi.ModelInfo
	expires time.Time
}

type catalogFlight struct {
	done  chan struct{}
	entry nativeCatalogEntry
	err   error
}

// nativeModels 按凭证缓存账号目录；目录失败返回明确错误码，绝不伪造静态目录。
func (r *pluginRuntime) nativeModels(auth copilotAuth, callbackID string, cfg pluginConfig) ([]pluginapi.ModelInfo, error) {
	if cfg.CopilotAPIEndpoint == "" {
		return nil, newPluginCallError("models_unavailable", "Copilot API endpoint is not configured", http.StatusServiceUnavailable, false)
	}
	r.mu.Lock()
	epoch := r.generation
	key := sessionDigest([]string{copilotTokenCacheKey(auth, cfg.GitHubAPIEndpoint), cfg.CopilotAPIEndpoint, strconv.FormatUint(epoch, 10)})
	if cached, ok := r.nativeCatalogs[key]; ok && time.Now().Before(cached.expires) {
		r.mu.Unlock()
		return cloneModels(cached.models), nil
	}
	if flight := r.catalogFlights[key]; flight != nil {
		r.mu.Unlock()
		<-flight.done
		return cloneModels(flight.entry.models), flight.err
	}
	flight := &catalogFlight{done: make(chan struct{})}
	r.catalogFlights[key] = flight
	r.mu.Unlock()
	entry, err := r.fetchNativeCatalog(auth, callbackID, cfg)
	r.mu.Lock()
	if err == nil && epoch == r.generation {
		entry.expires = time.Now().Add(cfg.ModelCacheTTL)
		r.nativeCatalogs[key] = entry
	}
	flight.entry, flight.err = entry, err
	delete(r.catalogFlights, key)
	close(flight.done)
	r.mu.Unlock()
	return cloneModels(entry.models), err
}

func (r *pluginRuntime) fetchNativeCatalog(auth copilotAuth, callbackID string, cfg pluginConfig) (nativeCatalogEntry, error) {
	if callbackID == "" || r.caller == nil {
		return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Copilot catalog requires a host callback context", http.StatusServiceUnavailable, true)
	}
	state, err := r.cachedCopilotToken(auth, callbackID, cfg, "")
	if err != nil {
		return nativeCatalogEntry{}, err
	}
	// 401 只重试一次：带 rejectedToken 强制重换短时 token 后重放目录请求。
	for attempt := 0; attempt < 2; attempt++ {
		response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
			HostCallbackID: callbackID, Method: http.MethodGet,
			URL: cfg.CopilotAPIEndpoint + "/models", Headers: copilotCatalogHeaders(cfg, state.Token),
		})
		if errRequest != nil {
			return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Copilot catalog request failed", http.StatusBadGateway, true)
		}
		if response.StatusCode != http.StatusOK {
			if attempt == 0 && response.StatusCode == http.StatusUnauthorized {
				state, err = r.cachedCopilotToken(auth, callbackID, cfg, state.Token)
				if err != nil {
					return nativeCatalogEntry{}, err
				}
				continue
			}
			return nativeCatalogEntry{}, copilotUpstreamError(response.StatusCode, response.Body)
		}
		return parseNativeCatalog(response.Body, cfg.ExcludedModelPrefixes)
	}
	return nativeCatalogEntry{}, newPluginCallError("models_schema_invalid", "Copilot catalog contains no valid enabled models", http.StatusBadGateway, true)
}

func copilotCatalogError() error {
	return newPluginCallError("models_schema_invalid", "Copilot catalog contains no valid enabled models or has an invalid schema", http.StatusBadGateway, true)
}

func parseNativeCatalog(raw []byte, excludedPrefixes []string) (nativeCatalogEntry, error) {
	entry := nativeCatalogEntry{}
	if len(raw) == 0 || len(raw) > maxNativeCatalogBytes {
		return entry, copilotCatalogError()
	}
	var object struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &object) != nil {
		return entry, copilotCatalogError()
	}
	if len(object.Data) > maxNativeCatalogItems {
		return entry, copilotCatalogError()
	}
	seen := make(map[string]struct{}, len(object.Data))
	for _, rawModel := range object.Data {
		var model copilotCatalogModel
		if json.Unmarshal(rawModel, &model) != nil {
			return entry, copilotCatalogError()
		}
		id := strings.TrimSpace(model.ID)
		if id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
			return entry, copilotCatalogError()
		}
		// policy.state 一旦声明，只有 enabled 才发布；未声明视为可用。
		if model.Policy != nil && !strings.EqualFold(strings.TrimSpace(model.Policy.State), "enabled") {
			continue
		}
		// 只发布声明了端点的现行模型。上游目录里的存档老模型（gpt-4o/4.1/3.5 等）
		// 没有 supported_endpoints 声明、picker 关闭；embeddings 模型无端点概念但
		// embeddings 线使用，按 capabilities.type 保留。
		if !catalogModelPublishable(model) {
			continue
		}
		if modelExcludedByPrefix(id, excludedPrefixes) {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		entry.models = append(entry.models, copilotModelInfo(model))
	}
	if len(entry.models) == 0 {
		return entry, copilotCatalogError()
	}
	return entry, nil
}

func modelExcludedByPrefix(id string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// copilotCatalogHeaders 用于 GET /models：与 chat 共用身份串，但保持 JSON Accept。
func copilotCatalogHeaders(cfg pluginConfig, token string) http.Header {
	headers := copilotIdentityHeaders(cfg)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Openai-Intent", "conversation-agent")
	return headers
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

// catalogModelPublishable 判断目录条目是否面向本插件发布：
//   - embeddings 类型：无端点声明也保留（embeddings 线按 ID 匹配使用）；
//   - 其余类型：必须声明 supported_endpoints，未声明的存档老模型不发布。
func catalogModelPublishable(model copilotCatalogModel) bool {
	if model.Capabilities != nil && strings.EqualFold(strings.TrimSpace(model.Capabilities.Type), "embeddings") {
		return true
	}
	return len(model.SupportedEndpoints) > 0
}
