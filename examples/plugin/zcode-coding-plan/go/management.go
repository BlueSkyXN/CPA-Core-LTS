package main

import (
	"encoding/json"
	"strings"
)

type managementConfig struct {
	HostLoggingDisabled *bool                  `json:"host_logging_disabled"`
	Upstream            *string                `json:"upstream"`
	Models              []string               `json:"models"`
	ModelLimits         map[string]modelLimits `json:"model_limits"`
}

func parseManagementConfig(raw []byte) (managementConfig, error) {
	var cfg managementConfig
	if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
		return cfg, problem(400, "invalid_config", "Host config_json is required")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return cfg, problem(400, "invalid_config", "Host config_json must be an object")
	}
	for _, name := range []string{"config_file", "credential", "identity", "prompt", "prompt_mode", "prompt_template", "prompt_move_position", "allow_request_override"} {
		if _, exists := fields[name]; exists {
			return cfg, problem(400, "invalid_config", "Private configuration and prompt overrides were removed; use inline accounts and the declared management fields")
		}
	}
	if cfg.Upstream != nil && upstreamEndpoint(*cfg.Upstream) == "" {
		return cfg, problem(400, "invalid_config", "Unknown upstream preset")
	}
	if cfg.Models != nil {
		if err := validateModelAllowlist(cfg.Models); err != nil {
			return cfg, err
		}
	}
	for _, limits := range cfg.ModelLimits {
		if limits.Context <= 0 || limits.Output <= 0 || limits.Output > limits.Context {
			return cfg, problem(400, "invalid_config", "Invalid model limits")
		}
	}
	return cfg, nil
}
func (m managementConfig) apply(c *config) error {
	if m.HostLoggingDisabled != nil {
		c.HostLoggingDisabled = *m.HostLoggingDisabled
	}
	if m.Upstream != nil {
		c.Upstream = *m.Upstream
		c.Endpoint = upstreamEndpoint(c.Upstream)
	}
	if m.Models != nil {
		c.Models = append([]string(nil), m.Models...)
	}
	if m.ModelLimits != nil {
		limits := map[string]modelLimits{}
		for name, value := range m.ModelLimits {
			limits[name] = value
		}
		c.ModelLimits = limits
	}
	return nil
}
func (r *pluginRuntime) resolveAuth(raw []byte) (authRecord, error) {
	return parseAuth(raw)
}
func (r *pluginRuntime) reconfigure(cfg managementConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.active) > 0 {
		return problem(409, "busy", "Finish or cancel requests before reconfiguring")
	}
	next := make(map[string]*accountState, len(r.accounts))
	if cfg.HostLoggingDisabled != nil && *cfg.HostLoggingDisabled {
		for id, account := range r.accounts {
			c := defaultConfig()
			if err := cfg.apply(c); err != nil {
				return err
			}
			c.APIKey, c.DeviceID = account.config.APIKey, account.config.DeviceID
			c.AccountScope = id
			next[id] = &accountState{authID: id, authIndex: account.authIndex, config: c, signer: &signer{apiKey: c.APIKey, endpoint: c.Endpoint}}
		}
	}
	for _, account := range r.accounts {
		r.clearAccountLocked(account)
	}
	r.management, r.accounts = cfg, next
	r.accepting = true
	return nil
}
func (r *pluginRuntime) readiness(raw []byte) (any, error) {
	var req struct {
		StorageJSON []byte
		AuthID      string
		AuthIndex   string
		Purpose     string
	}
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	authState, message := "unknown", "Select an imported account; no remote request has been made"
	ready := false
	if len(req.StorageJSON) > 0 {
		if _, err := r.configurationForAuth(req.StorageJSON, req.AuthID, req.AuthIndex); err != nil {
			authState = "not_ready"
			message = safeError(err).Message
		} else {
			authState = "ready"
			message = "Local configuration valid; remote acceptance and billing not verified"
			ready = accepting
			if req.Purpose == "admission" {
				r.mu.Lock()
				account := r.accounts[strings.TrimSpace(req.AuthID)]
				if account == nil || account.retiring || !r.accepting || r.loggingAcknowledgedLocked() != nil {
					authState, message, ready = "not_ready", "Account configuration changed before admission", false
				} else if account.active >= account.config.MaxInflight {
					authState, message, ready = "not_ready", "This account's local concurrency limit was reached", false
				}
				r.mu.Unlock()
			}
		}
	}
	protocol := map[bool]string{true: "ready", false: "not_ready"}[accepting]
	return map[string]any{"Provider": provider, "Ready": ready, "Generation": pluginVersion, "Capabilities": []string{"anthropic", "stream", "cancel", "manual_auth"}, "Checks": []any{
		map[string]any{"Level": "plugin_installed", "State": "ready", "Version": pluginVersion},
		map[string]any{"Level": "runner_installed", "State": "ready", "Version": "native-go", "Message": "No external runtime required"},
		map[string]any{"Level": "protocol_ready", "State": protocol},
		map[string]any{"Level": "auth_ready", "State": authState, "Message": message},
		map[string]any{"Level": "session_ready", "State": "unsupported", "Message": "History is supplied by caller"},
	}}, nil
}
func managementFields() []any {
	return []any{
		map[string]any{"Name": "host_logging_disabled", "Type": "boolean", "Description": "Required for credential loading and execution: acknowledge that CPA raw request and error-body logs are disabled. False or inherit blocks calls after successful reload. Setting true does not disable any logger."},
		map[string]any{"Name": "upstream", "Type": "enum", "EnumValues": []string{"bigmodel", "zai"}, "Description": "Upstream preset: bigmodel (open.bigmodel.cn, default) or zai (api.z.ai international). Clear to inherit the default."},
		map[string]any{"Name": "models", "Type": "array", "Description": "Optional explicit model allowlist (JSON array of model IDs). Clear to use the built-in glm-5.3 / glm-5.3-flash catalog."},
		map[string]any{"Name": "model_limits", "Type": "object", "Description": "Optional per-model context/output limits, e.g. {\"glm-5.3\":{\"context\":1000000,\"output\":128000}}. Clear to use built-in limits."},
	}
}
