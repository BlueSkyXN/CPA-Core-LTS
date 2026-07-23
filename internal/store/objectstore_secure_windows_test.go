//go:build windows

package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSecureAuthFileWindowsNestedLifecycle(t *testing.T) {
	baseDir := t.TempDir()
	root := mustCaptureSecureAuthRootIdentity(t, baseDir)
	credentialPath := filepath.Join("team", "token.json")
	siblingPath := filepath.Join("team", "sibling.json")

	if err := secureWriteAuthFile(baseDir, root, credentialPath, []byte("first")); err != nil {
		t.Fatalf("write nested auth file: %v", err)
	}
	data, info, err := secureReadAuthFile(baseDir, root, credentialPath)
	if err != nil {
		t.Fatalf("read nested auth file: %v", err)
	}
	if !bytes.Equal(data, []byte("first")) {
		t.Fatalf("nested auth file = %q, want %q", data, "first")
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("nested auth file mode = %v, want regular file", info.Mode())
	}

	if err = secureWriteAuthFile(baseDir, root, siblingPath, []byte("sibling")); err != nil {
		t.Fatalf("write sibling auth file: %v", err)
	}
	if err = secureWriteAuthFile(baseDir, root, credentialPath, []byte("replacement")); err != nil {
		t.Fatalf("replace nested auth file: %v", err)
	}
	data, _, err = secureReadAuthFile(baseDir, root, credentialPath)
	if err != nil {
		t.Fatalf("read replacement auth file: %v", err)
	}
	if !bytes.Equal(data, []byte("replacement")) {
		t.Fatalf("replacement auth file = %q, want %q", data, "replacement")
	}
	siblingData, _, err := secureReadAuthFile(baseDir, root, siblingPath)
	if err != nil {
		t.Fatalf("read sibling auth file: %v", err)
	}
	if !bytes.Equal(siblingData, []byte("sibling")) {
		t.Fatalf("sibling auth file = %q, want %q", siblingData, "sibling")
	}

	if err = secureRemoveAuthFile(baseDir, root, credentialPath); err != nil {
		t.Fatalf("remove nested auth file: %v", err)
	}
	if _, _, err = secureReadAuthFile(baseDir, root, credentialPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read removed auth file error = %v, want fs.ErrNotExist", err)
	}
	siblingData, _, err = secureReadAuthFile(baseDir, root, siblingPath)
	if err != nil {
		t.Fatalf("read sibling after removing replacement: %v", err)
	}
	if !bytes.Equal(siblingData, []byte("sibling")) {
		t.Fatalf("sibling after removing replacement = %q, want %q", siblingData, "sibling")
	}
}

func TestSecureAuthFileWindowsMissingLeaf(t *testing.T) {
	baseDir := t.TempDir()
	root := mustCaptureSecureAuthRootIdentity(t, baseDir)
	for _, relativePath := range []string{
		"missing.json",
		filepath.Join("missing-parent", "missing.json"),
	} {
		t.Run(relativePath, func(t *testing.T) {
			if _, _, err := secureReadAuthFile(baseDir, root, relativePath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("read missing auth file error = %v, want fs.ErrNotExist", err)
			}
			if err := secureRemoveAuthFile(baseDir, root, relativePath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("remove missing auth file error = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

func TestSecureAuthFileWindowsMissingInitializedRoot(t *testing.T) {
	parent := t.TempDir()
	baseDir := filepath.Join(parent, "auths")
	if err := os.Mkdir(baseDir, 0o700); err != nil {
		t.Fatalf("create initialized auth root: %v", err)
	}
	root := mustCaptureSecureAuthRootIdentity(t, baseDir)
	if err := os.Rename(baseDir, baseDir+"-moved"); err != nil {
		t.Fatalf("move initialized auth root: %v", err)
	}
	if _, _, err := secureReadAuthFile(baseDir, root, "missing.json"); !errors.Is(err, errObjectStoreAuthRootUnavailable) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read missing initialized root error = %v, want root unavailable only", err)
	}
	if err := secureRemoveAuthFile(baseDir, root, "missing.json"); !errors.Is(err, errObjectStoreAuthRootUnavailable) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove missing initialized root error = %v, want root unavailable only", err)
	}
}

func TestSecureAuthFileWindowsFinalDACL(t *testing.T) {
	baseDir := t.TempDir()
	root := mustCaptureSecureAuthRootIdentity(t, baseDir)
	relativePath := "token.json"
	path := filepath.Join(baseDir, relativePath)
	if err := os.WriteFile(path, []byte("existing"), 0o666); err != nil {
		t.Fatalf("write existing auth file: %v", err)
	}
	wideDescriptor, err := windows.SecurityDescriptorFromString("D:AI(A;;GA;;;WD)")
	if err != nil {
		t.Fatalf("build wide DACL: %v", err)
	}
	wideDACL, _, err := wideDescriptor.DACL()
	if err != nil {
		t.Fatalf("read wide DACL: %v", err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, wideDACL, nil); err != nil {
		t.Fatalf("apply wide DACL: %v", err)
	}

	if err = secureWriteAuthFile(baseDir, root, relativePath, []byte("replacement")); err != nil {
		t.Fatalf("replace auth file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open replacement auth file: %v", err)
	}
	defer file.Close()
	assertObjectStoreCurrentUserOnlyDACL(t, file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement auth file: %v", err)
	}
	if !bytes.Equal(data, []byte("replacement")) {
		t.Fatalf("replacement auth file = %q, want %q", data, "replacement")
	}
}

func TestSecureAuthFileWindowsRejectsDirectoryJunction(t *testing.T) {
	baseDir := t.TempDir()
	root := mustCaptureSecureAuthRootIdentity(t, baseDir)
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "escape.json")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside auth file: %v", err)
	}
	junctionPath := filepath.Join(baseDir, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junctionPath, outsideDir).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, bytes.TrimSpace(output))
	}

	relativePath := filepath.Join("junction", "escape.json")
	if err := secureWriteAuthFile(baseDir, root, relativePath, []byte("sensitive")); err == nil {
		t.Fatal("write through directory junction succeeded, want rejection")
	}
	if _, _, err := secureReadAuthFile(baseDir, root, relativePath); err == nil {
		t.Fatal("read through directory junction succeeded, want rejection")
	}
	if err := secureRemoveAuthFile(baseDir, root, relativePath); err == nil {
		t.Fatal("remove through directory junction succeeded, want rejection")
	}
	outsideData, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("read outside auth file after rejected operations: %v", err)
	}
	if !bytes.Equal(outsideData, []byte("outside")) {
		t.Fatalf("outside auth file = %q, want %q", outsideData, "outside")
	}
}

