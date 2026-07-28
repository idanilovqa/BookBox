package library

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"bookbox.local/server/internal/config"
)

var (
	ErrUnknownRoot = errors.New("unknown root")
	ErrPathEscape  = errors.New("path escapes root")
)

type ResolvedPath struct {
	RootID   string
	Relative string
	Absolute string
}

type canonicalRoot struct {
	path string
}

type Resolver struct {
	roots map[string]canonicalRoot
}

func NewResolver(roots []config.Root) (*Resolver, error) {
	resolver := &Resolver{roots: make(map[string]canonicalRoot, len(roots))}
	for _, root := range roots {
		if _, exists := resolver.roots[root.ID]; exists {
			return nil, fmt.Errorf("duplicate root id %q", root.ID)
		}
		canonical, err := filepath.EvalSymlinks(root.ContainerPath)
		if err != nil {
			return nil, fmt.Errorf("resolve root %q: %w", root.ID, err)
		}
		resolver.roots[root.ID] = canonicalRoot{path: canonical}
	}
	return resolver, nil
}

func (r *Resolver) Resolve(rootID, relative string) (ResolvedPath, error) {
	root, ok := r.roots[rootID]
	if !ok {
		return ResolvedPath{}, ErrUnknownRoot
	}

	if isAbsoluteOrForeignPath(relative) {
		return ResolvedPath{}, ErrPathEscape
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ResolvedPath{}, ErrPathEscape
	}

	candidate := filepath.Join(root.path, clean)
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return ResolvedPath{}, err
	}
	relativeToRoot, err := filepath.Rel(root.path, canonical)
	if err != nil || relativeToRoot == ".." ||
		strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return ResolvedPath{}, ErrPathEscape
	}
	if relativeToRoot == "." {
		relativeToRoot = ""
	}
	return ResolvedPath{
		RootID:   rootID,
		Relative: filepath.ToSlash(relativeToRoot),
		Absolute: canonical,
	}, nil
}

func isAbsoluteOrForeignPath(path string) bool {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.Contains(path, `\`) {
		return true
	}
	return len(path) >= 2 &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) &&
		path[1] == ':'
}
