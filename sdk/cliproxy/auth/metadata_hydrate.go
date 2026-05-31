package auth

import "strings"

// HydrateAuthFromMetadata applies typed fields mirrored in auth.Metadata.
// Store backends should call this after constructing an Auth from persisted
// metadata so runtime routing fields match the on-disk/source record.
func HydrateAuthFromMetadata(auth *Auth) {
	if auth == nil || len(auth.Metadata) == 0 {
		return
	}
	hydratePrefix(auth)
	ApplyCustomHeadersFromMetadata(auth)
	hydrateDisabled(auth)
}

func hydratePrefix(auth *Auth) {
	raw, ok := auth.Metadata["prefix"].(string)
	if !ok {
		return
	}
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return
	}
	auth.Prefix = trimmed
}

func hydrateDisabled(auth *Auth) {
	if disabled, ok := auth.Metadata["disabled"].(bool); ok && disabled {
		auth.Disabled = true
		auth.Status = StatusDisabled
	}
}
