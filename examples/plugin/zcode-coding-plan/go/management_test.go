package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func managementRegistration(settings any) []byte {
	return encode(map[string]any{"schema_version": 6, "host_features": []string{"anthropic-plugin-responses-v1", "plugin-model-compat-v1", "http-disable-redirects-v1", "sensitive-endpoints-v1", "plugin-management-v1"}, "config_json": encode(settings)})
}
func TestManagementConfigAndReadiness(t *testing.T) {
	path := fixtureConfig(t)
	r := newRuntime(nil)
	settings := map[string]any{"config_file": path, "prompt_mode": "preserve", "allow_request_override": true}
	if _, err := r.dispatch("plugin.register", managementRegistration(settings)); err != nil {
		t.Fatal(err)
	}
	storage := []byte(`{"type":"zcode-coding-plan","label":"synthetic"}`)
	if _, err := r.dispatch("auth.parse", encode(map[string]any{"RawJSON": storage})); err != nil {
		t.Fatal("configured file reference rejected:", err)
	}
	c, err := r.configuration(storage)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Prompt.AllowRequestOverride {
		t.Fatal("management prompt override not applied")
	}
	status, err := r.dispatch("executor.readiness", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["Ready"] != false {
		t.Fatal("unscoped probe claimed auth ready")
	}
	status, err = r.dispatch("executor.readiness", encode(map[string]any{"StorageJSON": storage}))
	if err != nil || status.(map[string]any)["Ready"] != true {
		t.Fatal("configured auth not ready", err)
	}
	raw := string(encode(status))
	if containsAny(raw, []string{"synthetic-key", "synthetic-device", path}) {
		t.Fatal("diagnostic leaked private data")
	}
	settings["prompt_mode"] = "not-a-mode"
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(settings)); err == nil {
		t.Fatal("invalid update accepted")
	}
	preserved, err := r.configuration(storage)
	if err != nil || preserved != c {
		t.Fatal("invalid reconfiguration replaced working snapshot")
	}
	other := encode(authRecord{Type: provider, ConfigFile: filepath.Join(t.TempDir(), "other.json")})
	if _, err = r.configuration(other); err == nil {
		t.Fatal("conflicting config references accepted")
	}
}
func TestManagementReconfigureRejectsStaleAdmission(t *testing.T) {
	r := newRuntime(nil)
	settings := map[string]any{"config_file": fixtureConfig(t)}
	if _, err := r.dispatch("plugin.register", managementRegistration(settings)); err != nil {
		t.Fatal(err)
	}
	storage := []byte(`{"type":"zcode-coding-plan"}`)
	old, err := r.configuration(storage)
	if err != nil {
		t.Fatal(err)
	}
	settings["allow_request_override"] = true
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
		t.Fatal(err)
	}
	req := executorRequest{RequestID: "synthetic-request", CallbackID: "synthetic-callback", AuthID: "synthetic-auth"}
	if _, err = r.admit(req, old); err == nil || safeError(err).Code != "config_changed" {
		t.Fatal("stale configuration admitted", err)
	}
	current, err := r.configuration(storage)
	if err != nil || current == old || !current.Prompt.AllowRequestOverride {
		t.Fatal("new configuration not applied", err)
	}
	e, err := r.admit(req, current)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(e)
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{})); err == nil {
		t.Fatal("active configuration replaced")
	}
	preserved, err := r.configuration(storage)
	if err != nil || preserved != current {
		t.Fatal("busy reconfigure changed active snapshot")
	}
}

func containsAny(s string, values []string) bool {
	for _, v := range values {
		if len(v) > 0 && strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func TestManagementTemplateOverrideAndNoSecretWrite(t *testing.T) {
	path := fixtureConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	cfg["prompt"] = map[string]any{"mode": "preserve", "templates": map[string]string{"t": "template.txt"}}
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), "template.txt"), []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	raw = encode(cfg)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := newRuntime(nil)
	_, err = r.dispatch("plugin.register", managementRegistration(map[string]any{"config_file": path, "prompt_mode": "replace", "prompt_template": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.configuration([]byte(`{"type":"zcode-coding-plan"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := transformExecution(executorRequest{Payload: []byte(`{"model":"test-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`)}, c, "session")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encode(result["system"])), "template") {
		t.Fatal("template selection lost")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("management wrote private config")
	}
}
