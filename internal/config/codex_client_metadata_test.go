package config

import (
	"os"
	"path/filepath"
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
			name:       "invalid uses safe defaults",
			config:     CodexClientMetadataConfig{Mode: "unexpected", WorkspacePolicy: "unexpected"},
			wantMode:   CodexClientMetadataModeRepair,
			wantPolicy: CodexClientMetadataWorkspacePolicyPassthrough,
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
