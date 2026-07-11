package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfigOptionalCodexModelFallback(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := []byte(`
codex:
  model-fallback:
    enabled: true
    triggers: [capacity]
    reasoning-continuity: context-reset
    mappings:
      - from: gpt-source
        to: [gpt-target, gpt-backup]
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	effective := cfg.Codex.ModelFallback.Effective()
	if !effective.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if effective.ReasoningContinuity != CodexModelFallbackReasoningContinuityContextReset {
		t.Fatalf("ReasoningContinuity = %q, want context-reset", effective.ReasoningContinuity)
	}
	if got := effective.TargetsFor("gpt-source", CodexModelFallbackTriggerCapacity); !reflect.DeepEqual(got, []string{"gpt-target", "gpt-backup"}) {
		t.Fatalf("TargetsFor() = %#v", got)
	}
}

func TestCodexModelFallbackEffectiveDefaultsAndNormalizesMappings(t *testing.T) {
	effective := (CodexModelFallbackConfig{
		Enabled: true,
		Mappings: []CodexModelFallbackMapping{
			{From: " gpt-5.6-sol ", To: []string{" gpt-5.6-terra ", "GPT-5.6-TERRA", "gpt-5.6-sol", ""}},
			{From: "", To: []string{"gpt-5.5"}},
		},
	}).Effective()

	if !effective.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if effective.ReasoningContinuity != CodexModelFallbackReasoningContinuitySameModelOnly {
		t.Fatalf("ReasoningContinuity = %q, want %q", effective.ReasoningContinuity, CodexModelFallbackReasoningContinuitySameModelOnly)
	}
	if !reflect.DeepEqual(effective.Triggers, []string{CodexModelFallbackTriggerUsageLimit, CodexModelFallbackTriggerCapacity}) {
		t.Fatalf("Triggers = %#v", effective.Triggers)
	}
	if len(effective.Mappings) != 1 {
		t.Fatalf("Mappings = %#v, want one normalized mapping", effective.Mappings)
	}
	if got := effective.TargetsFor("gpt-5.6-sol", CodexModelFallbackTriggerUsageLimit); !reflect.DeepEqual(got, []string{"gpt-5.6-terra"}) {
		t.Fatalf("TargetsFor() = %#v, want [gpt-5.6-terra]", got)
	}
	if got := effective.TargetsFor("gpt-5.6-sol", "rate-limit"); got != nil {
		t.Fatalf("TargetsFor(transient) = %#v, want nil", got)
	}
}

func TestCodexModelFallbackEffectiveSupportsExplicitContextResetAndTriggers(t *testing.T) {
	effective := (CodexModelFallbackConfig{
		Enabled:             true,
		Triggers:            []string{"capacity", "unknown", "CAPACITY"},
		ReasoningContinuity: "context-reset",
		Mappings: []CodexModelFallbackMapping{
			{From: "gpt-a", To: []string{"gpt-b", "gpt-c"}},
		},
	}).Effective()

	if effective.ReasoningContinuity != CodexModelFallbackReasoningContinuityContextReset {
		t.Fatalf("ReasoningContinuity = %q, want context-reset", effective.ReasoningContinuity)
	}
	if !reflect.DeepEqual(effective.Triggers, []string{CodexModelFallbackTriggerCapacity}) {
		t.Fatalf("Triggers = %#v, want [capacity]", effective.Triggers)
	}
	if got := effective.TargetsFor("GPT-A", CodexModelFallbackTriggerCapacity); !reflect.DeepEqual(got, []string{"gpt-b", "gpt-c"}) {
		t.Fatalf("TargetsFor(capacity) = %#v", got)
	}
	if got := effective.TargetsFor("gpt-a", CodexModelFallbackTriggerUsageLimit); got != nil {
		t.Fatalf("TargetsFor(usage-limit) = %#v, want nil", got)
	}
}

func TestCodexModelFallbackTargetsForDisabledPolicy(t *testing.T) {
	effective := (CodexModelFallbackConfig{
		Mappings: []CodexModelFallbackMapping{{From: "gpt-a", To: []string{"gpt-b"}}},
	}).Effective()
	if got := effective.TargetsFor("gpt-a", CodexModelFallbackTriggerUsageLimit); got != nil {
		t.Fatalf("TargetsFor(disabled) = %#v, want nil", got)
	}
}
