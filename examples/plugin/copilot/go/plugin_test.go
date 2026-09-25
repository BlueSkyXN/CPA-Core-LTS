package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCopilotAuthFileParsing(t *testing.T) {
	secret := "ghp-test-secret-never-log"
	oauth, errOAuth := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"oauth","github_token":"` + secret + `","label":"Work"}`))
	if errOAuth != nil || oauth.AuthMode != "oauth" || oauth.GitHubToken != secret || oauth.Label != "Work" {
		t.Fatalf("parse oauth = %#v, %v", oauth, errOAuth)
	}
	githubToken, errToken := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"github_token","github_token":"` + secret + `"}`))
	if errToken != nil || githubToken.AuthMode != "github_token" || githubToken.tokenSource() != secret {
		t.Fatalf("parse github_token = %#v, %v", githubToken, errToken)
	}
	if _, errMode := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"pat","github_token":"` + secret + `"}`)); errMode == nil {
		t.Fatal("foreign auth_mode accepted")
	}
	if _, errMissingMode := parseStoredAuth([]byte(`{"type":"copilot","github_token":"` + secret + `"}`)); errMissingMode == nil {
		t.Fatal("missing auth_mode accepted")
	}
	if _, errMissingToken := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"oauth"}`)); errMissingToken == nil {
		t.Fatal("missing github_token accepted")
	}
	if _, errForeign := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"oauth","github_token":"` + secret + `"}`)); errForeign == nil {
		t.Fatal("foreign type accepted")
	}
	if _, errWhitespace := parseStoredAuth([]byte(`{"type":"copilot","auth_mode":"oauth","github_token":" ` + secret + ` "}`)); errWhitespace == nil {
		t.Fatal("token with surrounding whitespace accepted")
	}
	if _, errControl := parseStoredAuth([]byte("{\"type\":\"copilot\",\"auth_mode\":\"oauth\",\"github_token\":\"a\\nb\"}")); errControl == nil {
		t.Fatal("token with control characters accepted")
	}
	if _, errBadJSON := parseStoredAuth([]byte(`{`)); errBadJSON == nil {
		t.Fatal("invalid JSON accepted")
	}
	// ParseAuth 对异 type 声明 Handled:false 透传。
	parseRaw, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "copilot.json", RawJSON: []byte(`{"type":"other","auth_mode":"oauth"}`)})
	parsed, errParse := parseAuthRequest(parseRaw)
	if errParse != nil || parsed.Handled {
		t.Fatalf("foreign parse = %#v, %v", parsed, errParse)
	}
	copilotRaw, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: " copilot.json ", RawJSON: []byte(`{"type":"copilot","auth_mode":"oauth","github_token":"` + secret + `"}`)})
	parsed, errParse = parseAuthRequest(copilotRaw)
	if errParse != nil || !parsed.Handled || parsed.Auth.Provider != pluginIdentifier || parsed.Auth.FileName != "copilot.json" || parsed.Auth.Label != "GitHub Copilot" {
		t.Fatalf("copilot parse = %#v, %v", parsed, errParse)
	}
	if parsed.Auth.Metadata["auth_mode"] != "oauth" || parsed.Auth.Attributes["multi_account"] != "supported" {
		t.Fatalf("parse metadata = %#v / %#v", parsed.Auth.Metadata, parsed.Auth.Attributes)
	}
	// 错误信息与 envelope 绝不携带凭据。
	safe := string(errorEnvelope(newPluginCallError("invalid_auth", "Copilot authentication failed", http.StatusUnauthorized, false)))
	if strings.Contains(safe, secret) {
		t.Fatalf("error envelope leaked secret: %s", safe)
	}
}

func TestRefreshAuthValidatesThenReturnsStorageVerbatim(t *testing.T) {
	storage := []byte(`{"type":"copilot","auth_mode":"github_token","github_token":"ghp-fixture","label":"Keep"}`)
	refreshRaw, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: "auth-1", StorageJSON: storage, Metadata: map[string]any{"type": "copilot"}, Attributes: map[string]string{"auth_mode": "github_token"}})
	refreshed, errRefresh := refreshAuthRequest(refreshRaw)
	if errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if string(refreshed.Auth.StorageJSON) != string(storage) || refreshed.Auth.ID != "auth-1" || refreshed.Auth.Provider != pluginIdentifier {
		t.Fatalf("refresh = %#v", refreshed.Auth)
	}
	if refreshed.Auth.Metadata["type"] != "copilot" || refreshed.Auth.Attributes["auth_mode"] != "github_token" {
		t.Fatalf("refresh metadata = %#v / %#v", refreshed.Auth.Metadata, refreshed.Auth.Attributes)
	}
	invalid, _ := json.Marshal(pluginapi.AuthRefreshRequest{StorageJSON: []byte(`{"type":"copilot","auth_mode":"oauth"}`)})
	if _, err := refreshAuthRequest(invalid); err == nil {
		t.Fatal("invalid refresh storage accepted")
	}
}

func TestCopilotConfigDefaultsAndAccountTypes(t *testing.T) {
	cfg, err := decodePluginConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountType != "individual" || cfg.CopilotAPIEndpoint != "https://api.githubcopilot.com" || cfg.GitHubEndpoint != "https://github.com" || cfg.GitHubAPIEndpoint != "https://api.github.com" {
		t.Fatalf("defaults = %#v", cfg)
	}
	if cfg.OAuthClientID != "Iv1.b507a08c87ecfe98" || cfg.EditorVersion != "1.110.1" || cfg.EditorPluginVersion != "copilot-chat/0.38.2" || cfg.UserAgent != "GitHubCopilotChat/0.38.2" || cfg.APIVersion != "2025-10-01" {
		t.Fatalf("identity defaults = %#v", cfg)
	}
	business, err := decodePluginConfig([]byte("account_type: Business\n"))
	if err != nil || business.CopilotAPIEndpoint != "https://api.business.githubcopilot.com" {
		t.Fatalf("business = %#v, %v", business, err)
	}
	enterprise, err := decodePluginConfig([]byte("account_type: enterprise\n"))
	if err != nil || enterprise.CopilotAPIEndpoint != "https://api.enterprise.githubcopilot.com" {
		t.Fatalf("enterprise = %#v, %v", enterprise, err)
	}
	if _, err := decodePluginConfig([]byte("account_type: team\n")); err == nil {
		t.Fatal("invalid account_type accepted")
	}
}

func TestCopilotConfigEnterpriseDomainAndOverrides(t *testing.T) {
	cfg, err := decodePluginConfig([]byte("enterprise_domain: ghes.example.internal/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnterpriseDomain != "ghes.example.internal" || cfg.GitHubEndpoint != "https://ghes.example.internal" || cfg.GitHubAPIEndpoint != "https://api.ghes.example.internal" || cfg.CopilotAPIEndpoint != "https://copilot-api.ghes.example.internal" {
		t.Fatalf("GHES endpoints = %#v", cfg)
	}
	stripped, err := decodePluginConfig([]byte("enterprise_domain: https://GHES.Example.internal/prefix\n"))
	if err != nil || stripped.EnterpriseDomain != "ghes.example.internal" {
		t.Fatalf("domain normalization = %q, %v", stripped.EnterpriseDomain, err)
	}
	if _, err := decodePluginConfig([]byte("enterprise_domain: \"https://only-scheme\"\n")); err != nil {
		t.Fatalf("scheme-only domain rejected: %v", err)
	}
	override, err := decodePluginConfig([]byte("github_api_endpoint: https://api.github.example.test\ncopilot_api_endpoint: https://copilot.example.test\n"))
	if err != nil || override.GitHubAPIEndpoint != "https://api.github.example.test" || override.CopilotAPIEndpoint != "https://copilot.example.test" {
		t.Fatalf("endpoint overrides = %#v, %v", override, err)
	}
	if _, err := decodePluginConfig([]byte("copilot_api_endpoint: http://api.example.test\n")); err == nil {
		t.Fatal("plain HTTP endpoint accepted off loopback")
	}
	loopback, err := decodePluginConfig([]byte("copilot_api_endpoint: http://127.0.0.1:8080\n"))
	if err != nil || loopback.CopilotAPIEndpoint != "http://127.0.0.1:8080" {
		t.Fatalf("loopback HTTP = %#v, %v", loopback, err)
	}
}

func TestCopilotConfigExcludedPrefixesAndTTLs(t *testing.T) {
	cfg, err := decodePluginConfig([]byte("excluded_model_prefixes:\n  - o3\n  - gpt-4\n  - o3\n  - \"  \"\n"))
	if err == nil {
		t.Fatalf("blank prefix accepted: %#v", cfg.ExcludedModelPrefixes)
	}
	cfg, err = decodePluginConfig([]byte("excluded_model_prefixes:\n  - o3\n  - gpt-4\n  - o3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ExcludedModelPrefixes, ",") != "o3,gpt-4" {
		t.Fatalf("prefix dedupe failed: %#v", cfg.ExcludedModelPrefixes)
	}
	if _, err := decodePluginConfig([]byte("model_cache_ttl: 30m\n")); err == nil {
		t.Fatal("oversized model_cache_ttl accepted")
	}
	cacheTTL, err := decodePluginConfig([]byte("model_cache_ttl: 2m\n"))
	if err != nil || cacheTTL.ModelCacheTTL != 2*time.Minute {
		t.Fatalf("ttl parsing = %#v, %v", cacheTTL, err)
	}
	// request_timeout 已移除：历史配置里遗留的该 key 必须被静默忽略而不是报错。
	if _, err := decodePluginConfig([]byte("request_timeout: 45s\n")); err != nil {
		t.Fatalf("legacy request_timeout key rejected: %v", err)
	}
}

func TestValidateCopilotModelBasics(t *testing.T) {
	if err := validateCanonicalModel("  "); err == nil {
		t.Fatal("empty model accepted")
	}
	if err := validateCanonicalModel("gpt-5-codex"); err != nil {
		t.Fatalf("exact model rejected: %v", err)
	}
	if err := validateCanonicalModel("bad\x00model"); err == nil {
		t.Fatal("control character model accepted")
	}
}

func TestConfigureResumesQuiescedRuntime(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.quiesce()
	if err := runtime.configure(nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	runtime.mu.Lock()
	accepting := runtime.accepting
	runtime.mu.Unlock()
	if !accepting {
		t.Fatal("successful reconfigure did not resume a quiesced runtime")
	}
}
