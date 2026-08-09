package workerlog

import (
	"errors"
	"os"
	"path/filepath"
)

func validateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	chain := []string{absolute}
	for current := absolute; ; {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		chain = append(chain, parent)
		current = parent
	}
	for index := len(chain) - 1; index >= 0; index-- {
		info, err := os.Lstat(chain[index])
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeTarget
		}
		if err := rejectReparsePoint(chain[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return rejectReparsePoint(path)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeTarget
	}
	return rejectReparsePoint(path)
}
