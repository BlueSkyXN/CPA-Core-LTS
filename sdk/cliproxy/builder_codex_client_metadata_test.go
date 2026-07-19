package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuilderBuildRejectsInvalidCodexClientMetadata(t *testing.T) {
	builder := NewBuilder().WithConfig(&config.Config{Codex: config.CodexConfig{
		ClientMetadata: config.CodexClientMetadataConfig{WorkspacePolicy: "pass-through"},
	}}).WithConfigPath("config.yaml")

	service, err := builder.Build()
	if err == nil {
		t.Fatal("Build() accepted invalid Codex client metadata")
	}
	if service != nil {
		t.Fatalf("Build() service = %#v, want nil", service)
	}
	if !strings.Contains(err.Error(), "codex.client-metadata") {
		t.Fatalf("Build() error = %v, want client metadata context", err)
	}
}
