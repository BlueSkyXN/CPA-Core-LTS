//go:build windows

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	authRenderStagingDirName = ".auth-render-staging"
	authRenderFilePrefix     = ".credential-"
	authRenderFileSuffix     = ".tmp"
)

func prepareAuthRenderStaging(spoolRoot string) (string, secureAuthRootIdentity, error) {
	stagingDir := filepath.Join(spoolRoot, authRenderStagingDirName)
	if err := os.Mkdir(stagingDir, 0o700); err != nil && !os.IsExist(err) {
		return "", secureAuthRootIdentity{}, fmt.Errorf("create managed auth staging directory: %w", err)
	}
	info, err := os.Lstat(stagingDir)
	if err != nil {
		return "", secureAuthRootIdentity{}, fmt.Errorf("inspect managed auth staging directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", secureAuthRootIdentity{}, fmt.Errorf("managed auth staging path is not a regular directory")
	}

	directoryHandle, err := openLockedWindowsRenderDirectory(stagingDir, secureAuthRootIdentity{})
	if err != nil {
		return "", secureAuthRootIdentity{}, err
	}
	directory := os.NewFile(uintptr(directoryHandle), stagingDir)
	if directory == nil {
		_ = windows.CloseHandle(directoryHandle)
		return "", secureAuthRootIdentity{}, fmt.Errorf("wrap managed auth staging directory handle")
	}
	defer func() { _ = directory.Close() }()

	identity, err := secureAuthRootIdentityForHandle(directoryHandle)
	if err != nil {
		return "", secureAuthRootIdentity{}, err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return "", secureAuthRootIdentity{}, fmt.Errorf("list managed auth staging directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, authRenderFilePrefix) || !strings.HasSuffix(name, authRenderFileSuffix) {
			return "", secureAuthRootIdentity{}, fmt.Errorf("managed auth staging directory contains unexpected entry %q", name)
		}
		fileHandle, errOpen := openSecureWindowsAuthFileAt(directoryHandle, name, windows.DELETE|windows.SYNCHRONIZE)
		if errOpen != nil {
			return "", secureAuthRootIdentity{}, fmt.Errorf("open stale auth staging file %q: %w", name, errOpen)
		}
		errDelete := deleteWindowsFileByHandle(fileHandle)
		errClose := windows.CloseHandle(fileHandle)
		if errDelete != nil {
			return "", secureAuthRootIdentity{}, fmt.Errorf("delete stale auth staging file %q: %w", name, errDelete)
		}
		if errClose != nil {
			return "", secureAuthRootIdentity{}, fmt.Errorf("close stale auth staging file %q: %w", name, errClose)
		}
	}
	return stagingDir, identity, nil
}

// renderAuthStorageIsolated captures legacy path-based TokenStorage output in
// a file whose parent and leaf are held open without FILE_SHARE_DELETE. The
// provider can open the same regular file for writing, but neither pathname can
// be replaced by a junction or reparse point while credential bytes are written.
func renderAuthStorageIsolated(storage interface{ SaveTokenToFile(string) error }, stagingDir string, stagingRoot secureAuthRootIdentity) (raw []byte, wrote bool, err error) {
	if strings.TrimSpace(stagingDir) == "" || !stagingRoot.valid {
		return nil, false, fmt.Errorf("object store: managed auth staging directory is not initialized")
	}
	directoryHandle, err := openLockedWindowsRenderDirectory(stagingDir, stagingRoot)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = windows.CloseHandle(directoryHandle) }()

	stagingName, fileHandle, err := createLockedWindowsRenderFileAt(directoryHandle)
	if err != nil {
		return nil, false, err
	}
	stagingPath := filepath.Join(stagingDir, stagingName)
	file := os.NewFile(uintptr(fileHandle), stagingPath)
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, false, fmt.Errorf("object store: create auth staging file: invalid handle")
	}
	fileIdentity, err := secureAuthRootIdentityForHandle(fileHandle)
	if err != nil {
		_ = file.Close()
		return nil, false, fmt.Errorf("object store: capture auth staging file identity: %w", err)
	}
	defer func() {
		closeErr := file.Close()
		removeErr := removeLockedWindowsRenderFileAt(directoryHandle, stagingName, fileIdentity)
		if closeErr != nil {
			closeErr = fmt.Errorf("object store: close auth staging file: %w", closeErr)
		}
		if removeErr != nil {
			removeErr = fmt.Errorf("object store: remove auth staging file: %w", removeErr)
		}
		if cleanupErr := errors.Join(closeErr, removeErr); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
			raw = nil
			wrote = false
		}
	}()

	if err = storage.SaveTokenToFile(stagingPath); err != nil {
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
	raw, err = io.ReadAll(file)
	if err != nil {
		return nil, false, fmt.Errorf("object store: read rendered auth file: %w", err)
	}
	return raw, true, nil
}

