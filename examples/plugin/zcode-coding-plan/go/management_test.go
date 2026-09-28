package main

import (
	"strings"
	"testing"
)

func managementRegistration(settings any) []byte {
	return encode(map[string]any{"schema_version": 6, "host_features": []string{"anthropic-plugin-responses-v1", "plugin-model-compat-v1", "http-disable-redirects-v1", "sensitive-endpoints-v1", "plugin-management-v1"}, "config_json": encode(settings)})
}

func TestManagementConfigAndReadiness(t *testing.T) {
	r := newRuntime(nil)
	ack := true
	upstream := "zai"
	settings := map[string]any{"host_logging_disabled": ack, "upstream": upstream}
	if _, err := r.dispatch("plugin.register", managementRegistration(settings)); err != nil {
		t.Fatal(err)
	}
	storage := inlineStorage()
	if _, err := r.dispatch("auth.parse", encode(map[string]any{"RawJSON": storage})); err != nil {
		t.Fatal("inline account rejected at parse:", err)
	}
	c, err := r.configuration(storage)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatal("upstream override not applied to effective config")
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
	if containsAny(raw, []string{"synthetic-key", "synthetic-secret", "synthetic-device"}) {
		t.Fatal("diagnostic leaked private data")
	}
	settings["upstream"] = "not-a-preset"
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(settings)); err == nil {
		t.Fatal("invalid update accepted")
	}
	preserved, err := r.configuration(storage)
	if err != nil || preserved != c {
		t.Fatal("invalid reconfiguration replaced working snapshot")
	}
	other := []byte(`{"type":"zcode-coding-plan","api_key":"other-key.other-secret","device_id":"other-device"}`)
	if _, err = r.configuration(other); err == nil {
		t.Fatal("second account accepted")
	}
}
func TestManagementReconfigureRejectsStaleAdmission(t *testing.T) {
	r := newRuntime(nil)
	ack := true
	if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": ack})); err != nil {
		t.Fatal(err)
	}
	storage := inlineStorage()
	old, err := r.configuration(storage)
	if err != nil {
		t.Fatal(err)
	}
	zai := "zai"
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": ack, "upstream": zai})); err != nil {
		t.Fatal(err)
	}
	req := executorRequest{RequestID: "synthetic-request", CallbackID: "synthetic-callback", AuthID: "synthetic-auth"}
	if _, err = r.admit(req, old); err == nil || safeError(err).Code != "config_changed" {
		t.Fatal("stale configuration admitted", err)
	}
	current, err := r.configuration(storage)
	if err != nil || current == old || current.Endpoint != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatal("new configuration not applied", err)
	}
	e, err := r.admit(req, current)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(e)
	if _, err = r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": ack})); err == nil {
		t.Fatal("active configuration replaced")
	}
	preserved, err := r.configuration(storage)
	if err != nil || preserved != current {
		t.Fatal("busy reconfigure changed active snapshot")
	}
}
func containsAny(text string, values []string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
