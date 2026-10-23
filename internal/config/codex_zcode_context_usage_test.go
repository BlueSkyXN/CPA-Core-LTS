package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodexZcodeContextUsageConfig(t *testing.T) {
	for _, mode := range []string{"", "disabled", "observe", "correct", " CORRECT ", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			raw := []byte("codex:\n  zcode-context-usage: '" + mode + "'\n")
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for _, load := range []func() (*Config, error){func() (*Config, error) { return ParseConfigBytes(raw) }, func() (*Config, error) { return LoadConfig(path) }} {
				cfg, err := load()
				if mode == "invalid" {
					if err == nil || !strings.Contains(err.Error(), "codex.zcode-context-usage") {
						t.Fatal("invalid mode accepted", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				want := CodexZcodeContextUsageMode(strings.ToLower(strings.TrimSpace(mode))).Effective()
				if cfg.Codex.ZcodeContextUsage.Effective() != want {
					t.Fatal("mode mismatch")
				}
				encoded, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				round, err := ParseConfigBytes(encoded)
				if err != nil || round.Codex.ZcodeContextUsage.Effective() != want {
					t.Fatal("roundtrip failed", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("loading rewrote file")
			}
		})
	}
	cfg, err := ParseConfigBytes([]byte("port: 8080\n"))
	if err != nil || cfg.Codex.ZcodeContextUsage.Effective() != ZcodeContextUsageDisabled {
		t.Fatal("missing mode must be disabled", err)
	}
}
