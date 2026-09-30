package main

import (
	"testing"
)

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
