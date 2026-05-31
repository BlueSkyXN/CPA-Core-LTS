package auth

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestNormalizeCompactMode(t *testing.T) {
	tests := map[string]string{
		"":              CompactModeAuto,
		" AUTO ":        CompactModeAuto,
		"force_on":      CompactModeForceOn,
		" FORCE_OFF ":   CompactModeForceOff,
		"unknown-value": CompactModeAuto,
	}
	for input, want := range tests {
		if got := NormalizeCompactMode(input); got != want {
			t.Fatalf("NormalizeCompactMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestApplyCompactAttributes(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		defaultAllow bool
		wantMode     string
		wantAllowed  string
	}{
		{name: "auto allow", mode: "auto", defaultAllow: true, wantMode: "auto", wantAllowed: "true"},
		{name: "auto deny", mode: "auto", defaultAllow: false, wantMode: "auto", wantAllowed: "false"},
		{name: "force on", mode: "force_on", defaultAllow: false, wantMode: "force_on", wantAllowed: "true"},
		{name: "force off", mode: "force_off", defaultAllow: true, wantMode: "force_off", wantAllowed: "false"},
		{name: "invalid", mode: "bad", defaultAllow: true, wantMode: "auto", wantAllowed: "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{}
			ApplyCompactAttributes(auth, tc.mode, tc.defaultAllow)
			if got := auth.Attributes["compact_mode"]; got != tc.wantMode {
				t.Fatalf("compact_mode = %q, want %q", got, tc.wantMode)
			}
			if got := auth.Attributes["compact_allowed"]; got != tc.wantAllowed {
				t.Fatalf("compact_allowed = %q, want %q", got, tc.wantAllowed)
			}
		})
	}
}

func TestCompactCandidateAllowed(t *testing.T) {
	if !authCompactAllowed(&Auth{ID: "true", Attributes: map[string]string{"compact_allowed": "true"}}) {
		t.Fatal("compact_allowed=true should allow compact")
	}
	if authCompactAllowed(&Auth{ID: "false", Attributes: map[string]string{"compact_allowed": "false"}}) {
		t.Fatal("compact_allowed=false should block compact")
	}
	if !requireCompactRequest(cliproxyexecutor.Options{Alt: cliproxyexecutor.ResponsesCompactAlt}) {
		t.Fatal("compact alt should require compact")
	}
	off := &Auth{ID: "off", Attributes: map[string]string{"compact_allowed": "false"}}
	if !compactCandidateAllowed(off, false) {
		t.Fatal("non-compact request must ignore compact_allowed")
	}
	if compactCandidateAllowed(off, true) {
		t.Fatal("compact request must honor compact_allowed=false")
	}
}

func TestNoCompactAuthError(t *testing.T) {
	err := noCompactAuthError()
	if err.Code != "compact_unsupported" {
		t.Fatalf("Code = %q, want compact_unsupported", err.Code)
	}
	if err.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want %d", err.StatusCode(), http.StatusServiceUnavailable)
	}
}
