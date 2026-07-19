//go:build !windows

package misc

import "os"

func openCredentialFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
}

func restrictCredentialFile(file *os.File) error {
	return file.Chmod(0o600)
}
