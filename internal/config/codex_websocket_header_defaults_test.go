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
	if len(effective.ReasoningEfforts) != 0 {
		t.Fatalf("ReasoningEfforts = %#v, want empty", effective.ReasoningEfforts)
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
	if effective.StreamBufferMaxBytes != 0 {
		t.Fatalf("StreamBufferMaxBytes = %d, want 0", effective.StreamBufferMaxBytes)
	}
	if effective.MaxRetries != 2 {
		t.Fatalf("MaxRetries = %d, want 2", effective.MaxRetries)
	}
	if effective.ExhaustedBehavior != CodexAbnormalReasoningRetryExhaustedBehaviorError {
		t.Fatalf("ExhaustedBehavior = %q, want %q", effective.ExhaustedBehavior, CodexAbnormalReasoningRetryExhaustedBehaviorError)
	}
	if effective.HedgedRetry.Enabled {
		t.Fatal("HedgedRetry.Enabled = true, want false")
	}
	if effective.HedgedRetry.HedgeDelayMS != 1000 {
		t.Fatalf("HedgedRetry.HedgeDelayMS = %d, want 1000", effective.HedgedRetry.HedgeDelayMS)
	}
	if !effective.HedgedRetry.RequireDistinctAuth {
		t.Fatal("HedgedRetry.RequireDistinctAuth = false, want true")
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
    reasoning-efforts:
      - " XHigh "
      - "xhigh"
      - "high"
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
    stream-buffer-max-bytes: 4096
    max-retries: 0
    exhausted-behavior: "passthrough"
    hedged-retry:
      enabled: true
      hedge-delay-ms: 250
      require-distinct-auth: false
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
	if got, want := effective.ReasoningEfforts, []string{"xhigh", "high"}; !equalStringSlices(got, want) {
		t.Fatalf("ReasoningEfforts = %#v, want %#v", got, want)
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
	if effective.StreamBufferMaxBytes != 4096 {
		t.Fatalf("StreamBufferMaxBytes = %d, want 4096", effective.StreamBufferMaxBytes)
	}
	if effective.MaxRetries != 0 {
		t.Fatalf("MaxRetries = %d, want 0", effective.MaxRetries)
	}
	if effective.ExhaustedBehavior != CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough {
		t.Fatalf("ExhaustedBehavior = %q, want %q", effective.ExhaustedBehavior, CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)
	}
	if !effective.HedgedRetry.Enabled {
		t.Fatal("HedgedRetry.Enabled = false, want true")
	}
	if effective.HedgedRetry.HedgeDelayMS != 250 {
		t.Fatalf("HedgedRetry.HedgeDelayMS = %d, want 250", effective.HedgedRetry.HedgeDelayMS)
	}
	if effective.HedgedRetry.RequireDistinctAuth {
		t.Fatal("HedgedRetry.RequireDistinctAuth = true, want false")
	}
}

func TestLoadConfigOptional_CodexAbnormalReasoningRetryNegativeMaxRetriesClamped(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  abnormal-reasoning-retry:
    enabled: true
    max-retries: -1
    stream-buffer-max-bytes: -128
    hedged-retry:
      hedge-delay-ms: -25
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	effective := cfg.Codex.AbnormalReasoningRetry.Effective()
	if effective.MaxRetries != 0 {
		t.Fatalf("MaxRetries = %d, want 0", effective.MaxRetries)
	}
	if effective.StreamBufferMaxBytes != 0 {
		t.Fatalf("StreamBufferMaxBytes = %d, want 0", effective.StreamBufferMaxBytes)
	}
	if effective.HedgedRetry.HedgeDelayMS != 0 {
		t.Fatalf("HedgedRetry.HedgeDelayMS = %d, want 0", effective.HedgedRetry.HedgeDelayMS)
	}
}

func TestLoadConfigOptional_CodexAbnormalReasoningRetryInvalidExhaustedBehaviorDefaultsToError(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  abnormal-reasoning-retry:
    enabled: true
    exhausted-behavior: unexpected
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	effective := cfg.Codex.AbnormalReasoningRetry.Effective()
	if effective.ExhaustedBehavior != CodexAbnormalReasoningRetryExhaustedBehaviorError {
		t.Fatalf("ExhaustedBehavior = %q, want %q", effective.ExhaustedBehavior, CodexAbnormalReasoningRetryExhaustedBehaviorError)
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
