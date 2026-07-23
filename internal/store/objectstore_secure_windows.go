//go:build windows

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

type windowsFileDispositionInformationEx struct {
	Flags uint32
}

// secureWriteAuthFile binds every traversal and the final replacement to
// directory handles. OBJ_DONT_REPARSE and FILE_OPEN_REPARSE_POINT prevent a
// junction or symlink swap from redirecting the write outside the auth root.
func secureWriteAuthFile(baseDir, relativePath string, data []byte) error {
	dirHandle, leaf, err := openSecureWindowsAuthParent(baseDir, relativePath, true)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(dirHandle) }()

	_, tempHandle, err := createSecureWindowsTempAt(dirHandle, leaf)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			_ = deleteWindowsFileByHandle(tempHandle)
		}
		_ = windows.CloseHandle(tempHandle)
	}()

	if err = writeWindowsHandle(tempHandle, data); err != nil {
		return fmt.Errorf("write auth temp file: %w", err)
	}
	if err = windows.FlushFileBuffers(tempHandle); err != nil {
		return fmt.Errorf("sync auth temp file: %w", err)
	}
	if err = renameWindowsFileAt(tempHandle, dirHandle, leaf); err != nil {
		return fmt.Errorf("install auth file: %w", err)
	}
	installed = true
	return nil
}

// secureReadAuthFile opens a regular final file beneath a no-reparse auth root.
func secureReadAuthFile(baseDir, relativePath string) ([]byte, fs.FileInfo, error) {
	dirHandle, leaf, err := openSecureWindowsAuthParent(baseDir, relativePath, false)
	if err != nil {
		return nil, nil, normalizeWindowsNotExist(err)
	}
	defer func() { _ = windows.CloseHandle(dirHandle) }()

	fileHandle, err := openSecureWindowsAuthFileAt(dirHandle, leaf, windows.FILE_GENERIC_READ)
	if err != nil {
		return nil, nil, fmt.Errorf("open auth file: %w", normalizeWindowsNotExist(err))
	}
	file := os.NewFile(uintptr(fileHandle), leaf)
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

// secureRemoveAuthFile opens the final path relative to a no-reparse parent
// handle, rejects reparse points, and deletes only through that file handle.
func secureRemoveAuthFile(baseDir, relativePath string) error {
	dirHandle, leaf, err := openSecureWindowsAuthParent(baseDir, relativePath, false)
	if err != nil {
		return normalizeWindowsNotExist(err)
	}
	defer func() { _ = windows.CloseHandle(dirHandle) }()

	fileHandle, err := openSecureWindowsAuthFileAt(dirHandle, leaf, windows.DELETE|windows.SYNCHRONIZE)
	if err != nil {
		return fmt.Errorf("open auth file for removal: %w", normalizeWindowsNotExist(err))
	}
	defer func() { _ = windows.CloseHandle(fileHandle) }()
	if err := deleteWindowsFileByHandle(fileHandle); err != nil {
		return fmt.Errorf("remove auth file: %w", err)
	}
	return nil
}

func validWindowsAuthPathComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		!strings.ContainsAny(component, `:/\\`)
}

func openSecureWindowsAuthParent(baseDir, relativePath string, create bool) (windows.Handle, string, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return 0, "", fmt.Errorf("resolve auth directory: %w", err)
	}
	parts, err := secureWindowsAuthPathParts(relativePath)
	if err != nil {
		return 0, "", err
	}
	dirHandle, err := openSecureWindowsRoot(absBase)
	if err != nil {
		return 0, "", err
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = windows.CloseHandle(dirHandle)
		}
	}()

	for _, component := range parts[:len(parts)-1] {
		var nextHandle windows.Handle
		if create {
			nextHandle, err = openOrCreateSecureWindowsDirectoryAt(dirHandle, component)
		} else {
			nextHandle, err = openSecureWindowsDirectory(dirHandle, component, windows.FILE_OPEN)
		}
		if err != nil {
			return 0, "", fmt.Errorf("open auth directory component %q: %w", component, err)
		}
		_ = windows.CloseHandle(dirHandle)
		dirHandle = nextHandle
	}
	keepOpen = true
	return dirHandle, parts[len(parts)-1], nil
}

func secureWindowsAuthPathParts(relativePath string) ([]string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return nil, fmt.Errorf("invalid auth relative path %q", relativePath)
	}
	clean := filepath.Clean(relativePath)
	if clean != relativePath || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid auth relative path %q", relativePath)
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	for _, component := range parts {
		if !validWindowsAuthPathComponent(component) {
			return nil, fmt.Errorf("invalid auth path component %q", component)
		}
	}
	return parts, nil
}

func openSecureWindowsRoot(path string) (windows.Handle, error) {
	fullPath, err := windows.FullPath(path)
	if err != nil {
		return 0, fmt.Errorf("resolve auth directory: %w", err)
	}
	handle, err := openSecureWindowsDirectory(0, windowsNTPath(fullPath), windows.FILE_OPEN)
	if err != nil {
		return 0, fmt.Errorf("open auth directory: %w", err)
	}
	return handle, nil
}

