//go:build !windows

package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCredentialFileRestrictsAccessToCurrentUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	if err := os.WriteFile(path, []byte("sensitive"), 0o644); err != nil {
		t.Fatalf("write existing credential: %v", err)
	}
	file, err := OpenCredentialFile(path)
	if err != nil {
		t.Fatalf("OpenCredentialFile: %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("close credential: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential permissions = %04o, want 0600", got)
	}
}
