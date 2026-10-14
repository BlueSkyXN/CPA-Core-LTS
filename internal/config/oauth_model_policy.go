package config

import (
	"fmt"
	"strings"
)

// OAuthModelPolicyList is one switchable model list inside an OAuth model policy.
// Enabled=false keeps the stored models visible to operators without applying them.
type OAuthModelPolicyList struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Models  []string `yaml:"models" json:"models"`
}

// OAuthModelPolicyEntry is the per-provider model policy applied on top of the
// account catalog: an allowlist intersects, a denylist subtracts, and both are
// evaluated per real upstream model ID before aliases and prefixes.
type OAuthModelPolicyEntry struct {
	Allowlist *OAuthModelPolicyList `yaml:"allowlist,omitempty" json:"allowlist,omitempty"`
	Denylist  *OAuthModelPolicyList `yaml:"denylist,omitempty" json:"denylist,omitempty"`
}

// maxOAuthModelPolicyModels bounds one policy list so a malformed config cannot
// grow unbounded matching work on every registration.
const maxOAuthModelPolicyModels = 512

// NormalizeOAuthModelPolicyList cleans one switchable list. It returns nil for a
// nil list and rejects structurally invalid values.
func NormalizeOAuthModelPolicyList(list *OAuthModelPolicyList) (*OAuthModelPolicyList, error) {
	if list == nil {
		return nil, nil
	}
	normalized := &OAuthModelPolicyList{Enabled: list.Enabled}
	if len(list.Models) > maxOAuthModelPolicyModels {
		return nil, fmt.Errorf("supports at most %d entries", maxOAuthModelPolicyModels)
	}
	seen := make(map[string]struct{}, len(list.Models))
	models := make([]string, 0, len(list.Models))
	for _, raw := range list.Models {
		model := strings.ToLower(strings.TrimSpace(raw))
		// Blank entries are skipped like legacy excluded-models normalization;
		// control characters are structural errors, not silently dropped.
		if model == "" {
			continue
		}
		if strings.ContainsAny(model, "\x00\r\n") {
			return nil, fmt.Errorf("contains an invalid model entry")
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	normalized.Models = models
	return normalized, nil
}

// NormalizeOAuthModelPolicyEntry cleans one provider policy entry.
func NormalizeOAuthModelPolicyEntry(entry OAuthModelPolicyEntry) (OAuthModelPolicyEntry, error) {
	allowlist, errAllow := NormalizeOAuthModelPolicyList(entry.Allowlist)
	if errAllow != nil {
		return OAuthModelPolicyEntry{}, fmt.Errorf("allowlist %w", errAllow)
	}
	denylist, errDeny := NormalizeOAuthModelPolicyList(entry.Denylist)
	if errDeny != nil {
		return OAuthModelPolicyEntry{}, fmt.Errorf("denylist %w", errDeny)
	}
	return OAuthModelPolicyEntry{Allowlist: allowlist, Denylist: denylist}, nil
}

// NormalizeOAuthModelPolicy cleans the provider -> policy mapping. Provider keys
// are lowercased and structurally invalid entries are dropped; an empty mapping
// normalizes to nil.
func NormalizeOAuthModelPolicy(entries map[string]OAuthModelPolicyEntry) map[string]OAuthModelPolicyEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]OAuthModelPolicyEntry, len(entries))
	for provider, entry := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		normalized, errNormalize := NormalizeOAuthModelPolicyEntry(entry)
		if errNormalize != nil {
			continue
		}
		if normalized.Allowlist == nil && normalized.Denylist == nil {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
