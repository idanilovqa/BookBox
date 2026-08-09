//go:build windows

package workerlog

import (
	"errors"

	"golang.org/x/sys/windows"
)

func rejectReparsePoint(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrUnsafeTarget
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil
		}
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafeTarget
	}
	return nil
}
