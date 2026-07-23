//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type lockedWindowsAuthStorage struct {
	removeErr    error
	renameDirErr error
}

func (s *lockedWindowsAuthStorage) SaveTokenToFile(path string) error {
	s.removeErr = os.Remove(path)
	dir := filepath.Dir(path)
	s.renameDirErr = os.Rename(dir, dir+"-moved")
	file, err := misc.OpenCredentialFile(path)
	if err != nil {
		return err
	}
	if _, err = file.Write([]byte(`{"type":"locked"}`)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func TestRenderAuthStorageLocksWindowsStagingPaths(t *testing.T) {
	storage := &lockedWindowsAuthStorage{}
	raw, wrote, err := renderAuthStorage(storage)
	if err != nil {
		t.Fatalf("render locked Windows auth storage: %v", err)
	}
	if !wrote || string(raw) != `{"type":"locked"}` {
		t.Fatalf("rendered locked auth = %q, wrote=%v", raw, wrote)
	}
	if storage.removeErr == nil {
		t.Fatal("pre-opened staging file could be removed while credentials were rendered")
	}
	if storage.renameDirErr == nil {
		t.Fatal("pre-opened staging directory could be renamed while credentials were rendered")
	}
}
