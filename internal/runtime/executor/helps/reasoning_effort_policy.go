package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RequestEffortPolicy selects by the client-requested, suffix-free model name.
// Selection is deterministic, and does not alter auth/model routing identities.
func RequestEffortPolicy(cfg *config.Config, req coreexecutor.Request, opts coreexecutor.Options) (config.ReasoningEffortRule, bool) {
	if cfg == nil || len(cfg.ReasoningEffort) == 0 {
		return config.ReasoningEffortRule{}, false
	}
	model := thinking.ParseSuffix(PayloadRequestedModel(opts, req.Model)).ModelName
	var selected config.ReasoningEffortRule
	for _, rule := range cfg.ReasoningEffort {
		for _, pattern := range rule.Models {
			if matchModelPattern(strings.TrimSpace(pattern), model) {
				selected = rule
				break
			}
		}
	}
	return selected, selected.Mode != ""
}

// RequestEffortOverride distinguishes field ownership from a configured value:
// follow and explicit-client default requests still exclude competing raw rules.
func RequestEffortOverride(cfg *config.Config, req coreexecutor.Request, opts coreexecutor.Options) (config.ReasoningEffortRule, bool) {
	selected, matched := RequestEffortPolicy(cfg, req, opts)
	if !matched || selected.Mode == "follow" {
		return selected, false
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	format := opts.SourceFormat.String()
	if format == "" {
		format = req.Format.String()
	}
	if selected.Mode == "default" && (thinking.HasEffortIntent(source, format, req.Model) || thinking.HasThinkingSuffix(PayloadRequestedModel(opts, req.Model))) {
		return selected, false
	}
	return selected, true
}

// Steering 沿用正在生成的响应设置；只有携带强度控制的帧才需覆盖。
// 动态控制可以更新，但不能向普通 steering 注入 response.create 的字段。
func ApplyConfiguredSteerThinking(cfg *config.Config, body []byte, req coreexecutor.Request, opts coreexecutor.Options) ([]byte, error) {
	if _, active := RequestEffortOverride(cfg, req, opts); !active || !thinking.HasEffortIntent(body, "codex", "") {
		return body, nil
	}
	hadEffort := gjson.GetBytes(body, "reasoning.effort").Exists()
	hadReasoning := gjson.GetBytes(body, "reasoning").Exists()
	result, err := ApplyConfiguredRequestThinking(cfg, body, req, opts, "codex", "codex", "codex")
	if err != nil {
		return nil, err
	}
	if !hadEffort {
		result, _ = sjson.DeleteBytes(result, "reasoning.effort")
		if !hadReasoning && len(gjson.GetBytes(result, "reasoning").Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "reasoning")
		}
	}
	return result, nil
}

func policyModelInfo(req coreexecutor.Request, provider string, fallback *registry.ModelInfo) *registry.ModelInfo {
	if info, resolved := coreauth.ResolvedModelInfo(req); resolved {
		return info
	}
	if info := registry.LookupModelInfo(thinking.ParseSuffix(req.Model).ModelName, provider); info != nil {
		return info
	}
	return fallback
}

// ApplyConfiguredRequestThinking preserves the existing pipeline unless a
// matching default/force policy actually owns this request's effort.
func ApplyConfiguredRequestThinking(cfg *config.Config, body []byte, req coreexecutor.Request, opts coreexecutor.Options, from, to, provider string, updatesChanged ...bool) ([]byte, error) {
	return ApplyConfiguredRequestThinkingWithModelInfo(cfg, body, req, opts, from, to, provider, nil, updatesChanged...)
}

func ApplyConfiguredRequestThinkingWithModelInfo(cfg *config.Config, body []byte, req coreexecutor.Request, opts coreexecutor.Options, from, to, provider string, fallback *registry.ModelInfo, updatesChanged ...bool) ([]byte, error) {
	policy, active := RequestEffortOverride(cfg, req, opts)
	if !active {
		return ApplyRequestThinkingWithModelInfo(body, req, opts, from, to, provider, fallback, updatesChanged...)
	}
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	summary := translatedRequestSummaryConfig(body, req.Payload, original, req.Model, from, to)
	return thinking.ApplyEffortPolicy(body, req.Payload, req.Model, from, to, provider, policy.Effort, policyModelInfo(req, provider, fallback), summary)
}

// ProtectConfiguredEffort is the final boundary shared by legacy executors and
// PayloadFinalizer. Only a selected semantic policy owns these amount fields.
func ProtectConfiguredEffort(cfg *config.Config, before, after []byte, format, provider string, req coreexecutor.Request, opts coreexecutor.Options) []byte {
	if _, active := RequestEffortPolicy(cfg, req, opts); !active {
		return after
	}
	if format == "meta" {
		format = "codex"
	}
	if format == "claude" && (provider == "kimi" || provider == "kimi-ai" || provider == "kimi.com" || provider == "kimi.ai") {
		format = "kimi"
	}
	return thinking.ProtectPolicyEffort(before, after, format)
}
