package main

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// 2026-11-14 用真实 PAT 对 copilot.tencent.com 的实测契约。
// 企业成员额度：credit/limitNum/周期；个人资源包对无包凭据恒空；
// /v2/accounts 列出同一 uid 的企业 ultimate 与 personal 身份。

func TestEnterpriseUsageQuotaSummary(t *testing.T) {
	auth, _ := parseStoredAuth(testAuthJSON())
	host := newFakeHost()
	host.enterpriseUsageResponse = hostHTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"msg":"OK","data":{"credit":6.36,"cycleStartTime":"2026-11-01 00:00:00","cycleEndTime":"2026-11-30 23:59:59","limitNum":2000,"cycleResetTime":"2026-12-01 00:00:00"}}`),
	}
	// 个人资源包为空：不得把空包解释为数值零，也不得吞掉企业额度。
	host.billingResponse = hostHTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":null,"TotalCount":0,"TotalDosage":0}}}}`),
	}
	q := newPluginRuntime(host).codeBuddyQuotaSummary(auth, "fixture")
	if q.Status != "available" || q.Scope != "enterprise_member" || q.Source != "get-enterprise-user-usage" {
		t.Fatalf("enterprise quota not primary: %+v", q)
	}
	if q.UsedExact != "6.36" || q.TotalExact != "2000" || q.RemainingExact != "1993.64" {
		t.Fatalf("enterprise amounts wrong: %+v", q)
	}
	if len(q.Packages) != 1 || q.Packages[0].CycleStart != "2026-11-01 00:00:00" || q.Packages[0].Basis != "cycle" {
		t.Fatalf("enterprise package wrong: %+v", q.Packages)
	}
}

func TestEnterpriseUsageUnavailableFallsBackToPersonal(t *testing.T) {
	auth, _ := parseStoredAuth(testAuthJSON())
	host := newFakeHost()
	// 无企业身份：字段缺失，必须降级而不是伪造数值。
	host.enterpriseUsageResponse = hostHTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"data":{"cycleStartTime":"2026-11-01 00:00:00"}}`),
	}
	host.billingResponse = hostHTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"data":{"Response":{"Data":{"TotalCount":1,"Accounts":[{"PackageName":"bonus","CapacitySizePrecise":"10","CapacityUsedPrecise":"2.5","CapacityRemainPrecise":"7.5"}]}}}}`),
	}
	q := newPluginRuntime(host).codeBuddyQuotaSummary(auth, "fixture")
	if q.Status != "available" || q.Scope != "personal_resources" || q.TotalExact != "10" || q.RemainingExact != "7.5" {
		t.Fatalf("personal fallback wrong: %+v", q)
	}
}

func TestEnterpriseUsageAuthRejectedWins(t *testing.T) {
	auth, _ := parseStoredAuth(testAuthJSON())
	host := newFakeHost()
	host.enterpriseUsageResponse = hostHTTPResponse{StatusCode: 401, Body: nil}
	q := newPluginRuntime(host).codeBuddyQuotaSummary(auth, "fixture")
	if q.Status != "auth_rejected" || q.Code != "billing_auth_rejected" {
		t.Fatalf("auth rejection must dominate: %+v", q)
	}
}

func TestParseAccountIdentities(t *testing.T) {
	account := parseCodeBuddyAccount([]byte(`{"code":0,"data":{"accounts":[
		{"uid":"fixture-uid","nickname":"fixture-user","type":"ultimate","lastLogin":true,"enterpriseId":"fixture-ent","enterpriseName":"Fixture Enterprise","enterpriseUserName":"Fixture Member","pluginEnabled":true},
		{"uid":"fixture-uid","nickname":"fixture-user","type":"personal","lastLogin":false,"pluginEnabled":true}
	]}}`))
	if account.Status != "available" || account.ID != "fixture-uid" || account.Name != "fixture-user" {
		t.Fatalf("account identity wrong: %+v", account)
	}
	if account.Scope != "enterprise_member" || account.AccountType != "ultimate" || account.EnterpriseID != "fixture-ent" {
		t.Fatalf("active enterprise identity not selected: %+v", account)
	}
	if len(account.EnterpriseUsers) != 2 || account.EnterpriseUsers[1].Type != "personal" {
		t.Fatalf("parallel identities lost: %+v", account.EnterpriseUsers)
	}
}

func TestParseAccountPersonalOnly(t *testing.T) {
	account := parseCodeBuddyAccount([]byte(`{"code":0,"data":{"accounts":[{"uid":"u1","nickname":"solo","type":"personal","lastLogin":true}]}}`))
	if account.Status != "available" || account.Scope != "personal" || account.AccountType != "personal" {
		t.Fatalf("personal scope wrong: %+v", account)
	}
}

func TestParseAccountRejected(t *testing.T) {
	account := parseCodeBuddyAccount([]byte(`{"code":401,"data":null}`))
	if account.Status != "unsupported" {
		t.Fatalf("rejected envelope must not be available: %+v", account)
	}
	account = parseCodeBuddyAccount([]byte(`{"code":0,"data":{"accounts":[]}}`))
	if account.Status != "unsupported" || account.Code != "account_fields_missing" {
		t.Fatalf("empty accounts wrong: %+v", account)
	}
}

