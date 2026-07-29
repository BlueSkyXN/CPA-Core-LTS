package store

import (
	"context"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTokenStoreListReturnsAuthFileErrors(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "main",
		testBranchSpec{name: "main", contents: "remote default branch\n"},
	)
	authDir := filepath.Join(root, "workspace", "auths")
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(authDir)

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "valid.json"), []byte(`{"type":"custom"}`), 0o600); err != nil {
		t.Fatalf("write valid auth: %v", err)
	}
	brokenPath := filepath.Join(authDir, "broken.json")
	if err := os.WriteFile(brokenPath, []byte(`{"type":`), 0o600); err != nil {
		t.Fatalf("write broken auth: %v", err)
	}

	entries, err := store.List(context.Background())
	if err == nil {
		t.Fatal("List succeeded, want error for broken auth file")
	}
	if entries != nil {
		t.Fatalf("entries = %#v, want nil on error", entries)
	}
	if !strings.Contains(err.Error(), brokenPath) {
		t.Fatalf("error = %q, want broken file path", err.Error())
	}
}
