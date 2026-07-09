package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigYAMLAddsHostDefaultsToRawPluginConfig(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("config1: true\nconfig2: value\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if len(node.Content) != 1 {
		t.Fatalf("yaml node content length = %d, want 1", len(node.Content))
	}
	item := config.PluginInstanceConfig{
		Priority: 3,
		Raw:      *node.Content[0],
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"config1: true",
		"config2: value",
		"enabled: true",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigYAMLAddsSynthesizedPermissions(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 2,
		Permissions: config.PluginPermissions{
			AuthList:     true,
			ModelExecute: true,
		},
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"enabled: true",
		"priority: 2",
		"permissions:",
		"auth-list: true",
		"model-execute: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "auth-read:") || strings.Contains(got, "auth-write:") {
		t.Fatalf("runtimeConfigYAML() included false permissions:\n%s", got)
	}
}

func TestRuntimeConfigYAMLDefaultsEnabledTrue(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 3,
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"enabled: true",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigFromConfigDefaultsConfiguredPluginEnabled(t *testing.T) {
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Priority: 3,
				},
			},
		},
	}

	got := runtimeConfigFromConfig(cfg)
	item, ok := got.Items["alpha"]
	if !ok {
		t.Fatal("runtimeConfigFromConfig() missing alpha item")
	}
	if !item.Enabled {
		t.Fatal("runtimeConfigFromConfig() alpha enabled = false, want true")
	}
	if !strings.Contains(string(item.ConfigYAML), "enabled: true") {
		t.Fatalf("runtimeConfigFromConfig() alpha ConfigYAML missing enabled true:\n%s", item.ConfigYAML)
	}
}

func TestRuntimeConfigFromConfigExtractsStoreVersion(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  version: 1.0.3\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got := runtimeConfigFromConfig(cfg)
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

func TestRuntimeConfigFromConfigDerivesStoreVersionFromReleaseTag(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got := runtimeConfigFromConfig(cfg)
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}
