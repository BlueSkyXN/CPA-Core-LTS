package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const modernCatalogFixture = `{"code":0,"data":{"enterpriseId":"tenant-fixture","models":[
{"id":"hy3","name":"Hy3","credits":"x0.00","onlyReasoning":true,"reasoning":{"defaultEffort":"high","supportedEfforts":["low","high"],"canDisableThinking":false}},
{"id":"hy3-x","name":"Hy3","credits":"x0.05","onlyReasoning":true,"canDisableThinking":false,"reasoning":{"defaultEffort":"high","supportedEfforts":["low","high","max"],"canDisableThinking":true},"maxInputTokens":1000000,"maxOutputTokens":64000,"contextWindow":{"defaultLength":300000,"supportedLengths":[300000,1000000]}},
{"id":"craft-only","name":"Craft Only"}],"agents":[{"name":"craft","models":["craft-only"]},{"name":"cli","models":["hy3","hy3-x"]}]}}`

func TestModernCatalogPreservesVariantsThinkingAndDefaultContext(t *testing.T) {
	catalog, err := parseCodeBuddyCatalog([]byte(modernCatalogFixture))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Scene != "cli" || len(catalog.Models) != 2 || catalog.Models[0].ID == catalog.Models[1].ID {
		t.Fatal("scene or exact variants lost")
	}
	if catalog.Models[0].DisplayName != catalog.Models[1].DisplayName {
		t.Fatal("fixture should cover equal display names")
	}
	if catalog.Models[0].Thinking.ZeroAllowed || !catalog.Models[1].Thinking.ZeroAllowed {
		t.Fatal("explicit thinking disable capability lost")
	}
	if got := strings.Join(catalog.Models[1].Thinking.Levels, ","); got != "low,high,max" {
		t.Fatalf("efforts=%s", got)
	}
	if catalog.Models[1].ContextLength != 300000 || catalog.Models[1].InputTokenLimit != 1000000 {
		t.Fatal("default context tier was escalated or maximum input lost")
	}
	if catalog.Hints["hy3"].Credits != "x0.00" || catalog.Hints["hy3-x"].Credits != "x0.05" {
		t.Fatal("raw credits hints lost")
	}
	copy := cloneCodeBuddyCatalog(catalog)
	copy.Models[1].Thinking.Levels[0] = "changed"
	copy.Models[1].SupportedInputModalities[0] = "changed"
	copy.Hints["hy3-x"].SupportedLengths[0] = 99
	delete(copy.Allowed, "hy3")
	if catalog.Models[1].Thinking.Levels[0] != "low" || catalog.Models[1].SupportedInputModalities[0] != "text" || catalog.Hints["hy3-x"].SupportedLengths[0] != 300000 || len(catalog.Allowed) != 2 {
		t.Fatal("catalog clone aliases cache")
	}
}

func TestClientProfileDefaultAndExplicitOverrides(t *testing.T) {
	cfg, err := decodePluginConfig(nil)
	if err != nil || !strings.Contains(cfg.CatalogUserAgent, "CLI/") || cfg.UserAgent != cfg.CatalogUserAgent {
		t.Fatalf("default profile invalid: %v", err)
	}
	cfg, err = decodePluginConfig([]byte("catalog_user_agent: fixture-catalog\nuser_agent: fixture-chat\n"))
	if err != nil || cfg.CatalogUserAgent != "fixture-catalog" || cfg.UserAgent != "fixture-chat" {
		t.Fatal("explicit override lost")
	}
}

