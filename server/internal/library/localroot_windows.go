//go:build windows

package library

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

var getDriveType = func(root string) uint32 {
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return windows.DRIVE_UNKNOWN
	}
	return windows.GetDriveType(pointer)
}

func validatePlatformLocalRoot(canonical string) error {
	volume := filepath.VolumeName(canonical)
	if volume == "" || isUNCPath(canonical) || isUNCPath(volume) {
		return ErrUnsupportedRoot
	}
	root := volume
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	switch getDriveType(root) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE:
		return nil
	default:
		return ErrUnsupportedRoot
	}
}
