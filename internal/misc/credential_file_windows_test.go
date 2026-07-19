//go:build windows

package misc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenCredentialFileRestrictsAccessToCurrentUser(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
	}{
		{name: "new file"},
		{name: "existing wide file", existing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential.json")
			if tt.existing {
				if err := os.WriteFile(path, []byte("sensitive"), 0o666); err != nil {
					t.Fatalf("write existing credential: %v", err)
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
			}

			file, err := OpenCredentialFile(path)
			if err != nil {
				t.Fatalf("OpenCredentialFile: %v", err)
			}
			defer file.Close()

			assertCredentialFileDACL(t, file)
			info, err := file.Stat()
			if err != nil {
				t.Fatalf("stat opened credential: %v", err)
			}
			if info.Size() != 0 {
				t.Fatalf("credential size = %d, want 0 after secure truncate", info.Size())
			}
			if _, err = file.Write([]byte("{}")); err != nil {
				t.Fatalf("write credential: %v", err)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatalf("stat credential: %v", err)
			}
		})
	}
	t.Run("long path", func(t *testing.T) {
		dir := t.TempDir()
		for len(filepath.Join(dir, "credential.json")) < 280 {
			dir = filepath.Join(dir, strings.Repeat("segment", 4))
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create long credential directory: %v", err)
		}
		path := filepath.Join(dir, "credential.json")
		file, err := OpenCredentialFile(path)
		if err != nil {
			t.Fatalf("OpenCredentialFile long path: %v", err)
		}
		defer file.Close()
		assertCredentialFileDACL(t, file)
		if _, err = file.Write([]byte("{}")); err != nil {
			t.Fatalf("write long-path credential: %v", err)
		}
	})
}

func assertCredentialFileDACL(t *testing.T, file *os.File) {
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