func TestCatalogStaleBoundAndRejection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		status int
		body   string
		stale  bool
	}{
		{"transient", 2 * time.Minute, 503, "", true},
		{"expired", 6 * time.Minute, 503, "", false},
		{"unauthorized", 2 * time.Minute, 401, "", false},
		{"forbidden", 2 * time.Minute, 403, "", false},
		{"empty_scene", 2 * time.Minute, 200, `{"code":0,"data":{"agents":[{"name":"cli","models":[]}]}}`, false},
		{"rejected_envelope", 2 * time.Minute, 200, `{"code":403,"data":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newFakeHost()
			runtime := newPluginRuntime(host)
			auth, _ := parseStoredAuth(testAuthJSON())
			if _, err := runtime.catalogForAuth(auth, "fixture"); err != nil {
				t.Fatal(err)
			}
			runtime.mu.Lock()
			for key, entry := range runtime.catalogCache {
				entry.FetchedAt = time.Now().Add(-tc.age)
				runtime.catalogCache[key] = entry
			}
			runtime.mu.Unlock()
			host.catalogResponse.StatusCode, host.catalogResponse.Body = tc.status, []byte(tc.body)
			catalog, err := runtime.catalogForAuth(auth, "fixture")
			if tc.stale {
				if err != nil || !catalog.Stale {
					t.Fatalf("want stale fallback: %v", err)
				}
			} else if err == nil {
				t.Fatal("denial or expired cache was hidden")
			}
		})
	}
}

type delayedCatalogHost struct {
	*fakeHost
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (h *delayedCatalogHost) Call(method string, payload any) (json.RawMessage, error) {
	if method == pluginabi.MethodHostHTTPDo {
		h.once.Do(func() { close(h.entered) })
		<-h.proceed
	}
	return h.fakeHost.Call(method, payload)
}

func TestCatalogConcurrentFetchAndReconfigureIsolation(t *testing.T) {
	host := &delayedCatalogHost{newFakeHost(), make(chan struct{}), make(chan struct{}), sync.Once{}}
	runtime := newPluginRuntime(host)
	auth, _ := parseStoredAuth(testAuthJSON())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runtime.catalogForAuth(auth, "fixture"); err != nil {
				t.Error(err)
			}
		}()
	}
	<-host.entered
	close(host.proceed)
	wg.Wait()
	host.mu.Lock()
	count := len(host.httpRequests)
	host.mu.Unlock()
	if count != 1 {
		t.Fatalf("concurrent catalog requests=%d", count)
	}
	host2 := &delayedCatalogHost{newFakeHost(), make(chan struct{}), make(chan struct{}), sync.Once{}}
	runtime2 := newPluginRuntime(host2)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = runtime2.catalogForAuth(auth, "fixture") }()
	<-host2.entered
	if err := runtime2.configure(nil); err != nil {
		t.Fatal(err)
	}
	close(host2.proceed)
	<-done
	runtime2.mu.Lock()
	defer runtime2.mu.Unlock()
	if len(runtime2.catalogCache) != 0 {
		t.Fatal("old in-flight catalog repopulated reconfigured cache")
	}
}

func TestSummarySeparatesTenantPlanAndPersonalQuota(t *testing.T) {
	host := newFakeHost()
	host.catalogResponse.Body = []byte(modernCatalogFixture)
	runtime := newPluginRuntime(host)
	req, _ := json.Marshal(rpcManagementRequest{ManagementRequest: pluginapi.ManagementRequest{Method: "GET", Path: "/plugins/codebuddy/summary", Query: url.Values{"auth_index": {"1"}}}, HostCallbackID: "fixture"})
	raw, err := runtime.dispatch(pluginabi.MethodManagementHandle, req)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	var response pluginapi.ManagementResponse
	_ = json.Unmarshal(env.Result, &response)
	var summary codeBuddySummary
	if err := json.Unmarshal(response.Body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Account.Scope != "tenant" || summary.Account.EnterpriseID != "tenant-fixture" || summary.Account.Status != "partial" {
		t.Fatal("tenant was presented as verified member")
	}
	if summary.Plan == nil || summary.Plan.Status != "unknown" {
		t.Fatal("unverified plan was inferred")
	}
	if summary.Quota.Scope != "personal_resources" || summary.Quota.Remaining != nil || summary.Quota.Code != "no_personal_resources" {
		t.Fatal("empty personal resources became enterprise zero")
	}
	if summary.Catalog == nil || summary.Catalog.Scene != "cli" || summary.Catalog.Count != 2 {
		t.Fatal("catalog summary missing")
	}
	if strings.Contains(string(response.Body), testSecret) {
		t.Fatal("summary leaked secret")
	}
	copy := cloneCodeBuddySummary(summary)
	copy.Catalog.Models[0].Thinking.Levels[0] = "changed"
	if summary.Catalog.Models[0].Thinking.Levels[0] != "low" {
		t.Fatal("summary cache mutated")
	}
}

func TestQuotaCyclePrecisionAndUnknownFields(t *testing.T) {
	for _, tc := range []struct{ name, body, status, total, used, remaining, basis string }{
		{"cycle", `{"CapacitySizePrecise":"10000","CapacityUsedPrecise":"9900","CycleCapacitySizePrecise":"1000.25","CycleCapacityRemainPrecise":"876.12"}`, "available", "1000.25", "124.13", "876.12", "cycle"},
		{"cycle_zero", `{"CapacitySize":5000,"CycleCapacitySize":0,"CycleCapacityUsed":0,"CycleCapacityRemain":0}`, "available", "0", "0", "0", "cycle"},
		{"no_mix", `{"CapacitySize":5000,"CapacityUsed":100,"CapacityRemain":4900,"CycleCapacityUsed":1}`, "partial", "", "", "", "cycle"},
		{"bonus", `{"CapacitySizePrecise":"2.75","CapacityUsedPrecise":"0.25"}`, "available", "2.75", "0.25", "2.5", "lifetime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := parseCodeBuddyQuota([]byte(`{"Accounts":[` + tc.body + `]}`))
			if err != nil || q.Status != tc.status || q.TotalExact != tc.total || q.UsedExact != tc.used || q.RemainingExact != tc.remaining || q.Packages[0].Basis != tc.basis {
				t.Fatalf("quota=%+v err=%v", q, err)
			}
		})
	}
	for _, raw := range []string{`{"Accounts":{}}`, `{"Accounts":[null]}`, `{"code":401,"Accounts":[]}`} {
		if _, err := parseCodeBuddyQuota([]byte(raw)); err == nil {
			t.Fatal("invalid quota became zero")
		}
	}
}

type paginatedQuotaHost struct {
	*fakeHost
	pages    int
	repeated bool
}

func (h *paginatedQuotaHost) Call(method string, payload any) (json.RawMessage, error) {
	if method == pluginabi.MethodHostHTTPDo && strings.Contains(payload.(hostHTTPRequest).URL, "/billing/") {
		h.pages++
		var body map[string]any
		_ = json.Unmarshal(payload.(hostHTTPRequest).Body, &body)
		if body["PageNumber"] != float64(h.pages) || body["PageSize"] != float64(100) || body["ProductCode"] != "p_tcaca" {
			return nil, fmt.Errorf("invalid pagination request")
		}
		n := h.pages
		if h.repeated {
			n = 1
		}
		return marshalFakeResult(hostHTTPResponse{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"Response":{"Data":{"TotalCount":2,"TotalDosage":99999,"Accounts":[{"PackageName":"page-%d","CapacitySizePrecise":"2.5","CapacityUsedPrecise":"0.25"}]}}}}`, n))})
	}
	return h.fakeHost.Call(method, payload)
}

