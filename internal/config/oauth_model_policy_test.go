package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeOAuthModelPolicy(t *testing.T) {
	raw := map[string]OAuthModelPolicyEntry{
		"  Copilot ": {
			Allowlist: &OAuthModelPolicyList{Enabled: true, Models: []string{" GPT-* ", "gpt-*", ""}},
			Denylist:  &OAuthModelPolicyList{Enabled: false, Models: []string{"*-preview"}},
		},
		"invalid": {
			Allowlist: &OAuthModelPolicyList{Enabled: true, Models: []string{"bad\x00id"}},
		},
		"empty": {},
	}
	normalized := NormalizeOAuthModelPolicy(raw)
	if len(normalized) != 1 {
		t.Fatalf("normalized = %#v, want only copilot", normalized)
	}
	entry, ok := normalized["copilot"]
	if !ok {
		t.Fatalf("provider key not lowercased: %#v", normalized)
	}
	if entry.Allowlist == nil || !entry.Allowlist.Enabled {
		t.Fatalf("allowlist lost: %#v", entry)
	}
	if got := entry.Allowlist.Models; len(got) != 1 || got[0] != "gpt-*" {
		t.Fatalf("allowlist models = %#v, want [gpt-*]", got)
	}
	// Disabled denylist keeps its stored models for a later toggle-on.
	if entry.Denylist == nil || entry.Denylist.Enabled || len(entry.Denylist.Models) != 1 {
		t.Fatalf("denylist = %#v, want disabled with stored models", entry.Denylist)
	}

	if NormalizeOAuthModelPolicy(nil) != nil {
		t.Fatal("nil map should normalize to nil")
	}
}

func TestNormalizeOAuthModelPolicyListRejectsOversized(t *testing.T) {
	models := make([]string, maxOAuthModelPolicyModels+1)
	for i := range models {
		models[i] = "m"
	}
	if _, err := NormalizeOAuthModelPolicyList(&OAuthModelPolicyList{Enabled: true, Models: models}); err == nil {
		t.Fatal("oversized list accepted")
	}
}

func TestOAuthModelPolicyInvalidRestrictionFailsBothLoaders(t *testing.T) {
	raw := []byte("oauth-model-policy:\n  codex:\n    allowlist:\n      enabled: true\n      models: [\"bad\\u0000id\"]\n")
	if _, err := ParseConfigBytes(raw); err == nil {
		t.Fatal("invalid restriction was silently dropped by byte parser")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("invalid restriction was silently dropped by file loader")
	}
}
