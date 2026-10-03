package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8LTSMigrationPreservesEffectiveConfigAndScope(t *testing.T) {
	raw := []byte(`port: 8317
api-keys: [synthetic-client]
api-request-body-max-bytes: 123456
flow-control: {enabled: false}
ampcode: {upstream-url: "https://amp.example"}
codex:
  identity-confuse: true
  cache-affinity: {strategy: client-aware}
  client-metadata: {mode: strict, workspace-policy: drop}
  desktop-tool-overlay: {enabled: false, tools: []}
  model-fallback: {enabled: false}
  rate-limit-continuity: {enabled: false}
  abnormal-reasoning-retry: {enabled: false}
plugins:
  enabled: true
  configs:
    synthetic:
      opaque: {arbitrary-key: [1, two, false]}
`)
	before, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	migrated, changed, err := NormalizeConfigLayout(raw, true)
	if err != nil || !changed {
		t.Fatalf("migration: changed=%v error=%v", changed, err)
	}
	if err := ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	assertV8LTSEffectiveEqual(t, before, after)

	if !reflect.DeepEqual(after.Codex, after.ForAPIKey().Codex) {
		t.Fatal("API-key scope removed shared LTS settings")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(migrated, &doc); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"flow-control", "api-request-body-max-bytes", "ampcode", "upstream.codex.identity-confuse", "upstream.codex.client-metadata", "upstream.codex.cache-affinity", "plugins.configs.synthetic.opaque"} {
		if yamlPath(doc.Content[0], path) == nil {
			t.Fatalf("migration lost %s", path)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, migrated, 0600); err != nil {
		t.Fatal(err)
	}
	after.RequestRetry++
	if err := SaveConfigPreserveComments(path, after); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	assertV8LTSEffectiveEqual(t, after, reloaded)
}

func TestV8LTSSharedAliasAndExplicitFalse(t *testing.T) {
	for _, raw := range []string{
		"oauth: {providers: {codex: {identity-confuse: true}}}\n",
		"codex: {identity-confuse: true}\n",
	} {
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil || !cfg.ForAPIKey().Codex.IdentityConfuse {
			t.Fatalf("shared alias was scoped away: %v", err)
		}
		cfg, err = ParseConfigBytes([]byte(raw + "upstream: {codex: {identity-confuse: false}}\n"))
		if err != nil || cfg.Codex.IdentityConfuse {
			t.Fatalf("explicit canonical false did not win: %v", err)
		}
	}
}

func assertV8LTSEffectiveEqual(t *testing.T, a, b *Config) {
	t.Helper()
	normalize := func(cfg *Config) any {
		data, err := yaml.Marshal((*legacyConfig)(cfg))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := yaml.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(normalize(a), normalize(b)) || !reflect.DeepEqual(a.OAuthOnlyFields, b.OAuthOnlyFields) {
		t.Fatal("effective configuration or credential scope changed")
	}
}

func TestLTSRootsDoNotImplicitlyMigrateLegacyLayout(t *testing.T) {
	for _, raw := range []string{"port: 8317\nflow-control: {enabled: false}\n", "api-request-body-max-bytes: 123456\n", "ampcode: {upstream-url: https://example.invalid}\n"} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if IsV8ConfigLayout(doc.Content[0]) {
			t.Fatal("LTS root misclassified a legacy layout")
		}
	}
}

func TestMixedConfigLoadsWithoutWritingReadOnlyFile(t *testing.T) {
	raw := []byte("request-retry: 9\nrouting: {retry: {request-retry: 0}}\napi-keys: [legacy]\naccess: {api-keys: []}\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0400); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.RequestRetry != 0 || len(cfg.APIKeys) != 0 {
		t.Fatalf("read-only mixed load: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(raw) {
		t.Fatal("read-only load modified layout")
	}
}

func TestV8LTSSharedStructExplicitEmptyWinsLegacy(t *testing.T) {
	for _, value := range []string{"{}", "null", "{mode: off, workspace-policy: passthrough}"} {
		raw := "codex: {client-metadata: {mode: strict, workspace-policy: drop}}\nupstream: {codex: {client-metadata: " + value + "}}\n"
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Codex.ClientMetadata.Mode == "strict" {
			t.Fatalf("canonical %s did not override legacy policy", value)
		}
	}
}

func TestV8EmptyOAuthContainerDoesNotEraseSharedLegacyPolicy(t *testing.T) {
	raw := []byte("codex: {client-metadata: {mode: strict, workspace-policy: drop}, response-steering: true}\noauth: {providers: {codex: {}}}\n")
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForAPIKey().Codex.ClientMetadata.Mode != "strict" || !cfg.ForAPIKey().Codex.ResponseSteering {
		t.Fatal("empty OAuth container erased shared legacy policy")
	}
	normalized, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = ParseConfigBytes(normalized)
	if err != nil || cfg.ForAPIKey().Codex.ClientMetadata.Mode != "strict" || !cfg.ForAPIKey().Codex.ResponseSteering {
		t.Fatal("migration erased shared policy", err)
	}
}
