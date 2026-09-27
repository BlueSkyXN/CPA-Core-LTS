package main

import (
	"encoding/json"
	"path/filepath"
)

type managementConfig struct {
	ConfigFile           string  `json:"config_file"`
	PromptMode           *string `json:"prompt_mode"`
	PromptTemplate       *string `json:"prompt_template"`
	PromptMovePosition   *string `json:"prompt_move_position"`
	AllowRequestOverride *bool   `json:"allow_request_override"`
}

func parseManagementConfig(raw []byte) (managementConfig, error) {
	var cfg managementConfig
	if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
		return cfg, problem(400, "invalid_config", "Host config_json is required")
	}
	if cfg.ConfigFile != "" && !filepath.IsAbs(cfg.ConfigFile) {
		return cfg, problem(400, "invalid_config", "config_file must be an absolute server path")
	}
	if cfg.PromptMode != nil {
		switch *cfg.PromptMode {
		case "preserve", "replace", "prepend", "append", "move_to_user":
		default:
			return cfg, problem(400, "invalid_prompt", "Unknown prompt mode")
		}
	}
	if cfg.PromptMovePosition != nil && *cfg.PromptMovePosition != "first_user" && *cfg.PromptMovePosition != "last_user" {
		return cfg, problem(400, "invalid_prompt", "Invalid move_position")
	}
	return cfg, nil
}
func (m managementConfig) apply(c *config) error {
	if m.PromptMode != nil {
		c.Prompt.Mode = *m.PromptMode
	}
	if m.PromptTemplate != nil {
		if _, exists := c.Prompt.Templates[*m.PromptTemplate]; !exists {
			return problem(400, "invalid_prompt", "Select a configured template name")
		}
		c.Prompt.Template = *m.PromptTemplate
	}
	if m.PromptMovePosition != nil {
		c.Prompt.MovePosition = *m.PromptMovePosition
	}
	if m.AllowRequestOverride != nil {
		c.Prompt.AllowRequestOverride = *m.AllowRequestOverride
	}
	return validatePrompt(c.Prompt)
}
func (r *pluginRuntime) authConfigPath(a authRecord) (string, error) {
	path := a.ConfigFile
	if r.management.ConfigFile != "" {
		if path != "" && path != r.management.ConfigFile {
			return "", problem(409, "config_conflict", "Auth and plugin config_file references must agree")
		}
		path = r.management.ConfigFile
	}
	if path == "" {
		return "", problem(400, "invalid_auth", "Configure an absolute config_file for this provider")
	}
	return path, nil
}
func (r *pluginRuntime) resolveAuth(raw []byte) (authRecord, error) {
	a, err := parseAuth(raw)
	if err != nil {
		return a, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a.ConfigFile, err = r.authConfigPath(a)
	return a, err
}
func (r *pluginRuntime) reconfigure(cfg managementConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.active) > 0 {
		return problem(409, "busy", "Finish or cancel requests before reconfiguring")
	}
	var next *config
	path := cfg.ConfigFile
	if r.config != nil {
		if path == "" {
			path = r.configPath
		}
		var err error
		next, err = loadConfig(path)
		if err != nil {
			return err
		}
		if err = cfg.apply(next); err != nil {
			return err
		}
	}
	if r.signer != nil {
		r.signer.close()
	}
	r.management = cfg
	r.config = next
	r.configPath = path
	r.authOwner = ""
	r.signer = nil
	r.accepting = true
	if next != nil {
		r.signer = &signer{apiKey: next.APIKey, endpoint: next.Endpoint}
	}
	return nil
}
func (r *pluginRuntime) readiness(raw []byte) (any, error) {
	var req struct{ StorageJSON []byte }
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	authState, message := "unknown", "Select an imported account; no remote request has been made"
	ready := false
	if len(req.StorageJSON) > 0 {
		if _, err := r.configuration(req.StorageJSON); err != nil {
			authState = "not_ready"
			message = safeError(err).Message
		} else {
			authState = "ready"
			message = "Local configuration valid; remote acceptance and billing not verified"
			ready = accepting
		}
	}
	protocol := map[bool]string{true: "ready", false: "not_ready"}[accepting]
	return map[string]any{"Provider": provider, "Ready": ready, "Generation": "0.3.0", "Capabilities": []string{"anthropic", "stream", "cancel", "prompt_policies", "manual_auth"}, "Checks": []any{
		map[string]any{"Level": "plugin_installed", "State": "ready", "Version": "0.3.0"},
		map[string]any{"Level": "runner_installed", "State": "ready", "Version": "native-go", "Message": "No external runtime required"},
		map[string]any{"Level": "protocol_ready", "State": protocol},
		map[string]any{"Level": "auth_ready", "State": authState, "Message": message},
		map[string]any{"Level": "session_ready", "State": "unsupported", "Message": "History is supplied by caller"},
	}}, nil
}
func managementFields() []any {
	return []any{
		map[string]any{"Name": "config_file", "Type": "string", "Description": "Absolute server path to your private JSON config. Keep secrets and device identity in that file, not here."},
		map[string]any{"Name": "prompt_mode", "Type": "enum", "EnumValues": []string{"preserve", "replace", "prepend", "append", "move_to_user"}, "Description": "Override the private file prompt mode; clear to inherit."},
		map[string]any{"Name": "prompt_template", "Type": "string", "Description": "Select a template name already registered in the private file, never a path or URL."},
		map[string]any{"Name": "prompt_move_position", "Type": "enum", "EnumValues": []string{"first_user", "last_user"}, "Description": "Position used by move_to_user; clear to inherit."},
		map[string]any{"Name": "allow_request_override", "Type": "boolean", "Description": "Allow per-request prompt selection. Unset inherits the private file policy."},
	}
}
