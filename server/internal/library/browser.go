package library

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"
	"time"
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
