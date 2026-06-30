package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_CodexHeaderDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex-header-defaults:
  user-agent: "  my-codex-client/1.0  "
  beta-features: "  feature-a,feature-b  "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.CodexHeaderDefaults.UserAgent; got != "my-codex-client/1.0" {
		t.Fatalf("UserAgent = %q, want %q", got, "my-codex-client/1.0")
	}
	if got := cfg.CodexHeaderDefaults.BetaFeatures; got != "feature-a,feature-b" {
		t.Fatalf("BetaFeatures = %q, want %q", got, "feature-a,feature-b")
	}
}

func TestLoadConfigOptional_CodexIdentityConfuse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  identity-confuse: true
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.IdentityConfuse {
		t.Fatalf("IdentityConfuse = false, want true")
	}
}

func TestLoadConfigOptional_CodexAbnormalReasoningRetryDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`codex: {}`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	effective := cfg.Codex.AbnormalReasoningRetry.Effective()
	if effective.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if got, want := effective.ModelContains, []string{"gpt-5.5"}; !equalStringSlices(got, want) {
		t.Fatalf("ModelContains = %#v, want %#v", got, want)
	}
	if got, want := effective.ReasoningTokens, []int64{516, 1034}; !equalInt64Slices(got, want) {
		t.Fatalf("ReasoningTokens = %#v, want %#v", got, want)
	}
	if got, want := effective.AuthKinds, []string{"oauth"}; !equalStringSlices(got, want) {
		t.Fatalf("AuthKinds = %#v, want %#v", got, want)
	}
	if len(effective.AuthIDs) != 0 {
		t.Fatalf("AuthIDs = %#v, want empty", effective.AuthIDs)
	}
	if !effective.StreamBuffer {
		t.Fatal("StreamBuffer = false, want true")
	}
}

func TestLoadConfigOptional_CodexAbnormalReasoningRetryExplicit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  abnormal-reasoning-retry:
    enabled: true
    model-contains:
      - "  gpt-5.5  "
      - "gpt-5.5"
      - "custom"
    reasoning-tokens:
      - 516
      - 516
      - 1034
    auth-kinds:
      - "OAuth"
      - " api-key "
    auth-ids:
      - " auth-1 "
      - "auth-1"
    stream-buffer: false
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	effective := cfg.Codex.AbnormalReasoningRetry.Effective()
	if !effective.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got, want := effective.ModelContains, []string{"gpt-5.5", "custom"}; !equalStringSlices(got, want) {
		t.Fatalf("ModelContains = %#v, want %#v", got, want)
	}
	if got, want := effective.ReasoningTokens, []int64{516, 1034}; !equalInt64Slices(got, want) {
		t.Fatalf("ReasoningTokens = %#v, want %#v", got, want)
	}
	if got, want := effective.AuthKinds, []string{"oauth", "api-key"}; !equalStringSlices(got, want) {
		t.Fatalf("AuthKinds = %#v, want %#v", got, want)
	}
	if got, want := effective.AuthIDs, []string{"auth-1"}; !equalStringSlices(got, want) {
		t.Fatalf("AuthIDs = %#v, want %#v", got, want)
	}
	if effective.StreamBuffer {
		t.Fatal("StreamBuffer = true, want false")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInt64Slices(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
