//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type lockedWindowsAuthStorage struct {
	removeErr    error
	renameDirErr error
	path         string
}

func (s *lockedWindowsAuthStorage) SaveTokenToFile(path string) error {
	s.path = path
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
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(t.TempDir())
	if err != nil {
		t.Fatalf("prepare managed Windows auth staging: %v", err)
	}
	storage := &lockedWindowsAuthStorage{}
	raw, wrote, err := renderAuthStorage(storage, stagingDir, stagingRoot)
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
	if filepath.Dir(storage.path) != stagingDir {
		t.Fatalf("auth staging path = %q, want managed directory %q", storage.path, stagingDir)
	}
	name := filepath.Base(storage.path)
	if !strings.HasPrefix(name, authRenderFilePrefix) || !strings.HasSuffix(name, authRenderFileSuffix) || strings.HasSuffix(strings.ToLower(name), ".json") {
		t.Fatalf("auth staging filename = %q, want non-JSON managed temp name", name)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("read managed auth staging after render: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("managed auth staging retained normal-path files: %v", entries)
	}
}

func TestRenderAuthStorageLocksWindowsStagingCleanup(t *testing.T) {
	spoolRoot := t.TempDir()
	stagingDir, _, err := prepareAuthRenderStaging(spoolRoot)
	if err != nil {
		t.Fatalf("prepare initial managed Windows auth staging: %v", err)
	}
	stalePath := filepath.Join(stagingDir, authRenderFilePrefix+"crash"+authRenderFileSuffix)
	if err = os.WriteFile(stalePath, []byte(`{"type":"stale"}`), 0o600); err != nil {
		t.Fatalf("write simulated crash residue: %v", err)
	}
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot)
	if err != nil {
		t.Fatalf("clean simulated crash residue: %v", err)
	}
	if _, err = os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("simulated crash residue still exists: %v", err)
	}

	if _, _, err = renderAuthStorage(&writeThenFailTokenStorage{}, stagingDir, stagingRoot); !errors.Is(err, errObjectStoreTestWrite) {
		t.Fatalf("render writer error = %v, want %v", err, errObjectStoreTestWrite)
	}
	if _, _, err = renderAuthStorage(&noOpTokenStorage{}, stagingDir, stagingRoot); err != nil {
		t.Fatalf("render no-op storage: %v", err)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("read managed auth staging after cleanup paths: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("managed auth staging retained error/no-op files: %v", entries)
	}

	directory, err := os.Open(stagingDir)
	if err != nil {
		t.Fatalf("open managed auth staging directory: %v", err)
	}
	defer directory.Close()
	assertObjectStoreCurrentUserOnlyDACL(t, directory)
}

func TestRenderAuthStorageLocksWindowsStagingRootReplacement(t *testing.T) {
	spoolRoot := t.TempDir()
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot)
	if err != nil {
		t.Fatalf("prepare managed Windows auth staging: %v", err)
	}
	originalDir := stagingDir + "-original"
	if err = os.Rename(stagingDir, originalDir); err != nil {
		t.Fatalf("move initialized auth staging directory: %v", err)
	}
	if err = os.Mkdir(stagingDir, 0o700); err != nil {
		t.Fatalf("install replacement auth staging directory: %v", err)
	}

	storage := &lockedWindowsAuthStorage{}
	if _, _, err = renderAuthStorage(storage, stagingDir, stagingRoot); !errors.Is(err, errObjectStoreAuthRootChanged) {
		t.Fatalf("render with replacement staging root error = %v, want root identity change", err)
	}
	if storage.path != "" {
		t.Fatalf("credential writer received replacement-root path %q", storage.path)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("read replacement auth staging directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement auth staging directory received files: %v", entries)
	}
}
