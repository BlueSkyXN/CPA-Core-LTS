package thinking

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// HasEffortIntent includes explicit off/auto/budget settings and in-turn
// controls. Defaults must not replace a choice merely because it uses a
// different protocol field.
func HasEffortIntent(body []byte, format, model string) bool {
	if HasThinkingSuffix(model) || hasThinkingConfig(extractSourceThinkingConfig(body, format)) {
		return true
	}
	for _, path := range []string{"reasoning_effort", "reasoning.effort", "output_config.effort", "thinking.budget_tokens", "thinking.effort", "generationConfig.thinkingConfig.thinkingBudget", "generationConfig.thinkingConfig.thinkingLevel", "generation_config.thinking_budget", "generation_config.thinking_level"} {
		if gjson.GetBytes(body, path).Exists() {
			return true
		}
	}
	if kind := gjson.GetBytes(body, "thinking.type").String(); kind == "disabled" || kind == "enabled" {
		return true
	}
	if isResponsesFormat(format) {
		controls, err := util.InspectResponsesControls(body)
		if err != nil || controls.ConfigurationEffort != "" {
			return true
		}
	}
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("output_config.effort").Exists() {
			return true
		}
	}
	return false
}

// ApplyEffortPolicy reuses provider appliers, while making the administrator's
// selected level authoritative over the request and its model suffix.
func ApplyEffortPolicy(body, source []byte, model, from, to, provider, effort string, modelInfo *registry.ModelInfo, summary SummaryConfig) ([]byte, error) {
	format := policyFormat(to)
	applier, _ := resolveProviderApplier(format, provider)
	if applier == nil || len(effortPaths(format)) == 0 {
		return nil, NewThinkingError(ErrThinkingNotSupported, "reasoning effort policy is not supported by this execution protocol")
	}
	if modelInfo != nil && !modelInfo.UserDefined && modelInfo.Thinking == nil {
		return nil, NewThinkingError(ErrThinkingNotSupported, "the selected model does not support a reasoning effort policy")
	}
	if modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 && !HasLevel(modelInfo.Thinking.Levels, effort) {
		return nil, NewThinkingError(ErrLevelNotSupported, "the configured reasoning effort is not supported by the selected model")
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, NewThinkingError(ErrInvalidPolicyRequest, "reasoning effort policy requires a valid request object")
	}
	base := ParseSuffix(model).ModelName
	configured := base + "(" + effort + ")"
	// The suffix only selects the internal thinking configuration. Model IDs
	// used by routing, auth selection and usage attribution remain unchanged.
	body = clearForeignEffortFields(body, format)
	result, err := ApplyThinkingWithModelInfoAndSummary(body, source, configured, from, to, provider, modelInfo, summary)
	if err != nil {
		return nil, err
	}
	result = clearForeignEffortFields(result, format)
	return applyCurrentPolicyControl(result, format, modelInfo), nil
}

func policyFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "openai-response" {
		return "codex"
	}
	return format
}

func effortPaths(format string) []string {
	switch policyFormat(format) {
	case "openai":
		return []string{"reasoning_effort"}
	case "codex", "xai":
		return []string{"reasoning.effort"}
	case "claude":
		return []string{"thinking.type", "thinking.budget_tokens", "output_config.effort"}
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return []string{"thinking.type", "thinking.effort"}
	case "gemini":
		return []string{"generationConfig.thinkingConfig.thinkingLevel", "generationConfig.thinkingConfig.thinkingBudget"}
	case "antigravity":
		return []string{"request.generationConfig.thinkingConfig.thinkingLevel", "request.generationConfig.thinkingConfig.thinkingBudget"}
	case "interactions":
		return []string{"generation_config.thinking_level", "generation_config.thinking_budget"}
	}
	return nil
}

func clearForeignEffortFields(body []byte, format string) []byte {
	native := map[string]bool{}
	for _, path := range effortPaths(format) {
		native[path] = true
	}
	for _, path := range effortAliasPaths(format) {
		if !native[path] {
			body, _ = sjson.DeleteBytes(body, path)
		}
	}
	return body
}

func effortAliasPaths(format string) []string {
	paths := []string{"reasoning_effort", "reasoning.effort", "output_config.effort"}
	if policyFormat(format) == "antigravity" {
		paths = append(paths, "request.reasoning_effort", "request.reasoning.effort", "request.output_config.effort")
	}
	return paths
}

