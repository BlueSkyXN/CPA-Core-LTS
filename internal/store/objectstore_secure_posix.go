//go:build !windows

package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// secureWriteAuthFile creates every path component relative to an opened auth
// root and installs the file with renameat. The root and its canonical ancestors
// are opened component by component without following symlinks.
func secureWriteAuthFile(baseDir, relativePath string, data []byte) error {
	return secureWriteAuthFileWithRootHook(baseDir, relativePath, data, nil)
}

func secureWriteAuthFileWithRootHook(baseDir, relativePath string, data []byte, afterRootSnapshot func()) error {
	dirFD, leaf, err := openSecureAuthParentWithRootHook(baseDir, relativePath, true, afterRootSnapshot)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()

	tempName, tempFD, err := createSecureTempAt(dirFD, leaf)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		_ = unix.Close(tempFD)
		if !installed {
			_ = unix.Unlinkat(dirFD, tempName, 0)
		}
	}()

	for written := 0; written < len(data); {
		n, errWrite := unix.Write(tempFD, data[written:])
		if errWrite != nil {
			return fmt.Errorf("write auth temp file: %w", errWrite)
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		written += n
	}
	if errChmod := unix.Fchmod(tempFD, 0o600); errChmod != nil {
		return fmt.Errorf("restrict auth temp file: %w", errChmod)
	}
	if errSync := unix.Fsync(tempFD); errSync != nil {
		return fmt.Errorf("sync auth temp file: %w", errSync)
	}
	if errClose := unix.Close(tempFD); errClose != nil {
		return fmt.Errorf("close auth temp file: %w", errClose)
	}
	tempFD = -1
	if errRename := unix.Renameat(dirFD, tempName, dirFD, leaf); errRename != nil {
		return fmt.Errorf("install auth file: %w", errRename)
	}
	installed = true
	if errSync := unix.Fsync(dirFD); errSync != nil {
		return fmt.Errorf("sync auth directory: %w", errSync)
	}
	return nil
}

// secureReadAuthFile opens each path component from an auth-root descriptor and
// reads a regular final file without following symlinks.
func secureReadAuthFile(baseDir, relativePath string) ([]byte, fs.FileInfo, error) {
	return secureReadAuthFileWithRootHook(baseDir, relativePath, nil)
}

func secureReadAuthFileWithRootHook(baseDir, relativePath string, afterRootSnapshot func()) ([]byte, fs.FileInfo, error) {
	dirFD, leaf, err := openSecureAuthParentWithRootHook(baseDir, relativePath, false, afterRootSnapshot)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()

	fd, err := unix.Openat(dirFD, leaf, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open auth file: %w", err)
	}
	file := os.NewFile(uintptr(fd), leaf)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat auth file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("auth file %q is not a regular file", relativePath)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, fmt.Errorf("read auth file: %w", err)
	}
	return data, info, nil
}

// secureRemoveAuthFile removes a final path relative to an auth-root
// descriptor. Parent traversal never follows symlinks.
func secureRemoveAuthFile(baseDir, relativePath string) error {
	return secureRemoveAuthFileWithRootHook(baseDir, relativePath, nil)
}

func secureRemoveAuthFileWithRootHook(baseDir, relativePath string, afterRootSnapshot func()) error {
	dirFD, leaf, err := openSecureAuthParentWithRootHook(baseDir, relativePath, false, afterRootSnapshot)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	if err := unix.Unlinkat(dirFD, leaf, 0); err != nil {
		return fmt.Errorf("remove auth file: %w", err)
	}
	return nil
}

func openSecureAuthParent(baseDir, relativePath string, create bool) (int, string, error) {
	return openSecureAuthParentWithRootHook(baseDir, relativePath, create, nil)
}