func TestSecureAuthFileWindowsRejectsInitializedRootReplacement(t *testing.T) {
	tests := []struct {
		name string
		run  func(string, secureAuthRootIdentity, string) error
	}{
		{
			name: "read",
			run: func(baseDir string, root secureAuthRootIdentity, relativePath string) error {
				data, _, err := secureReadAuthFile(baseDir, root, relativePath)
				if len(data) != 0 {
					t.Fatalf("secure read returned replacement-root data %q", data)
				}
				return err
			},
		},
		{
			name: "write",
			run: func(baseDir string, root secureAuthRootIdentity, relativePath string) error {
				return secureWriteAuthFile(baseDir, root, relativePath, []byte("sensitive"))
			},
		},
		{
			name: "delete",
			run: func(baseDir string, root secureAuthRootIdentity, relativePath string) error {
				return secureRemoveAuthFile(baseDir, root, relativePath)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			baseDir := filepath.Join(parent, "auths")
			originalDir := filepath.Join(parent, "auths-original")
			replacementDir := filepath.Join(parent, "auths-replacement")
			relativePath := filepath.Join("team", "token.json")
			if err := os.MkdirAll(filepath.Join(baseDir, "team"), 0o700); err != nil {
				t.Fatalf("create initialized auth root: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(replacementDir, "team"), 0o700); err != nil {
				t.Fatalf("create replacement auth root: %v", err)
			}
			if err := os.WriteFile(filepath.Join(baseDir, relativePath), []byte("inside"), 0o600); err != nil {
				t.Fatalf("write initialized auth fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(replacementDir, relativePath), []byte("outside"), 0o600); err != nil {
				t.Fatalf("write replacement auth fixture: %v", err)
			}
			root := mustCaptureSecureAuthRootIdentity(t, baseDir)
			if err := os.Rename(baseDir, originalDir); err != nil {
				t.Fatalf("move initialized auth root: %v", err)
			}
			if err := os.Rename(replacementDir, baseDir); err != nil {
				t.Fatalf("install replacement auth root: %v", err)
			}

			err := test.run(baseDir, root, relativePath)
			if err == nil {
				t.Fatal("secure auth operation accepted a replacement root identity")
			}
			if !errors.Is(err, errObjectStoreAuthRootChanged) {
				t.Fatalf("secure auth operation error = %v, want root identity change", err)
			}
			assertFileContents(t, filepath.Join(originalDir, relativePath), "inside")
			assertFileContents(t, filepath.Join(baseDir, relativePath), "outside")
		})
	}
}

func assertObjectStoreCurrentUserOnlyDACL(t *testing.T, file *os.File) {
	t.Helper()
	securityDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	control, _, err := securityDescriptor.Control()
	if err != nil {
		t.Fatalf("security descriptor control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL is not protected: control=%#x", control)
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("DACL ACE count = %v, want 1", dacl)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatalf("GetAce: %v", err)
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Fatalf("ACE = %#v, want access-allowed ACE", ace)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser: %v", err)
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(currentUser.User.Sid) {
		t.Fatalf("ACE SID = %s, want current user %s", aceSID.String(), currentUser.User.Sid.String())
	}
}
