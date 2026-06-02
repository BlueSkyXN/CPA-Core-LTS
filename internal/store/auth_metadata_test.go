package store

import (
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestEnsureDisabledMetadataInitializesAndStoresFlag(t *testing.T) {
	auth := &cliproxyauth.Auth{Disabled: true}

	meta := ensureDisabledMetadata(auth)
	if meta == nil {
		t.Fatal("ensureDisabledMetadata() returned nil metadata")
	}
	if disabled, _ := meta["disabled"].(bool); !disabled {
		t.Fatalf("disabled=%v, want true", meta["disabled"])
	}

	auth.Disabled = false
	meta = ensureDisabledMetadata(auth)
	if disabled, _ := meta["disabled"].(bool); disabled {
		t.Fatalf("disabled=%v, want false", meta["disabled"])
	}
}

func TestApplyDisabledMetadataMarksAuthDisabled(t *testing.T) {
	auth := &cliproxyauth.Auth{Status: cliproxyauth.StatusActive}

	applyDisabledMetadata(auth, map[string]any{"disabled": true})

	if !auth.Disabled {
		t.Fatal("Disabled=false, want true")
	}
	if auth.Status != cliproxyauth.StatusDisabled {
		t.Fatalf("Status=%q, want %q", auth.Status, cliproxyauth.StatusDisabled)
	}
}

func TestGitTokenStoreReadAuthFileUsesDisabledMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disabled.json")
	if err := os.WriteFile(path, []byte(`{"type":"test","email":"u@example.com","disabled":true}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := &GitTokenStore{}
	auth, err := store.readAuthFile(path, dir)
	if err != nil {
		t.Fatalf("readAuthFile() error: %v", err)
	}
	assertDisabledAuth(t, auth)
}

func TestObjectTokenStoreReadAuthFileUsesDisabledMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disabled.json")
	if err := os.WriteFile(path, []byte(`{"type":"test","email":"u@example.com","disabled":true}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := &ObjectTokenStore{}
	auth, err := store.readAuthFile(path, dir)
	if err != nil {
		t.Fatalf("readAuthFile() error: %v", err)
	}
	assertDisabledAuth(t, auth)
}

func assertDisabledAuth(t *testing.T, auth *cliproxyauth.Auth) {
	t.Helper()
	if auth == nil {
		t.Fatal("auth=nil, want auth")
	}
	if !auth.Disabled {
		t.Fatal("Disabled=false, want true")
	}
	if auth.Status != cliproxyauth.StatusDisabled {
		t.Fatalf("Status=%q, want %q", auth.Status, cliproxyauth.StatusDisabled)
	}
}
