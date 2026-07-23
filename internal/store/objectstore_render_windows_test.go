//go:build windows

package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"golang.org/x/sys/windows"
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
	spoolRoot := t.TempDir()
	spoolIdentity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot, spoolIdentity)
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
	spoolIdentity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
	stagingDir, _, err := prepareAuthRenderStaging(spoolRoot, spoolIdentity)
	if err != nil {
		t.Fatalf("prepare initial managed Windows auth staging: %v", err)
	}
	stalePath := filepath.Join(stagingDir, authRenderFilePrefix+"crash"+authRenderFileSuffix)
	if err = os.WriteFile(stalePath, []byte(`{"type":"stale"}`), 0o600); err != nil {
		t.Fatalf("write simulated crash residue: %v", err)
	}
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot, spoolIdentity)
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
	spoolIdentity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot, spoolIdentity)
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

func TestRenderAuthStorageLocksWindowsStagingInitializationRootReplacement(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (string, secureAuthRootIdentity, string)
	}{
		{
			name: "ordinary spool directory",
			setup: func(t *testing.T) (string, secureAuthRootIdentity, string) {
				parent := t.TempDir()
				spoolRoot := filepath.Join(parent, "spool")
				if err := os.Mkdir(spoolRoot, 0o700); err != nil {
					t.Fatalf("create initialized spool root: %v", err)
				}
				identity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
				replacementRoot := filepath.Join(parent, "replacement")
				residue := filepath.Join(replacementRoot, authRenderStagingDirName, authRenderFilePrefix+"outside"+authRenderFileSuffix)
				if err := os.MkdirAll(filepath.Dir(residue), 0o700); err != nil {
					t.Fatalf("create replacement staging root: %v", err)
				}
				if err := os.WriteFile(residue, []byte("outside"), 0o600); err != nil {
					t.Fatalf("write replacement staging residue: %v", err)
				}
				setWindowsPathWideDACL(t, filepath.Dir(residue))
				if err := os.Rename(spoolRoot, spoolRoot+"-original"); err != nil {
					t.Fatalf("move initialized spool root: %v", err)
				}
				if err := os.Rename(replacementRoot, spoolRoot); err != nil {
					t.Fatalf("install replacement spool root: %v", err)
				}
				return spoolRoot, identity, filepath.Join(spoolRoot, authRenderStagingDirName, filepath.Base(residue))
			},
		},
		{
			name: "ancestor junction",
			setup: func(t *testing.T) (string, secureAuthRootIdentity, string) {
				root := t.TempDir()
				ancestor := filepath.Join(root, "managed")
				spoolRoot := filepath.Join(ancestor, "spool")
				if err := os.MkdirAll(spoolRoot, 0o700); err != nil {
					t.Fatalf("create initialized spool root: %v", err)
				}
				identity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
				outsideAncestor := filepath.Join(root, "outside")
				residue := filepath.Join(outsideAncestor, "spool", authRenderStagingDirName, authRenderFilePrefix+"outside"+authRenderFileSuffix)
				if err := os.MkdirAll(filepath.Dir(residue), 0o700); err != nil {
					t.Fatalf("create outside staging root: %v", err)
				}
				if err := os.WriteFile(residue, []byte("outside"), 0o600); err != nil {
					t.Fatalf("write outside staging residue: %v", err)
				}
				setWindowsPathWideDACL(t, filepath.Dir(residue))
				if err := os.Rename(ancestor, ancestor+"-original"); err != nil {
					t.Fatalf("move initialized spool ancestor: %v", err)
				}
				if output, err := exec.Command("cmd", "/c", "mklink", "/J", ancestor, outsideAncestor).CombinedOutput(); err != nil {
					t.Skipf("mklink /J unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
				}
				return spoolRoot, identity, residue
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spoolRoot, identity, outsideResidue := test.setup(t)
			if _, _, err := prepareAuthRenderStaging(spoolRoot, identity); err == nil {
				t.Fatal("managed staging initialization accepted a replaced spool root")
			}
			if got, err := os.ReadFile(outsideResidue); err != nil || string(got) != "outside" {
				t.Fatalf("outside staging residue changed: data=%q err=%v", got, err)
			}
			assertWindowsPathDACLUnprotected(t, filepath.Dir(outsideResidue))
		})
	}
}

func setWindowsPathWideDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString("D:AI(A;;GA;;;WD)")
	if err != nil {
		t.Fatalf("build wide staging DACL: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read wide staging DACL: %v", err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("apply wide staging DACL: %v", err)
	}
}

func assertWindowsPathDACLUnprotected(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read staging DACL: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read staging DACL control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		t.Fatalf("outside staging DACL was unexpectedly protected: control=%#x", control)
	}
}
