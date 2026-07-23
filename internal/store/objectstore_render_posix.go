//go:build !windows

package store

import (
	"fmt"
	"io"
	"os"
)

// renderAuthStorageIsolated captures legacy path-based TokenStorage output
// through an anonymous file descriptor. The provider sees only /dev/fd (or
// /proc/self/fd), so another process cannot replace a staging pathname with a
// symlink between validation and the credential write.
func prepareAuthRenderStaging(string, secureAuthRootIdentity) (string, secureAuthRootIdentity, error) {
	return "", secureAuthRootIdentity{}, nil
}

func renderAuthStorageIsolated(storage interface{ SaveTokenToFile(string) error }, _ string, _ secureAuthRootIdentity) ([]byte, bool, error) {
	file, err := os.CreateTemp("", "cpa-object-auth-*")
	if err != nil {
		return nil, false, fmt.Errorf("object store: create auth staging file: %w", err)
	}
	backingPath := file.Name()
	defer func() {
		_ = file.Close()
		if backingPath != "" {
			_ = os.Remove(backingPath)
		}
	}()
	if err = file.Chmod(0o600); err != nil {
		return nil, false, fmt.Errorf("object store: restrict auth staging file: %w", err)
	}
	if err = os.Remove(backingPath); err != nil {
		return nil, false, fmt.Errorf("object store: unlink auth staging file: %w", err)
	}
	backingPath = ""

	descriptorPath := ""
	for _, directory := range []string{"/dev/fd", "/proc/self/fd"} {
		if info, errStat := os.Stat(directory); errStat == nil && info.IsDir() {
			descriptorPath = fmt.Sprintf("%s/%d", directory, file.Fd())
			break
		}
	}
	if descriptorPath == "" {
		return nil, false, fmt.Errorf("object store: no file-descriptor path is available for auth rendering")
	}
	if err = storage.SaveTokenToFile(descriptorPath); err != nil {
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("object store: stat rendered auth file: %w", err)
	}
	if info.Size() == 0 {
		return nil, false, nil
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("object store: rewind rendered auth file: %w", err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, false, fmt.Errorf("object store: read rendered auth file: %w", err)
	}
	return raw, true, nil
}
