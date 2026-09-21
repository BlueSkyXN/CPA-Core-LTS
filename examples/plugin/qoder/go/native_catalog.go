package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxNativeCatalogBytes = 2 * 1024 * 1024

type nativeCatalogEntry struct {
	models     []pluginapi.ModelInfo
	rawConfigs map[string]json.RawMessage
	expires    time.Time
}
type catalogFlight struct {
	done  chan struct{}
	entry nativeCatalogEntry
	err   error
}

func (r *pluginRuntime) nativeModels(auth qoderAuth, callbackID string, cfg pluginConfig) ([]pluginapi.ModelInfo, error) {
	// 手动目录继续具有原来的精确覆盖语义，不因升级而扩展账号可用模型。
	if len(cfg.DirectModels) > 0 {
		return cloneModels(configuredDirectModels(cfg.DirectModels)), nil
	}
	if cfg.DirectModelsEndpoint == "" {
		return nil, newPluginCallError("models_unavailable", "Configure direct_models or direct_models_endpoint", 503, false)
	}
	r.mu.Lock()
	epoch := r.generation
	key := sessionDigest([]string{auth.tokenSource(), cfg.OpenAPIEndpoint, cfg.DirectEndpoint, cfg.DirectModelsEndpoint, cfg.DirectCatalogFormat, cfg.DirectTokenMode, strconv.FormatUint(epoch, 10)})
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

func (r *pluginRuntime) fetchNativeCatalog(auth qoderAuth, callbackID string, cfg pluginConfig) (nativeCatalogEntry, error) {
	if callbackID == "" || r.caller == nil {
		return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Qoder catalog requires a host callback context", 503, true)
	}
	state, err := r.nativeToken(auth, callbackID, cfg, "")
	if err != nil {
		return nativeCatalogEntry{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		endpoint := cfg.DirectModelsEndpoint
		headers := http.Header{"Accept": {"application/json"}, "Authorization": {"Bearer " + state.Token}}
		if cfg.DirectCatalogFormat == "qoder" {
			if cfg.OpenAPIEndpoint == "" {
				return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Qoder COSY catalog requires openapi_endpoint", 503, false)
			}
			identity, err := doHostHTTP(r.caller, hostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodGet, URL: cfg.OpenAPIEndpoint + "/api/v1/userinfo", Headers: headers})
			if err != nil {
				return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Qoder user identity request failed", 502, true)
			}
			if identity.StatusCode != http.StatusOK {
				if attempt == 0 && auth.isPAT() && cfg.DirectTokenMode != "bearer" && qoderAuthRejected(identity.StatusCode, identity.Body) {
					state, err = r.nativeToken(auth, callbackID, cfg, state.Token)
					if err != nil {
						return nativeCatalogEntry{}, err
					}
					continue
				}
				return nativeCatalogEntry{}, qoderUpstreamError(identity.StatusCode, identity.Body)
			}
			if len(identity.Body) > maxQoderAccountBody {
				return nativeCatalogEntry{}, nativeCatalogError()
			}
			var user map[string]any
			if json.Unmarshal(identity.Body, &user) != nil {
				return nativeCatalogEntry{}, nativeCatalogError()
			}
			uid := stringValueFromMap(user, "id", "user_id", "userId")
			if uid == "" {
				return nativeCatalogEntry{}, nativeCatalogError()
			}
			u, errURL := url.Parse(endpoint)
			if errURL != nil {
				return nativeCatalogEntry{}, nativeCatalogError()
			}
			query := u.Query()
			query.Set("Encode", "1")
			u.RawQuery = query.Encode()
			endpoint = u.String()
			headers, err = qoderCatalogHeaders(endpoint, user, uid, state)
			if err != nil {
				return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Qoder catalog signing failed", 502, false)
			}
		}
		response, err := doHostHTTP(r.caller, hostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodGet, URL: endpoint, Headers: headers})
		if err != nil {
			return nativeCatalogEntry{}, newPluginCallError("catalog_unavailable", "Qoder catalog request failed", 502, true)
		}
		if response.StatusCode != http.StatusOK {
			if attempt == 0 && auth.isPAT() && cfg.DirectTokenMode != "bearer" && qoderAuthRejected(response.StatusCode, response.Body) {
				state, err = r.nativeToken(auth, callbackID, cfg, state.Token)
				if err != nil {
					return nativeCatalogEntry{}, err
				}
				continue
			}
			return nativeCatalogEntry{}, qoderUpstreamError(response.StatusCode, response.Body)
		}
		return parseNativeCatalog(response.Body, cfg.DirectCatalogFormat)
	}
	return nativeCatalogEntry{}, nativeCatalogError()
}

