package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginConfig struct {
	ExcludedModelPrefixes []string      `yaml:"excluded_model_prefixes"`
	AccountType           string        `yaml:"account_type"`
	EnterpriseDomain      string        `yaml:"enterprise_domain"`
	OAuthClientID         string        `yaml:"oauth_client_id"`
	EditorVersion         string        `yaml:"editor_version"`
	EditorPluginVersion   string        `yaml:"editor_plugin_version"`
	UserAgent             string        `yaml:"user_agent"`
	APIVersion            string        `yaml:"api_version"`
	GitHubEndpoint        string        `yaml:"github_endpoint"`
	GitHubAPIEndpoint     string        `yaml:"github_api_endpoint"`
	CopilotAPIEndpoint    string        `yaml:"copilot_api_endpoint"`
	ModelCacheTTL         time.Duration `yaml:"-"`
	ModelCacheTTLRaw      string        `yaml:"model_cache_ttl"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		AccountType:         "individual",
		OAuthClientID:       "Iv1.b507a08c87ecfe98",
		EditorVersion:       "1.110.1",
		EditorPluginVersion: "copilot-chat/0.38.2",
		UserAgent:           "GitHubCopilotChat/0.38.2",
		APIVersion:          "2025-10-01",
		ModelCacheTTL:       time.Minute,
	}
}

// fillEndpoints 按 account_type/enterprise_domain 推导默认域；显式覆盖永远优先。
// GHES 的 enterprise_domain 同时改写 github.com 与 api.github.com 两个面。
func (cfg *pluginConfig) fillEndpoints() {
	githubBase, githubAPIBase, copilotBase := "https://github.com", "https://api.github.com", "https://api.githubcopilot.com"
	switch cfg.AccountType {
	case "business":
		copilotBase = "https://api.business.githubcopilot.com"
	case "enterprise":
		copilotBase = "https://api.enterprise.githubcopilot.com"
	}
	if cfg.EnterpriseDomain != "" {
		githubBase = "https://" + cfg.EnterpriseDomain
		githubAPIBase = "https://api." + cfg.EnterpriseDomain
		copilotBase = "https://copilot-api." + cfg.EnterpriseDomain
	}
	if cfg.GitHubEndpoint == "" {
		cfg.GitHubEndpoint = githubBase
	}
	if cfg.GitHubAPIEndpoint == "" {
		cfg.GitHubAPIEndpoint = githubAPIBase
	}
	if cfg.CopilotAPIEndpoint == "" {
		cfg.CopilotAPIEndpoint = copilotBase
	}
}

func decodePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return pluginConfig{}, fmt.Errorf("decode Copilot plugin config: %w", errUnmarshal)
		}
	}
	cfg.AccountType = strings.ToLower(strings.TrimSpace(cfg.AccountType))
	if cfg.AccountType == "" {
		cfg.AccountType = "individual"
	}
	if cfg.AccountType != "individual" && cfg.AccountType != "business" && cfg.AccountType != "enterprise" {
		return pluginConfig{}, fmt.Errorf("account_type must be individual, business, or enterprise")
	}
	cfg.EnterpriseDomain = strings.TrimSpace(cfg.EnterpriseDomain)
	if cfg.EnterpriseDomain != "" {
		cfg.EnterpriseDomain = normalizeEnterpriseDomain(cfg.EnterpriseDomain)
		if cfg.EnterpriseDomain == "" {
			return pluginConfig{}, fmt.Errorf("enterprise_domain must be a bare hostname without scheme or path")
		}
	}
	singleLine := func(value, name string, max int) (string, error) {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > max || strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("%s must be a single line of at most %d bytes", name, max)
		}
		return value, nil
	}
	var errField error
	if cfg.OAuthClientID, errField = singleLine(cfg.OAuthClientID, "oauth_client_id", 128); errField != nil {
		return pluginConfig{}, errField
	}
	if cfg.EditorVersion, errField = singleLine(cfg.EditorVersion, "editor_version", 64); errField != nil {
		return pluginConfig{}, errField
	}
	if cfg.EditorPluginVersion, errField = singleLine(cfg.EditorPluginVersion, "editor_plugin_version", 128); errField != nil {
		return pluginConfig{}, errField
	}
	if cfg.UserAgent, errField = singleLine(cfg.UserAgent, "user_agent", 256); errField != nil {
		return pluginConfig{}, errField
	}
	if cfg.APIVersion, errField = singleLine(cfg.APIVersion, "api_version", 64); errField != nil {
		return pluginConfig{}, errField
	}
	cfg.GitHubEndpoint = strings.TrimRight(strings.TrimSpace(cfg.GitHubEndpoint), "/")
	cfg.GitHubAPIEndpoint = strings.TrimRight(strings.TrimSpace(cfg.GitHubAPIEndpoint), "/")
	cfg.CopilotAPIEndpoint = strings.TrimRight(strings.TrimSpace(cfg.CopilotAPIEndpoint), "/")
	cfg.fillEndpoints()
	for _, endpoint := range []struct{ name, value string }{
		{"github_endpoint", cfg.GitHubEndpoint},
		{"github_api_endpoint", cfg.GitHubAPIEndpoint},
		{"copilot_api_endpoint", cfg.CopilotAPIEndpoint},
	} {
		if errEndpoint := validateDirectURL(endpoint.value, endpoint.name); errEndpoint != nil {
			return pluginConfig{}, errEndpoint
		}
	}
	if len(cfg.ExcludedModelPrefixes) > 64 {
		return pluginConfig{}, fmt.Errorf("excluded_model_prefixes supports at most 64 entries")
	}
	prefixes := make([]string, 0, len(cfg.ExcludedModelPrefixes))
	seenPrefixes := make(map[string]struct{}, len(cfg.ExcludedModelPrefixes))
	for _, entry := range cfg.ExcludedModelPrefixes {
		prefix := strings.TrimSpace(entry)
		if prefix == "" || len(prefix) > 256 || strings.ContainsAny(prefix, "\x00\r\n") {
			return pluginConfig{}, fmt.Errorf("excluded_model_prefixes contains an invalid entry")
		}
		if _, exists := seenPrefixes[prefix]; exists {
			continue
		}
		seenPrefixes[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	cfg.ExcludedModelPrefixes = prefixes
	var errDuration error
	if cfg.ModelCacheTTLRaw != "" {
		cfg.ModelCacheTTL, errDuration = time.ParseDuration(cfg.ModelCacheTTLRaw)
		if errDuration != nil || cfg.ModelCacheTTL < 0 || cfg.ModelCacheTTL > 10*time.Minute {
			return pluginConfig{}, fmt.Errorf("model_cache_ttl must be between 0 and 10m")
		}
	}
	return cfg, nil
}

// normalizeEnterpriseDomain 剥离 scheme、端口后缀路径与尾斜杠，只保留裸主机名。
func normalizeEnterpriseDomain(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
	if index := strings.Index(value, "/"); index >= 0 {
		value = value[:index]
	}
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "/"))
	if value == "" || strings.ContainsAny(value, "@\\ \t\r\n") {
		return ""
	}
	return value
}

func validateDirectURL(raw, name string) error {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute URL without credentials, query, or fragment", name)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("%s must use HTTPS; plain HTTP is allowed only on loopback", name)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
