package auth

import "strings"

// NormalizeAuthPrefix applies the same single-segment prefix rules used by
// auth-file synthesis.
func NormalizeAuthPrefix(raw string) string {
	trimmed := strings.Trim(strings.TrimSpace(raw), "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

// HydrateAuthFromMetadata applies typed runtime fields mirrored in Metadata.
// Store backends call this after constructing an Auth from persisted JSON.
func HydrateAuthFromMetadata(auth *Auth) {
	if auth == nil || len(auth.Metadata) == 0 {
		return
	}
	if rawPrefix, ok := auth.Metadata["prefix"].(string); ok {
		auth.Prefix = NormalizeAuthPrefix(rawPrefix)
	}
	ApplyCustomHeadersFromMetadata(auth)
	if disabled, ok := auth.Metadata["disabled"].(bool); ok && disabled {
		auth.Disabled = true
		auth.Status = StatusDisabled
	}
}
