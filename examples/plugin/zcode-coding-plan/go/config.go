package main

import (
	"strings"
)

type identityConfig struct {
	Platform string `json:"platform"`
	Category string `json:"os_category"`
	Version  string `json:"os_version"`
	Language string `json:"language"`
	Timezone string `json:"timezone"`
}
type modelLimits struct {
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
}

func limitsFor(c *config, model string) modelLimits {
	if limits, ok := c.ModelLimits[model]; ok {
		return limits
	}
	if limits, ok := builtinModelLimits(model); ok {
		return limits
	}
	return modelLimits{}
}

// builtinModels 是无显式 allowlist 时的默认模型目录。
var builtinModels = []string{"glm-5.3", "glm-5.3-flash"}

const builtinContextLength int64 = 1000000
const builtinOutputLimit int64 = 128000

var builtinThinkingLevels = []string{"low", "high", "max"}

// builtinModelLimits 按（大小写不敏感的）模型 ID 返回内置限额。
func builtinModelLimits(model string) (modelLimits, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "glm-5.3", "glm-5.3-flash":
		return modelLimits{Context: builtinContextLength, Output: builtinOutputLimit}, true
	}
	return modelLimits{}, false
}

// builtinModelImageInput 报告内置模型是否接受图片输入；glm-5.3 仅文本。
func builtinModelImageInput(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "glm-5.3":
		return false
	case "glm-5.3-flash":
		return true
	}
	// 非内置 ID 的能力未验收，保守按纯文本处理。
	return false
}

func upstreamEndpoint(upstream string) string {
	return map[string]string{"bigmodel": "https://open.bigmodel.cn/api/anthropic/v1/messages", "zai": "https://api.z.ai/api/anthropic/v1/messages"}[upstream]
}

// defaultIdentity 是内联账号的固定设备身份。
func defaultIdentity() identityConfig {
	return identityConfig{Platform: "linux-x64", Category: "linux", Version: "6.8.0", Language: "zh-CN", Timezone: "Asia/Shanghai"}
}

type config struct {
	Identity                   identityConfig         `json:"identity"`
	Models                     []string               `json:"models"`
	ModelLimits                map[string]modelLimits `json:"model_limits,omitempty"`
	Upstream                   string                 `json:"upstream"`
	MaxInflight                int                    `json:"max_inflight"`
	AccountScope               string                 `json:"account_scope"`
	HostLoggingDisabled        bool                   `json:"host_logging_disabled"`
	APIKey, DeviceID, Endpoint string
}

// defaultConfig 构建内联账号的基线配置：不读文件、不含敏感值；
// 管理面覆盖（upstream/models/model_limits）在 apply 中生效。
func defaultConfig() *config {
	c := &config{}
	c.Upstream = "bigmodel"
	c.Endpoint = upstreamEndpoint(c.Upstream)
	c.MaxInflight = 2
	c.AccountScope = "local-account"
	c.Identity = defaultIdentity()
	c.Models = append([]string(nil), builtinModels...)
	return c
}

func validateModelAllowlist(models []string) error {
	if len(models) == 0 {
		return problem(400, "invalid_config", "Configure a model allowlist")
	}
	for _, v := range models {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "<>\r\n") {
			return problem(400, "invalid_config", "Invalid model allowlist")
		}
	}
	return nil
}

type authRecord struct {
	Type         string `json:"type"`
	Label        string `json:"label"`
	APIKey       string `json:"api_key,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	RequestRetry int    `json:"request_retry"`
}

func validateInlineSecret(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\x00<") {
		return problem(400, "invalid_auth", "Inline "+name+" is missing or invalid")
	}
	return nil
}

func parseAuth(raw []byte) (authRecord, error) {
	var a authRecord
	if decode(raw, &a) != nil || a.Type != provider {
		return a, problem(400, "invalid_auth", "Invalid account record for this provider")
	}
	var fields map[string]any
	if decode(raw, &fields) != nil {
		return a, problem(400, "invalid_auth", "Invalid account record for this provider")
	}
	if _, exists := fields["config_file"]; exists {
		return a, problem(400, "invalid_auth", "config_file references are no longer supported; provide inline api_key and device_id")
	}
	if a.APIKey == "" || a.DeviceID == "" {
		return a, problem(400, "invalid_auth", "Provide both api_key and device_id inline")
	}
	if err := validateInlineSecret("api_key", a.APIKey); err != nil {
		return a, err
	}
	if err := validateInlineSecret("device_id", a.DeviceID); err != nil {
		return a, err
	}
	if _, _, err := splitKey(strings.TrimSpace(a.APIKey)); err != nil {
		return a, err
	}
	a.APIKey = strings.TrimSpace(a.APIKey)
	a.DeviceID = strings.TrimSpace(a.DeviceID)
	return a, nil
}
