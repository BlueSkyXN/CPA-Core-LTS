package auth

import (
	"encoding/json"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// AttributeModelPolicy carries the per-account model policy synthesized from the
// auth file's "model_policy" object. The raw JSON stays in Metadata; this
// attribute holds the parsed runtime form for selection and registration.
const AttributeModelPolicy = "model_policy"

// OAuthModelPolicy is the runtime form of one switchable model list.
type OAuthModelPolicy struct {
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models"`
}

// AccountModelPolicy is the effective per-account policy: the account-level
// lists plus the always-on legacy "excluded_models" union source.
type AccountModelPolicy struct {
	Allowlist *OAuthModelPolicy `json:"allowlist,omitempty"`
	Denylist  *OAuthModelPolicy `json:"denylist,omitempty"`
}

// active reports whether the list participates in evaluation.
func (p *OAuthModelPolicy) active() bool { return p != nil && p.Enabled }

// modelPolicyDecision describes one evaluation outcome with the layer that
// produced it, so previews and rejection errors can be explained.
type modelPolicyDecision struct {
	Allowed bool
	Reason  string
	Layer   string
}

func (d modelPolicyDecision) String() string {
	if d.Reason == "" {
		if d.Allowed {
			return "allowed"
		}
		return "denied"
	}
	return d.Reason
}

// policyAllLayer / policyAccountLayer name the two evaluation layers.
const (
	policyLayerChannel = "channel"
	policyLayerAccount = "account"
	policySourceLegacy = "legacy-excluded-models"
)

// matchPolicyWildcard performs case-insensitive wildcard matching where '*'
// matches any substring, mirroring the legacy excluded-models matcher in the
// registration path (sdk/cliproxy matchWildcard). Inputs are expected
// pre-lowercased.
func matchPolicyWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}
	return true
}

// matchPolicyModels reports whether modelID matches any pattern. Matching is
// case-insensitive with '*' wildcards, mirroring the legacy excluded-models
// semantics in the registration path.
func matchPolicyModels(modelID string, patterns []string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" {
		return false
	}
	for _, pattern := range patterns {
		if matchPolicyWildcard(strings.ToLower(strings.TrimSpace(pattern)), modelID) {
			return true
		}
	}
	return false
}

func normalizeAccountModelPolicyList(raw *OAuthModelPolicy) *OAuthModelPolicy {
	if raw == nil {
		return nil
	}
	models := NormalizePolicyModels(raw.Models)
	if models == nil && len(raw.Models) > 0 {
		// keep an empty non-nil list when the source had entries that all
		// normalized away: an enabled empty allowlist must still mean "none".
		models = []string{}
	}
	return &OAuthModelPolicy{Enabled: raw.Enabled, Models: models}
}

