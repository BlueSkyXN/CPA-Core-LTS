package main

import (
	"encoding/json"
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
	if r.config != nil {
		next := defaultConfig()
		if err := cfg.apply(next); err != nil {
			return err
		}
		next.APIKey, next.DeviceID = r.inlineAPIKey, r.inlineDeviceID
		if r.signer != nil {
			r.signer.close()
		}
		r.management = cfg
		r.config = next
		r.signer = &signer{apiKey: next.APIKey, endpoint: next.Endpoint}
	} else {
		r.management = cfg
	}
	r.authOwner = ""
	r.accepting = true
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
		map[string]any{"Name": "host_logging_disabled", "Type": "boolean", "Description": "Required before inline credentials may load: acknowledge that CPA raw request and error-body logs are disabled. Setting it true does not disable any logger."},
		map[string]any{"Name": "upstream", "Type": "enum", "EnumValues": []string{"bigmodel", "zai"}, "Description": "Upstream preset: bigmodel (open.bigmodel.cn, default) or zai (api.z.ai international). Clear to inherit the default."},
		map[string]any{"Name": "models", "Type": "array", "Description": "Optional explicit model allowlist (JSON array of model IDs). Clear to use the built-in glm-5.3 / glm-5.3-flash catalog."},
		map[string]any{"Name": "model_limits", "Type": "object", "Description": "Optional per-model context/output limits, e.g. {\"glm-5.3\":{\"context\":1000000,\"output\":128000}}. Clear to use built-in limits."},
	}
}
