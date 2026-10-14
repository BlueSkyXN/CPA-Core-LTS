package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func policyList(enabled bool, models ...string) *internalconfig.OAuthModelPolicyList {
	return &internalconfig.OAuthModelPolicyList{Enabled: enabled, Models: models}
}

func TestEvaluateModelPolicyLayersMatrix(t *testing.T) {
	legacyAttrs := func(patterns string) map[string]string {
		return map[string]string{AttributeExcludedModels: patterns}
	}
	accountAttrs := func(allow, deny string) map[string]string {
		attrs := map[string]string{}
		encoded := ""
		if allow != "" {
			encoded += "allow=" + allow
		}
		if deny != "" {
			if encoded != "" {
				encoded += ";"
			}
			encoded += "deny=" + deny
		}
		if encoded != "" {
			attrs[AttributeModelPolicy] = encoded
		}
		return attrs
	}

	tests := []struct {
		name       string
		cfg        *internalconfig.Config
		attributes map[string]string
		model      string
		provider   string // optional explicit provider; default derives below
		wantAllow  bool
		wantLayer  string
	}{
		{
			name:      "no policy allows everything",
			cfg:       &internalconfig.Config{},
			model:     "gpt-5-codex",
			wantAllow: true,
		},
		{
			name: "channel allowlist intersect match",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true, "gpt-*")},
			}},
			attributes: map[string]string{},
			model:      "gpt-5-codex",
			wantAllow:  true,
		},
		{
			name: "channel allowlist intersect miss",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true, "gpt-*")},
			}},
			attributes: map[string]string{},
			model:      "claude-sonnet-4",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name: "channel allowlist disabled does not restrict",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(false, "gpt-*")},
			}},
			attributes: map[string]string{},
			model:      "claude-sonnet-4",
			wantAllow:  true,
		},
		{
			name: "channel allowlist enabled empty denies all",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true)},
			}},
			attributes: map[string]string{},
			model:      "gpt-5-codex",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name: "channel denylist subtracts",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Denylist: policyList(true, "*-preview")},
			}},
			attributes: map[string]string{},
			model:      "gpt-5-preview",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name: "denylist wins over allowlist within channel",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {
					Allowlist: policyList(true, "gpt-*"),
					Denylist:  policyList(true, "*-preview"),
				},
			}},
			attributes: map[string]string{},
			model:      "gpt-5-preview",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name: "account allowlist intersects channel allowlist",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true, "gpt-*")},
			}},
			attributes: accountAttrs("gpt-5-codex", ""),
			model:      "gpt-5-codex",
			wantAllow:  true,
		},
		{
			name: "account allowlist cannot exceed channel allowlist",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true, "gpt-*")},
			}},
			attributes: accountAttrs("gpt-5-codex,claude-sonnet-4", ""),
			model:      "claude-sonnet-4",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name: "account denylist subtracts inside channel allowlist",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"copilot": {Allowlist: policyList(true, "gpt-*")},
			}},
			attributes: accountAttrs("", "gpt-5-codex"),
			model:      "gpt-5-codex",
			wantAllow:  false,
			wantLayer:  policyLayerAccount,
		},
		{
			name:       "account-only allowlist applies without channel policy",
			cfg:        &internalconfig.Config{},
			attributes: accountAttrs("qmodel_38max", ""),
			model:      "qmodel_38max",
			wantAllow:  true,
		},
		{
			name:       "legacy excluded models stays on (exact id match)",
			cfg:        &internalconfig.Config{},
			attributes: legacyAttrs("o3-mini"),
			model:      "o3-mini",
			wantAllow:  false,
			wantLayer:  policyLayerAccount,
		},
		{
			name:       "legacy excluded models supports wildcard",
			cfg:        &internalconfig.Config{},
			attributes: legacyAttrs("o3*"),
			model:      "o3-mini",
			wantAllow:  false,
			wantLayer:  policyLayerAccount,
		},
		{
			name: "legacy excluded models unions with new denylist",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"codex": {Denylist: policyList(true, "gpt-4*")},
			}},
			attributes: legacyAttrs("o3-mini"),
			model:      "o3-mini",
			wantAllow:  false,
			wantLayer:  policyLayerAccount,
		},
		{
			name: "legacy excluded models union also denies the new denylist pattern",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"codex": {Denylist: policyList(true, "gpt-4*")},
			}},
			attributes: legacyAttrs("unrelated"),
			model:      "gpt-4.1",
			wantAllow:  false,
			wantLayer:  policyLayerChannel,
		},
		{
			name:       "policy is case-insensitive on model and pattern",
			cfg:        &internalconfig.Config{},
			attributes: accountAttrs("", "GPT-5-*"),
			model:      " GPT-5-Codex ",
			wantAllow:  false,
			wantLayer:  policyLayerAccount,
		},
		{
			name: "other provider entry does not leak",
			cfg: &internalconfig.Config{OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
				"qoder": {Allowlist: policyList(true, "qmodel_*")},
			}},
			attributes: map[string]string{},
			model:      "gpt-5-codex",
			wantAllow:  true,
			provider:   "copilot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == "" {
				// Default: target the single configured channel entry, else codex.
				provider = "codex"
				if len(tt.cfg.OAuthModelPolicy) == 1 {
					for key := range tt.cfg.OAuthModelPolicy {
						provider = key
					}
				}
			}
			decision := evaluateModelPolicyLayers(tt.cfg, tt.attributes, provider, tt.model)
			if decision.Allowed != tt.wantAllow {
				t.Fatalf("allowed = %v, want %v (reason=%q layer=%q)", decision.Allowed, tt.wantAllow, decision.Reason, decision.Layer)
			}
			if !tt.wantAllow && decision.Layer != tt.wantLayer {
				t.Fatalf("layer = %q, want %q", decision.Layer, tt.wantLayer)
			}
		})
	}
}