func TestPaymentTypeSummary(t *testing.T) {
	auth, _ := parseStoredAuth(testAuthJSON())
	host := newFakeHost()
	host.paymentResponse = hostHTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"data":{"paymentType":"free"}}`),
	}
	plan := newPluginRuntime(host).fetchPaymentType(auth, "fixture")
	if plan.Status != "available" || plan.Name != "free" || plan.Source != "get-payment-type" {
		t.Fatalf("payment type wrong: %+v", plan)
	}
	host.paymentResponse = hostHTTPResponse{StatusCode: 401, Body: nil}
	plan = newPluginRuntime(host).fetchPaymentType(auth, "fixture")
	if plan.Status != "unknown" || plan.Code != "payment_type_auth_rejected" {
		t.Fatalf("payment type rejection wrong: %+v", plan)
	}
}

func TestUpstreamBusinessCodeClassification(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{11128, "upstream_channel_rejected"},
		{10105, "upstream_active_session_limit"},
		{11140, "upstream_auth_forbidden"},
		{11142, "upstream_auth_forbidden"},
		{11141, "upstream_model_error"},
		{15001, "upstream_web_search_quota"},
	} {
		err := codeBuddyUpstreamError(400, tc.code)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			sub := ""
			if typed, ok := err.(*pluginCallError); ok {
				sub = typed.code
			}
			if sub != tc.want {
				t.Fatalf("code %d classified as %q, want %q", tc.code, sub, tc.want)
			}
		}
	}
}

func TestDefaultClientProfileMatchesWorkBuddyShape(t *testing.T) {
	cfg, err := decodePluginConfig(nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// WorkBuddy 5.8.2 内置 CLI 的 buildUserAgent：平台段在前，应用段在后；
	// 旧默认值的重复应用段是日志合并产物，不允许回归。
	if !strings.HasPrefix(cfg.CatalogUserAgent, "CLI/") || strings.Count(cfg.CatalogUserAgent, "WorkBuddy/") != 1 {
		t.Fatalf("default UA shape wrong: %q", cfg.CatalogUserAgent)
	}
	if cfg.EnterpriseUsageEndpoint == "" || cfg.AccountEndpoint == "" || cfg.PaymentTypeEndpoint == "" {
		t.Fatalf("new endpoint defaults missing: %+v", cfg)
	}
}

func TestSummaryCacheKeyCoversNewEndpoints(t *testing.T) {
	base := defaultPluginConfig()
	changed := defaultPluginConfig()
	changed.EnterpriseUsageEndpoint = "https://fixture.example.com/v2/billing/meter/get-enterprise-user-usage"
	auth, _ := parseStoredAuth(testAuthJSON())
	if codeBuddySummaryCacheKey(auth, base, "1") == codeBuddySummaryCacheKey(auth, changed, "1") {
		t.Fatal("cache key ignores enterprise_usage_endpoint")
	}
}

var _ = pluginabi.MethodHostHTTPDo

func TestNormalizeCodeBuddyRolesMapsDeveloperToSystem(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "developer", "content": "You are a helpful assistant."},
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "developer", "content": "second"},
		},
	}
	normalizeCodeBuddyRoles(body)
	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "You are a helpful assistant." {
		t.Fatalf("developer not mapped verbatim: %v", messages[0])
	}
	if messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("user message mutated: %v", messages[1])
	}
	if messages[2].(map[string]any)["role"] != "system" || messages[2].(map[string]any)["content"] != "second" {
		t.Fatalf("second developer not mapped: %v", messages[2])
	}
}

func TestNormalizeCodeBuddyRolesNoopWithoutDeveloper(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "keep"},
		map[string]any{"role": "user", "content": "hi"},
	}
	body := map[string]any{"messages": messages}
	normalizeCodeBuddyRoles(body)
	if body["messages"].([]any)[0].(map[string]any)["content"] != "keep" {
		t.Fatal("messages were rewritten without developer roles")
	}
	if _, ok := body["messages"].([]any); !ok {
		t.Fatal("messages type changed")
	}
}

func TestSanitizeCodeBuddyFingerprintsReplacesVerifiedLiterals(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude.\n\nMain branch (you will usually use this for PRs)"},
		map[string]any{"role": "user", "content": "You are Claude Code, Anthropic's official CLI for Claude. Main branch (you will usually use this for PRs)"},
	}}
	sanitizeCodeBuddyFingerprints(body)
	messages := body["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	if strings.Contains(system, "You are Claude Code") || strings.Contains(system, "Main branch (") {
		t.Fatalf("system fingerprints survived: %q", system)
	}
	if !strings.Contains(system, "Default branch (you will usually use this for PRs)") {
		t.Fatalf("minimal rewrite lost meaning: %q", system)
	}
	user := messages[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "You are Claude Code") || !strings.Contains(user, "Main branch (") {
		t.Fatal("user-role content was rewritten; only system/developer may be sanitized")
	}
}

func TestSanitizeCodeBuddyFingerprintsNoopOnCleanBody(t *testing.T) {
	messages := []any{map[string]any{"role": "system", "content": "You are ZCode, keep everything."}}
	body := map[string]any{"messages": messages}
	sanitizeCodeBuddyFingerprints(body)
	if body["messages"].([]any)[0].(map[string]any)["content"] != "You are ZCode, keep everything." {
		t.Fatal("clean system prompt was rewritten")
	}
}

func TestSanitizeCodeBuddyFingerprintsBillingHeaderAnywhere(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "attribution: x-anthropic-billing-header: cc_version=2.1.280; cch=abc"},
	}}
	sanitizeCodeBuddyFingerprints(body)
	content := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(content, "x-anthropic-billing-header") {
		t.Fatalf("billing header fingerprint survived: %q", content)
	}
}
