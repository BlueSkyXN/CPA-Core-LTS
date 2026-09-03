package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const qoderTestPAT = "pt-test-secret"

type qoderSummaryFakeHost struct {
	mu       sync.Mutex
	requests []hostHTTPRequest
}

func (h *qoderSummaryFakeHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case pluginabi.MethodHostAuthGet:
		return json.Marshal(pluginapi.HostAuthGetResponse{
			AuthIndex: "qoder-index",
			Name:      "qoder.json",
			JSON:      json.RawMessage(`{"type":"qoder","auth_mode":"pat","pat":"` + qoderTestPAT + `","label":"Qoder Test"}`),
		})
	case pluginabi.MethodHostAuthGetRuntime:
		return json.Marshal(pluginapi.HostAuthGetRuntimeResponse{Auth: pluginapi.HostAuthFileEntry{
			AuthIndex: "qoder-index", Name: "qoder.json", Label: "Qoder Test",
		}})
	case pluginabi.MethodHostHTTPDo:
		req, ok := payload.(hostHTTPRequest)
		if !ok {
			return nil, errors.New("unexpected host HTTP request")
		}
		h.mu.Lock()
		h.requests = append(h.requests, req)
		h.mu.Unlock()
		return qoderFakeHTTPResponse(req.URL)
	default:
		return nil, errors.New("unexpected host callback")
	}
}

func qoderFakeHTTPResponse(rawURL string) (json.RawMessage, error) {
	response := hostHTTPResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}}
	switch {
	case strings.HasSuffix(rawURL, "/jobToken/exchange"):
		response.Body = []byte(`{"token":"jt-test","refresh_token":"jrt-test","expires_in":86400}`)
	case strings.HasSuffix(rawURL, "/jobToken/refresh"):
		response.Body = []byte(`{"token":"jt-test-refresh","refresh_token":"jrt-test-refresh","expires_in":86400}`)
	case strings.HasSuffix(rawURL, "/userinfo"):
		response.Body = []byte(`{"id":"qoder-user-1","name":"Qoder Test User","username":"qoder-test"}`)
	case strings.HasSuffix(rawURL, "/user/plan"):
		response.Body = []byte(`{"user_type":"personal","plan_tier_name":"Pro","plan_tier":"pro","is_paid_plan":true,"is_personal_version":true,"start_date":1788172800,"end_date":1790851200}`)
	case strings.HasSuffix(rawURL, "/quota/usage"):
		response.Body = []byte(`{"userId":"qoder-user-1","userType":"personal","totalUsagePercentage":0.25,"isQuotaExceeded":false,"userQuota":{"total":1000.5,"used":250.25,"remaining":750.25,"percentage":25.0,"unit":"credits"},"addOnQuota":{"total":2.25,"used":1.25,"remaining":1.0,"percentage":55.56,"unit":"credits"},"dedicatedResourcePackages":[]}`)
	default:
		response.StatusCode = http.StatusNotFound
	}
	return json.Marshal(response)
}

func TestQoderManagementSummaryUsesPATAndCache(t *testing.T) {
	host := &qoderSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = pluginConfig{OpenAPIEndpoint: "https://openapi.example.test", OpenAPIUserAgent: "qoder/1.1.40"}
	raw, errMarshal := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/qoder/summary",
			Query:  url.Values{"auth_index": {"qoder-index"}},
		},
		HostCallbackID: "management-callback",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errHandle := runtime.handleManagement(raw)
	if errHandle != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("management response = %#v, err=%v", response, errHandle)
	}
	if bytes.Contains(response.Body, []byte(qoderTestPAT)) || bytes.Contains(response.Body, []byte("jt-test")) {
		t.Fatal("management response leaked a credential")
	}
	var summary qoderSummary
	if errDecode := json.Unmarshal(response.Body, &summary); errDecode != nil {
		t.Fatal(errDecode)
	}
	if summary.Provider != pluginIdentifier || summary.AuthIndex != "qoder-index" || summary.Account.ID != "qoder-user-1" || summary.Plan.Name != "Pro" {
		t.Fatalf("summary identity = %#v", summary)
	}
	if summary.Quota.Status != "available" || summary.Quota.TotalExact != "1002.75" || summary.Quota.UsedExact != "251.5" || summary.Quota.RemainingExact != "751.25" {
		t.Fatalf("summary quota = %#v", summary.Quota)
	}
	second, errSecond := runtime.handleManagement(raw)
	if errSecond != nil || !bytes.Contains(second.Body, []byte(`"cached":true`)) {
		t.Fatalf("cached summary = %s, err=%v", second.Body, errSecond)
	}
	host.mu.Lock()
	requestCount := len(host.requests)
	host.mu.Unlock()
	if requestCount != 4 {
		t.Fatalf("host HTTP calls = %d, want exchange + three summary endpoints", requestCount)
	}
	if qoderSummaryCacheKey(qoderAuth{PAT: qoderTestPAT}, "https://openapi.example.test", "1") == qoderSummaryCacheKey(qoderAuth{PAT: qoderTestPAT}, "https://openapi.example.test", "2") {
		t.Fatal("summary cache key ignored auth index")
	}
}

