package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func quotaFetchRequestRaw(t *testing.T, authJSON string, callbackID string) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(rpcQuotaFetchRequest{
		QuotaFetchRequest: pluginapi.QuotaFetchRequest{
			AuthIndex:   "copilot-index",
			AuthID:      "auth-1",
			Provider:    pluginIdentifier,
			StorageJSON: json.RawMessage(authJSON),
		},
		HostCallbackID: callbackID,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

func TestQuotaFetchNormalizesSnapshots(t *testing.T) {
	host := &copilotSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = defaultPluginConfig()
	runtime.config.GitHubAPIEndpoint = "https://api.github.example.test"
	raw := quotaFetchRequestRaw(t, `{"type":"copilot","auth_mode":"github_token","github_token":"`+copilotTestToken+`","label":"Copilot Test"}`, "quota-callback")
	response, errFetch := runtime.fetchQuota(raw)
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if response.Subscription == nil || response.Subscription.Plan != "business" {
		t.Fatalf("subscription = %#v, want plan business", response.Subscription)
	}
	if len(response.Groups) != 1 || response.Groups[0].DisplayName != "Copilot usage" {
		t.Fatalf("groups = %#v", response.Groups)
	}
	buckets := response.Groups[0].Buckets
	if len(buckets) != 3 {
		t.Fatalf("buckets = %#v, want chat/completions/premium_interactions", buckets)
	}
	if buckets[0].Window != "chat" || buckets[0].RemainingFraction != 0.995 {
		t.Fatalf("chat bucket = %#v, want window chat fraction 0.995", buckets[0])
	}
	if buckets[1].Window != "completions" || buckets[1].RemainingFraction != 1 {
		t.Fatalf("completions bucket = %#v, want unlimited fraction 1", buckets[1])
	}
	if !strings.Contains(buckets[1].Description, "unlimited") {
		t.Fatalf("completions description = %q, want unlimited marker", buckets[1].Description)
	}
	if buckets[2].Window != "premium_interactions" || buckets[2].RemainingFraction != 0.8325 {
		t.Fatalf("premium bucket = %#v, want fraction 0.8325", buckets[2])
	}
	if !strings.Contains(buckets[0].Description, "298.5") || !strings.Contains(buckets[0].Description, "300") {
		t.Fatalf("chat description = %q, want remaining/entitlement", buckets[0].Description)
	}
	// 剩余数值进 summary 指标行；重置日期也作为指标行透出。
	summaryKeys := make(map[string]pluginapi.QuotaMetric)
	for _, metric := range response.Summary {
		summaryKeys[metric.Key] = metric
	}
	if metric, ok := summaryKeys["chat_remaining"]; !ok || metric.Value != 298.5 {
		t.Fatalf("chat_remaining metric = %#v", summaryKeys["chat_remaining"])
	}
	if metric, ok := summaryKeys["premium_interactions_remaining"]; !ok || metric.Value != 249.75 {
		t.Fatalf("premium metric = %#v", summaryKeys["premium_interactions_remaining"])
	}
	if _, ok := summaryKeys["quota_reset_date"]; !ok {
		t.Fatalf("quota_reset_date metric missing: %#v", response.Summary)
	}
	// upstream URL 是 github api，不是 copilot api。
	host.mu.Lock()
	requestCount := len(host.requests)
	host.mu.Unlock()
	if requestCount != 1 || !strings.HasSuffix(host.requests[0].URL, "/copilot_internal/user") {
		t.Fatalf("host calls = %d %#v, want single copilot_internal/user", requestCount, host.requests)
	}
}

func TestQuotaFetchDispatchAndCacheShareUpstreamCall(t *testing.T) {
	host := &copilotSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = defaultPluginConfig()
	runtime.config.GitHubAPIEndpoint = "https://api.github.example.test"
	raw := quotaFetchRequestRaw(t, `{"type":"copilot","auth_mode":"github_token","github_token":"`+copilotTestToken+`"}`, "quota-callback")
	first, errDispatch := runtime.dispatch(pluginabi.MethodQuotaFetch, raw)
	if errDispatch != nil {
		t.Fatal(errDispatch)
	}
	if strings.Contains(string(first), copilotTestToken) {
		t.Fatal("quota response leaked the GitHub token")
	}
	second, errSecond := runtime.dispatch(pluginabi.MethodQuotaFetch, raw)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	host.mu.Lock()
	requestCount := len(host.requests)
	host.mu.Unlock()
	// 第二次命中 summary 缓存（同 token+endpoint+auth_index），不再打上游。
	if requestCount != 1 {
		t.Fatalf("host HTTP calls = %d, want single upstream call across two fetches", requestCount)
	}
	var decoded pluginapi.QuotaFetchResponse
	if errDecode := json.Unmarshal(second, &decoded); errDecode != nil {
		t.Fatal(errDecode)
	}
	// dispatch 直接回 envelope；校验 ok=true 且 result 里 subscription 完整。
	var env envelope
	if errEnv := json.Unmarshal(second, &env); errEnv != nil || !env.OK {
		t.Fatalf("envelope = %#v err=%v", env, errEnv)
	}
	if errDecode := json.Unmarshal(env.Result, &decoded); errDecode != nil || decoded.Subscription == nil || decoded.Subscription.Plan != "business" {
		t.Fatalf("result = %#v err=%v", decoded, errDecode)
	}
}

func TestQuotaIdentifierAndDescribe(t *testing.T) {
	runtime := newPluginRuntime(&copilotSummaryFakeHost{})
	raw, errDispatch := runtime.dispatch(pluginabi.MethodQuotaIdentifier, nil)
	if errDispatch != nil {
		t.Fatal(errDispatch)
	}
	if !strings.Contains(string(raw), `"identifier":"copilot"`) {
		t.Fatalf("identifier response = %s", raw)
	}
	raw, errDescribe := runtime.dispatch(pluginabi.MethodQuotaDescribe, nil)
	if errDescribe != nil {
		t.Fatal(errDescribe)
	}
	for _, expected := range []string{`"display_name":"GitHub Copilot"`, `"supported_providers":["copilot"]`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("describe response %s missing %s", raw, expected)
		}
	}
}

func TestQuotaResetUnsupported(t *testing.T) {
	runtime := newPluginRuntime(&copilotSummaryFakeHost{})
	_, errReset := runtime.dispatch(pluginabi.MethodQuotaReset, []byte(`{}`))
	if errReset == nil {
		t.Fatal("quota reset must not succeed")
	}
	callErr, okCast := errReset.(*pluginCallError)
	if !okCast || callErr.statusCode != 501 {
		t.Fatalf("reset error = %#v, want 501 unsupported", errReset)
	}
}

func TestQuotaFetchRejectsForeignProviderAndInvalidAuth(t *testing.T) {
	runtime := newPluginRuntime(&copilotSummaryFakeHost{})
	runtime.config = defaultPluginConfig()
	foreign := quotaFetchRequestRaw(t, `{"type":"copilot","auth_mode":"github_token","github_token":"x"}`, "quota-callback")
	// 同名 provider 字段改为其他厂商。
	var mutated rpcQuotaFetchRequest
	if errDecode := json.Unmarshal(foreign, &mutated); errDecode != nil {
		t.Fatal(errDecode)
	}
	mutated.Provider = "qoder"
	raw, _ := json.Marshal(mutated)
	if _, errFetch := runtime.fetchQuota(raw); errFetch == nil {
		t.Fatal("foreign provider must be rejected")
	}
	invalid := quotaFetchRequestRaw(t, `{"type":"codex"}`, "quota-callback")
	if _, errFetch := runtime.fetchQuota(invalid); errFetch == nil {
		t.Fatal("invalid auth must be rejected")
	}
}
