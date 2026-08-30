package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type permissionRule struct {
	ToolName      string         `yaml:"tool_name" json:"tool_name"`
	Behavior      string         `yaml:"behavior" json:"behavior"`
	ModifiedInput map[string]any `yaml:"modified_input,omitempty" json:"modified_input,omitempty"`
}

type pluginConfig struct {
	RunnerCommand     string           `yaml:"runner_command"`
	RunnerArgs        []string         `yaml:"runner_args"`
	QoderCLIPath      string           `yaml:"qoder_cli_path"`
	WorkingDirectory  string           `yaml:"working_directory"`
	MaxQueueFrames    int              `yaml:"max_queue_frames"`
	RequestTimeout    time.Duration    `yaml:"-"`
	RequestTimeoutRaw string           `yaml:"request_timeout"`
	ModelCacheTTL     time.Duration    `yaml:"-"`
	ModelCacheTTLRaw  string           `yaml:"model_cache_ttl"`
	PermissionDefault string           `yaml:"permission_default"`
	PermissionRules   []permissionRule `yaml:"permission_rules"`
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
	return cfg, nil
}
