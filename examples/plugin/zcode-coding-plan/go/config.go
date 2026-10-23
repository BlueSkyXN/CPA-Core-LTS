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

// modelTemplateID 只用于能力查找；请求和统计始终保留实际模型 ID。
func modelTemplateID(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "glm-5.3", "glm-5.3-highspeed":
		return "glm-5.3"
	case "glm-5.3-flash", "glm-5.3-flashx":
		return "glm-5.3-flash"
	default:
		return ""
	}
}

// builtinModelLimits 按（大小写不敏感的）模型 ID 返回内置限额。
func builtinModelLimits(model string) (modelLimits, bool) {
	switch modelTemplateID(model) {
	case "glm-5.3", "glm-5.3-flash":
		return modelLimits{Context: builtinContextLength, Output: builtinOutputLimit}, true
	}
	return modelLimits{}, false
}

// builtinModelImageInput 报告内置模型是否接受图片输入；glm-5.3 仅文本。
func builtinModelImageInput(model string) bool {
	switch modelTemplateID(model) {
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
	ExtraModels                []string               `json:"exmodel,omitempty"`
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
	c.MaxInflight = 10
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
	Type         string   `json:"type"`
	Label        string   `json:"label"`
	APIKey       string   `json:"api_key,omitempty"`
	DeviceID     string   `json:"device_id,omitempty"`
	RequestRetry int      `json:"request_retry"`
	ExtraModels  []string `json:"exmodel,omitempty"`
}

func normalizeExtraModels(models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, nil
	}
	if err := validateModelAllowlist(models); err != nil {
		return nil, problem(400, "invalid_auth", "Invalid exmodel list")
	}
	result := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if !seen[model] {
			seen[model] = true
			result = append(result, model)
		}
	}
	return result, nil
}

// applyExtraModels 在全局基础目录之后追加账号模型；保存独立列表以支持重载。
func (c *config) applyExtraModels(models []string) {
	c.ExtraModels = append([]string(nil), models...)
	merged := make([]string, 0, len(c.Models)+len(models))
	seen := make(map[string]bool, cap(merged))
	for _, list := range [][]string{c.Models, models} {
		for _, model := range list {
			if !seen[model] {
				seen[model] = true
				merged = append(merged, model)
			}
		}
	}
	c.Models = merged
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
	var err error
	a.ExtraModels, err = normalizeExtraModels(a.ExtraModels)
	if err != nil {
		return a, err
	}
	return a, nil
}
