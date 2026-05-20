package store

import cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"

func ensureDisabledMetadata(auth *cliproxyauth.Auth) map[string]any {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = auth.Disabled
	return auth.Metadata
}

func applyDisabledMetadata(auth *cliproxyauth.Auth, metadata map[string]any) {
	if auth == nil || metadata == nil {
		return
	}
	disabled, _ := metadata["disabled"].(bool)
	auth.Disabled = disabled
	if disabled {
		auth.Status = cliproxyauth.StatusDisabled
	}
}