// ProtectPolicyEffort keeps the already validated native controls authoritative
// after generic payload rules, without re-running plugin callbacks or changing
// summary, output format, token limits or unrelated request fields.
func ProtectPolicyEffort(before, after []byte, format string) []byte {
	format = policyFormat(format)
	paths := append(effortAliasPaths(format), effortPaths(format)...)
	for _, path := range paths {
		value := gjson.GetBytes(before, path)
		if value.Exists() {
			after, _ = sjson.SetRawBytes(after, path, []byte(value.Raw))
		} else {
			after, _ = sjson.DeleteBytes(after, path)
		}
	}
	return protectDynamicPolicyEffort(before, after, format)
}

// 强度控制的历史顺序属于专用策略。普通字段改写可以保留；整段替换
// 带控制项的历史时，恢复原数组，避免按旧索引把控制项写到另一条消息上。
func protectDynamicPolicyEffort(before, after []byte, format string) []byte {
	array, field := "", ""
	switch format {
	case "codex", "xai":
		array, field = "input", "reasoning.effort"
	case "claude":
		array, field = "messages", "output_config.effort"
	default:
		return after
	}
	oldItems, newItems := gjson.GetBytes(before, array).Array(), gjson.GetBytes(after, array).Array()
	isControl := func(item gjson.Result) bool {
		if array == "input" {
			return item.Get("type").String() == "configuration_update"
		}
		return item.Get(field).Exists()
	}
	hasControl := false
	for _, items := range [][]gjson.Result{oldItems, newItems} {
		for _, item := range items {
			hasControl = hasControl || isControl(item)
		}
	}
	if !hasControl {
		return after
	}
	structureChanged := len(oldItems) != len(newItems)
	if !structureChanged {
		for i, item := range oldItems {
			if item.Get("type").String() != newItems[i].Get("type").String() || item.Get("role").String() != newItems[i].Get("role").String() {
				structureChanged = true
				break
			}
		}
	}
	if structureChanged {
		return restorePolicyPath(before, after, array)
	}
	for i := range oldItems {
		if isControl(oldItems[i]) || isControl(newItems[i]) {
			after = restorePolicyPath(before, after, fmt.Sprintf("%s.%d.%s", array, i, field))
		}
	}
	return after
}

func restorePolicyPath(before, after []byte, path string) []byte {
	value := gjson.GetBytes(before, path)
	if value.Exists() {
		after, _ = sjson.SetRawBytes(after, path, []byte(value.Raw))
	} else {
		after, _ = sjson.DeleteBytes(after, path)
	}
	return after
}

// Existing turn controls retain their history. A final control establishes the
// policy for the current generation; a trailing control can be updated in place.
func applyCurrentPolicyControl(body []byte, format string, modelInfo *registry.ModelInfo) []byte {
	switch format {
	case "codex", "xai":
		controls, err := util.InspectResponsesControls(body)
		if err != nil || (!controls.HasConfigurationUpdate && !gjson.GetBytes(body, "previous_response_id").Exists()) {
			return body
		}
		if (modelInfo == nil || !modelInfo.SupportConfigurationUpdate) && !controls.HasConfigurationUpdate {
			return body
		}
		effort := gjson.GetBytes(body, "reasoning.effort").String()
		if effort == "" {
			return body
		}
		input := gjson.GetBytes(body, "input")
		if input.Type == gjson.String {
			body, _ = sjson.SetBytes(body, "input", []any{map[string]any{"role": "user", "content": input.String()}})
		}
		items := gjson.GetBytes(body, "input").Array()
		if len(items) > 0 && items[len(items)-1].Get("type").String() == "configuration_update" {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("input.%d.reasoning.effort", len(items)-1), effort)
		} else {
			body, _ = sjson.SetBytes(body, "input.-1", map[string]any{"type": "configuration_update", "reasoning": map[string]any{"effort": effort}})
		}
	case "claude":
		effort := gjson.GetBytes(body, "output_config.effort").String()
		if effort == "" {
			return body
		}
		messages := gjson.GetBytes(body, "messages").Array()
		hasControl := false
		for _, message := range messages {
			hasControl = hasControl || message.Get("output_config.effort").Exists()
		}
		if !hasControl {
			return body
		}
		last := messages[len(messages)-1]
		if last.Get("role").String() == "system" && last.Get("output_config.effort").Exists() && last.Get("content").IsArray() && len(last.Get("content").Array()) == 0 {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("messages.%d.output_config.effort", len(messages)-1), effort)
		} else {
			body, _ = sjson.SetBytes(body, "messages.-1", map[string]any{"role": "system", "content": []any{}, "output_config": map[string]any{"effort": effort}})
		}
	}
	return body
}
