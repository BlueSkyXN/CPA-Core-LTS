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
	defaultUserAgent         = "CPA-CodeBuddy-Provider/" + pluginVersion
)

type pluginConfig struct {
	Endpoint  string `yaml:"endpoint"`
	UserAgent string `yaml:"user_agent"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Endpoint:  defaultCodeBuddyEndpoint,
		UserAgent: defaultUserAgent,
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
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if len(cfg.UserAgent) > 256 || strings.ContainsAny(cfg.UserAgent, "\r\n") {
		return pluginConfig{}, fmt.Errorf("CodeBuddy user_agent must be a single line of at most 256 bytes")
	}
	if errValidate := validateEndpoint(cfg.Endpoint); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	return cfg, nil
}

func validateEndpoint(raw string) error {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Host == "" {
		return fmt.Errorf("CodeBuddy endpoint is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("CodeBuddy endpoint must not contain credentials, query, or fragment")
	}
	if parsed.Path != "/v2/chat/completions" {
		return fmt.Errorf("CodeBuddy endpoint path must be /v2/chat/completions")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("CodeBuddy endpoint must use HTTPS")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("plain HTTP CodeBuddy endpoint is allowed only on loopback")
		}
	}
	return nil
}
