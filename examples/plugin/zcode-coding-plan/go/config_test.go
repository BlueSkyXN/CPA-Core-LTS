package main

import (
	"slices"
	"testing"
)

func TestAccountExtraModels(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        []string
		invalid     bool
	}{
		{name: "missing"},
		{name: "empty", field: `,"exmodel":[]`},
		{name: "null", field: `,"exmodel":null`},
		{name: "normalized", field: `,"exmodel":[" glm-5.3-highspeed ","glm-5.3-highspeed","glm-5.3-flashx"]`, want: []string{"glm-5.3-highspeed", "glm-5.3-flashx"}},
		{name: "custom", field: `,"exmodel":["custom-model"]`, want: []string{"custom-model"}},
		{name: "string", field: `,"exmodel":"glm-5.3-highspeed"`, invalid: true},
		{name: "object", field: `,"exmodel":{}`, invalid: true},
		{name: "number", field: `,"exmodel":[1]`, invalid: true},
		{name: "blank", field: `,"exmodel":[" "]`, invalid: true},
		{name: "null entry", field: `,"exmodel":[null]`, invalid: true},
		{name: "newline", field: `,"exmodel":["model\n"]`, invalid: true},
		{name: "placeholder", field: `,"exmodel":["<model>"]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseAuth([]byte(`{"type":"zcode-coding-plan","api_key":"synthetic.secret","device_id":"synthetic-device"` + tc.field + `}`))
			if (err != nil) != tc.invalid {
				t.Fatalf("parse error = %v, want invalid %v", err, tc.invalid)
			}
			if !tc.invalid && !slices.Equal(a.ExtraModels, tc.want) {
				t.Fatalf("exmodel = %v, want %v", a.ExtraModels, tc.want)
			}
		})
	}
}

func TestExtraModelsPreserveGlobalOverrides(t *testing.T) {
	c := defaultConfig()
	cfg, err := parseManagementConfig(encode(map[string]any{
		"models":       []string{"glm-5.3-flash", "glm-5.3-highspeed"},
		"model_limits": map[string]modelLimits{"glm-5.3-highspeed": {Context: 200000, Output: 16000}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.apply(c); err != nil {
		t.Fatal(err)
	}
	c.applyExtraModels([]string{"glm-5.3-highspeed", "glm-5.3-flashx"})
	if !slices.Equal(c.Models, []string{"glm-5.3-flash", "glm-5.3-highspeed", "glm-5.3-flashx"}) {
		t.Fatalf("merged models = %v", c.Models)
	}
	if limitsFor(c, "glm-5.3-highspeed").Output != 16000 || limitsFor(c, "glm-5.3-flashx").Output != 128000 {
		t.Fatal("exact model override or inherited template limits lost")
	}
}

func TestManagementConfigValidation(t *testing.T) {
	if _, err := parseManagementConfig(nil); err == nil {
		t.Fatal("empty config accepted")
	}
	unknown := "zai-mirror"
	if _, err := parseManagementConfig(encode(map[string]any{"upstream": unknown})); err == nil {
		t.Fatal("unknown upstream accepted")
	}
	if _, err := parseManagementConfig(encode(map[string]any{"models": []string{" "}})); err == nil {
		t.Fatal("blank model entry accepted")
	}
	if _, err := parseManagementConfig(encode(map[string]any{"model_limits": map[string]any{"m": map[string]any{"context": 100, "output": 200}}})); err == nil {
		t.Fatal("output above context accepted")
	}
	valid := encode(map[string]any{"host_logging_disabled": true, "upstream": "zai", "models": []string{"glm-5.3"}, "model_limits": map[string]any{"glm-5.3": map[string]any{"context": 1000000, "output": 128000}}})
	cfg, err := parseManagementConfig(valid)
	if err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	if err := cfg.apply(c); err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatal("upstream override did not switch the endpoint")
	}
	if len(c.Models) != 1 || c.Models[0] != "glm-5.3" {
		t.Fatal("model override not applied")
	}
	if !c.HostLoggingDisabled {
		t.Fatal("logging acknowledgement not applied")
	}
}
