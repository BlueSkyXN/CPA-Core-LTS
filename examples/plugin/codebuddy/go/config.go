package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultCodeBuddyEndpoint = "https://copilot.tencent.com/v2/chat/completions"
	defaultCatalogEndpoint   = "https://copilot.tencent.com/v3/config"
	defaultBillingEndpoint   = "https://copilot.tencent.com/v2/billing/meter/get-user-resource"
	// 企业成员额度（credit/limitNum/周期）。2026-11-14 用真实 PAT 验证 key 可访问；
	// 服务端按凭据解析成员身份，无需额外身份头。
	defaultEnterpriseUsageEndpoint = "https://copilot.tencent.com/v2/billing/meter/get-enterprise-user-usage"
	defaultAccountEndpoint         = "https://copilot.tencent.com/v2/accounts"
	defaultPaymentTypeEndpoint     = "https://copilot.tencent.com/v2/billing/meter/get-payment-type"
	// UA 形状按 WorkBuddy 5.8.2 内置 CLI 的 buildUserAgent 逆向：平台段在前、应用段在后
	// （CLI/2.161.0 来自内嵌 @tencent-ai/codebuddy-code，5.8.2 为 genieVersion）。
	// 仅 HTTP 标识，无需安装 CLI；旧默认值 WorkBuddy/5.3.14 双写为日志合并产物，形状错误。
	defaultCatalogUserAgent = "CLI/2.161.0 WorkBuddy/5.8.2"
	defaultUserAgent        = defaultCatalogUserAgent
)

type pluginConfig struct {
	Endpoint                string `yaml:"endpoint"`
	CatalogEndpoint         string `yaml:"catalog_endpoint"`
	BillingEndpoint         string `yaml:"billing_endpoint"`
	EnterpriseUsageEndpoint string `yaml:"enterprise_usage_endpoint"`
	AccountEndpoint         string `yaml:"account_endpoint"`
	PaymentTypeEndpoint     string `yaml:"payment_type_endpoint"`
	UserAgent               string `yaml:"user_agent"`
	CatalogUserAgent        string `yaml:"catalog_user_agent"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Endpoint:                defaultCodeBuddyEndpoint,
		CatalogEndpoint:         defaultCatalogEndpoint,
		BillingEndpoint:         defaultBillingEndpoint,
		EnterpriseUsageEndpoint: defaultEnterpriseUsageEndpoint,
		AccountEndpoint:         defaultAccountEndpoint,
		PaymentTypeEndpoint:     defaultPaymentTypeEndpoint,
		CatalogUserAgent:        defaultCatalogUserAgent,
		UserAgent:               defaultUserAgent,
	}
}

func decodePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return pluginConfig{}, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.CatalogEndpoint = strings.TrimSpace(cfg.CatalogEndpoint)
	cfg.BillingEndpoint = strings.TrimSpace(cfg.BillingEndpoint)
	cfg.EnterpriseUsageEndpoint = strings.TrimSpace(cfg.EnterpriseUsageEndpoint)
	cfg.AccountEndpoint = strings.TrimSpace(cfg.AccountEndpoint)
	cfg.PaymentTypeEndpoint = strings.TrimSpace(cfg.PaymentTypeEndpoint)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.CatalogUserAgent = strings.TrimSpace(cfg.CatalogUserAgent)
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if cfg.CatalogUserAgent == "" {
		cfg.CatalogUserAgent = defaultCatalogUserAgent
	}
	if errAgent := validateUserAgent(cfg.UserAgent, "user_agent"); errAgent != nil {
		return pluginConfig{}, errAgent
	}
	if errAgent := validateUserAgent(cfg.CatalogUserAgent, "catalog_user_agent"); errAgent != nil {
		return pluginConfig{}, errAgent
	}
	if errValidate := validateEndpointPath(cfg.Endpoint, "endpoint", "/v2/chat/completions"); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	if errValidate := validateEndpointPath(cfg.CatalogEndpoint, "catalog_endpoint", "/v3/config"); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	if errValidate := validateEndpointPath(cfg.BillingEndpoint, "billing_endpoint", "/v2/billing/meter/get-user-resource"); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	if cfg.EnterpriseUsageEndpoint != "" {
		if errValidate := validateEndpointPath(cfg.EnterpriseUsageEndpoint, "enterprise_usage_endpoint", "/v2/billing/meter/get-enterprise-user-usage"); errValidate != nil {
			return pluginConfig{}, errValidate
		}
	}
	if cfg.AccountEndpoint != "" {
		if errValidate := validateEndpointPath(cfg.AccountEndpoint, "account_endpoint", "/v2/accounts"); errValidate != nil {
			return pluginConfig{}, errValidate
		}
	}
	if cfg.PaymentTypeEndpoint != "" {
		if errValidate := validateEndpointPath(cfg.PaymentTypeEndpoint, "payment_type_endpoint", "/v2/billing/meter/get-payment-type"); errValidate != nil {
			return pluginConfig{}, errValidate
		}
	}
	return cfg, nil
}

func validateUserAgent(value, name string) error {
	if len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("CodeBuddy %s must be a single line of at most 256 bytes", name)
	}
	return nil
}

func validateEndpoint(raw string) error {
	return validateEndpointPath(raw, "endpoint", "/v2/chat/completions")
}

func validateEndpointPath(raw, name, expectedPath string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("CodeBuddy %s is required", name)
	}
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Host == "" {
		return fmt.Errorf("CodeBuddy %s is invalid", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("CodeBuddy %s must not contain credentials, query, or fragment", name)
	}
	if parsed.Path != expectedPath {
		return fmt.Errorf("CodeBuddy %s path must be %s", name, expectedPath)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("CodeBuddy %s must use HTTPS", name)
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("plain HTTP CodeBuddy %s is allowed only on loopback", name)
		}
	}
	return nil
}
