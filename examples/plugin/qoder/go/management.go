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

func (r *pluginRuntime) handleManagement(raw []byte) (pluginapi.ManagementResponse, error) {
	var req rpcManagementRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ManagementResponse{}, errDecode
	}
	if !strings.EqualFold(strings.TrimSpace(req.Method), http.MethodGet) {
		return managementJSONResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"}), nil
	}
	if !strings.HasSuffix(strings.TrimRight(strings.TrimSpace(req.Path), "/"), "/plugins/qoder/summary") {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "not_found"}), nil
	}
	authIndex := strings.TrimSpace(req.Query.Get("auth_index"))
	if authIndex == "" {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "auth_index_required"}), nil
	}
	if r.caller == nil {
		return managementJSONResponse(http.StatusServiceUnavailable, map[string]any{"error": "host_callback_unavailable"}), nil
	}
	authFile, errAuthFile := callQoderHostAuthGet(r.caller, authIndex)
	if errAuthFile != nil || len(authFile.JSON) == 0 {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "auth_not_found"}), nil
	}
	auth, errAuth := parseStoredAuth(authFile.JSON)
	if errAuth != nil {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid_auth"}), nil
	}

	cfg := r.loadedConfig()
	cacheKey := qoderSummaryCacheKey(auth, cfg.OpenAPIEndpoint, authIndex)
	r.mu.Lock()
	cached, okCached := r.summaryCache[cacheKey]
	r.mu.Unlock()
	if okCached && time.Since(cached.FetchedAt) < qoderSummaryCacheTTL {
		result := cloneQoderSummary(cached.Summary)
		result.Cached = true
		return managementJSONResponse(http.StatusOK, result), nil
	}

	name := strings.TrimSpace(authFile.Name)
	label := auth.Label
	if runtimeInfo, errRuntime := callQoderHostAuthGetRuntime(r.caller, authIndex); errRuntime == nil {
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
		label = "Qoder PAT"
	}

	result := r.qoderSummary(auth, req.HostCallbackID)
	result.AuthIndex = authIndex
	result.Name = name
	result.Label = label
	result.UpdatedAt = time.Now().UTC()

	r.mu.Lock()
	if r.summaryCache == nil {
		r.summaryCache = make(map[string]qoderSummaryCacheEntry)
	}
	r.summaryCache[cacheKey] = qoderSummaryCacheEntry{FetchedAt: time.Now(), Summary: cloneQoderSummary(result)}
	r.mu.Unlock()
	return managementJSONResponse(http.StatusOK, result), nil
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

func callQoderHostAuthGet(caller hostCaller, authIndex string) (pluginapi.HostAuthGetResponse, error) {
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

func callQoderHostAuthGetRuntime(caller hostCaller, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
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

func qoderSummaryCacheKey(auth qoderAuth, endpoint, authIndex string) string {
	return qoderTokenCacheKey(auth, endpoint) + ":summary:" + strings.TrimSpace(authIndex)
}

func cloneQoderSummary(input qoderSummary) qoderSummary {
	output := input
	output.Quota.Packages = append([]qoderQuotaPackage(nil), input.Quota.Packages...)
	return output
}
