package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrDestinationExists = errors.New("destination already exists")
var ErrFingerprintChanged = errors.New("source fingerprint changed")

func (r *Resolver) ProbeRootWritable(rootID string) error {
	root, ok := r.roots[rootID]
	if !ok {
		return ErrUnknownRoot
	}
	probe, err := os.CreateTemp(root.path, ".bookbox-write-probe-")
	if err != nil {
		return err
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func (r *Resolver) ValidateMove(rootID, source, destination string, size int64, modifiedAt time.Time) error {
	handle, _, err := r.OpenRegular(rootID, source)
	if err != nil {
		return ErrFingerprintChanged
	}
	info, statErr := handle.Stat()
	closeErr := handle.Close()
	if statErr != nil || closeErr != nil || info.Size() != size ||
		!info.ModTime().UTC().Truncate(time.Microsecond).Equal(modifiedAt.UTC().Truncate(time.Microsecond)) {
		return ErrFingerprintChanged
	}
	root, destinationAbsolute, err := r.destinationPath(rootID, destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destinationAbsolute); err == nil {
		return ErrDestinationExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrDestinationExists
	}
	return validateExistingParents(root.path, filepath.Dir(destinationAbsolute))
}

func (r *Resolver) MoveConfined(rootID, source, destination string, size int64, modifiedAt time.Time) error {
	if err := r.ValidateMove(rootID, source, destination, size, modifiedAt); err != nil {
		return err
	}
	sourceResolved, err := r.Resolve(rootID, source)
	if err != nil {
		return ErrFingerprintChanged
	}
	root, destinationAbsolute, err := r.destinationPath(rootID, destination)
	if err != nil {
		return err
	}
	if err := makeDirectoriesConfined(root.path, filepath.Dir(destinationAbsolute)); err != nil {
		return err
	}
	if err := r.ValidateMove(rootID, source, destination, size, modifiedAt); err != nil {
		return err
	}
	if err := os.Rename(sourceResolved.Absolute, destinationAbsolute); err != nil {
		return fmt.Errorf("move file: %w", err)
	}
	destinationHandle, _, err := r.OpenRegular(rootID, destination)
	if err != nil {
		_ = os.Rename(destinationAbsolute, sourceResolved.Absolute)
		return ErrFingerprintChanged
	}
	destinationInfo, statErr := destinationHandle.Stat()
	closeErr := destinationHandle.Close()
	if statErr != nil || closeErr != nil || destinationInfo.Size() != size ||
		!destinationInfo.ModTime().UTC().Truncate(time.Microsecond).Equal(modifiedAt.UTC().Truncate(time.Microsecond)) {
		_ = os.Rename(destinationAbsolute, sourceResolved.Absolute)
		return ErrFingerprintChanged
	}
	return nil
}

func (r *Resolver) destinationPath(rootID, relative string) (canonicalRoot, string, error) {
	root, ok := r.roots[rootID]
	if !ok {
		return canonicalRoot{}, "", ErrUnknownRoot
	}
	if relative == "" || isAbsoluteOrForeignPath(relative) {
		return canonicalRoot{}, "", ErrPathEscape
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.ToSlash(clean) != relative || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return canonicalRoot{}, "", ErrPathEscape
	}
	destination := filepath.Join(root.path, clean)
	relativeToRoot, err := filepath.Rel(root.path, destination)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return canonicalRoot{}, "", ErrPathEscape
	}
	return root, destination, nil
}

func validateExistingParents(root, directory string) error {
	for current := directory; current != root; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrPathEscape
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrPathEscape
		}
	}
	return nil
}

func makeDirectoriesConfined(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrPathEscape
	}
	current := root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create destination directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrPathEscape
		}
	}
	return nil
}