func nativeCatalogError() error {
	return newPluginCallError("models_schema_invalid", "Qoder catalog contains no valid enabled models or has an invalid schema", 502, true)
}

func parseNativeCatalog(raw []byte, format string) (nativeCatalogEntry, error) {
	entry := nativeCatalogEntry{rawConfigs: make(map[string]json.RawMessage)}
	if len(raw) == 0 || len(raw) > maxNativeCatalogBytes {
		return entry, nativeCatalogError()
	}
	var records []json.RawMessage
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		field := object["chat"]
		if format != "qoder" {
			field = object["data"]
			if len(field) == 0 {
				field = object["models"]
			}
		}
		if json.Unmarshal(field, &records) != nil {
			return entry, nativeCatalogError()
		}
	} else if format == "qoder" || json.Unmarshal(raw, &records) != nil {
		return entry, nativeCatalogError()
	}
	if len(records) > 256 {
		return entry, nativeCatalogError()
	}
	for _, rawModel := range records {
		original := rawModel
		var stringID string
		if format != "qoder" && json.Unmarshal(rawModel, &stringID) == nil {
			if strings.TrimSpace(stringID) == "" {
				continue
			}
			rawModel, _ = json.Marshal(map[string]string{"id": stringID})
		}
		var value map[string]any
		if json.Unmarshal(rawModel, &value) != nil || value == nil {
			return entry, nativeCatalogError()
		}
		id := stringValueFromMap(value, "id", "model", "value")
		if format == "qoder" {
			id = stringValueFromMap(value, "key")
			if enabled, _ := value["enable"].(bool); !enabled {
				continue
			}
		} else if value["is_enabled"] == false || value["isEnabled"] == false {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
			return entry, nativeCatalogError()
		}
		if _, seen := entry.rawConfigs[id]; seen {
			continue
		}
		model, err := decodeNativeCatalogModel(rawModel)
		if err != nil {
			return entry, nativeCatalogError()
		}
		model.ID = id
		model.DisplayName = qoderDisplayName(id, stringValueFromMap(value, "display_name", "displayName", "name"))
		if model.MaxInputTokens < 0 || model.MaxOutputTokens < 0 {
			return entry, nativeCatalogError()
		}
		info := qoderModelInfo(model)
		if model.DefaultContextWindow > 0 {
			info.ContextLength = model.DefaultContextWindow
		} else if format == "qoder" {
			// COSY 目录仅声明当前默认窗口，不自动升级到更大的计费档位。
			info.ContextLength = model.MaxInputTokens
		}
		entry.models = append(entry.models, info)
		entry.rawConfigs[id] = append(json.RawMessage(nil), original...)
	}
	if len(entry.models) == 0 {
		return entry, nativeCatalogError()
	}
	return entry, nil
}

// 保留旧 Direct 目录接受的 camelCase 元数据，snake_case 显式值优先。
func decodeNativeCatalogModel(raw []byte) (runnerModel, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return runnerModel{}, err
	}
	for canonical, alias := range map[string]string{
		"display_name": "displayName", "is_default": "isDefault", "is_enabled": "isEnabled",
		"is_reasoning": "isReasoning", "is_vl": "isVl", "max_input_tokens": "maxInputTokens",
		"max_output_tokens": "maxOutputTokens", "reasoning_efforts": "efforts",
		"default_reasoning_effort": "defaultEffort", "supports_disabled": "supportsDisabled",
		"available_context_windows": "availableContextWindows", "default_context_window": "defaultContextWindow",
	} {
		if value := fields[canonical]; len(value) == 0 || bytes.Equal(value, []byte("null")) {
			if alternate, exists := fields[alias]; exists {
				fields[canonical] = alternate
			}
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return runnerModel{}, err
	}
	var model runnerModel
	err = json.Unmarshal(normalized, &model)
	return model, err
}
