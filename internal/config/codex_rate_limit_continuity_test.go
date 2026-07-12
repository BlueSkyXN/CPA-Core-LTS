package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexRateLimitContinuityEffectiveDefaults(t *testing.T) {
	effective := (CodexRateLimitContinuityConfig{}).Effective()
	if effective.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if effective.ObservationWindowSeconds != CodexRateLimitContinuityDefaultObservationWindowSeconds {
		t.Fatalf("ObservationWindowSeconds = %d", effective.ObservationWindowSeconds)
	}
	if effective.EstablishedSuccessThreshold != CodexRateLimitContinuityDefaultEstablishedSuccessThreshold {
		t.Fatalf("EstablishedSuccessThreshold = %d", effective.EstablishedSuccessThreshold)
	}
	if effective.EstablishedSessionTTLSeconds != CodexRateLimitContinuityDefaultEstablishedSessionTTLSeconds {
		t.Fatalf("EstablishedSessionTTLSeconds = %d", effective.EstablishedSessionTTLSeconds)
	}
}

func TestLoadConfigOptionalCodexRateLimitContinuity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := []byte(`
routing:
  session-affinity: true
codex:
  rate-limit-continuity:
    enabled: true
    observation-window-seconds: 12
    established-success-threshold: 3
    established-session-ttl-seconds: 900
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if !cfg.Routing.SessionAffinity {
		t.Fatal("Routing.SessionAffinity = false, want true")
	}
	effective := cfg.Codex.RateLimitContinuity.Effective()
	if !effective.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if effective.ObservationWindowSeconds != 12 || effective.EstablishedSuccessThreshold != 3 || effective.EstablishedSessionTTLSeconds != 900 {
		t.Fatalf("Effective() = %+v", effective)
	}
}
