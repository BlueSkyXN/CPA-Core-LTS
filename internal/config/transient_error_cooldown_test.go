package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_TransientErrorCooldownDefaultAndLegacyOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional default: %v", err)
	}
	if cfg.TransientErrorCooldownSeconds != 30 {
		t.Fatalf("default transient-error-cooldown-seconds = %d, want 30", cfg.TransientErrorCooldownSeconds)
	}

	if err := os.WriteFile(configPath, []byte("transient-error-cooldown-seconds: 0\n"), 0o600); err != nil {
		t.Fatalf("write legacy override config: %v", err)
	}
	cfg, err = LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional legacy override: %v", err)
	}
	if cfg.TransientErrorCooldownSeconds != 0 {
		t.Fatalf("explicit transient-error-cooldown-seconds = %d, want 0", cfg.TransientErrorCooldownSeconds)
	}
}

func TestParseConfigBytes_TransientErrorCooldownDefault(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.TransientErrorCooldownSeconds != 30 {
		t.Fatalf("default transient-error-cooldown-seconds = %d, want 30", cfg.TransientErrorCooldownSeconds)
	}
}
