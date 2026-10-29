package config

import (
	"os"
	"strings"
	"testing"
)

// F04: uncommenting the Antigravity and Devin examples in place must produce
// settings that reach the runtime configuration and pass v8 validation.
func TestConfigExampleProviderBlocksUncommentInPlace(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	inProviders := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if line == "    providers:" {
			inProviders = true
			continue
		}
		if inProviders && line != "" && !strings.HasPrefix(line, "    ") {
			inProviders = false
		}
		if !inProviders {
			continue
		}
		for _, marker := range []string{"# sensitive-words:", "#   - \"API\"", "#   - \"proxy\"", "# connection-pool:", "#   enabled: false", "#   idle-conn-timeout:", "#   max-idle-conns-per-host:", "# devin:", "#     sensitive-words:", "#         - \"API\"", "#         - \"proxy\""} {
			if strings.HasPrefix(trimmed, marker) {
				idx := strings.Index(line, "# ")
				lines[i] = line[:idx] + line[idx+2:]
				break
			}
		}
	}
	uncommented := []byte(strings.Join(lines, "\n"))
	if err = ValidateV8Config(uncommented); err != nil {
		t.Fatalf("uncommented example rejected by v8 validation: %v", err)
	}
	cfg, err := ParseConfigBytes(uncommented)
	if err != nil {
		t.Fatalf("parse uncommented example: %v", err)
	}
	if got := cfg.Antigravity.SensitiveWords; len(got) != 2 || got[0] != "API" {
		t.Fatalf("antigravity sensitive-words=%v", got)
	}
	if cfg.Antigravity.ConnectionPool.IdleConnTimeout == "" {
		t.Fatalf("antigravity connection-pool not applied: %+v", cfg.Antigravity.ConnectionPool)
	}
	if got := cfg.Devin.SensitiveWords; len(got) != 2 || got[1] != "proxy" {
		t.Fatalf("devin sensitive-words=%v", got)
	}
}
