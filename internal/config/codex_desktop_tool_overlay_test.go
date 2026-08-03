package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexDesktopToolOverlayDefaultsDisabled(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("{}"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.Codex.DesktopToolOverlay.Enabled {
		t.Fatal("desktop tool overlay enabled by default")
	}
	if len(cfg.Codex.DesktopToolOverlay.Tools) != 0 {
		t.Fatalf("default tools = %v, want empty", cfg.Codex.DesktopToolOverlay.Tools)
	}
}

func TestCodexDesktopToolOverlayNormalizesSelection(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
codex:
  desktop-tool-overlay:
    enabled: true
    tools:
      - " read_thread "
      - list_threads
      - read_thread
      - get_handoff_status
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	want := []string{"get_handoff_status", "list_threads", "read_thread"}
	if got := cfg.Codex.DesktopToolOverlay.Tools; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestCodexDesktopToolOverlayRejectsInvalidSelection(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "enabled empty",
			yaml: "codex:\n  desktop-tool-overlay:\n    enabled: true\n    tools: []\n",
		},
		{
			name: "enabled whitespace only",
			yaml: "codex:\n  desktop-tool-overlay:\n    enabled: true\n    tools: [\"  \"]\n",
		},
		{
			name: "unknown enabled",
			yaml: "codex:\n  desktop-tool-overlay:\n    enabled: true\n    tools: [unknown_tool]\n",
		},
		{
			name: "unknown disabled",
			yaml: "codex:\n  desktop-tool-overlay:\n    enabled: false\n    tools: [unknown_tool]\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(test.yaml)); err == nil {
				t.Fatal("ParseConfigBytes() accepted invalid overlay config")
			}
		})
	}
}

func TestLoadConfigOptionalCodexDesktopToolOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	payload := []byte(`
codex:
  desktop-tool-overlay:
    enabled: true
    tools: [wait_threads, read_thread]
`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	want := []string{"read_thread", "wait_threads"}
	if got := cfg.Codex.DesktopToolOverlay.Tools; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestLoadConfigOptionalRejectsInvalidOverlayBeforePersistingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "remote-management:\n  secret-key: plaintext-secret\ncodex:\n  desktop-tool-overlay:\n    enabled: true\n    tools: [unknown_tool]\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfigOptional(path, false); err == nil {
		t.Fatal("LoadConfigOptional() accepted invalid overlay config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatal("invalid overlay config triggered a persistence side effect")
	}
}

func TestCloneForRuntimeDeepCopiesCodexDesktopToolOverlay(t *testing.T) {
	cfg := &Config{Codex: CodexConfig{DesktopToolOverlay: CodexDesktopToolOverlayConfig{
		Enabled: true,
		Tools:   []string{"read_thread"},
	}}}
	clone := cfg.CloneForRuntime()
	clone.Codex.DesktopToolOverlay.Tools[0] = "list_threads"
	if got := cfg.Codex.DesktopToolOverlay.Tools[0]; got != "read_thread" {
		t.Fatalf("original tool = %q, want read_thread", got)
	}
}
