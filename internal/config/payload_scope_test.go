package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_PayloadModelScope(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
payload:
  override:
    - models:
        - name: "gpt-5.5-fast"
          scope: "requested"
        - name: "gpt-5.5"
      params:
        service_tier: "priority"
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.Payload.Override[0].Models[0].Scope; got != "requested" {
		t.Fatalf("first model scope = %q, want requested", got)
	}
	if got := cfg.Payload.Override[0].Models[1].Scope; got != "" {
		t.Fatalf("second model scope = %q, want empty default", got)
	}
}
