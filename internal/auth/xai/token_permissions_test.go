//go:build !windows

package xai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFileUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xai.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("precreate token file: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod token file: %v", err)
	}

	if err := (&TokenStorage{}).SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func TestSaveTokenToFilePersistsUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xai.json")
	storage := &TokenStorage{
		AccessToken:  "access-token-fixture",
		RefreshToken: "refresh-token-fixture",
		UserID:       "billing-user-fixture",
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode token file: %v", err)
	}
	if got := payload["user_id"]; got != "billing-user-fixture" {
		t.Fatalf("user_id = %#v, want billing-user-fixture", got)
	}
}
