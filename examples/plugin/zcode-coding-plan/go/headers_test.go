package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestModelHeadersPreserveNativeSystemBetas(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "synthetic-key.synthetic-secret"
	incoming := http.Header{
		"Anthropic-Beta": {
			" mid-conversation-system-2026-04-07,mid-conversation-system-clear-at-2026-08-21 ",
			"mid-conversation-output-config-2026-07-01,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,inline-tools-2026-09-15,inline-tools-2026-09-15",
			"oauth-2025-04-20,unknown-beta,inline-tools-2026-09-15\r\nX-Injected: value",
		},
		"Authorization":     {"Bearer synthetic-caller-key"},
		"X-Api-Key":         {"synthetic-caller-key"},
		"X-App-Id":          {"synthetic-caller-app"},
		"User-Agent":        {"synthetic-caller-agent"},
		"Anthropic-Version": {"synthetic-caller-version"},
		"X-Client-Sig":      {"synthetic-caller-signature"},
	}
	before := incoming.Clone()
	got := modelHeaders(cfg, incoming)
	want := []string{
		"mid-conversation-system-2026-04-07",
		"mid-conversation-system-clear-at-2026-08-21",
		"mid-conversation-output-config-2026-07-01",
		"per-turn-control-2026-07-01",
		"mid-conversation-tool-changes-2026-07-01",
		"inline-tools-2026-09-15",
	}
	if !reflect.DeepEqual(strings.Split(got.Get("Anthropic-Beta"), ","), want) {
		t.Fatal("native system betas changed or unrelated beta leaked")
	}
	got.Del("Anthropic-Beta")
	identity := identityHeaders(cfg)
	identity.Del("Anthropic-Beta")
	if !reflect.DeepEqual(got, identity) {
		t.Fatal("caller headers changed the configured provider identity")
	}
	if !reflect.DeepEqual(incoming, before) {
		t.Fatal("caller headers were mutated")
	}
}

func TestModelHeadersKeepDefaultsWithoutNativeSystemBetas(t *testing.T) {
	cfg := defaultConfig()
	for _, incoming := range []http.Header{nil, {}, {"Anthropic-Beta": {"unknown-beta,oauth-2025-04-20"}}} {
		if !reflect.DeepEqual(modelHeaders(cfg, incoming), identityHeaders(cfg)) {
			t.Fatal("default provider headers changed")
		}
	}
}
