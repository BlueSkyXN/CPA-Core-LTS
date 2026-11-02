package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func payloadScopeConfigYAML(layout, section, gates string) string {
	params := "      params: {service_tier: flex}\n"
	if strings.HasSuffix(section, "-raw") {
		params = "      params: {service_tier: '\"flex\"'}\n"
	} else if section == "filter" {
		params = "      params: [service_tier]\n"
	}
	source := "payload:\n  " + section + ":\n    - models:\n        - name: scope-model\n          protocol: codex\n" + gates + params
	if layout == "v8" {
		source = "requests:\n" + "  " + strings.ReplaceAll(strings.TrimSuffix(source, "\n"), "\n", "\n  ") + "\n"
	}
	return source
}

func TestPayloadExecutionScopeConfigRoundTrip(t *testing.T) {
	for _, layout := range []string{"legacy", "v8"} {
		for _, section := range []string{"default", "default-raw", "override", "override-raw", "filter"} {
			t.Run(layout+"/"+section, func(t *testing.T) {
				source := payloadScopeConfigYAML(layout, section, "          auth-kind: apikey\n          request-paths: [/v1/responses, /backend-api/codex/responses]\n")
				cfg, err := ParseConfigBytes([]byte(source))
				if err != nil {
					t.Fatal(err)
				}
				rule := payloadScopeModel(cfg, section)
				if rule.AuthKind != "apikey" || !reflect.DeepEqual(rule.RequestPaths, []string{"/v1/responses", "/backend-api/codex/responses"}) {
					t.Fatalf("lost execution scope: %+v", rule)
				}
				file := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(file, []byte("# keep-comment\n"+source), 0600); err != nil {
					t.Fatal(err)
				}
				cfg.Port = 8317
				if err := SaveConfigPreserveComments(file, cfg); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadConfigOptional(file, false)
				if err != nil {
					t.Fatal(err)
				}
				if got := payloadScopeModel(loaded, section); got.AuthKind != rule.AuthKind || !reflect.DeepEqual(got.RequestPaths, rule.RequestPaths) {
					t.Fatalf("save/reload changed execution scope: %+v", got)
				}
				saved, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(saved), "keep-comment") {
					t.Fatal("lost comment")
				}
				for _, format := range []string{"json", "yaml"} {
					var data []byte
					if format == "json" {
						data, err = json.Marshal(cfg)
					} else {
						data, err = yaml.Marshal(cfg)
					}
					if err != nil {
						t.Fatal(err)
					}
					var restored Config
					if format == "json" {
						err = json.Unmarshal(data, &restored)
					} else {
						err = yaml.Unmarshal(data, &restored)
					}
					if err != nil {
						t.Fatal(err)
					}
					if got := payloadScopeModel(&restored, section); got.AuthKind != rule.AuthKind || !reflect.DeepEqual(got.RequestPaths, rule.RequestPaths) {
						t.Fatalf("%s round trip changed payload scope", format)
					}
				}
			})
		}
	}
}

func TestPayloadExecutionScopeRejectsInvalidConfig(t *testing.T) {
	invalid := []string{
		"auth-kind: api-key", "auth-kind: unknown", "auth-kind: ' '",
		"request-paths: []", "request-paths: ['']", "request-paths: [v1/responses]",
		"request-paths: ['/v1/*']", "request-paths: ['/v1/responses?x=1']",
		"request-paths: ['/v1/responses#fragment']", "request-paths: ['/v1/../responses']",
		"request-paths: ['//host/responses']", "request-paths: ['/v1/%72esponses']",
		"request-paths: ['/v1/responses', '/v1/invalid path']",
	}
	for _, section := range []string{"default", "default-raw", "override", "override-raw", "filter"} {
		for _, gate := range invalid {
			t.Run(section+"/"+gate, func(t *testing.T) {
				source := payloadScopeConfigYAML("v8", section, "          "+gate+"\n")
				if _, err := ParseConfigBytes([]byte(source)); err == nil || !strings.Contains(err.Error(), "requests.payload."+section) {
					t.Fatalf("ParseConfigBytes error=%v", err)
				}
				file := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(file, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				for _, optional := range []bool{false, true} {
					if _, err := LoadConfigOptional(file, optional); err == nil {
						t.Fatalf("LoadConfigOptional(%v) accepted invalid scope", optional)
					}
				}
			})
		}
	}
}

func TestPayloadExecutionScopeOmittedKeepsLegacyRules(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(payloadScopeConfigYAML("legacy", "override", "")))
	if err != nil {
		t.Fatal(err)
	}
	rule := cfg.Payload.Override[0].Models[0]
	if rule.AuthKind != "" || rule.RequestPaths != nil {
		t.Fatalf("unexpected default scope: %+v", rule)
	}
	cfg.Payload.Override[0].Models[0].AuthKind = "oauth"
	if err := cfg.ValidatePayloadExecutionScopes(); err != nil {
		t.Fatal(err)
	}
}

func payloadScopeModel(cfg *Config, section string) PayloadModelRule {
	switch section {
	case "default":
		return cfg.Payload.Default[0].Models[0]
	case "default-raw":
		return cfg.Payload.DefaultRaw[0].Models[0]
	case "override":
		return cfg.Payload.Override[0].Models[0]
	case "override-raw":
		return cfg.Payload.OverrideRaw[0].Models[0]
	case "filter":
		return cfg.Payload.Filter[0].Models[0]
	default:
		panic("unknown test section")
	}
}