func TestQuotaPaginationDoesNotCountTotalDosageAsUsage(t *testing.T) {
	auth, _ := parseStoredAuth(testAuthJSON())
	host := &paginatedQuotaHost{fakeHost: newFakeHost()}
	q := newPluginRuntime(host).codeBuddyQuotaSummary(auth, "fixture")
	if host.pages != 2 || q.TotalExact != "5" || q.UsedExact != "0.5" || q.RemainingExact != "4.5" {
		t.Fatalf("quota=%+v pages=%d", q, host.pages)
	}
	host = &paginatedQuotaHost{fakeHost: newFakeHost(), repeated: true}
	q = newPluginRuntime(host).codeBuddyQuotaSummary(auth, "fixture")
	if q.Status != "partial" || q.Code != "billing_pagination_repeated" || q.Remaining != nil {
		t.Fatal("repeated pages were double counted")
	}
}

func nonStreamRequest(t *testing.T, id string) []byte {
	t.Helper()
	var req rpcExecutorRequest
	_ = json.Unmarshal(executorRequestJSON(t, id), &req)
	req.Stream = false
	req.StreamID = ""
	req.Payload = []byte(`{"model":"hy3","stream":false,"messages":[{"role":"user","content":"fixture"}]}`)
	raw, _ := json.Marshal(req)
	return raw
}

