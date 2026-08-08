package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestListSortsFoldersFirstWithoutAbsolutePaths(t *testing.T) {
	root := createTree(t,
		"z.mp3",
		"A Book/chapter.mp3",
		"b Book/chapter.mp3",
		"Book/chapter.mp3",
		"book/chapter.mp3",
	)

	got, err := NewBrowser(resolverFor(t, root)).List(context.Background(), "audiobooks", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A Book", "b Book", "Book", "book", "z.mp3"}
	if actual := entryNames(got.Entries); !slices.Equal(actual, want) {
		t.Fatalf("names = %v, want %v", actual, want)
	}
	if got.RootID != "audiobooks" || got.Path != "" {
		t.Fatalf("directory = %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(root)) {
		t.Fatalf("response leaked absolute root %q: %s", root, encoded)
	}
}

func TestListReturnsRelativePathsAndFileMetadata(t *testing.T) {
	root := createTree(t, "Author One/Book One/chapter.mp3")
	chapter := filepath.Join(root, "Author One", "Book One", "chapter.mp3")
	if err := os.WriteFile(chapter, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NewBrowser(resolverFor(t, root)).List(
		context.Background(), "audiobooks", "Author One/Book One",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "Author One/Book One" || len(got.Entries) != 1 {
		t.Fatalf("directory = %#v", got)
	}
	entry := got.Entries[0]
	if entry.Name != "chapter.mp3" ||
		entry.RelativePath != "Author One/Book One/chapter.mp3" ||
		entry.Kind != "file" ||
		entry.Size != 5 ||
		entry.ModifiedAt.IsZero() {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestListMarksDirectoriesAndOmitsTheirSize(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")

	got, err := NewBrowser(resolverFor(t, root)).List(context.Background(), "audiobooks", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %#v", got.Entries)
	}
	entry := got.Entries[0]
	if entry.Kind != "folder" || entry.RelativePath != "Book" || entry.Size != 0 {
		t.Fatalf("entry = %#v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"size"`)) {
		t.Fatalf("directory JSON contains size: %s", encoded)
	}
}

func TestListHonorsCancelledContext(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewBrowser(resolverFor(t, root)).List(ctx, "audiobooks", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
}

func createTree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func entryNames(entries []Entry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name
	}
	return names
}

func entryPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for index, entry := range entries {
		paths[index] = entry.RelativePath
	}
	return paths
}
