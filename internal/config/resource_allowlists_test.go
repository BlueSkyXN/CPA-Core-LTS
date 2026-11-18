package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResourceAllowlistsUpstreamKeysAcrossV8Providers(t *testing.T) {
	ref := strings.Repeat("a", 64)
	var groups, rules strings.Builder
	for _, family := range v8KeyFamilies {
		fmt.Fprintf(&groups, "  %s:\n    - name: fixture\n      base-url: https://example.invalid\n      keys:\n        - api-key: example-%s\n          credential-id: fixture-%s\n", family.current, family.current, family.current)
		fmt.Fprintf(&rules, "      fixture-%s: [%q]\n", family.current, ref)
	}
	raw := []byte("access:\n  resource-allowlists:\n    upstream-api-keys:\n" + rules.String() + "api-keys:\n" + groups.String())
	if err := ValidateV8Config(raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.CompileResourceAllowlists(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range v8KeyFamilies {
		value := "example-" + family.current
		if !policy.AllowsConfiguredUpstreamAPIKey(value, ref) || policy.AllowsConfiguredUpstreamAPIKey(value, "") {
			t.Errorf("wrong authorization for family %s", family.current)
		}
	}
}

func TestResourceAllowlistsDuplicateUpstreamMaterialUsesIntersection(t *testing.T) {
	owner, shared, partner := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	cfg := &Config{
		GeminiKey: []GeminiKey{
			{APIKey: "example-reused", CredentialID: "team-main"},
			{APIKey: "example-reused"},
			{APIKey: "example-public"},
			{APIKey: "example-disabled", CredentialID: "disabled"},
		},
		ClaudeKey: []ClaudeKey{{APIKey: "example-reused", CredentialID: "team-other"}},
		ResourceAllowlists: &ResourceAllowlists{
			UpstreamAPIKeys: map[string][]string{
				"team-main": {owner, shared}, "team-other": {shared, partner}, "disabled": {},
			},
		},
	}
	policy, err := cfg.CompileResourceAllowlists(false)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsConfiguredUpstreamAPIKey("example-reused", shared) ||
		policy.AllowsConfiguredUpstreamAPIKey("example-reused", owner) ||
		policy.AllowsConfiguredUpstreamAPIKey("example-reused", partner) ||
		policy.AllowsConfiguredUpstreamAPIKey("example-disabled", shared) ||
		!policy.AllowsConfiguredUpstreamAPIKey("example-public", "") ||
		policy.AllowsConfiguredUpstreamAPIKey("example-removed", shared) {
		t.Fatal("unexpected decisions for duplicated, shared, suspended or stale upstream keys")
	}
}

func TestResourceAllowlistsRejectInvalidUpstreamReferences(t *testing.T) {
	validKey := "  codex:\n    - base-url: https://example.invalid\n      keys:\n        - api-key: example-upstream\n          credential-id: team\n"
	split := "access:\n  resource-allowlists:\n    upstream-api-keys:\n      team: []\napi-keys:\n"
	tests := []struct {
		name, input string
	}{
		{"unknown-id", strings.ReplaceAll(validKey, "credential-id: team", "credential-id: other")},
		{"duplicate-id", validKey + "  claude:\n    - keys:\n        - api-key: example-other\n          credential-id: TEAM\n"},
		{"id-on-group", strings.ReplaceAll(validKey, "    - base-url: https://example.invalid", "    - credential-id: team")},
		{"missing-key", strings.ReplaceAll(validKey, "api-key: example-upstream", "api-key: ''")},
		{"key-removed-by-sanitization", strings.ReplaceAll(validKey, "https://example.invalid", "''")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(split + tc.input)
			if _, err := ParseConfigBytes(raw); err == nil || strings.Contains(err.Error(), "example-upstream") {
				t.Fatalf("want safe validation error, got %v", err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfigOptional(path, false); err == nil || strings.Contains(err.Error(), "example-upstream") {
				t.Fatalf("want safe load error, got %v", err)
			}
		})
	}
}

func TestResourceAllowlistsV8RoundTrip(t *testing.T) {
	ref := strings.Repeat("a", 64)
	raw := []byte(`access:
  api-keys: [example-key]
  resource-allowlists:
    auth-files:
      Team.json: ["` + strings.ToUpper(ref) + `"] # reserved credential
    models:
      Example-Private: []
`)
	if err := ValidateV8Config(raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := &ResourceAllowlists{AuthFiles: map[string][]string{"Team.json": {strings.ToUpper(ref)}}, Models: map[string][]string{"Example-Private": {}}}
	if !reflect.DeepEqual(cfg.ResourceAllowlists, want) {
		t.Fatalf("parsed rules = %#v", cfg.ResourceAllowlists)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = append(cfg.APIKeys, "another-example-key")
	if err := SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.ResourceAllowlists, want) || len(reloaded.APIKeys) != 2 {
		t.Fatalf("reloaded rules = %#v, got %d keys", reloaded.ResourceAllowlists, len(reloaded.APIKeys))
	}
	normalized, err := reloaded.ResourceAllowlists.Normalize()
	if err != nil || !reflect.DeepEqual(normalized.AuthFiles["team.json"], []string{ref}) {
		t.Fatalf("normalized rules = %#v, error = %v", normalized, err)
	}
	reloaded.Home.Enabled = true
	if err := reloaded.ValidateResourceAllowlists(); err == nil {
		t.Fatal("Home must reject local resource rules")
	}
}

func TestResourceAllowlistsRejectInvalidRules(t *testing.T) {
	invalid := []string{
		"models: {private: null}",
		"models: {private: [example-plaintext-key]}",
		"models: {private: [false]}",
		"models: {private: [], Private: []}",
		"auth-files: {'../team.json': []}",
		"auth-file: {team.json: []}",
	}
	for _, fields := range invalid {
		t.Run(fields, func(t *testing.T) {
			raw := []byte("access:\n  resource-allowlists:\n    " + fields + "\n")
			if _, err := ParseConfigBytes(raw); err == nil || strings.Contains(err.Error(), "example-plaintext-key") {
				t.Fatalf("expected redacted validation error, got %v", err)
			}
			if err := ValidateV8Config(raw); err == nil {
				t.Fatal("v8 accepted invalid resource rules")
			}
		})
	}
}
