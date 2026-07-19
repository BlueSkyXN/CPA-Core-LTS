package misc

import "os"

// OpenCredentialFile opens a credential file for replacement and restricts it
// to the current OS user before truncating any pre-existing contents.
func OpenCredentialFile(path string) (*os.File, error) {
	file, err := openCredentialFile(path)
	if err != nil {
		return nil, err
	}
	if err = restrictCredentialFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
