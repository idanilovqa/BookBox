package library

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Entry struct {
	Name         string    `json:"name"`
	RelativePath string    `json:"relativePath"`
	Kind         string    `json:"kind"`
	Size         int64     `json:"size,omitempty"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

type Directory struct {
	RootID  string  `json:"rootId"`
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type SearchResult struct {
	RootID  string  `json:"rootId"`
	Query   string  `json:"query"`
	Entries []Entry `json:"entries"`
}

type Browser struct {
	resolver *Resolver
}

func NewBrowser(resolver *Resolver) *Browser {
	return &Browser{resolver: resolver}
}

func (b *Browser) List(ctx context.Context, rootID, relativePath string) (Directory, error) {
	if err := ctx.Err(); err != nil {
		return Directory{}, err
	}
	resolved, err := b.resolver.Resolve(rootID, relativePath)
	if err != nil {
		return Directory{}, err
	}
	dirEntries, err := os.ReadDir(resolved.Absolute)
	if err != nil {
		return Directory{}, err
	}

	entries := make([]Entry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		if err := ctx.Err(); err != nil {
			return Directory{}, err
		}
		info, err := dirEntry.Info()
		if err != nil {
			return Directory{}, err
		}
		kind := "file"
		var size int64
		if dirEntry.IsDir() {
			kind = "folder"
		} else {
			size = info.Size()
		}
		entries = append(entries, Entry{
			Name:         dirEntry.Name(),
			RelativePath: path.Join(resolved.Relative, dirEntry.Name()),
			Kind:         kind,
			Size:         size,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.SliceStable(entries, func(left, right int) bool {
		leftFolder := entries[left].Kind == "folder"
		rightFolder := entries[right].Kind == "folder"
		if leftFolder != rightFolder {
			return leftFolder
		}
		leftName := strings.ToLower(entries[left].Name)
		rightName := strings.ToLower(entries[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return entries[left].Name < entries[right].Name
	})

	return Directory{
		RootID:  rootID,
		Path:    resolved.Relative,
		Entries: entries,
	}, nil
}

func (b *Browser) Search(ctx context.Context, rootID, query string) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	resolved, err := b.resolver.Resolve(rootID, "")
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{
		RootID:  rootID,
		Query:   query,
		Entries: []Entry{},
	}
	normalizedQuery := normalizeSearchText(query)
	if normalizedQuery == "" {
		return result, nil
	}

	err = filepath.WalkDir(
		resolved.Absolute,
		func(absolute string, dirEntry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if absolute == resolved.Absolute {
				return nil
			}
			if dirEntry.Type()&os.ModeSymlink != 0 {
				if dirEntry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !dirEntry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(resolved.Absolute, absolute)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !strings.Contains(normalizeSearchText(relative), normalizedQuery) {
				return nil
			}
			info, err := dirEntry.Info()
			if err != nil {
				return err
			}
			result.Entries = append(result.Entries, Entry{
				Name:         dirEntry.Name(),
				RelativePath: relative,
				Kind:         "folder",
				ModifiedAt:   info.ModTime(),
			})
			return nil
		},
	)
	if err != nil {
		return SearchResult{}, err
	}

	sort.SliceStable(result.Entries, func(left, right int) bool {
		leftPath := strings.ToLower(result.Entries[left].RelativePath)
		rightPath := strings.ToLower(result.Entries[right].RelativePath)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return result.Entries[left].RelativePath < result.Entries[right].RelativePath
	})
	if len(result.Entries) > maxFolderSearchResults {
		result.Entries = result.Entries[:maxFolderSearchResults]
	}
	return result, nil
}

func normalizeSearchText(value string) string {
	var normalized strings.Builder
	for _, character := range value {
		switch {
		case unicode.IsLetter(character):
			normalized.WriteRune(unicode.ToLower(character))
		case unicode.IsDigit(character):
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

const maxFolderSearchResults = 100