func openOrCreateSecureWindowsDirectoryAt(parent windows.Handle, component string) (windows.Handle, error) {
	handle, err := openSecureWindowsDirectory(parent, component, windows.FILE_OPEN)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) && !errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
		return 0, err
	}
	handle, err = openSecureWindowsDirectory(parent, component, windows.FILE_CREATE)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return openSecureWindowsDirectory(parent, component, windows.FILE_OPEN)
	}
	return handle, err
}

func openSecureWindowsDirectory(parent windows.Handle, name string, disposition uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	securityDescriptor, err := currentUserOnlySecurityDescriptor()
	if err != nil {
		return 0, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      parent,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: securityDescriptor,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocationSize int64
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		attributes,
		&status,
		&allocationSize,
		windows.FILE_ATTRIBUTE_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	runtime.KeepAlive(securityDescriptor)
	if err != nil {
		return 0, err
	}
	if err = rejectWindowsReparseHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func openSecureWindowsAuthFileAt(parent windows.Handle, leaf string, access uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(leaf)
	if err != nil {
		return 0, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocationSize int64
	err = windows.NtCreateFile(
		&handle,
		access|windows.FILE_READ_ATTRIBUTES,
		attributes,
		&status,
		&allocationSize,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return 0, err
	}
	if err = rejectWindowsReparseHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func createSecureWindowsTempAt(parent windows.Handle, _ string) (string, windows.Handle, error) {
	securityDescriptor, err := currentUserOnlySecurityDescriptor()
	if err != nil {
		return "", 0, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		var random [8]byte
		if _, err = rand.Read(random[:]); err != nil {
			return "", 0, fmt.Errorf("generate auth temp name: %w", err)
		}
		name := ".auth-tmp-" + hex.EncodeToString(random[:])
		objectName, errName := windows.NewNTUnicodeString(name)
		if errName != nil {
			return "", 0, errName
		}
		attributes := &windows.OBJECT_ATTRIBUTES{
			Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
			RootDirectory:      parent,
			ObjectName:         objectName,
			Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
			SecurityDescriptor: securityDescriptor,
		}
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		var allocationSize int64
		err = windows.NtCreateFile(
			&handle,
			windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.DELETE|windows.READ_CONTROL|windows.WRITE_DAC,
			attributes,
			&status,
			&allocationSize,
			windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_TEMPORARY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH,
			0,
			0,
		)
		runtime.KeepAlive(securityDescriptor)
		if err == nil {
			if err = rejectWindowsReparseHandle(handle); err != nil {
				_ = windows.CloseHandle(handle)
				return "", 0, fmt.Errorf("validate auth temp file: %w", err)
			}
			return name, handle, nil
		}
		if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			return "", 0, fmt.Errorf("create auth temp file: %w", err)
		}
	}
	return "", 0, fmt.Errorf("create auth temp file: exhausted unique names")
}

func currentUserOnlySecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current user SID: %w", err)
	}
	if user.User.Sid == nil || user.User.Sid.String() == "" {
		return nil, fmt.Errorf("get current user SID: empty SID")
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, fmt.Errorf("build auth file DACL: %w", err)
	}
	return descriptor, nil
}

func rejectWindowsReparseHandle(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("auth path contains a reparse point")
	}
	return nil
}

func writeWindowsHandle(handle windows.Handle, data []byte) error {
	for written := 0; written < len(data); {
		var count uint32
		if err := windows.WriteFile(handle, data[written:], &count, nil); err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		written += int(count)
	}
	return nil
}

func renameWindowsFileAt(fileHandle, parent windows.Handle, leaf string) error {
	fileName, err := windows.UTF16FromString(leaf)
	if err != nil {
		return err
	}
	if len(fileName)-1 > windows.MAX_LONG_PATH {
		return fmt.Errorf("auth file name is too long")
	}
	fileNameLength := (len(fileName) - 1) * 2
	var layout windowsFileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + fileNameLength
	buffer := make([]byte, bufferSize)
	info := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.RootDirectory = parent
	info.FileNameLength = uint32(fileNameLength)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:fileNameLength/2:fileNameLength/2], fileName[:len(fileName)-1])
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(fileHandle, &status, &buffer[0], uint32(bufferSize), windows.FileRenameInformation)
}

func normalizeWindowsNotExist(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.STATUS_NO_SUCH_FILE) ||
		errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) ||
		errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	}
	return err
}

func deleteWindowsFileByHandle(handle windows.Handle) error {
	info := windowsFileDispositionInformationEx{
		Flags: windows.FILE_DISPOSITION_DELETE |
			windows.FILE_DISPOSITION_POSIX_SEMANTICS |
			windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE,
	}
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		handle,
		&status,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		windows.FileDispositionInformationEx,
	)
}

func windowsNTPath(path string) string {
	path = strings.ReplaceAll(path, "/", `\`)
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\?\UNC\`)
	case strings.HasPrefix(path, `\\?\`):
		return `\??\` + strings.TrimPrefix(path, `\\?\`)
	case strings.HasPrefix(path, `\\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	default:
		return `\??\` + path
	}
}
