//go:build windows

package misc

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

func openCredentialFile(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(credentialFileWindowsPath(path))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_WRITE|windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: windows.ERROR_INVALID_HANDLE}
	}
	return file, nil
}

func credentialFileWindowsPath(path string) string {
	if len(path) >= 4 {
		if strings.HasPrefix(path, `\??\`) || (isWindowsPathSeparator(path[0]) && isWindowsPathSeparator(path[1]) && path[2] == '?' && isWindowsPathSeparator(path[3])) {
			return path
		}
		if isWindowsPathSeparator(path[0]) && isWindowsPathSeparator(path[1]) && path[2] == '.' && isWindowsPathSeparator(path[3]) {
			return path
		}
	}
	fullPath, err := windows.FullPath(path)
	if err != nil || len(fullPath) < 248 {
		return path
	}
	if len(fullPath) >= 2 && isWindowsPathSeparator(fullPath[0]) && isWindowsPathSeparator(fullPath[1]) {
		return `\\?\UNC\` + strings.TrimLeft(fullPath, `\/`)
	}
	return `\\?\` + fullPath
}

func isWindowsPathSeparator(char byte) bool {
	return char == '\\' || char == '/'
}

func restrictCredentialFile(file *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	sid := user.User.Sid
	if sid == nil || sid.String() == "" {
		return fmt.Errorf("get current user SID: empty SID")
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")")
	if err != nil {
		return fmt.Errorf("build credential DACL: %w", err)
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil {
		return fmt.Errorf("read credential DACL: %w", err)
	}
	err = windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(securityDescriptor)
	if err != nil {
		return fmt.Errorf("apply credential DACL: %w", err)
	}
	return nil
}
