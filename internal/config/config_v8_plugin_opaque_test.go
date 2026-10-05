package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func pluginRawYAML(t *testing.T, cfg *Config, id string) string {
	t.Helper()
	plugin, ok := cfg.Plugins.Configs[id]
	if !ok {
		t.Fatalf("plugin %q missing", id)
	}
	out, err := yaml.Marshal(&plugin.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	return string(out)
}

func TestPluginConfigsStayOpaqueToV8LayoutHelpers(t *testing.T) {
	layouts := map[string]string{
		"legacy": "port: 8317\n",
		"v8":     "config-version: 8\nserver:\n  port: 8317\n",
	}
	plugins := map[string]struct {
		body  string
		check func(t *testing.T, raw string)
	}{
		"composite-key": {
			body: "plugins:\n  configs:\n    sample:\n      enabled: false\n      table:\n        ? [a, b]\n        : pair\n",
			check: func(t *testing.T, raw string) {
				if !strings.Contains(raw, "pair") {
					t.Fatalf("composite key value lost:\n%s", raw)
				}
			},
		},
		"same-text-different-tag": {
			body: "plugins:\n  configs:\n    sample:\n      table:\n        1: numeric\n        \"1\": string\n",
			check: func(t *testing.T, raw string) {
				if !strings.Contains(raw, "numeric") || !strings.Contains(raw, "string") {
					t.Fatalf("typed keys collapsed:\n%s", raw)
				}
			},
		},
		"merge-typed-key": {
			body: "plugins:\n  configs:\n    sample:\n      base: &base {1: numeric}\n      table: {<<: *base, \"1\": string}\n",
			check: func(t *testing.T, raw string) {
				var decoded struct {
					Table map[any]string `yaml:"table"`
				}
				if err := yaml.Unmarshal([]byte(raw), &decoded); err != nil {
					t.Fatalf("raw not decodable: %v\n%s", err, raw)
				}
				if decoded.Table[1] != "numeric" || decoded.Table["1"] != "string" {
					t.Fatalf("merge lost typed key: %#v\n%s", decoded.Table, raw)
				}
			},
		},
	}
	for layoutName, layout := range layouts {
		for name, plugin := range plugins {
			t.Run(layoutName+"/"+name, func(t *testing.T) {
				data := []byte(layout + plugin.body)
				cfg, err := ParseConfigBytes(data)
				if err != nil {
					t.Fatalf("ParseConfigBytes: %v", err)
				}
				plugin.check(t, pluginRawYAML(t, cfg, "sample"))

				path := filepath.Join(t.TempDir(), "config.yaml")
				if err = os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadConfig(path)
				if err != nil {
					t.Fatalf("LoadConfig: %v", err)
				}
				plugin.check(t, pluginRawYAML(t, loaded, "sample"))

				normalized, _, err := NormalizeConfigLayout(data, true)
				if err != nil {
					t.Fatalf("NormalizeConfigLayout: %v", err)
				}
				reparsed, err := ParseConfigBytes(normalized)
				if err != nil {
					t.Fatalf("parse normalized: %v\n%s", err, normalized)
				}
				plugin.check(t, pluginRawYAML(t, reparsed, "sample"))
			})
		}
	}
}

func TestPluginConfigsExternalAnchorIsExpandedForSave(t *testing.T) {
	data := []byte("config-version: 8\nserver:\n  port: 8317\nshared: &shared {token-name: demo}\nplugins:\n  configs:\n    sample:\n      settings: *shared\n")
	normalized, _, err := NormalizeConfigLayout(data, true)
	if err != nil {
		// Unknown roots may be rejected by v8 validation elsewhere; the
		// normalization step itself must not leave a dangling alias.
		t.Fatalf("NormalizeConfigLayout: %v", err)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(normalized, &doc); err != nil {
		t.Fatalf("normalized YAML has dangling alias: %v\n%s", err, normalized)
	}
	if !strings.Contains(string(normalized), "token-name: demo") {
		t.Fatalf("external anchor not expanded:\n%s", normalized)
	}
}

func TestPluginConfigsRecursiveAliasRejected(t *testing.T) {
	data := []byte("plugins: &p\n  configs:\n    sample:\n      self: *p\n")
	if _, err := ParseConfigBytes(data); err == nil {
		t.Fatal("expected recursive plugin alias to be rejected")
	}
}

func TestMergeKeyEqualityUsesTag(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("base: &b {1: numeric}\ntable: {<<: *b, \"1\": string}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	expanded := expandYAMLAliases(doc.Content[0])
	table := yamlPath(expanded, "table")
	if table == nil || len(table.Content) != 4 {
		t.Fatalf("expected both typed keys after merge, got %#v", table)
	}
}
