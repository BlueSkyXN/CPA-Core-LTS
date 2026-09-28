package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type identityConfig struct {
	DeviceEnv  string `json:"device_id_env"`
	DeviceFile string `json:"device_id_file"`
	Platform   string `json:"platform"`
	Category   string `json:"os_category"`
	Version    string `json:"os_version"`
	Language   string `json:"language"`
	Timezone   string `json:"timezone"`
}
type modelLimits struct {
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
}

func limitsFor(c *config, model string) modelLimits {
	if limits, ok := c.ModelLimits[model]; ok {
		return limits
	}
	if model == "GLM-5.3-Flash" {
		return modelLimits{Context: 500000, Output: 128000}
	}
	return modelLimits{}
}

type config struct {
	Credential struct {
		Env  string `json:"api_key_env"`
		File string `json:"api_key_file"`
	} `json:"credential"`
	Identity                   identityConfig         `json:"identity"`
	Models                     []string               `json:"models"`
	ModelLimits                map[string]modelLimits `json:"model_limits,omitempty"`
	Upstream                   string                 `json:"upstream"`
	MaxInflight                int                    `json:"max_inflight"`
	AccountScope               string                 `json:"account_scope"`
	Prompt                     promptConfig           `json:"prompt"`
	HostLoggingDisabled        bool                   `json:"host_logging_disabled"`
	APIKey, DeviceID, Endpoint string
}
type authRecord struct {
	Type         string `json:"type"`
	Label        string `json:"label"`
	ConfigFile   string `json:"config_file"`
	RequestRetry int    `json:"request_retry"`
}

func parseAuth(raw []byte) (authRecord, error) {
	var a authRecord
	if decode(raw, &a) != nil || a.Type != provider || (a.ConfigFile != "" && !filepath.IsAbs(a.ConfigFile)) {
		return a, problem(400, "invalid_auth", "Configure an absolute config_file for this provider")
	}
	return a, nil
}
func privateText(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return "", problem(400, "invalid_config", "Cannot read configuration resource")
	}
	b, err := os.ReadFile(path)
	// Go 的 JSON 解码会替换非法 UTF-8；必须提前拒绝，避免与 Node 的严格解码产生提示词差异。
	if err != nil || len(b) > 1024*1024 || !utf8.Valid(b) {
		return "", problem(400, "invalid_config", "Cannot read configuration resource")
	}
	return string(b), nil
}
func secret(env, file, base string) (string, error) {
	if (env == "") == (file == "") {
		return "", problem(400, "invalid_config", "Specify one secret environment variable or file")
	}
	s := os.Getenv(env)
	if file != "" {
		var err error
		if !filepath.IsAbs(file) {
			file = filepath.Join(base, file)
		}
		s, err = privateText(file)
		if err != nil {
			return "", err
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n\x00<") {
		return "", problem(400, "invalid_config", "Missing or invalid secret")
	}
	return s, nil
}
func loadConfig(path string) (*config, error) {
	raw, err := privateText(path)
	if err != nil {
		return nil, err
	}
	var c config
	if json.Unmarshal([]byte(raw), &c) != nil {
		return nil, problem(400, "invalid_config", "Invalid configuration JSON")
	}
	base := filepath.Dir(path)
	if !c.HostLoggingDisabled {
		return nil, problem(503, "unsafe_host_logging", "Set host_logging_disabled only after disabling CPA raw request and error-body logs")
	}
	c.APIKey, err = secret(c.Credential.Env, c.Credential.File, base)
	if err != nil {
		return nil, err
	}
	if _, _, err = splitKey(c.APIKey); err != nil {
		return nil, err
	}
	c.DeviceID, err = secret(c.Identity.DeviceEnv, c.Identity.DeviceFile, base)
	if err != nil {
		return nil, err
	}
	ascii := regexp.MustCompile(`^[\x20-\x7e]+$`)
	for _, v := range []string{c.Identity.Platform, c.Identity.Category, c.Identity.Version, c.Identity.Language, c.Identity.Timezone} {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "<>") || !ascii.MatchString(v) {
			return nil, problem(400, "invalid_config", "Configure identity fields explicitly")
		}
	}
	family := map[string]string{"darwin": "macos", "linux": "linux", "win32": "windows"}[strings.Split(c.Identity.Platform, "-")[0]]
	if family == "" || family != c.Identity.Category {
		return nil, problem(400, "invalid_config", "Identity platform and category disagree")
	}
	if len(c.Models) == 0 {
		return nil, problem(400, "invalid_config", "Configure a model allowlist")
	}
	for _, v := range c.Models {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "<>\r\n") {
			return nil, problem(400, "invalid_config", "Invalid model allowlist")
		}
	}
	for _, limits := range c.ModelLimits {
		if limits.Context <= 0 || limits.Output <= 0 || limits.Output > limits.Context {
			return nil, problem(400, "invalid_config", "Invalid model limits")
		}
	}
	if c.Upstream == "" {
		c.Upstream = "bigmodel"
	}
	c.Endpoint = map[string]string{"bigmodel": "https://open.bigmodel.cn/api/anthropic/v1/messages", "zai": "https://api.z.ai/api/anthropic/v1/messages"}[c.Upstream]
	if c.Endpoint == "" {
		return nil, problem(400, "invalid_config", "Unknown upstream preset")
	}
	if c.MaxInflight == 0 {
		c.MaxInflight = 2
	}
	if c.MaxInflight < 1 || c.MaxInflight > 16 {
		return nil, problem(400, "invalid_config", "max_inflight must be 1..16")
	}
	if c.AccountScope == "" {
		c.AccountScope = "local-account"
	}
	templates := map[string]string{}
	for name, path := range c.Prompt.Templates {
		if !regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`).MatchString(name) {
			return nil, problem(400, "invalid_config", "Invalid template name")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		templates[name], err = privateText(path)
		if err != nil {
			return nil, err
		}
	}
	c.Prompt.Templates = templates
	if c.Prompt.Mode == "" {
		c.Prompt.Mode = "preserve"
	}
	if err := validatePrompt(c.Prompt); err != nil {
		return nil, err
	}
	return &c, nil
}
