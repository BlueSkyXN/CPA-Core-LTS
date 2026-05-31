package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_OpenAICompatibilityImageModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
openai-compatibility:
  - name: image-provider
    base-url: https://example.invalid/v1
    api-key-entries:
      - api-key: test-key
    models:
      - name: upstream-image-model
        alias: image-alias
        image: true
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 || len(cfg.OpenAICompatibility[0].Models) != 1 {
		t.Fatalf("unexpected openai-compatibility models: %+v", cfg.OpenAICompatibility)
	}
	if !cfg.OpenAICompatibility[0].Models[0].Image {
		t.Fatalf("expected image flag to load as true")
	}
}

func TestLoadConfig_GPTImage2BaseModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
gpt-image-2-base-model: gpt-5.5-mini
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.GPTImage2BaseModel != "gpt-5.5-mini" {
		t.Fatalf("GPTImage2BaseModel = %q, want gpt-5.5-mini", cfg.GPTImage2BaseModel)
	}
}
