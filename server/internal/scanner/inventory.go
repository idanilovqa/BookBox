package scanner

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
	"github.com/google/uuid"
)

// Scope bounds an inventory to either a complete root or one source-relative
// subtree. A path scope never changes the source-relative paths in the result.
type Scope struct {
	RelativePath string
	Full         bool
}

func FullScope() Scope {
	return Scope{Full: true}
}

func PathScope(relative string) (Scope, error) {
	if !validInventoryRelativePath(relative) {
		return Scope{}, errors.New("invalid inventory scope")
	}
	return Scope{RelativePath: relative}, nil
}

type InventoryFile struct {
	ID            uuid.UUID
	RootID        string
	RelativePath  string
	Name          string
	Folder        string
	Kind          FileKind
	CompanionKind string
	Size          int64
	ModifiedAt    time.Time
}

type InventoryResult struct {
	RootID   string
	Scope    Scope
	Files    []InventoryFile
	Warnings []Warning
}

type Inventory struct {
	exclusions []string
}

func NewInventory(options Options) *Inventory {
	return &Inventory{exclusions: normalizeExclusions(options.Exclusions)}
}

func (inventory *Inventory) Scan(
	ctx context.Context,
	root config.Root,
	scope Scope,
) (InventoryResult, error) {
	result := InventoryResult{RootID: root.ID, Scope: scope}
	if inventory == nil || !validInventoryScope(scope) {
		return result, errors.New("invalid inventory")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	resolver, err := library.NewResolver([]config.Root{root})
	if err != nil {
		return result, errors.New("configured root is unavailable")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.ContainerPath)
	if err != nil {
		return result, errors.New("configured root is unavailable")
	}
	start := canonicalRoot
	if !scope.Full {
		resolvedScope, resolveErr := resolver.Resolve(root.ID, scope.RelativePath)
		if resolveErr != nil || resolvedScope.Relative != scope.RelativePath {
			return result, errors.New("configured inventory scope is unavailable")
		}
		start = resolvedScope.Absolute
	}

	err = filepath.WalkDir(start, func(
		absolute string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			if absolute == start {
				return walkErr
			}
			if isRaceError(walkErr) {
				return nil
			}
			relativeOS, relativeErr := filepath.Rel(canonicalRoot, absolute)
			if relativeErr != nil {
				return walkErr
			}
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: filepath.ToSlash(relativeOS),
				Operation:    "walk",
			})
			return nil
		}
		relativeOS, relativeErr := filepath.Rel(canonicalRoot, absolute)
		if relativeErr != nil {
			return relativeErr
		}
		if relativeOS == "." {
			return nil
		}
		relative := filepath.ToSlash(relativeOS)
		if isHidden(relative) || isExcludedPath(relative, inventory.exclusions) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		kind, companionKind, recognized := classifyInventoryFile(entry.Name())
		if !recognized {
			return nil
		}

		handle, _, openErr := resolver.OpenRegular(root.ID, relative)
		if openErr != nil {
			if isRaceError(openErr) || errors.Is(openErr, library.ErrNotRegular) ||
				errors.Is(openErr, library.ErrPathEscape) {
				return nil
			}
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "open",
			})
			return nil
		}
		info, statErr := handle.Stat()
		if statErr != nil {
			_ = handle.Close()
			if isRaceError(statErr) {
				return nil
			}
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "stat",
			})
			return nil
		}
		file := InventoryFile{
			ID:            stableFileID(root.ID, relative),
			RootID:        root.ID,
			RelativePath:  relative,
			Name:          path.Base(relative),
			Folder:        path.Dir(relative),
			Kind:          kind,
			CompanionKind: companionKind,
			Size:          info.Size(),
			ModifiedAt:    info.ModTime().UTC(),
		}
		if file.Folder == "." {
			file.Folder = ""
		}
		if closeErr := handle.Close(); closeErr != nil {
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "close",
			})
		}
		result.Files = append(result.Files, file)
		return nil
	})
	if err != nil {
		return InventoryResult{RootID: root.ID, Scope: scope}, err
	}
	sort.Slice(result.Files, func(left, right int) bool {
		leftPath := strings.ToLower(result.Files[left].RelativePath)
		rightPath := strings.ToLower(result.Files[right].RelativePath)
		if leftPath == rightPath {
			return result.Files[left].RelativePath < result.Files[right].RelativePath
		}
		return leftPath < rightPath
	})
	sort.Slice(result.Warnings, func(left, right int) bool {
		if result.Warnings[left].RelativePath == result.Warnings[right].RelativePath {
			return result.Warnings[left].Operation < result.Warnings[right].Operation
		}
		return result.Warnings[left].RelativePath < result.Warnings[right].RelativePath
	})
	if result.Files == nil {
		result.Files = []InventoryFile{}
	}
	if result.Warnings == nil {
		result.Warnings = []Warning{}
	}
	return result, nil
}

func validInventoryScope(scope Scope) bool {
	if scope.Full {
		return scope.RelativePath == ""
	}
	return validInventoryRelativePath(scope.RelativePath)
}

func validInventoryRelativePath(relative string) bool {
	if relative == "" || relative == "." || !utf8.ValidString(relative) ||
		strings.HasPrefix(relative, "/") || strings.ContainsRune(relative, '\\') ||
		path.Clean(relative) != relative || isWindowsDrivePath(relative) {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return false
			}
		}
	}
	return true
}

func isWindowsDrivePath(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'a' && value[0] <= 'z') ||
			(value[0] >= 'A' && value[0] <= 'Z'))
}
