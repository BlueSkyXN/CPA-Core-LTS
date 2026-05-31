package synthesizer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestConfigSynthesizer_CompactMetadata(t *testing.T) {
	cfg := &config.Config{
		CompactDefault: "deny",
		CodexKey: []config.CodexKey{
			{APIKey: "codex-on", BaseURL: "https://codex.example.com", Compact: "force_on"},
			{APIKey: "codex-auto", BaseURL: "https://codex.example.com", Compact: "auto"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:          "compat",
				BaseURL:       "https://compat.example.com/v1",
				Compact:       "force_off",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "compat-key"}},
			},
		},
	}
	ctx := &SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()}

	auths, err := NewConfigSynthesizer().Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	got := map[string]string{}
	for _, auth := range auths {
		got[auth.Attributes["api_key"]] = auth.Attributes["compact_allowed"]
	}
	if got["codex-on"] != "true" {
		t.Fatalf("codex force_on compact_allowed = %q, want true", got["codex-on"])
	}
	if got["codex-auto"] != "false" {
		t.Fatalf("codex auto compact_allowed = %q, want false", got["codex-auto"])
	}
	if got["compat-key"] != "false" {
		t.Fatalf("compat force_off compact_allowed = %q, want false", got["compat-key"])
	}
}

func TestFileSynthesizer_CodexCompactMetadata(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","compact":"force_off"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	ctx := &SynthesisContext{
		Config:      &config.Config{CompactDefault: "allow"},
		AuthDir:     authDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := NewFileSynthesizer().Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths len = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes["compact_allowed"]; got != "false" {
		t.Fatalf("compact_allowed = %q, want false", got)
	}
	if got := auths[0].Attributes["compact_mode"]; got != "force_off" {
		t.Fatalf("compact_mode = %q, want force_off", got)
	}
}
