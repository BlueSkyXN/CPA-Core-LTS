package misc

import "os"

// OpenCredentialFile opens a credential file for replacement and enforces
// owner-only permissions on both new and pre-existing files.
func OpenCredentialFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
