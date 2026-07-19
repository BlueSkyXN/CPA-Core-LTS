package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestObjectTokenStoreRejectsAuthPathsOutsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "token.json")
	if err := os.WriteFile(outsidePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write outside auth: %v", err)
	}
	store := &ObjectTokenStore{authDir: authDir}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "absolute attribute path",
			call: func() error {
				_, err := store.resolveAuthPath(&cliproxyauth.Auth{
					ID:         "outside",
					Attributes: map[string]string{cliproxyauth.AttributePath: outsidePath},
				})
				return err
			},
		},
		{
			name: "filename traversal",
			call: func() error {
				_, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "traversal", FileName: "../token.json"})
				return err
			},
		},
		{
			name: "absolute delete path",
			call: func() error {
				_, err := store.resolveDeletePath(outsidePath)
				return err
			},
		},
		{name: "upload", call: func() error { return store.uploadAuth(context.Background(), outsidePath) }},
		{name: "remote delete", call: func() error { return store.deleteAuthObject(context.Background(), outsidePath) }},
		{name: "persist files", call: func() error { return store.PersistAuthFiles(context.Background(), "", outsidePath) }},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("accepted auth path outside auth directory")
			}
		})
	}
}

func TestObjectTokenStoreAcceptsNestedAuthPathInsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir}

	got, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "nested", FileName: "team/token"})
	if err != nil {
		t.Fatalf("resolveAuthPath returned error: %v", err)
	}
	want := filepath.Join(authDir, "team", "token.json")
	if got != want {
		t.Fatalf("resolveAuthPath = %q, want %q", got, want)
	}
}

func TestObjectTokenStoreRejectsSiblingPrefixAndSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	store := &ObjectTokenStore{authDir: authDir}

	siblingPath := filepath.Join(root, "auths-other", "token.json")
	if _, err := store.resolveDeletePath(siblingPath); err == nil {
		t.Fatal("accepted sibling path sharing the auth directory prefix")
	}

	outsideDir := t.TempDir()
	linkPath := filepath.Join(authDir, "escape")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "symlink", FileName: "escape/token.json"}); err == nil {
		t.Fatal("accepted auth path through symlink outside auth directory")
	}
}
