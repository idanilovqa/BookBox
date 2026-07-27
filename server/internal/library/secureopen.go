package library

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrNotRegular = errors.New("path is not a regular file")

// OpenRegular resolves a canonical source path and opens it without following
// any path-component symlink. The returned handle, rather than a later
// path-based open, is the authority for hashing and streaming.
func (r *Resolver) OpenRegular(
	rootID string,
	relative string,
) (*os.File, ResolvedPath, error) {
	root, exists := r.roots[rootID]
	if !exists {
		return nil, ResolvedPath{}, ErrUnknownRoot
	}
	if isAbsoluteOrForeignPath(relative) {
		return nil, ResolvedPath{}, ErrPathEscape
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.ToSlash(clean) != relative {
		return nil, ResolvedPath{}, ErrPathEscape
	}
	resolved, err := r.Resolve(rootID, relative)
	if err != nil {
		return nil, ResolvedPath{}, err
	}
	if resolved.Relative != relative {
		return nil, ResolvedPath{}, ErrPathEscape
	}
	handle, err := openRegularBeneath(root.path, relative)
	if err != nil {
		return nil, ResolvedPath{}, err
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, ResolvedPath{}, err
	}
	if !info.Mode().IsRegular() {
		_ = handle.Close()
		return nil, ResolvedPath{}, ErrNotRegular
	}
	return handle, resolved, nil
}
