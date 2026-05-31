package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_CompactRoutingControls(t *testing.T) {
	cfg := loadCompactConfigForTest(t, `
compact-default: deny
codex-api-key:
  - api-key: "codex-key"
    base-url: "https://codex.example.com"
    compact: force_on
openai-compatibility:
  - name: "compat"
    base-url: "https://compat.example.com/v1"
    compact: force_off
`)

	if cfg.CompactDefault != "deny" {
		t.Fatalf("CompactDefault = %q, want deny", cfg.CompactDefault)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].Compact != "force_on" {
		t.Fatalf("CodexKey compact = %+v, want force_on", cfg.CodexKey)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Compact != "force_off" {
		t.Fatalf("OpenAICompatibility compact = %+v, want force_off", cfg.OpenAICompatibility)
	}
}

func TestLoadConfig_CompactRoutingControlsNormalizeCase(t *testing.T) {
	cfg := loadCompactConfigForTest(t, `
compact-default: "  DeNy "
codex-api-key:
  - api-key: "codex-key"
    base-url: "https://codex.example.com"
    compact: " FORCE_OFF "
openai-compatibility:
  - name: "compat"
    base-url: "https://compat.example.com/v1"
    compact: " Force_On "
`)

	if cfg.CompactDefault != "deny" {
		t.Fatalf("CompactDefault = %q, want deny", cfg.CompactDefault)
	}
	if got := cfg.CodexKey[0].Compact; got != "force_off" {
		t.Fatalf("CodexKey compact = %q, want force_off", got)
	}
	if got := cfg.OpenAICompatibility[0].Compact; got != "force_on" {
		t.Fatalf("OpenAICompatibility compact = %q, want force_on", got)
	}
}

func TestLoadConfig_CompactRoutingControlsInvalidFallbacks(t *testing.T) {
	cfg := loadCompactConfigForTest(t, `
compact-default: invalid
codex-api-key:
  - api-key: "codex-key"
    base-url: "https://codex.example.com"
    compact: wrong
openai-compatibility:
  - name: "compat"
    base-url: "https://compat.example.com/v1"
    compact: wrong
`)

	if cfg.CompactDefault != "allow" {
		t.Fatalf("CompactDefault = %q, want allow", cfg.CompactDefault)
	}
	if got := cfg.CodexKey[0].Compact; got != "auto" {
		t.Fatalf("CodexKey compact = %q, want auto", got)
	}
	if got := cfg.OpenAICompatibility[0].Compact; got != "auto" {
		t.Fatalf("OpenAICompatibility compact = %q, want auto", got)
	}
}

func loadCompactConfigForTest(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}
