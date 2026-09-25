package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// embeddingsRequestPayload 校验并规范化 embeddings 请求：模型必须是账号目录中的
// 精确 ID，input 必填，其余字段原样透传给上游。
func embeddingsRequestPayload(req pluginapi.ExecutorRequest) ([]byte, error) {
	if req.Format != "" && req.Format != "embeddings" {
		return nil, newPluginCallError("unsupported_format", "Copilot embeddings executor accepts the embeddings format only", http.StatusBadRequest, false)
	}
	if err := validateCanonicalModel(req.Model); err != nil {
		return nil, err
	}
	if len(req.Payload) == 0 || len(req.Payload) > maxExecutorBodyBytes {
		return nil, newPluginCallError("invalid_request", "Copilot embeddings request is empty or oversized", http.StatusBadRequest, false)
	}
	var body map[string]any
	if json.Unmarshal(req.Payload, &body) != nil || body == nil {
		return nil, newPluginCallError("invalid_request", "Copilot embeddings requires a JSON object payload", http.StatusBadRequest, false)
	}
	input := body["input"]
	switch typed := input.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, newPluginCallError("invalid_request", "Copilot embeddings requires input", http.StatusBadRequest, false)
		}
	case []any:
		if len(typed) == 0 {
			return nil, newPluginCallError("invalid_request", "Copilot embeddings requires a non-empty input array", http.StatusBadRequest, false)
		}
	default:
		return nil, newPluginCallError("invalid_request", "Copilot embeddings requires input", http.StatusBadRequest, false)
	}
	// Copilot 网关的 /embeddings 只接受数组 input（OpenAI 官方两种都收），
	// 客户端侧两种形状都接受，发往上游前统一归一化为数组。
	if value, ok := body["input"].(string); ok {
		body["input"] = []any{value}
	}
	body["model"] = req.Model
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > maxExecutorBodyBytes {
		return nil, newPluginCallError("direct_request_too_large", "Copilot embeddings request exceeds the body limit", http.StatusRequestEntityTooLarge, false)
	}
	return raw, nil
}

// executeEmbeddings 把 embeddings 请求透传到上游 /embeddings；非流式，401 只
// 重换票重试一次，与 chat 共享账号目录校验、换票缓存和执行生命周期。
func (r *pluginRuntime) executeEmbeddings(req rpcExecutorRequest) (pluginapi.ExecutorResponse, error) {
	body, err := embeddingsRequestPayload(req.ExecutorRequest)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	auth, err := parseStoredAuth(req.StorageJSON)
	if err != nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_auth", "Copilot credential is invalid", http.StatusUnauthorized, false)
	}
	exec, err := r.registerNative(req, auth)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	defer r.releaseNative(exec)
	cfg := r.loadedConfig()
	if cfg.CopilotAPIEndpoint == "" || validateDirectURL(cfg.CopilotAPIEndpoint, "copilot_api_endpoint") != nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("direct_endpoint_required", "Copilot API endpoint is not configured", http.StatusServiceUnavailable, false)
	}
	models, err := r.nativeModels(auth, req.HostCallbackID, cfg)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	allowed := false
	for _, model := range models {
		if model.ID == req.Model {
			allowed = true
			break
		}
	}
	if !allowed {
		return pluginapi.ExecutorResponse{}, newPluginCallError("unsupported_model", "Copilot model must match an exact catalog ID", http.StatusBadRequest, false)
	}
	state, err := r.cachedCopilotToken(auth, req.HostCallbackID, cfg, "")
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if exec.isCanceled() {
			return pluginapi.ExecutorResponse{}, nativeCanceled()
		}
		headers := copilotCatalogHeaders(cfg, state.Token)
		response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
			HostCallbackID: req.HostCallbackID, Method: http.MethodPost,
			URL: cfg.CopilotAPIEndpoint + "/embeddings", Headers: headers, Body: body,
		})
		if errRequest != nil {
			return pluginapi.ExecutorResponse{}, newPluginCallError("connection_lifecycle", "Copilot embeddings request failed", http.StatusBadGateway, true)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if attempt == 0 && response.StatusCode == http.StatusUnauthorized {
				state, err = r.cachedCopilotToken(auth, req.HostCallbackID, cfg, state.Token)
				if err != nil {
					return pluginapi.ExecutorResponse{}, err
				}
				continue
			}
			return pluginapi.ExecutorResponse{}, copilotUpstreamError(response.StatusCode, response.Body)
		}
		if len(response.Body) == 0 || len(response.Body) > maxNativeResponseBytes {
			return pluginapi.ExecutorResponse{}, nativeInvalidResponse()
		}
		return pluginapi.ExecutorResponse{
			Payload:  response.Body,
			Headers:  http.Header{"Content-Type": {"application/json"}},
			Metadata: map[string]any{"usage_provenance": "provider_reported_unverified"},
		}, nil
	}
	return pluginapi.ExecutorResponse{}, newPluginCallError("auth_expired", "Copilot authentication was rejected", http.StatusUnauthorized, false)
}
