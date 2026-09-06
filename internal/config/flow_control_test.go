package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

func TestFlowControlLegacyDefaultAndRoundtrip(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FlowControl.Enabled {
		t.Fatal("legacy config enabled flow control")
	}
	cfg, err = ParseConfigBytes([]byte(`port: 8317
flow-control:
  enabled: true
  rules:
    - id: km
      stage: request
      scope: key-model
      max-concurrent: 2
      windows:
        - requests: 10
          period-ms: 60000
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FlowControl.Rules) != 1 || cfg.FlowControl.Rules[0].Scope != "key-model" {
		t.Fatal("rule lost")
	}
	clone := cfg.CloneForRuntime()
	clone.FlowControl.Rules[0].Windows[0].Requests = 99
	if cfg.FlowControl.Rules[0].Windows[0].Requests != 10 {
		t.Fatal("config alias")
	}
}
func TestFlowControlRejectsHomeAndBadDraftOnlyWhenEnabled(t *testing.T) {
	cfg := Config{FlowControl: flowcontrol.Config{Enabled: true}}
	cfg.Home.Enabled = true
	if cfg.ValidateFlowControl() == nil {
		t.Fatal("Home double admission accepted")
	}
	cfg.FlowControl.Enabled = false
	cfg.FlowControl.Rules = []flowcontrol.Rule{{ID: "bad", Stage: "future"}}
	if err := cfg.ValidateFlowControl(); err != nil {
		t.Fatal(err)
	}
}

func TestFlowV3ConfigCollectionsAndObservationRoundtrip(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`port: 8317
flow-control:
  version: 3
  enabled: true
  observation:
    realtime: false
    resources: true
    interval-ms: 5000
  rules:
    - id: models
      stage: attempt
      scope: custom
      group-by: [key, account]
      models: [m1, m2]
      max-concurrent: 3
`))
	if err != nil {
		t.Fatal(err)
	}
	clone := cfg.CloneForRuntime()
	clone.FlowControl.Rules[0].Models[0] = "changed"
	if cfg.FlowControl.Rules[0].Models[0] != "m1" {
		t.Fatal("selector slice aliases clone")
	}
	if !cfg.FlowControl.Observation.Resources || cfg.FlowControl.Observation.Realtime || cfg.FlowControl.Version != 3 {
		t.Fatal("observation/version missing")
	}
	for _, bad := range []string{"models: []", "models: [\"*\"]", "model: m1\n      models: [m2]"} {
		_, err = ParseConfigBytes([]byte("port: 8317\nflow-control:\n  version: 3\n  enabled: true\n  rules:\n    - id: r\n      stage: attempt\n      scope: global\n      max-concurrent: 1\n      " + bad + "\n"))
		if err == nil {
			t.Fatal("invalid selector accepted", bad)
		}
	}
}

func TestFlowV3EmptySelectorYAMLRoundTripDoesNotWiden(t *testing.T) {
	input := "version: 3\nenabled: false\nrules:\n  - id: empty\n    stage: attempt\n    scope: custom\n    models: []\n    max-concurrent: 1\n"
	var cfg FlowControlConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored FlowControlConfig
	if err := yaml.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Rules[0].Models == nil {
		t.Fatal("empty set widened to all during YAML save")
	}
	restored.Enabled = true
	if err := restored.Validate(); err == nil {
		t.Fatal("invalid empty selection enabled")
	}
}