// NormalizePolicyModels trims, lowercases, and deduplicates policy entries.
func NormalizePolicyModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, raw := range models {
		model := strings.ToLower(strings.TrimSpace(raw))
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseAccountModelPolicyMetadata parses the "model_policy" object from auth
// metadata. It returns nil when absent, and (nil, false) when present but
// structurally invalid so callers can reject the credential instead of
// silently treating it as unrestricted.
func ParseAccountModelPolicyMetadata(metadata map[string]any) (*AccountModelPolicy, bool) {
	if metadata == nil {
		return nil, true
	}
	raw, present := metadata["model_policy"]
	if !present || raw == nil {
		return nil, true
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	policy := &AccountModelPolicy{}
	for name, target := range map[string]**OAuthModelPolicy{
		"allowlist": &policy.Allowlist,
		"denylist":  &policy.Denylist,
	} {
		value, present := obj[name]
		if !present || value == nil {
			continue
		}
		list, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		parsed := &OAuthModelPolicy{}
		if enabled, ok := list["enabled"].(bool); ok {
			parsed.Enabled = enabled
		} else if _, present := list["enabled"]; present {
			return nil, false
		}
		if models, ok := list["models"]; ok && models != nil {
			items, ok := models.([]any)
			if !ok {
				return nil, false
			}
			for _, item := range items {
				entry, ok := item.(string)
				if !ok {
					return nil, false
				}
				parsed.Models = append(parsed.Models, entry)
			}
		}
		normalized, err := internalconfig.NormalizeOAuthModelPolicyList(&internalconfig.OAuthModelPolicyList{Enabled: parsed.Enabled, Models: parsed.Models})
		if err != nil {
			return nil, false
		}
		*target = &OAuthModelPolicy{Enabled: normalized.Enabled, Models: normalized.Models}
	}
	return policy, true
}

// channelModelPolicyForProvider returns the provider channel policy from the
// runtime config snapshot. Nil config or missing entries mean no restriction.
func channelModelPolicyForProvider(cfg *internalconfig.Config, provider string) internalconfig.OAuthModelPolicyEntry {
	if cfg == nil {
		return internalconfig.OAuthModelPolicyEntry{}
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if providerKey == "" {
		return internalconfig.OAuthModelPolicyEntry{}
	}
	return cfg.OAuthModelPolicy[providerKey]
}

func channelPolicyListActive(list *internalconfig.OAuthModelPolicyList) bool {
	return list != nil && list.Enabled
}

// evaluateModelPolicyLayers is the single evaluation used by registration,
// selection, and dispatch. modelID must be the real upstream model ID before
// aliases/prefixes are applied. The legacy "excluded_models" attribute is an
// always-on deny source unioned with any denylist.
func evaluateModelPolicyLayers(cfg *internalconfig.Config, attributes map[string]string, provider, modelID string) modelPolicyDecision {
	modelKey := strings.ToLower(strings.TrimSpace(modelID))
	if modelKey == "" {
		return modelPolicyDecision{Allowed: true}
	}
	channel := channelModelPolicyForProvider(cfg, provider)
	if strings.TrimSpace(attributes[AttributeExcludedModels]) == "" && cfg != nil && matchPolicyModels(modelKey, cfg.OAuthExcludedModels[strings.ToLower(strings.TrimSpace(provider))]) {
		return modelPolicyDecision{Allowed: false, Layer: policyLayerChannel, Reason: policySourceLegacy + ": pattern matched"}
	}
	if channelPolicyListActive(channel.Allowlist) && !matchPolicyModels(modelKey, channel.Allowlist.Models) {
		return modelPolicyDecision{Allowed: false, Layer: policyLayerChannel, Reason: "channel allowlist: no pattern matched"}
	}
	if channelPolicyListActive(channel.Denylist) && matchPolicyModels(modelKey, channel.Denylist.Models) {
		return modelPolicyDecision{Allowed: false, Layer: policyLayerChannel, Reason: "channel denylist: pattern matched"}
	}
	account := parseModelPolicyAttribute(attributes)
	if account.Allowlist.active() && !matchPolicyModels(modelKey, account.Allowlist.Models) {
		return modelPolicyDecision{Allowed: false, Layer: policyLayerAccount, Reason: "account allowlist: no pattern matched"}
	}
	if account.Denylist.active() && matchPolicyModels(modelKey, account.Denylist.Models) {
		return modelPolicyDecision{Allowed: false, Layer: policyLayerAccount, Reason: "account denylist: pattern matched"}
	}
	if legacy := strings.TrimSpace(attributes[AttributeExcludedModels]); legacy != "" {
		patterns := strings.Split(legacy, ",")
		for i := range patterns {
			patterns[i] = strings.ToLower(strings.TrimSpace(patterns[i]))
		}
		if matchPolicyModels(modelKey, patterns) {
			return modelPolicyDecision{Allowed: false, Layer: policyLayerAccount, Reason: policySourceLegacy + ": pattern matched"}
		}
	}
	return modelPolicyDecision{Allowed: true}
}

// AttributeExcludedModels mirrors the synthesizer's merged attribute key.
const AttributeExcludedModels = "excluded_models"

// parseModelPolicyAttribute decodes the runtime account policy attribute.
// 使用 JSON 保留模型 ID 内的分隔符，避免将模型文本解析为另一条策略。
func parseModelPolicyAttribute(attributes map[string]string) AccountModelPolicy {
	policy := AccountModelPolicy{}
	raw := strings.TrimSpace(attributes[AttributeModelPolicy])
	if raw == "" {
		return policy
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return AccountModelPolicy{Allowlist: &OAuthModelPolicy{Enabled: true}}
	}
	return policy
}

// encodeModelPolicyAttribute renders the account policy into the compact
// attribute form. Disabled lists are omitted so toggling off returns the
// account to its unrestricted baseline for that list.
func encodeModelPolicyAttribute(policy *AccountModelPolicy) string {
	if policy == nil {
		return ""
	}
	active := AccountModelPolicy{}
	if policy.Allowlist.active() {
		active.Allowlist = policy.Allowlist
	}
	if policy.Denylist.active() {
		active.Denylist = policy.Denylist
	}
	if active.Allowlist == nil && active.Denylist == nil {
		return ""
	}
	raw, _ := json.Marshal(active)
	return string(raw)
}

// EvaluateModelPolicy evaluates the channel + account model policy for one
// real upstream model ID on one auth. It is exported so the registration path
// (sdk/cliproxy) and management previews share the same decision function.
// A nil auth is treated as having no account lists; the channel policy and the
// legacy excluded_models union still apply when derivable from arguments.
func EvaluateModelPolicy(cfg *internalconfig.Config, auth *Auth, provider, modelID string) modelPolicyDecision {
	if auth != nil {
		source := auth.AuthSourceKind()
		if (source == AuthSourceConfig || source == "") && isConfiguredModelRoutingAuth(auth) {
			return modelPolicyDecision{Allowed: true}
		}
	}
	attributes := map[string]string{}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if auth != nil {
		if auth.Attributes != nil {
			for key, value := range auth.Attributes {
				attributes[key] = value
			}
		}
		if providerKey == "" {
			providerKey = strings.ToLower(strings.TrimSpace(auth.Provider))
		}
	}
	return evaluateModelPolicyLayers(cfg, attributes, providerKey, modelID)
}

// modelPolicyAllowedForExecution reports whether dispatching auth with the
// given real upstream model violates policy. The check is against the runtime
// config snapshot and the auth's own attributes, so Home-returned auths carry
// their account policy with them.
func (m *Manager) modelPolicyAllowedForExecution(auth *Auth, provider, modelID string) (modelPolicyDecision, bool) {
	if auth == nil || strings.TrimSpace(modelID) == "" {
		return modelPolicyDecision{Allowed: true}, true
	}
	if strings.TrimSpace(provider) == "" {
		provider = auth.Provider
	}
	decision := EvaluateModelPolicy(m.runtimeConfigSnapshot(), auth, provider, thinking.ParseSuffix(modelID).ModelName)
	return decision, decision.Allowed
}

// modelPolicyDeniedErrorFor wraps a denial decision into the typed local
// rejection error. Nil decisions are treated as allowed.
func modelPolicyDeniedErrorFor(modelID string, decision modelPolicyDecision) error {
	if decision.Allowed {
		return nil
	}
	return newModelPolicyDeniedError(modelID, decision)
}

// SetModelPolicyAttribute installs the parsed account policy onto an auth's
// attributes, replacing any previous value.
func SetModelPolicyAttribute(auth *Auth, policy *AccountModelPolicy) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string, 1)
	}
	if encoded := encodeModelPolicyAttribute(policy); encoded != "" {
		auth.Attributes[AttributeModelPolicy] = encoded
	} else {
		delete(auth.Attributes, AttributeModelPolicy)
	}
}

// ModelPolicyDeniedError is returned when an execution attempt violates the
// channel or account model policy. It is a local rejection: no upstream call
// is made and the credential must not enter cooldown because of it.
type ModelPolicyDeniedError struct {
	Model string
	Layer string
}

func (e *ModelPolicyDeniedError) Error() string {
	return "model policy denied " + e.Layer + " access to " + e.Model
}

func newModelPolicyDeniedError(modelID string, decision modelPolicyDecision) *ModelPolicyDeniedError {
	layer := decision.Layer
	if layer == "" {
		layer = "model policy"
	}
	return &ModelPolicyDeniedError{Model: modelID, Layer: layer}
}