func openSecureAuthParentWithRootHook(baseDir, relativePath string, create bool, afterRootSnapshot func()) (int, string, error) {
	parts, err := secureAuthPathParts(relativePath)
	if err != nil {
		return -1, "", err
	}

	dirFD, err := openSecureAuthRootWithHook(baseDir, afterRootSnapshot)
	if err != nil {
		return -1, "", err
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = unix.Close(dirFD)
		}
	}()

	for _, component := range parts[:len(parts)-1] {
		nextFD, errOpen := unix.Openat(dirFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errOpen == unix.ENOENT && create {
			if errMkdir := unix.Mkdirat(dirFD, component, 0o700); errMkdir != nil && errMkdir != unix.EEXIST {
				return -1, "", fmt.Errorf("create auth directory component %q: %w", component, errMkdir)
			}
			nextFD, errOpen = unix.Openat(dirFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if errOpen != nil {
			return -1, "", fmt.Errorf("open auth directory component %q: %w", component, errOpen)
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	keepOpen = true
	return dirFD, parts[len(parts)-1], nil
}

func openSecureAuthRootWithHook(baseDir string, afterRootSnapshot func()) (int, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return -1, fmt.Errorf("resolve auth directory: %w", err)
	}

	// Resolve parent aliases such as macOS /var -> /private/var, but keep the
	// auth-root leaf unresolved so a root symlink is still rejected. The later
	// descriptor walk and identity check close swaps after this snapshot.
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absBase))
	if err != nil {
		return -1, fmt.Errorf("resolve auth directory parent: %w", err)
	}
	canonicalBase := filepath.Join(canonicalParent, filepath.Base(absBase))
	expectedInfo, err := os.Lstat(canonicalBase)
	if err != nil {
		return -1, fmt.Errorf("inspect auth directory: %w", err)
	}
	if !expectedInfo.IsDir() {
		return -1, fmt.Errorf("auth directory %q is not a directory", baseDir)
	}
	if afterRootSnapshot != nil {
		afterRootSnapshot()
	}

	components, err := secureAbsolutePathParts(canonicalBase)
	if err != nil {
		return -1, err
	}
	dirFD, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = unix.Close(dirFD)
		}
	}()

	for _, component := range components {
		nextFD, errOpen := unix.Openat(dirFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errOpen != nil {
			return -1, fmt.Errorf("open auth root component %q: %w", component, errOpen)
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}

	openedInfo, err := fileInfoForDescriptor(dirFD, canonicalBase)
	if err != nil {
		return -1, fmt.Errorf("inspect opened auth directory: %w", err)
	}
	if !os.SameFile(expectedInfo, openedInfo) {
		return -1, fmt.Errorf("auth directory changed while opening")
	}
	keepOpen = true
	return dirFD, nil
}

func secureAbsolutePathParts(path string) ([]string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("invalid absolute auth path %q", path)
	}
	if clean == string(os.PathSeparator) {
		return nil, nil
	}
	trimmed := strings.TrimPrefix(clean, string(os.PathSeparator))
	parts := strings.Split(trimmed, string(os.PathSeparator))
	for _, component := range parts {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("invalid auth root component %q", component)
		}
	}
	return parts, nil
}

func fileInfoForDescriptor(fd int, name string) (fs.FileInfo, error) {
	dupFD, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(dupFD)
	file := os.NewFile(uintptr(dupFD), name)
	if file == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("wrap auth directory descriptor")
	}
	defer func() { _ = file.Close() }()
	return file.Stat()
}

func secureAuthPathParts(relativePath string) ([]string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return nil, fmt.Errorf("invalid auth relative path %q", relativePath)
	}
	clean := filepath.Clean(relativePath)
	if clean != relativePath || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid auth relative path %q", relativePath)
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	for _, component := range parts {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("invalid auth path component %q", component)
		}
	}
	return parts, nil
}

func createSecureTempAt(dirFD int, _ string) (string, int, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, fmt.Errorf("generate auth temp name: %w", err)
		}
		name := ".auth-tmp-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if err != unix.EEXIST {
			return "", -1, fmt.Errorf("create auth temp file: %w", err)
		}
	}
	return "", -1, fmt.Errorf("create auth temp file: exhausted unique names")
}
