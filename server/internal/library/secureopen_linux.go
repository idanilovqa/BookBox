//go:build linux

package library

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openRegularBeneath(rootPath string, relative string) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
	current, err := unix.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(current) }()

	rootRelative := strings.TrimPrefix(filepath.Clean(rootPath), string(filepath.Separator))
	directories := make([]string, 0)
	if rootRelative != "" && rootRelative != "." {
		directories = append(
			directories,
			strings.Split(rootRelative, string(filepath.Separator))...,
		)
	}
	parts := strings.Split(relative, "/")
	directories = append(directories, parts[:len(parts)-1]...)
	for _, component := range directories {
		next, openErr := unix.Openat(current, component, flags, 0)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(current)
		current = next
	}

	fileDescriptor, err := unix.Openat(
		current,
		parts[len(parts)-1],
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	handle := os.NewFile(uintptr(fileDescriptor), parts[len(parts)-1])
	if handle == nil {
		_ = unix.Close(fileDescriptor)
		return nil, os.ErrInvalid
	}
	return handle, nil
}
