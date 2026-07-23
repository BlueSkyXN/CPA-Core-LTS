//go:build windows

package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// renderAuthStorageIsolated captures legacy path-based TokenStorage output in
// a file whose parent and leaf are held open without FILE_SHARE_DELETE. The
// provider can open the same regular file for writing, but neither pathname can
// be replaced by a junction or reparse point while credential bytes are written.
func renderAuthStorageIsolated(storage interface{ SaveTokenToFile(string) error }) ([]byte, bool, error) {
	stagingDir, err := os.MkdirTemp("", "cpa-object-auth-*")
	if err != nil {
		return nil, false, fmt.Errorf("object store: create auth staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	directoryHandle, err := openLockedWindowsRenderDirectory(stagingDir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = windows.CloseHandle(directoryHandle) }()

	stagingPath := filepath.Join(stagingDir, "credential.json")
	fileHandle, err := createLockedWindowsRenderFile(stagingPath)
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fileHandle), stagingPath)
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, false, fmt.Errorf("object store: create auth staging file: invalid handle")
	}
	defer func() { _ = file.Close() }()

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
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, false, fmt.Errorf("object store: read rendered auth file: %w", err)
	}
	return raw, true, nil
}

func openLockedWindowsRenderDirectory(path string) (windows.Handle, error) {
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
	if err = restrictWindowsRenderHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func createLockedWindowsRenderFile(path string) (windows.Handle, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("object store: encode auth staging file: %w", err)
	}
	descriptor, err := currentUserOnlySecurityDescriptor()
	if err != nil {
		return 0, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return 0, fmt.Errorf("object store: create auth staging file: %w", err)
	}
	if err = rejectWindowsReparseHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fmt.Errorf("object store: validate auth staging file: %w", err)
	}
	return handle, nil
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
