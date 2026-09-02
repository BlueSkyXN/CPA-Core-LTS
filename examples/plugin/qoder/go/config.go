package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type permissionRule struct {
	ToolName      string         `yaml:"tool_name" json:"tool_name"`
	Behavior      string         `yaml:"behavior" json:"behavior"`
	ModifiedInput map[string]any `yaml:"modified_input,omitempty" json:"modified_input,omitempty"`
}

type mcpServerConfig struct {
	Type    string            `yaml:"type,omitempty" json:"type,omitempty"`
	Command string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type pluginConfig struct {
	Transport            string                     `yaml:"transport"`
	RunnerCommand        string                     `yaml:"runner_command"`
	RunnerArgs           []string                   `yaml:"runner_args"`
	QoderCLIPath         string                     `yaml:"qoder_cli_path"`
	DirectEndpoint       string                     `yaml:"direct_endpoint"`
	DirectModelsEndpoint string                     `yaml:"direct_models_endpoint"`
	DirectAuthEndpoint   string                     `yaml:"direct_auth_endpoint"`
	DirectTokenMode      string                     `yaml:"direct_token_mode"`
	OpenAPIEndpoint      string                     `yaml:"openapi_endpoint"`
	OpenAPIUserAgent     string                     `yaml:"openapi_user_agent"`
	DirectModels         []directModelConfig        `yaml:"direct_models"`
	WorkingDirectory     string                     `yaml:"working_directory"`
	MaxQueueFrames       int                        `yaml:"max_queue_frames"`
	RequestTimeout       time.Duration              `yaml:"-"`
	RequestTimeoutRaw    string                     `yaml:"request_timeout"`
	ModelCacheTTL        time.Duration              `yaml:"-"`
	ModelCacheTTLRaw     string                     `yaml:"model_cache_ttl"`
	PermissionDefault    string                     `yaml:"permission_default"`
	PermissionRules      []permissionRule           `yaml:"permission_rules"`
	Skills               []string                   `yaml:"skills"`
	SettingSources       []string                   `yaml:"setting_sources"`
	AllowedTools         []string                   `yaml:"allowed_tools"`
	DisallowedTools      []string                   `yaml:"disallowed_tools"`
	MCPServers           map[string]mcpServerConfig `yaml:"mcp_servers"`
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
		Transport:         "sdk_cli",
		RunnerCommand:     "cpa-qoder-runner",
		WorkingDirectory:  os.TempDir(),
		MaxQueueFrames:    128,
		RequestTimeout:    30 * time.Second,
		ModelCacheTTL:     time.Minute,
		PermissionDefault: "deny",
		DirectTokenMode:   "auto",
		OpenAPIUserAgent:  "qoder/1.1.40",
	}
}

func decodePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return pluginConfig{}, fmt.Errorf("decode Qoder plugin config: %w", errUnmarshal)
		}
	}
	cfg.RunnerCommand = strings.TrimSpace(cfg.RunnerCommand)
	cfg.Transport = strings.ToLower(strings.TrimSpace(cfg.Transport))
	cfg.QoderCLIPath = strings.TrimSpace(cfg.QoderCLIPath)
	cfg.DirectEndpoint = strings.TrimSpace(cfg.DirectEndpoint)
	cfg.DirectModelsEndpoint = strings.TrimSpace(cfg.DirectModelsEndpoint)
	cfg.DirectAuthEndpoint = strings.TrimRight(strings.TrimSpace(cfg.DirectAuthEndpoint), "/")
	cfg.DirectTokenMode = strings.ToLower(strings.TrimSpace(cfg.DirectTokenMode))
	cfg.OpenAPIEndpoint = strings.TrimRight(strings.TrimSpace(cfg.OpenAPIEndpoint), "/")
	cfg.OpenAPIUserAgent = strings.TrimSpace(cfg.OpenAPIUserAgent)
	if cfg.Transport == "" {
		cfg.Transport = "sdk_cli"
	}
	if cfg.Transport != "sdk_cli" && cfg.Transport != "direct_openai" {
		return pluginConfig{}, fmt.Errorf("transport must be sdk_cli or direct_openai")
	}
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
	cfg.WorkingDirectory = strings.TrimSpace(cfg.WorkingDirectory)
	if cfg.RunnerCommand == "" || strings.ContainsRune(cfg.RunnerCommand, '\x00') {
		return pluginConfig{}, fmt.Errorf("runner_command is required")
	}
	for _, arg := range cfg.RunnerArgs {
		if strings.ContainsRune(arg, '\x00') {
			return pluginConfig{}, fmt.Errorf("runner_args contains an invalid argument")
		}
		for _, owned := range []string{"--stdio", "--cli-path", "--cwd", "--max-queue-frames", "--version", "--transport", "--direct-endpoint", "--direct-models-endpoint", "--direct-auth-endpoint", "--direct-token-mode", "--openapi-endpoint", "--openapi-user-agent", "--direct-models-json"} {
			if arg == owned || strings.HasPrefix(arg, owned+"=") {
				return pluginConfig{}, fmt.Errorf("runner_args must not override plugin-owned runner arguments")
			}
		}
	}
	if cfg.QoderCLIPath != "" && (!filepath.IsAbs(cfg.QoderCLIPath) || strings.ContainsRune(cfg.QoderCLIPath, '\x00')) {
		return pluginConfig{}, fmt.Errorf("qoder_cli_path must be an absolute path")
	}
	if cfg.Transport == "direct_openai" && cfg.DirectEndpoint == "" {
		return pluginConfig{}, fmt.Errorf("direct_endpoint is required when transport is direct_openai")
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
	if cfg.DirectTokenMode == "pat_exchange" && cfg.DirectAuthEndpoint == "" {
		return pluginConfig{}, fmt.Errorf("direct_auth_endpoint or openapi_endpoint is required when direct_token_mode is pat_exchange")
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
		if model.ID == "" || len(model.ID) > 512 || strings.ContainsAny(model.ID, "\\x00\\r\\n") {
			return pluginConfig{}, fmt.Errorf("direct_models contains an invalid id")
		}
		if _, exists := seenDirectModels[model.ID]; exists {
			return pluginConfig{}, fmt.Errorf("direct_models contains duplicate id %q", model.ID)
		}
		seenDirectModels[model.ID] = struct{}{}
		if model.DisplayName == "" {
			model.DisplayName = model.ID
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
	if cfg.DirectModelsEndpoint == "" && len(cfg.DirectModels) == 0 && cfg.Transport == "direct_openai" {
		return pluginConfig{}, fmt.Errorf("direct_models or direct_models_endpoint is required for direct_openai transport")
	}
	if cfg.WorkingDirectory == "" || !filepath.IsAbs(cfg.WorkingDirectory) {
		return pluginConfig{}, fmt.Errorf("working_directory must be an absolute path")
	}
	if cfg.MaxQueueFrames < 1 || cfg.MaxQueueFrames > 4096 {
		return pluginConfig{}, fmt.Errorf("max_queue_frames must be between 1 and 4096")
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
	if cfg.PermissionDefault != "deny" && cfg.PermissionDefault != "cancel_turn" {
		return pluginConfig{}, fmt.Errorf("permission_default must be deny or cancel_turn")
	}
	for index := range cfg.PermissionRules {
		rule := &cfg.PermissionRules[index]
		rule.ToolName = strings.TrimSpace(rule.ToolName)
		if rule.ToolName == "" {
			return pluginConfig{}, fmt.Errorf("permission_rules tool_name is required")
		}
		switch rule.Behavior {
		case "allow", "deny", "cancel_turn":
		case "allow_with_modified_input":
			if rule.ModifiedInput == nil {
				return pluginConfig{}, fmt.Errorf("allow_with_modified_input requires modified_input")
			}
		default:
			return pluginConfig{}, fmt.Errorf("permission_rules behavior is unsupported")
		}
	}
	var errList error
	if cfg.Skills, errList = validateConfigList("skills", cfg.Skills, 64, 128); errList != nil {
		return pluginConfig{}, errList
	}
	if cfg.AllowedTools, errList = validateConfigList("allowed_tools", cfg.AllowedTools, 256, 256); errList != nil {
		return pluginConfig{}, errList
	}
	if cfg.DisallowedTools, errList = validateConfigList("disallowed_tools", cfg.DisallowedTools, 256, 256); errList != nil {
		return pluginConfig{}, errList
	}
	denied := make(map[string]struct{}, len(cfg.DisallowedTools))
	for _, tool := range cfg.DisallowedTools {
		denied[tool] = struct{}{}
	}
	for _, tool := range cfg.AllowedTools {
		if _, exists := denied[tool]; exists {
			return pluginConfig{}, fmt.Errorf("tool %q cannot appear in both allowed_tools and disallowed_tools", tool)
		}
	}
	if len(cfg.SettingSources) > 3 {
		return pluginConfig{}, fmt.Errorf("setting_sources supports at most user, project, and local")
	}
	seenSources := make(map[string]struct{}, len(cfg.SettingSources))
	for index, source := range cfg.SettingSources {
		source = strings.TrimSpace(source)
		switch source {
		case "user", "project", "local":
		default:
			return pluginConfig{}, fmt.Errorf("setting_sources contains unsupported source %q", source)
		}
		if _, exists := seenSources[source]; exists {
			return pluginConfig{}, fmt.Errorf("setting_sources contains duplicate source %q", source)
		}
		seenSources[source] = struct{}{}
		cfg.SettingSources[index] = source
	}
	if len(cfg.MCPServers) > 32 {
		return pluginConfig{}, fmt.Errorf("mcp_servers supports at most 32 fixed servers")
	}
	for name, server := range cfg.MCPServers {
		if errMCP := validateMCPServer(name, &server); errMCP != nil {
			return pluginConfig{}, errMCP
		}
		cfg.MCPServers[name] = server
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

func directModelsJSON(models []directModelConfig) (string, error) {
	if len(models) == 0 {
		return "", nil
	}
	raw, errMarshal := json.Marshal(models)
	if errMarshal != nil {
		return "", fmt.Errorf("encode direct_models: %w", errMarshal)
	}
	if len(raw) > 128*1024 {
		return "", fmt.Errorf("direct_models JSON exceeds the bounded 128KiB runner argument limit")
	}
	return string(raw), nil
}

var (
	configEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mcpNamePattern       = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func validateConfigList(name string, values []string, maxItems, maxLength int) ([]string, error) {
	if len(values) > maxItems {
		return nil, fmt.Errorf("%s supports at most %d entries", name, maxItems)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > maxLength || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("%s contains an invalid entry", name)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s contains duplicate entry %q", name, value)
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func validateMCPServer(name string, server *mcpServerConfig) error {
	if server == nil || len(name) > 64 || !mcpNamePattern.MatchString(name) {
		return fmt.Errorf("mcp_servers contains invalid server name %q", name)
	}
	server.Type = strings.ToLower(strings.TrimSpace(server.Type))
	if server.Type == "" {
		server.Type = "stdio"
	}
	switch server.Type {
	case "stdio":
		server.Command = strings.TrimSpace(server.Command)
		if !filepath.IsAbs(server.Command) || strings.ContainsAny(server.Command, "\x00\r\n") {
			return fmt.Errorf("mcp_servers.%s command must be an absolute path", name)
		}
		if server.URL != "" || len(server.Headers) != 0 {
			return fmt.Errorf("mcp_servers.%s stdio config cannot contain url or headers", name)
		}
		if len(server.Args) > 64 {
			return fmt.Errorf("mcp_servers.%s args supports at most 64 entries", name)
		}
		for _, arg := range server.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
				return fmt.Errorf("mcp_servers.%s args contains an invalid entry", name)
			}
		}
		if errEnv := validateMCPStringMap("env", name, server.Env, true); errEnv != nil {
			return errEnv
		}
	case "sse", "http":
		if server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
			return fmt.Errorf("mcp_servers.%s remote config cannot contain command, args, or env", name)
		}
		server.URL = strings.TrimSpace(server.URL)
		parsed, errParse := url.Parse(server.URL)
		if errParse != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return fmt.Errorf("mcp_servers.%s url must be an absolute HTTP or HTTPS URL without credentials", name)
		}
		if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
			return fmt.Errorf("mcp_servers.%s plain HTTP is allowed only on loopback", name)
		}
		if errHeaders := validateMCPStringMap("headers", name, server.Headers, false); errHeaders != nil {
			return errHeaders
		}
	default:
		return fmt.Errorf("mcp_servers.%s type must be stdio, sse, or http", name)
	}
	return nil
}

func validateMCPStringMap(kind, serverName string, values map[string]string, environment bool) error {
	if len(values) > 64 {
		return fmt.Errorf("mcp_servers.%s %s supports at most 64 entries", serverName, kind)
	}
	for key, value := range values {
		validKey := key != "" && len(key) <= 256 && !strings.ContainsAny(key, "\x00\r\n")
		if environment {
			validKey = configEnvNamePattern.MatchString(key)
		}
		if !validKey || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("mcp_servers.%s %s contains an invalid entry", serverName, kind)
		}
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
