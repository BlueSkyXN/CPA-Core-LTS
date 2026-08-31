package main

import (
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
	RunnerCommand     string                     `yaml:"runner_command"`
	RunnerArgs        []string                   `yaml:"runner_args"`
	QoderCLIPath      string                     `yaml:"qoder_cli_path"`
	WorkingDirectory  string                     `yaml:"working_directory"`
	MaxQueueFrames    int                        `yaml:"max_queue_frames"`
	RequestTimeout    time.Duration              `yaml:"-"`
	RequestTimeoutRaw string                     `yaml:"request_timeout"`
	ModelCacheTTL     time.Duration              `yaml:"-"`
	ModelCacheTTLRaw  string                     `yaml:"model_cache_ttl"`
	PermissionDefault string                     `yaml:"permission_default"`
	PermissionRules   []permissionRule           `yaml:"permission_rules"`
	Skills            []string                   `yaml:"skills"`
	SettingSources    []string                   `yaml:"setting_sources"`
	AllowedTools      []string                   `yaml:"allowed_tools"`
	DisallowedTools   []string                   `yaml:"disallowed_tools"`
	MCPServers        map[string]mcpServerConfig `yaml:"mcp_servers"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		RunnerCommand:     "cpa-qoder-runner",
		WorkingDirectory:  os.TempDir(),
		MaxQueueFrames:    128,
		RequestTimeout:    30 * time.Second,
		ModelCacheTTL:     time.Minute,
		PermissionDefault: "deny",
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
	cfg.QoderCLIPath = strings.TrimSpace(cfg.QoderCLIPath)
	cfg.WorkingDirectory = strings.TrimSpace(cfg.WorkingDirectory)
	if cfg.RunnerCommand == "" || strings.ContainsRune(cfg.RunnerCommand, '\x00') {
		return pluginConfig{}, fmt.Errorf("runner_command is required")
	}
	for _, arg := range cfg.RunnerArgs {
		if strings.ContainsRune(arg, '\x00') {
			return pluginConfig{}, fmt.Errorf("runner_args contains an invalid argument")
		}
		switch arg {
		case "--stdio", "--cli-path", "--cwd", "--max-queue-frames", "--version":
			return pluginConfig{}, fmt.Errorf("runner_args must not override plugin-owned runner arguments")
		}
	}
	if cfg.QoderCLIPath != "" && (!filepath.IsAbs(cfg.QoderCLIPath) || strings.ContainsRune(cfg.QoderCLIPath, '\x00')) {
		return pluginConfig{}, fmt.Errorf("qoder_cli_path must be an absolute path")
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
