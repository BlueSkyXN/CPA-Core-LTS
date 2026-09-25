package main

import (
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

const copilotTestToken = "ghp-test-secret-never-log"

type copilotSummaryFakeHost struct {
	mu       sync.Mutex
	requests []hostHTTPRequest
}

func (h *copilotSummaryFakeHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case pluginabi.MethodHostAuthGet:
		return json.Marshal(pluginapi.HostAuthGetResponse{
			AuthIndex: "copilot-index",
			Name:      "copilot.json",
			JSON:      json.RawMessage(`{"type":"copilot","auth_mode":"github_token","github_token":"` + copilotTestToken + `","label":"Copilot Test"}`),
		})
	case pluginabi.MethodHostAuthGetRuntime:
		return json.Marshal(pluginapi.HostAuthGetRuntimeResponse{Auth: pluginapi.HostAuthFileEntry{
			AuthIndex: "copilot-index", Name: "copilot.json", Label: "Copilot Test",
		}})
	case pluginabi.MethodHostHTTPDo:
		req, ok := payload.(hostHTTPRequest)
		if !ok {
			return nil, errors.New("unexpected host HTTP request")
		}
		h.mu.Lock()
		h.requests = append(h.requests, req)
		h.mu.Unlock()
		return copilotFakeHTTPResponse(req.URL, req.Headers)
	default:
		return nil, errors.New("unexpected host callback")
	}
}

func copilotFakeHTTPResponse(rawURL string, headers http.Header) (json.RawMessage, error) {
	response := hostHTTPResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}}
	switch {
	case strings.HasSuffix(rawURL, "/copilot_internal/user"):
		if !strings.HasPrefix(headers.Get("Authorization"), "token ghp-") {
			response.StatusCode = http.StatusUnauthorized
			response.Body = []byte(`{"message":"Bad credentials"}`)
			return json.Marshal(response)
		}
		// unlimited/percent_remaining 的原始值必须字节级透传。
		response.Body = []byte(`{"login":"octocat","id":12345,"access_type_sku":"copilot_for_business","analytics_tracking_id":"anon-1","assigned_date":"2025-01-15","can_signup_for_limited":false,"chat_enabled":true,"copilot_plan":"business","organization_login_list":[],"organization_list":[],"quota_reset_date":"2026-10-01","quota_snapshots":{"chat":{"entitlement":300,"overage_count":0,"overage_permitted":false,"percent_remaining":99.5,"quota_id":"chat","quota_remaining":298.5,"remaining":298.5,"unlimited":false},"completions":{"entitlement":0,"overage_count":0,"overage_permitted":false,"percent_remaining":100,"quota_id":"completions","quota_remaining":0,"remaining":0,"unlimited":true},"premium_interactions":{"entitlement":300,"overage_count":0,"overage_permitted":false,"percent_remaining":83.25,"quota_id":"premium_interactions","quota_remaining":249.75,"remaining":249.75,"unlimited":false}}}`)
	default:
		response.StatusCode = http.StatusNotFound
	}
	return json.Marshal(response)
}

func TestManagementSummaryPassesQuotaThroughAndCaches(t *testing.T) {
	host := &copilotSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = defaultPluginConfig()
	runtime.config.GitHubAPIEndpoint = "https://api.github.example.test"
	raw, errMarshal := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/copilot/summary",
			Query:  url.Values{"auth_index": {"copilot-index"}},
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
	body := string(response.Body)
	if strings.Contains(body, copilotTestToken) {
		t.Fatal("management response leaked the GitHub token")
	}
	// unlimited/percent_remaining 原样透传，不猜订阅档位。
	for _, expected := range []string{`"unlimited":true`, `"unlimited":false`, `"percent_remaining":99.5`, `"percent_remaining":100`, `"percent_remaining":83.25`, `"copilot_plan":"business"`, `"quota_reset_date":"2026-10-01"`, `"login"`, `"auth_index":"copilot-index"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("summary body missing %s: %s", expected, body)
		}
	}
	if strings.Contains(body, `"plan_tier"`) || strings.Contains(body, `"plan_name"`) {
		t.Fatal("summary invented a subscription tier")
	}
	second, errSecond := runtime.handleManagement(raw)
	if errSecond != nil || !strings.Contains(string(second.Body), `"cached":true`) {
		t.Fatalf("cached summary = %s, err=%v", second.Body, errSecond)
	}
	host.mu.Lock()
	requestCount := len(host.requests)
	host.mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("host HTTP calls = %d, want a single copilot_internal/user call", requestCount)
	}
	if copilotSummaryCacheKey(copilotAuth{GitHubToken: copilotTestToken}, "https://api.github.example.test", "1") == copilotSummaryCacheKey(copilotAuth{GitHubToken: copilotTestToken}, "https://api.github.example.test", "2") {
		t.Fatal("summary cache key ignored auth index")
	}
}

func TestManagementSummaryRejectsBadRequests(t *testing.T) {
	host := &copilotSummaryFakeHost{}
	runtime := newPluginRuntime(host)
	runtime.config = defaultPluginConfig()
	runtime.config.GitHubAPIEndpoint = "https://api.github.example.test"
	missing, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/copilot/summary"},
	})
	if response, err := runtime.handleManagement(missing); err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing auth_index = %#v, %v", response, err)
	}
	unknown, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet, Path: "/v0/management/plugins/copilot/summary",
			Query: url.Values{"auth_index": {"missing"}},
		},
		HostCallbackID: "management-callback",
	})
	if _, err := runtime.handleManagement(unknown); err != nil {
		t.Fatal(err)
	}
	// 假 host 对任意 auth_index 都返回同一份 auth 文件，这里只验证 unknown path。
	unknownPath, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet, Path: "/v0/management/plugins/copilot/other",
			Query: url.Values{"auth_index": {"copilot-index"}},
		},
		HostCallbackID: "management-callback",
	})
	if response, err := runtime.handleManagement(unknownPath); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path = %#v, %v", response, err)
	}
}
