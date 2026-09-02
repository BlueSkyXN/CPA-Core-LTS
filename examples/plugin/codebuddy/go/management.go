package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const codeBuddySummaryCacheTTL = time.Minute

type codeBuddySummary struct {
	Provider   string            `json:"provider"`
	AuthIndex  string            `json:"auth_index"`
	Name       string            `json:"name,omitempty"`
	Label      string            `json:"label,omitempty"`
	Credential summaryCredential `json:"credential"`
	Account    summaryAccount    `json:"account"`
	Plan       *summaryPlan      `json:"plan,omitempty"`
	Quota      codeBuddyQuota    `json:"quota"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Cached     bool              `json:"cached"`
}

type summaryCredential struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type summaryAccount struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Source string `json:"source,omitempty"`
}

type summaryPlan struct {
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Name       string `json:"name,omitempty"`
	CycleStart string `json:"cycle_start,omitempty"`
	CycleEnd   string `json:"cycle_end,omitempty"`
}

type codeBuddySummaryCacheEntry struct {
	FetchedAt time.Time
	Summary   codeBuddySummary
}

func (r *pluginRuntime) handleManagement(raw []byte) (pluginapi.ManagementResponse, error) {
	var req rpcManagementRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ManagementResponse{}, errDecode
	}
	if !strings.EqualFold(strings.TrimSpace(req.Method), http.MethodGet) {
		return managementJSONResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"}), nil
	}
	if !strings.HasSuffix(strings.TrimRight(strings.TrimSpace(req.Path), "/"), "/plugins/codebuddy/summary") {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "not_found"}), nil
	}
	authIndex := strings.TrimSpace(req.Query.Get("auth_index"))
	if authIndex == "" {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "auth_index_required"}), nil
	}
	if r.caller == nil {
		return managementJSONResponse(http.StatusServiceUnavailable, map[string]any{"error": "host_callback_unavailable"}), nil
	}
	authFile, errAuthFile := callHostAuthGet(r.caller, authIndex)
	if errAuthFile != nil || len(authFile.JSON) == 0 {
		return managementJSONResponse(http.StatusNotFound, map[string]any{"error": "auth_not_found"}), nil
	}
	auth, errAuth := parseStoredAuth(authFile.JSON)
	if errAuth != nil {
		return managementJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid_auth"}), nil
	}

	cfg := r.loadedConfig()
	cacheKey := codeBuddySummaryCacheKey(auth, cfg, authIndex)
	r.mu.Lock()
	cached, okCached := r.summaryCache[cacheKey]
	r.mu.Unlock()
	if okCached && time.Since(cached.FetchedAt) < codeBuddySummaryCacheTTL {
		result := cloneCodeBuddySummary(cached.Summary)
		result.Cached = true
		return managementJSONResponse(http.StatusOK, result), nil
	}

	name := strings.TrimSpace(authFile.Name)
	label := auth.Label
	if runtimeInfo, errRuntime := callHostAuthGetRuntime(r.caller, authIndex); errRuntime == nil {
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
		label = authLabelForDisplay(authFile.JSON)
	}

	result := codeBuddySummary{
		Provider:  pluginIdentifier,
		AuthIndex: authIndex,
		Name:      name,
		Label:     label,
		Credential: summaryCredential{
			Kind:        codeBuddyCredentialKind(auth),
			Fingerprint: codeBuddyCredentialFingerprint(auth),
		},
		Account:   summaryAccount{Status: "fallback", Source: "auth_label"},
		Quota:     r.codeBuddyQuotaSummary(auth, req.HostCallbackID),
		UpdatedAt: time.Now().UTC(),
	}

	if catalog, errCatalog := r.catalogForAuth(auth, req.HostCallbackID); errCatalog == nil && catalog.EnterpriseID != "" {
		result.Account = summaryAccount{
			Status: "available",
			ID:     catalog.EnterpriseID,
			Source: "catalog",
		}
	}
	if cfg.AccountEndpoint != "" {
		if account := r.fetchCodeBuddyAccount(auth, req.HostCallbackID); account.Status == "available" {
			result.Account = account
		}
	}

	r.mu.Lock()
	if r.summaryCache == nil {
		r.summaryCache = make(map[string]codeBuddySummaryCacheEntry)
	}
	r.summaryCache[cacheKey] = codeBuddySummaryCacheEntry{FetchedAt: time.Now(), Summary: cloneCodeBuddySummary(result)}
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

func callHostAuthGet(caller hostCaller, authIndex string) (pluginapi.HostAuthGetResponse, error) {
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

func callHostAuthGetRuntime(caller hostCaller, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
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

func (r *pluginRuntime) fetchCodeBuddyAccount(auth codeBuddyAuth, callbackID string) summaryAccount {
	cfg := r.loadedConfig()
	if strings.TrimSpace(cfg.AccountEndpoint) == "" || strings.TrimSpace(callbackID) == "" {
		return summaryAccount{Status: "fallback", Source: "auth_label"}
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("X-API-Key", auth.APIKey)
	headers.Set("X-Product", "SaaS")
	headers.Set("User-Agent", cfg.CatalogUserAgent)
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            cfg.AccountEndpoint,
		Headers:        headers,
	})
	if errRequest != nil {
		return summaryAccount{Status: "upstream_error", Code: "account_request_failed"}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return summaryAccount{Status: "unsupported", Code: "account_auth_rejected"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || len(response.Body) == 0 || len(response.Body) > 256*1024 {
		return summaryAccount{Status: "upstream_error", Code: "account_http_error"}
	}
	return parseCodeBuddyAccount(response.Body)
}

func parseCodeBuddyAccount(raw []byte) summaryAccount {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var root any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return summaryAccount{Status: "upstream_error", Code: "account_response_invalid"}
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return summaryAccount{Status: "upstream_error", Code: "account_response_invalid"}
	}
	account := mapValue(rootMap, "account")
	if account == nil {
		account = mapValue(rootMap, "data")
	}
	if account == nil {
		account = rootMap
	}
	result := summaryAccount{
		ID:     stringValue(account, "id", "user_id", "uid", "account_id", "enterpriseId"),
		Name:   stringValue(account, "name", "username", "nickname", "account_name"),
		Email:  stringValue(account, "email", "email_address"),
		Source: "account_endpoint",
	}
	if result.ID == "" && result.Name == "" && result.Email == "" {
		return summaryAccount{Status: "unsupported", Code: "account_fields_missing"}
	}
	result.Status = "available"
	return result
}

func codeBuddyCredentialKind(auth codeBuddyAuth) string {
	if auth.AuthMode == "api_key" {
		return "api_key_legacy"
	}
	return "pat"
}

func codeBuddyCredentialFingerprint(auth codeBuddyAuth) string {
	sum := sha256.Sum256([]byte(auth.APIKey))
	return hex.EncodeToString(sum[:8])
}

func codeBuddySummaryCacheKey(auth codeBuddyAuth, cfg pluginConfig, authIndex string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		codeBuddyCredentialFingerprint(auth), strings.TrimSpace(authIndex), cfg.CatalogEndpoint, cfg.BillingEndpoint, cfg.AccountEndpoint,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func cloneCodeBuddySummary(input codeBuddySummary) codeBuddySummary {
	output := input
	output.Quota.Packages = append([]codeBuddyQuotaPackage(nil), input.Quota.Packages...)
	return output
}