func TestNonStreamAggregatesFragmentedToolsReasoningAndUsage(t *testing.T) {
	host := newFakeHost()
	sse := "data: " + `{"id":"test-id","model":"hy3","created":123,"choices":[{"index":0,"delta":{"content":"你好","reasoning_content":"想","tool_calls":[{"index":1,"id":"call-2","type":"function","function":{"name":"lookup","arguments":"{\"city\":\""}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{"content":"世界","reasoning_content":"好了","tool_calls":[{"index":0,"id":"call-1","function":{"name":"first","arguments":"{}"}},{"index":1,"function":{"arguments":"北京\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: " + `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\ndata: [DONE]\n\n"
	for len(sse) > 0 {
		n := min(17, len(sse))
		host.reads = append(host.reads, hostHTTPStreamReadResponse{Payload: []byte(sse[:n])})
		sse = sse[n:]
	}
	runtime := newPluginRuntime(host)
	raw, err := runtime.dispatch(pluginabi.MethodExecutorExecute, nonStreamRequest(t, "nonstream"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	var response pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &response)
	var body map[string]any
	if json.Unmarshal(response.Payload, &body) != nil {
		t.Fatal("invalid JSON result")
	}
	message := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if body["object"] != "chat.completion" || message["content"] != "你好世界" || message["reasoning_content"] != "想好了" {
		t.Fatalf("content lost: %s", response.Payload)
	}
	calls := message["tool_calls"].([]any)
	if len(calls) != 2 || calls[1].(map[string]any)["function"].(map[string]any)["arguments"] != `{"city":"北京"}` {
		t.Fatal("tool fragments lost")
	}
	usage := body["usage"].(map[string]any)
	if usage["total_tokens"] != float64(18) || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(4) {
		t.Fatal("usage lost or double counted")
	}
	if host.openCount() != 1 || host.httpCloseCount() != 1 || host.pluginCloseCount() != 0 || len(host.emittedPayloads()) != 0 {
		t.Fatal("nonstream used extra request or fake downstream stream")
	}
	var upstream map[string]any
	_ = json.Unmarshal(host.lastOpenRequest().Body, &upstream)
	if upstream["stream"] != true || upstream["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatal("upstream usage not requested")
	}
}

func TestNonStreamRejectsTruncationAndCancelsWithoutReplay(t *testing.T) {
	for _, tail := range []string{"", "data: [DONE]\n\n"} {
		host := newFakeHost()
		host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n" + tail), Done: true}}
		if _, err := newPluginRuntime(host).execute(nonStreamRequest(t, "incomplete")); err == nil {
			t.Fatal("truncated answer became success")
		}
		if host.openCount() != 1 || host.httpCloseCount() != 1 {
			t.Fatal("incomplete request replayed or leaked")
		}
	}
	host := newFakeHost()
	host.blockReads = true
	runtime := newPluginRuntime(host)
	done := make(chan error, 1)
	go func() { _, err := runtime.execute(nonStreamRequest(t, "cancel-nonstream")); done <- err }()
	deadline := time.After(time.Second)
	for host.openCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("request not opened")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	runtime.quiesceAndWait()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled result succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not drain")
	}
	if host.httpCloseCount() != 1 || host.pluginCloseCount() != 0 {
		t.Fatal("invalid nonstream cancellation lifecycle")
	}
}

func TestCollectorLimitsAndLateUsage(t *testing.T) {
	c := completionCollector{fields: map[string]json.RawMessage{}, choices: map[int]*collectedChoice{}, bytes: maxCodeBuddyResponseBytes}
	if c.accept([]byte(`{}`)) == nil {
		t.Fatal("response cap ignored")
	}
	v := &sseValidator{}
	frames, err := v.consume([]byte("data: {\"choices\":[],\"usage\":{\"total_tokens\":7}}\n\ndata: invalid\n\n"))
	if err == nil || len(frames) != 1 {
		t.Fatal("valid usage before malformed data was discarded")
	}
}
