package config

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexCacheAffinityConfig(t *testing.T) {
	for _, strategy := range []string{"", "legacy", "stable-id", "client-aware", " CLIENT-AWARE ", "invalid"} {
		t.Run(strategy, func(t *testing.T) {
			raw := []byte("port: 8080\ncodex:\n  cache-affinity:\n    strategy: '" + strategy + "'\n")
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for _, load := range []func() (*Config, error){func() (*Config, error) { return ParseConfigBytes(raw) }, func() (*Config, error) { return LoadConfig(path) }} {
				cfg, err := load()
				if strategy == "invalid" {
					if err == nil {
						t.Fatal("invalid value accepted")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				expected := strings.ToLower(strings.TrimSpace(strategy))
				if expected == "" {
					expected = "client-aware"
				}
				if cfg.Codex.CacheAffinity.Strategy != expected {
					t.Fatal("incorrect default")
				}
				b, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var round Config
				if json.Unmarshal(b, &round) != nil || round.Codex.CacheAffinity.Strategy != expected {
					t.Fatal("JSON roundtrip")
				}
				b, err = yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if yaml.Unmarshal(b, &round) != nil || round.Codex.CacheAffinity.Strategy != expected {
					t.Fatal("YAML roundtrip")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("loading rewrote config")
			}
		})
	}
	raw := []byte("port: 8080\n")
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "cache-affinity") {
		t.Fatal("unrelated save inserted default strategy")
	}
}
