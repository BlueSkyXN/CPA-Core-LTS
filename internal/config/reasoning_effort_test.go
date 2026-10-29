package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReasoningEffortConfigV8RoundTrip(t *testing.T) {
	data := []byte("config-version: 8\nrequests:\n  reasoning-effort:\n    - models: [policy-model, 'glm-*']\n      mode: force\n      effort: max\n")
	cfg, err := ParseConfigBytes(data)
	if err != nil || len(cfg.ReasoningEffort) != 1 || cfg.ReasoningEffort[0].Effort != "max" {
		t.Fatalf("parse rules: %v, %v", cfg, err)
	}
	cloned := cfg.CloneForRuntime()
	cloned.ReasoningEffort[0].Models[0] = "changed"
	if cfg.ReasoningEffort[0].Models[0] != "policy-model" {
		t.Fatal("runtime clone shared a policy model list")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.ReasoningEffort) != 1 {
		t.Fatalf("load rules: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(data) {
		t.Fatal("loading an effort policy rewrote its source")
	}
}

func TestReasoningEffortConfigRejectsInvalidRules(t *testing.T) {
	for _, rule := range []string{
		"{models: [], mode: force, effort: max}",
		"{models: [' '], mode: force, effort: max}",
		"{models: ['*'], mode: unknown, effort: max}",
		"{models: ['*'], mode: force}",
		"{models: ['*'], mode: default, effort: typo}",
		"{models: ['*'], mode: follow, effort: max}",
	} {
		data := []byte("config-version: 8\nrequests:\n  reasoning-effort: [" + rule + "]\n")
		_, err := ParseConfigBytes(data)
		if err == nil || !strings.Contains(err.Error(), "requests.reasoning-effort") {
			t.Fatalf("invalid rule accepted: %s, %v", rule, err)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		_ = os.WriteFile(path, data, 0600)
		if _, err := LoadConfigOptional(path, false); err == nil {
			t.Fatalf("startup accepted invalid rule: %s", rule)
		}
	}
}
