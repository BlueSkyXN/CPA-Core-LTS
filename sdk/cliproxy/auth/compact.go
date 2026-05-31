package auth

import (
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Compact mode values stored in Attributes["compact_mode"].
const (
	// CompactModeAuto follows the global compact-default when deciding eligibility.
	CompactModeAuto = "auto"
	// CompactModeForceOn always treats the credential as compact-capable.
	CompactModeForceOn = "force_on"
	// CompactModeForceOff always excludes the credential from compact routing.
	CompactModeForceOff = "force_off"
)

// NormalizeCompactMode maps arbitrary input to a known compact mode, defaulting to auto.
func NormalizeCompactMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case CompactModeForceOn:
		return CompactModeForceOn
	case CompactModeForceOff:
		return CompactModeForceOff
	default:
		return CompactModeAuto
	}
}

// ApplyCompactAttributes resolves the credential compact mode against the global default
// and stores both the normalized mode and resolved boolean on the auth.
func ApplyCompactAttributes(auth *Auth, mode string, defaultAllow bool) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	norm := NormalizeCompactMode(mode)
	auth.Attributes["compact_mode"] = norm

	allowed := defaultAllow
	switch norm {
	case CompactModeForceOn:
		allowed = true
	case CompactModeForceOff:
		allowed = false
	}
	if allowed {
		auth.Attributes["compact_allowed"] = "true"
		return
	}
	auth.Attributes["compact_allowed"] = "false"
}

func authCompactAllowed(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes == nil {
		return true
	}
	return auth.Attributes["compact_allowed"] != "false"
}

func requireCompactRequest(opts cliproxyexecutor.Options) bool {
	return opts.Alt == cliproxyexecutor.ResponsesCompactAlt
}

func compactCandidateAllowed(auth *Auth, requireCompact bool) bool {
	if !requireCompact {
		return true
	}
	return authCompactAllowed(auth)
}

func noCompactAuthError() *Error {
	return &Error{
		Code:       "compact_unsupported",
		Message:    "no available credential supports /responses/compact",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}