func removeLockedWindowsRenderFileAt(parent windows.Handle, name string, expected secureAuthRootIdentity) error {
	handle, err := openSecureWindowsAuthFileAt(parent, name, windows.DELETE|windows.SYNCHRONIZE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	actual, err := secureAuthRootIdentityForHandle(handle)
	if err != nil {
		return err
	}
	if !expected.valid || actual.volumeSerial != expected.volumeSerial || actual.fileIndex != expected.fileIndex {
		return fmt.Errorf("auth staging file identity changed before cleanup")
	}
	return deleteWindowsFileByHandle(handle)
}

func openLockedWindowsRenderDirectory(path string, expectedRoot secureAuthRootIdentity) (windows.Handle, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("object store: encode auth staging directory: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("object store: lock auth staging directory: %w", err)
	}
	if err = rejectWindowsReparseHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fmt.Errorf("object store: validate auth staging directory: %w", err)
	}
	if expectedRoot.valid {
		if err = validateSecureAuthRootIdentity(handle, expectedRoot); err != nil {
			_ = windows.CloseHandle(handle)
			return 0, fmt.Errorf("object store: validate managed auth staging identity: %w", err)
		}
	}
	if err = restrictWindowsRenderHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func createLockedWindowsRenderFileAt(parent windows.Handle) (string, windows.Handle, error) {
	descriptor, err := currentUserOnlySecurityDescriptor()
	if err != nil {
		return "", 0, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		var random [8]byte
		if _, err = rand.Read(random[:]); err != nil {
			return "", 0, fmt.Errorf("object store: generate auth staging file name: %w", err)
		}
		name := authRenderFilePrefix + hex.EncodeToString(random[:]) + authRenderFileSuffix
		objectName, errName := windows.NewNTUnicodeString(name)
		if errName != nil {
			return "", 0, fmt.Errorf("object store: encode auth staging file name: %w", errName)
		}
		attributes := &windows.OBJECT_ATTRIBUTES{
			Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
			RootDirectory:      parent,
			ObjectName:         objectName,
			Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
			SecurityDescriptor: descriptor,
		}
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		var allocationSize int64
		err = windows.NtCreateFile(
			&handle,
			windows.FILE_GENERIC_READ|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC,
			attributes,
			&status,
			&allocationSize,
			windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_TEMPORARY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			0,
			0,
		)
		runtime.KeepAlive(descriptor)
		if err == nil {
			if err = rejectWindowsReparseHandle(handle); err != nil {
				_ = windows.CloseHandle(handle)
				return "", 0, fmt.Errorf("object store: validate auth staging file: %w", err)
			}
			return name, handle, nil
		}
		if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			return "", 0, fmt.Errorf("object store: create auth staging file: %w", err)
		}
	}
	return "", 0, fmt.Errorf("object store: create auth staging file: exhausted unique names")
}

func restrictWindowsRenderHandle(handle windows.Handle) error {
	descriptor, err := currentUserOnlySecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("object store: read auth staging DACL: %w", err)
	}
	err = windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return fmt.Errorf("object store: restrict auth staging path: %w", err)
	}
	return nil
}
