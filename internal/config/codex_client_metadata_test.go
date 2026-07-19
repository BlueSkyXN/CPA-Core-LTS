package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexClientMetadataEffectiveDefaults(t *testing.T) {
	effective := (CodexClientMetadataConfig{}).Effective()
	if effective.Mode != CodexClientMetadataModeRepair {
		t.Fatalf("Mode = %q, want %q", effective.Mode, CodexClientMetadataModeRepair)
	}
	if effective.WorkspacePolicy != CodexClientMetadataWorkspacePolicyPassthrough {
		t.Fatalf("WorkspacePolicy = %q, want %q", effective.WorkspacePolicy, CodexClientMetadataWorkspacePolicyPassthrough)
	}
}

func TestCodexClientMetadataEffectiveNormalizesInvalidValues(t *testing.T) {
	tests := []struct {
		name       string
		config     CodexClientMetadataConfig
		wantMode   string
		wantPolicy string
	}{
		{
			name:       "explicit strict redact",
			config:     CodexClientMetadataConfig{Mode: " Strict ", WorkspacePolicy: " REDACT "},
			wantMode:   CodexClientMetadataModeStrict,
			wantPolicy: CodexClientMetadataWorkspacePolicyRedact,
		},
		{
			name:       "disabled remove aliases",
			config:     CodexClientMetadataConfig{Mode: "disabled", WorkspacePolicy: "remove"},
			wantMode:   CodexClientMetadataModeOff,
			wantPolicy: CodexClientMetadataWorkspacePolicyDrop,
		},
		{
			name:       "invalid fails closed",
			config:     CodexClientMetadataConfig{Mode: "unexpected", WorkspacePolicy: "unexpected"},
			wantMode:   CodexClientMetadataModeStrict,
			wantPolicy: CodexClientMetadataWorkspacePolicyDrop,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effective := test.config.Effective()
			if effective.Mode != test.wantMode || effective.WorkspacePolicy != test.wantPolicy {
				t.Fatalf("Effective() = %+v, want mode=%q workspace=%q", effective, test.wantMode, test.wantPolicy)
			}
		})
	}
}

func TestLoadConfigOptionalCodexClientMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
codex:
  client-metadata:
    mode: strict
    workspace-policy: drop
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	effective := cfg.Codex.ClientMetadata.Effective()
	if effective.Mode != CodexClientMetadataModeStrict || effective.WorkspacePolicy != CodexClientMetadataWorkspacePolicyDrop {
		t.Fatalf("client metadata = %+v", effective)
	}
}

func TestCodexClientMetadataValidateRejectsUnknownNonEmptyValues(t *testing.T) {
	tests := []struct {
		name   string
		config CodexClientMetadataConfig
	}{
		{name: "mode", config: CodexClientMetadataConfig{Mode: "repiar"}},
		{name: "workspace policy", config: CodexClientMetadataConfig{WorkspacePolicy: "pass-through"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if err == nil {
				t.Fatal("Validate() accepted unknown non-empty value")
			}
			if strings.Contains(err.Error(), "repiar") || strings.Contains(err.Error(), "pass-through") {
				t.Fatalf("Validate() echoed untrusted value: %v", err)
			}
		})
	}

	for _, valid := range []CodexClientMetadataConfig{
		{},
		{Mode: " disabled ", WorkspacePolicy: " remove "},
		{Mode: "STRICT", WorkspacePolicy: "REDACT"},
	} {
		if err := valid.Validate(); err != nil {
			t.Fatalf("Validate(%+v) error = %v", valid, err)
		}
	}
}

func TestLoadConfigOptionalRejectsInvalidCodexClientMetadataBeforePersistingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "remote-management:\n  secret-key: plaintext-secret\ncodex:\n  client-metadata:\n    mode: repiar\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfigOptional(path, false); err == nil {
		t.Fatal("LoadConfigOptional() accepted invalid client metadata mode")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original || !strings.Contains(string(data), "plaintext-secret") {
		t.Fatalf("invalid config triggered persistence side effect: %s", data)
	}
	if _, err := LoadConfigOptional(path, true); err == nil {
		t.Fatal("LoadConfigOptional(optional=true) accepted explicit invalid client metadata mode")
	}
}

func TestParseConfigBytesRejectsInvalidCodexClientMetadata(t *testing.T) {
	for _, payload := range []string{
		"codex:\n  client-metadata:\n    mode: repiar\n",
		"codex:\n  client-metadata:\n    workspace-policy: pass-through\n",
	} {
		if _, err := ParseConfigBytes([]byte(payload)); err == nil {
			t.Fatalf("ParseConfigBytes() accepted invalid payload: %s", payload)
		}
	}
}