func TestQoderQuotaPreservesExactValues(t *testing.T) {
	if quota := parseQoderQuota([]byte(`{"status":"ok"}`)); quota.Status != "upstream_error" || quota.Code != "quota_fields_missing" {
		t.Fatalf("quota without recognized fields = %#v", quota)
	}
	quota := parseQoderQuota([]byte(`{"userQuota":{"total":"1000.5000","used":"250.2500","remaining":"750.2500","unit":"credits"},"addOnQuota":{"total":"2.25","used":"1.25","remaining":"1.00","unit":"credits"}}`))
	if quota.Status != "available" || quota.TotalExact != "1002.75" || quota.UsedExact != "251.5" || quota.RemainingExact != "751.25" {
		t.Fatalf("quota = %#v", quota)
	}
}

func TestQoderQuotaExcludesUnavailableHistoricalPackagesFromCurrentTotals(t *testing.T) {
	quota := parseQoderQuota([]byte(`{
		"totalUsagePercentage":0.01,
		"isQuotaExceeded":false,
		"userQuota":{"total":2000.0,"used":0.0,"remaining":2000.0,"percentage":0.0,"unit":"credits"},
		"addOnQuota":{"total":4000.0,"used":53.0,"remaining":3947.0,"percentage":0.02,"unit":"credits"},
		"dedicatedResourcePackages":[
			{"name":"historical-1","status":"QUOTA_DETAIL_STATUS_EXHAUSTED","available":false,"total":500.0,"used":500.0,"remaining":0.0,"percentage":1.0,"unit":"credits","expiresAt":1787882400000},
			{"name":"historical-2","status":"QUOTA_DETAIL_STATUS_EXHAUSTED","available":false,"total":500.0,"used":500.0,"remaining":0.0,"percentage":1.0,"unit":"credits","expiresAt":1787968800000},
			{"name":"historical-3","status":"QUOTA_DETAIL_STATUS_EXHAUSTED","available":false,"total":500.0,"used":500.0,"remaining":0.0,"percentage":1.0,"unit":"credits","expiresAt":1788228000000}
		]
	}`))
	if quota.Status != "available" || quota.TotalExact != "6000" || quota.UsedExact != "53" || quota.RemainingExact != "5947" {
		t.Fatalf("current quota totals = %#v", quota)
	}
	if quota.Percentage == nil || *quota.Percentage != 0.01 || len(quota.Packages) != 5 {
		t.Fatalf("quota metadata = %#v", quota)
	}
	for _, item := range quota.Packages[2:] {
		if item.Available == nil || *item.Available || item.TotalExact != "500" || item.ExpiresAt == "" {
			t.Fatalf("historical package detail = %#v", item)
		}
	}
}

func TestQoderLocalCLISummaryDoesNotExchangeProfileCredentials(t *testing.T) {
	host := &qoderSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = pluginConfig{OpenAPIEndpoint: "https://openapi.example.test", OpenAPIUserAgent: "qoder/1.1.40"}
	summary := runtime.qoderSummary(qoderAuth{AuthMode: "local_cli", ProfileID: "cn-main", ConfigDir: "/tmp/qoder-cn"}, "management-callback")
	if summary.Credential.Kind != "local_cli" || summary.Account.Status != "unsupported" || summary.Plan == nil || summary.Plan.Status != "unsupported" || summary.Quota.Status != "unsupported" {
		t.Fatalf("local_cli summary = %#v", summary)
	}
	host.mu.Lock()
	requestCount := len(host.requests)
	host.mu.Unlock()
	if requestCount != 0 {
		t.Fatalf("local_cli summary made %d HTTP calls", requestCount)
	}
}
