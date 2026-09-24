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
	Transport            string              `yaml:"transport"`
	DirectEndpoint       string              `yaml:"direct_endpoint"`
	DirectModelsEndpoint string              `yaml:"direct_models_endpoint"`
	DirectCatalogFormat  string              `yaml:"direct_catalog_format"`
	DirectAuthEndpoint   string              `yaml:"direct_auth_endpoint"`
	DirectTokenMode      string              `yaml:"direct_token_mode"`
	OpenAPIEndpoint      string              `yaml:"openapi_endpoint"`
	OpenAPIUserAgent     string              `yaml:"openapi_user_agent"`
	DirectModels         []directModelConfig `yaml:"direct_models"`
	RequestTimeout       time.Duration       `yaml:"-"`
	RequestTimeoutRaw    string              `yaml:"request_timeout"`
	ModelCacheTTL        time.Duration       `yaml:"-"`
	ModelCacheTTLRaw     string              `yaml:"model_cache_ttl"`
}

type directModelConfig struct {
	ID                      string   `yaml:"id" json:"id"`
	DisplayName             string   `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Description             string   `yaml:"description,omitempty" json:"description,omitempty"`
	IsReasoning             bool     `yaml:"is_reasoning,omitempty" json:"is_reasoning,omitempty"`
	IsVL                    bool     `yaml:"is_vl,omitempty" json:"is_vl,omitempty"`
	MaxInputTokens          int64    `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	MaxOutputTokens         int64    `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	ReasoningEfforts        []string `yaml:"reasoning_efforts,omitempty" json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort  string   `yaml:"default_reasoning_effort,omitempty" json:"default_reasoning_effort,omitempty"`
	SupportsDisabled        bool     `yaml:"supports_disabled,omitempty" json:"supports_disabled,omitempty"`
	AvailableContextWindows []int64  `yaml:"available_context_windows,omitempty" json:"available_context_windows,omitempty"`
	DefaultContextWindow    int64    `yaml:"default_context_window,omitempty" json:"default_context_window,omitempty"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Transport:           "direct_openai",
		DirectCatalogFormat: "qoder",
		DirectTokenMode:     "auto",
		OpenAPIUserAgent:    "qoder/1.1.40",
		RequestTimeout:      30 * time.Second,
		ModelCacheTTL:       time.Minute,
	}
}

// nativeDirectEndpointDefaults 补齐运行原生 direct 所需的中国区默认 endpoints。
// decode 阶段在显式配置归一化之后才应用，保证显式覆盖永远优先。
func nativeDirectEndpointDefaults() (direct, models, openapi string) {
	return "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation",
		"https://gateway.qoder.com.cn/algo/api/v2/model/list",
		"https://openapi.qoder.com.cn"
}

// removedRunnerFields 是随 runner 一起移除的旧配置字段。旧版本未声明 transport 时默认
// sdk_cli，这类配置若被静默接受会改走原生 direct 默认值，因此 key 一出现就报迁移错误。
var removedRunnerFields = []string{
	"runner_command", "runner_args", "qoder_cli_path", "working_directory",
	"max_queue_frames", "permission_default", "permission_rules",
	"skills", "setting_sources", "allowed_tools", "disallowed_tools", "mcp_servers",
}

func rejectRemovedRunnerFields(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var probe map[string]any
	if errUnmarshal := yaml.Unmarshal(raw, &probe); errUnmarshal != nil {
		return nil // malformed YAML 由主解析统一报错
	}
	for _, field := range removedRunnerFields {
		if _, exists := probe[field]; exists {
			return fmt.Errorf("Qoder config field %q was removed with the sdk_cli runner; migrate the configuration to native direct (see docs/lts/pat-providers.md)", field)
		}
	}
	return nil
}

func decodePluginConfig(raw []byte) (pluginConfig, error) {
	if errRemoved := rejectRemovedRunnerFields(raw); errRemoved != nil {
		return pluginConfig{}, errRemoved
	}
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return pluginConfig{}, fmt.Errorf("decode Qoder plugin config: %w", errUnmarshal)
		}
	}
	cfg.Transport = strings.ToLower(strings.TrimSpace(cfg.Transport))
	cfg.DirectEndpoint = strings.TrimSpace(cfg.DirectEndpoint)
	cfg.DirectModelsEndpoint = strings.TrimSpace(cfg.DirectModelsEndpoint)
	cfg.DirectCatalogFormat = strings.ToLower(strings.TrimSpace(cfg.DirectCatalogFormat))
	if cfg.DirectCatalogFormat == "" {
		cfg.DirectCatalogFormat = "qoder"
	}
	if cfg.DirectCatalogFormat != "openai" && cfg.DirectCatalogFormat != "qoder" {
		return pluginConfig{}, fmt.Errorf("direct_catalog_format must be openai or qoder")
	}
	cfg.DirectAuthEndpoint = strings.TrimRight(strings.TrimSpace(cfg.DirectAuthEndpoint), "/")
	cfg.DirectTokenMode = strings.ToLower(strings.TrimSpace(cfg.DirectTokenMode))
	cfg.OpenAPIEndpoint = strings.TrimRight(strings.TrimSpace(cfg.OpenAPIEndpoint), "/")
	cfg.OpenAPIUserAgent = strings.TrimSpace(cfg.OpenAPIUserAgent)
	if cfg.Transport == "sdk_cli" {
		return pluginConfig{}, fmt.Errorf("Qoder no longer supports the sdk_cli transport; the plugin always runs native direct_openai")
	}
	if cfg.Transport != "" && cfg.Transport != "direct_openai" {
		return pluginConfig{}, fmt.Errorf("Qoder transport is not supported: %s", cfg.Transport)
	}
	// transport is a compatibility field only; execution is fixed to native direct.
	cfg.Transport = "direct_openai"
	if cfg.DirectTokenMode == "" {
		cfg.DirectTokenMode = "auto"
	}
	if cfg.DirectTokenMode != "auto" && cfg.DirectTokenMode != "bearer" && cfg.DirectTokenMode != "pat_exchange" {
		return pluginConfig{}, fmt.Errorf("direct_token_mode must be auto, bearer, or pat_exchange")
	}
	if cfg.OpenAPIUserAgent == "" {
		cfg.OpenAPIUserAgent = "qoder/1.1.40"
	}
	if len(cfg.OpenAPIUserAgent) > 256 || strings.ContainsAny(cfg.OpenAPIUserAgent, "\r\n") {
		return pluginConfig{}, fmt.Errorf("openapi_user_agent must be a single line of at most 256 bytes")
	}
	if cfg.DirectEndpoint != "" {
		if errEndpoint := validateDirectURL(cfg.DirectEndpoint, "direct_endpoint"); errEndpoint != nil {
			return pluginConfig{}, errEndpoint
		}
	}
	if cfg.DirectModelsEndpoint != "" {
		if errEndpoint := validateDirectURL(cfg.DirectModelsEndpoint, "direct_models_endpoint"); errEndpoint != nil {
			return pluginConfig{}, errEndpoint
		}
	}
	if cfg.OpenAPIEndpoint != "" {
		if errEndpoint := validateDirectURL(cfg.OpenAPIEndpoint, "openapi_endpoint"); errEndpoint != nil {
			return pluginConfig{}, errEndpoint
		}
	}
	if cfg.DirectAuthEndpoint != "" {
		if errEndpoint := validateDirectURL(cfg.DirectAuthEndpoint, "direct_auth_endpoint"); errEndpoint != nil {
			return pluginConfig{}, errEndpoint
		}
	}
	if cfg.OpenAPIEndpoint != "" && cfg.DirectAuthEndpoint != "" && cfg.OpenAPIEndpoint != cfg.DirectAuthEndpoint {
		return pluginConfig{}, fmt.Errorf("openapi_endpoint and direct_auth_endpoint must match when both are configured")
	}
	if cfg.OpenAPIEndpoint == "" {
		cfg.OpenAPIEndpoint = cfg.DirectAuthEndpoint
	}
	if cfg.DirectAuthEndpoint == "" {
		cfg.DirectAuthEndpoint = cfg.OpenAPIEndpoint
	}
	// Native direct defaults fill in only what the operator left unset, so an
	// explicit regional override always wins over the China-zone defaults.
	defaultDirect, defaultModels, defaultOpenAPI := nativeDirectEndpointDefaults()
	if cfg.DirectEndpoint == "" {
		cfg.DirectEndpoint = defaultDirect
	}
	if cfg.DirectModelsEndpoint == "" {
		cfg.DirectModelsEndpoint = defaultModels
	}
	if cfg.OpenAPIEndpoint == "" {
		cfg.OpenAPIEndpoint = defaultOpenAPI
		cfg.DirectAuthEndpoint = cfg.OpenAPIEndpoint
	}
	if cfg.DirectModels != nil && len(cfg.DirectModels) > 256 {
		return pluginConfig{}, fmt.Errorf("direct_models supports at most 256 entries")
	}
	seenDirectModels := make(map[string]struct{}, len(cfg.DirectModels))
	for index := range cfg.DirectModels {
		model := &cfg.DirectModels[index]
		model.ID = strings.TrimSpace(model.ID)
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		model.Description = strings.TrimSpace(model.Description)
		if model.ID == "" || len(model.ID) > 512 || strings.ContainsAny(model.ID, "\x00\r\n") {
			return pluginConfig{}, fmt.Errorf("direct_models contains an invalid id")
		}
		if _, exists := seenDirectModels[model.ID]; exists {
			return pluginConfig{}, fmt.Errorf("direct_models contains duplicate id %q", model.ID)
		}
		seenDirectModels[model.ID] = struct{}{}
		if model.DisplayName == "" {
			model.DisplayName = qoderDisplayName(model.ID, "")
		}
		if len(model.DisplayName) > 512 || len(model.Description) > 4096 {
			return pluginConfig{}, fmt.Errorf("direct_models contains an oversized display name or description")
		}
		if model.MaxInputTokens < 0 || model.MaxOutputTokens < 0 || model.DefaultContextWindow < 0 {
			return pluginConfig{}, fmt.Errorf("direct_models contains a negative token limit")
		}
		if len(model.ReasoningEfforts) > 16 {
			return pluginConfig{}, fmt.Errorf("direct_models reasoning_efforts supports at most 16 entries")
		}
		for effortIndex := range model.ReasoningEfforts {
			model.ReasoningEfforts[effortIndex] = strings.TrimSpace(model.ReasoningEfforts[effortIndex])
			if model.ReasoningEfforts[effortIndex] == "" || len(model.ReasoningEfforts[effortIndex]) > 64 {
				return pluginConfig{}, fmt.Errorf("direct_models contains an invalid reasoning effort")
			}
		}
	}
	var errDuration error
	if cfg.RequestTimeoutRaw != "" {
		cfg.RequestTimeout, errDuration = time.ParseDuration(cfg.RequestTimeoutRaw)
		if errDuration != nil || cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 10*time.Minute {
			return pluginConfig{}, fmt.Errorf("request_timeout must be between 1s and 10m")
		}
	}
	if cfg.ModelCacheTTLRaw != "" {
		cfg.ModelCacheTTL, errDuration = time.ParseDuration(cfg.ModelCacheTTLRaw)
		if errDuration != nil || cfg.ModelCacheTTL < 0 || cfg.ModelCacheTTL > 10*time.Minute {
			return pluginConfig{}, fmt.Errorf("model_cache_ttl must be between 0 and 10m")
		}
	}
	return cfg, nil
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
