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
	defaultCatalogUserAgent  = "WorkBuddy/5.4.5"
	defaultUserAgent         = "CPA-CodeBuddy-Provider/" + pluginVersion
)

type pluginConfig struct {
	Endpoint         string `yaml:"endpoint"`
	CatalogEndpoint  string `yaml:"catalog_endpoint"`
	BillingEndpoint  string `yaml:"billing_endpoint"`
	AccountEndpoint  string `yaml:"account_endpoint"`
	UserAgent        string `yaml:"user_agent"`
	CatalogUserAgent string `yaml:"catalog_user_agent"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Endpoint:         defaultCodeBuddyEndpoint,
		CatalogEndpoint:  defaultCatalogEndpoint,
		BillingEndpoint:  defaultBillingEndpoint,
		CatalogUserAgent: defaultCatalogUserAgent,
		UserAgent:        defaultUserAgent,
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
	cfg.AccountEndpoint = strings.TrimSpace(cfg.AccountEndpoint)
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
	if cfg.AccountEndpoint != "" {
		if errValidate := validateEndpointPath(cfg.AccountEndpoint, "account_endpoint", "/v2/plugin/accounts"); errValidate != nil {
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
