package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	copilotLoginInfoSuffix = "/plugins/copilot/login-info"
	copilotSummarySuffix   = "/plugins/copilot/summary"
)

// handleManagement 永远返回 (resp, nil)：业务错误用 HTTP 状态码表达，不走 errorEnvelope。
func (r *pluginRuntime) handleManagement(raw []byte) (pluginapi.ManagementResponse, error) {
	var req rpcManagementRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ManagementResponse{}, errDecode
	}
	if !strings.EqualFold(strings.TrimSpace(req.Method), http.MethodGet) {
		return managementJSONResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"}), nil
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	switch {
	case strings.HasSuffix(path, copilotLoginInfoSuffix):
		return r.handleLoginInfo(req), nil
	case strings.HasSuffix(path, copilotSummarySuffix):
		return r.handleSummary(req), nil
	default:
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "not_found"}), nil
	}
}

func (r *pluginRuntime) handleLoginInfo(req rpcManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	if state == "" {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "state_required"})
	}
	snapshot, ok := r.loginFlowSnapshot(state)
	if !ok {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "login_not_found"})
	}
	return managementJSONResponse(http.StatusOK, snapshot)
}

func (r *pluginRuntime) handleSummary(req rpcManagementRequest) pluginapi.ManagementResponse {
	authIndex := strings.TrimSpace(req.Query.Get("auth_index"))
	if authIndex == "" {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "auth_index_required"})
	}
	if r.caller == nil {
		return managementJSONResponse(http.StatusServiceUnavailable, map[string]any{"error": "host_callback_unavailable"})
	}
	authFile, errAuthFile := callCopilotHostAuthGet(r.caller, authIndex)
	if errAuthFile != nil || len(authFile.JSON) == 0 {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "auth_not_found"})
	}
	auth, errAuth := parseStoredAuth(authFile.JSON)
	if errAuth != nil {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid_auth"})
	}
	cfg := r.loadedConfig()
	cacheKey := copilotSummaryCacheKey(auth, cfg.GitHubAPIEndpoint, authIndex)
	r.mu.Lock()
	epoch := r.generation
	cached, okCached := r.summaryCache[cacheKey]
	r.mu.Unlock()
	if okCached && time.Since(cached.FetchedAt) < copilotSummaryCacheTTL {
		result := cloneCopilotSummary(cached.Summary)
		result.Cached = true
		return managementJSONResponse(http.StatusOK, result)
	}
	name := strings.TrimSpace(authFile.Name)
	label := auth.Label
	if runtimeInfo, errRuntime := callCopilotHostAuthGetRuntime(r.caller, authIndex); errRuntime == nil {
		if strings.TrimSpace(runtimeInfo.Auth.Name) != "" {
			name = strings.TrimSpace(runtimeInfo.Auth.Name)
		}
		if strings.TrimSpace(runtimeInfo.Auth.Label) != "" {
			label = strings.TrimSpace(runtimeInfo.Auth.Label)
		}
	}
	if name == "" {
		name = authIndex
	}
	if label == "" {
		label = "GitHub Copilot"
	}
	result := r.copilotSummary(auth, req.HostCallbackID, cfg)
	result.AuthIndex = authIndex
	result.Name = name
	result.Label = label
	result.UpdatedAt = time.Now().UTC()
	r.mu.Lock()
	// configure() 会整体重置 summaryCache；旧配置代次的在途结果不再写回新缓存。
	if r.generation == epoch {
		if r.summaryCache == nil {
			r.summaryCache = make(map[string]copilotSummaryCacheEntry)
		}
		r.summaryCache[cacheKey] = copilotSummaryCacheEntry{FetchedAt: time.Now(), Summary: cloneCopilotSummary(result)}
	}
	r.mu.Unlock()
	return managementJSONResponse(http.StatusOK, result)
}

func managementJSONResponse(status int, value any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		raw = []byte(`{"error":"management_response_encode_failed"}`)
		status = http.StatusInternalServerError
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       raw,
	}
}

func callCopilotHostAuthGet(caller hostCaller, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	raw, errCall := caller.Call(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("host auth get failed")
	}
	var response pluginapi.HostAuthGetResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host auth get response")
	}
	return response, nil
}

func callCopilotHostAuthGetRuntime(caller hostCaller, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	raw, errCall := caller.Call(pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("host auth runtime get failed")
	}
	var response pluginapi.HostAuthGetRuntimeResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("decode host auth runtime response")
	}
	return response, nil
}
