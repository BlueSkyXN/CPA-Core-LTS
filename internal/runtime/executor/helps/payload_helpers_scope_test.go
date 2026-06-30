package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRequest_ModelScopeDefaultKeepsMergedCandidates(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5"},
					},
					Params: map[string]any{
						"metadata.legacy": true,
					},
				},
			},
		},
	}

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5-fast", "", nil)

	if !gjson.GetBytes(out, "metadata.legacy").Bool() {
		t.Fatalf("expected no-scope rule to match merged candidates, payload=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRequest_ModelScopeRequestedSeparatesAliasFromPlainModel(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5", Scope: "requested"},
					},
					Params: map[string]any{
						"metadata.plain": true,
					},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5-fast", Scope: "requested"},
					},
					Params: map[string]any{
						"metadata.fast": true,
					},
				},
			},
		},
	}

	fast := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5-fast", "", nil)
	if gjson.GetBytes(fast, "metadata.plain").Exists() {
		t.Fatalf("expected requested plain rule to skip fast alias, payload=%s", string(fast))
	}
	if !gjson.GetBytes(fast, "metadata.fast").Bool() {
		t.Fatalf("expected requested fast rule to match fast alias, payload=%s", string(fast))
	}

	plain := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5", "", nil)
	if !gjson.GetBytes(plain, "metadata.plain").Bool() {
		t.Fatalf("expected requested plain rule to match plain request, payload=%s", string(plain))
	}
	if gjson.GetBytes(plain, "metadata.fast").Exists() {
		t.Fatalf("expected requested fast rule to skip plain request, payload=%s", string(plain))
	}

	suffixed := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5(high)", "", nil)
	if !gjson.GetBytes(suffixed, "metadata.plain").Bool() {
		t.Fatalf("expected requested plain rule to match suffix-normalized request, payload=%s", string(suffixed))
	}
}

func TestApplyPayloadConfigWithRequest_ModelScopeUpstreamMatchesResolvedModel(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5", Scope: "upstream"},
					},
					Params: map[string]any{
						"metadata.upstream": true,
					},
				},
			},
		},
	}

	fast := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5-fast", "", nil)
	if !gjson.GetBytes(fast, "metadata.upstream").Bool() {
		t.Fatalf("expected upstream rule to match fast alias resolved model, payload=%s", string(fast))
	}

	plain := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5", "", nil)
	if !gjson.GetBytes(plain, "metadata.upstream").Bool() {
		t.Fatalf("expected upstream rule to match plain resolved model, payload=%s", string(plain))
	}
}

func TestApplyPayloadConfigWithRequest_ModelScopeRequestedFilterDoesNotRemoveFastPriority(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5-fast", Scope: "requested"},
					},
					Params: map[string]any{
						"service_tier": "priority",
					},
				},
			},
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5", Scope: "requested"},
					},
					Params: []string{"service_tier"},
				},
			},
		},
	}

	fast := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5-fast", "", nil)
	if got := gjson.GetBytes(fast, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier for fast alias = %q, want priority; payload=%s", got, string(fast))
	}

	plain := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil, "gpt-5.5", "", nil)
	if gjson.GetBytes(plain, "service_tier").Exists() {
		t.Fatalf("expected requested plain filter to remove service_tier, payload=%s", string(plain))
	}
}

func TestApplyPayloadConfigWithRequest_UnknownModelScopeFallsBackToAny(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.5", Scope: "requestd"},
					},
					Params: map[string]any{
						"metadata.applied": true,
					},
				},
			},
		},
	}

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.5", "codex", "responses", "", []byte(`{"model":"gpt-5.5"}`), nil, "gpt-5.5-fast", "", nil)

	if !gjson.GetBytes(out, "metadata.applied").Bool() {
		t.Fatalf("expected unknown scope to fall back to any candidates, payload=%s", string(out))
	}
}
