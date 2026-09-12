package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type testTokenStorage struct {
	meta map[string]any
}

func (s *testTokenStorage) SetMetadata(meta map[string]any) { s.meta = meta }

func (s *testTokenStorage) SaveTokenToFile(authFilePath string) error {
	raw, err := json.Marshal(s.meta)
	if err != nil {
		return err
	}
	return os.WriteFile(authFilePath, raw, 0o600)
}

func TestFileTokenStore_Save_DisabledPersistsFlagForTokenStorage(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "disabled.json")

	if err := os.WriteFile(path, []byte(`{"type":"test","disabled":true}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	storage := &testTokenStorage{}

	auth := &cliproxyauth.Auth{
		ID:       "disabled.json",
		Provider: "test",
		FileName: "disabled.json",
		Disabled: true,
		Storage:  storage,
		Metadata: map[string]any{"type": "test"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal auth file: %v", err)
	}
	if disabled, _ := meta["disabled"].(bool); !disabled {
		t.Fatalf("disabled=%v, want true (raw=%s)", meta["disabled"], string(raw))
	}
}

func TestFileTokenStoreListHydratesMetadataFields(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "team.json")
	raw := []byte(`{
		"type":"claude",
		"prefix":" /team/ ",
		"disabled":true,
		"headers":{"X-Team":"blue"}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("List() returned %d auths, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Prefix != "team" {
		t.Fatalf("Prefix = %q, want team", auth.Prefix)
	}
	if !auth.Disabled || auth.Status != cliproxyauth.StatusDisabled {
		t.Fatalf("Disabled/Status = %v/%q, want true/%q", auth.Disabled, auth.Status, cliproxyauth.StatusDisabled)
	}
	if got := auth.Attributes["header:X-Team"]; got != "blue" {
		t.Fatalf("header:X-Team = %q, want blue", got)
	}
}

func TestFileTokenStore_Save_DisabledLoginCreatesCanonicalFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "canonical-disabled.json")
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:       "canonical-disabled.json",
		Provider: "test",
		FileName: "canonical-disabled.json",
		Disabled: true,
		Storage:  &testTokenStorage{},
		Metadata: map[string]any{"type": "test"},
	}

	ctx := cliproxyauth.WithAuthCreationIntent(context.Background())
	savedPath, err := store.Save(ctx, auth)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if savedPath != path {
		t.Fatalf("Save() path = %q, want %q", savedPath, path)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read canonical disabled credential: %v", errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatalf("unmarshal canonical disabled credential: %v", errUnmarshal)
	}
	if disabled, _ := metadata["disabled"].(bool); !disabled {
		t.Fatalf("disabled = %v, want true", metadata["disabled"])
	}
}

func TestFileTokenStore_Save_DisabledStorageBackedRuntimeDoesNotRecreateMissingFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "removed-disabled.json")
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:       "removed-disabled.json",
		Provider: "test",
		FileName: "removed-disabled.json",
		Disabled: true,
		Storage:  &testTokenStorage{},
		Metadata: map[string]any{"type": "test"},
	}

	savedPath, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if savedPath != "" {
		t.Fatalf("Save() path = %q, want empty", savedPath)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("removed credential was recreated or stat failed: %v", errStat)
	}
}