func TestEvaluateModelPolicyTreatsNilAuthAsUnrestricted(t *testing.T) {
	cfg := &internalconfig.Config{}
	decision := EvaluateModelPolicy(cfg, nil, "codex", "gpt-5-codex")
	if !decision.Allowed {
		t.Fatalf("nil auth should be allowed, got %+v", decision)
	}
}

func TestParseAccountModelPolicyMetadataShapes(t *testing.T) {
	valid := map[string]any{
		"model_policy": map[string]any{
			"allowlist": map[string]any{"enabled": true, "models": []any{"gpt-5-codex"}},
			"denylist":  map[string]any{"enabled": false, "models": []any{}},
		},
	}
	policy, ok := ParseAccountModelPolicyMetadata(valid)
	if !ok || policy == nil {
		t.Fatalf("valid policy rejected: %v", policy)
	}
	if !policy.Allowlist.Enabled || len(policy.Allowlist.Models) != 1 {
		t.Fatalf("allowlist = %+v", policy.Allowlist)
	}

	invalid := []map[string]any{
		{"model_policy": "nope"},
		{"model_policy": map[string]any{"allowlist": "nope"}},
		{"model_policy": map[string]any{"allowlist": map[string]any{"enabled": "true"}}},
		{"model_policy": map[string]any{"allowlist": map[string]any{"models": []any{1}}}},
	}
	for _, metadata := range invalid {
		if _, ok := ParseAccountModelPolicyMetadata(metadata); ok {
			t.Fatalf("invalid shape accepted: %#v", metadata)
		}
	}

	absent := map[string]any{"type": "codex"}
	if policy, ok := ParseAccountModelPolicyMetadata(absent); !ok || policy != nil {
		t.Fatalf("absent policy should be nil+ok, got %+v %v", policy, ok)
	}
}

func TestSetModelPolicyAttributeRoundTrip(t *testing.T) {
	auth := &Auth{}
	SetModelPolicyAttribute(auth, &AccountModelPolicy{
		Allowlist: &OAuthModelPolicy{Enabled: true, Models: []string{"gpt-5-codex"}},
		Denylist:  &OAuthModelPolicy{Enabled: false, Models: []string{"kept-but-off"}},
	})
	if got := auth.Attributes[AttributeModelPolicy]; got != "allow=gpt-5-codex" {
		t.Fatalf("attribute = %q, want allow=gpt-5-codex", got)
	}
	parsed := parseModelPolicyAttribute(auth.Attributes)
	if parsed.Denylist != nil {
		t.Fatalf("disabled denylist should not be encoded, got %+v", parsed.Denylist)
	}
	SetModelPolicyAttribute(auth, nil)
	if _, exists := auth.Attributes[AttributeModelPolicy]; exists {
		t.Fatal("nil policy should remove the attribute")
	}
}
