//go:build !windows

package store

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type descriptorAuthStorage struct {
	path string
}

func (s *descriptorAuthStorage) SaveTokenToFile(path string) error {
	s.path = path
	file, err := misc.OpenCredentialFile(path)
	if err != nil {
		return err
	}
	if _, err = file.Write([]byte(`{"type":"descriptor"}`)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func TestRenderAuthStorageUsesAnonymousDescriptorPath(t *testing.T) {
	storage := &descriptorAuthStorage{}
	raw, wrote, err := renderAuthStorage(storage, "", secureAuthRootIdentity{})
	if err != nil {
		t.Fatalf("render descriptor auth storage: %v", err)
	}
	if !wrote || string(raw) != `{"type":"descriptor"}` {
		t.Fatalf("rendered descriptor auth = %q, wrote=%v", raw, wrote)
	}
	if !strings.HasPrefix(storage.path, "/dev/fd/") && !strings.HasPrefix(storage.path, "/proc/self/fd/") {
		t.Fatalf("storage path = %q, want an anonymous descriptor alias", storage.path)
	}
}
