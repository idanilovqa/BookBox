package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrRootUnavailable = errors.New("library root is unavailable")
	ErrUnsupportedRoot = errors.New("library root is unsupported")
)

// ValidateLocalRoot verifies that path currently resolves to a supported local
// directory. Platform checks are applied to the canonical destination so a
// reparse point cannot disguise a remote root.
func ValidateLocalRoot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || isUNCPath(path) {
		return ErrUnsupportedRoot
	}
	info, err := os.Stat(path)
	if err != nil {
		return ErrRootUnavailable
	}
	if !info.IsDir() {
		return ErrUnsupportedRoot
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ErrRootUnavailable
	}
	if !filepath.IsAbs(canonical) || isUNCPath(canonical) {
		return ErrUnsupportedRoot
	}
	canonicalInfo, err := os.Stat(canonical)
	if err != nil {
		return ErrRootUnavailable
	}
	if !canonicalInfo.IsDir() {
		return ErrUnsupportedRoot
	}
	return validatePlatformLocalRoot(canonical)
}

func isUNCPath(path string) bool {
	cleaned := strings.ReplaceAll(path, `/`, `\`)
	return strings.HasPrefix(cleaned, `\\`)
}
