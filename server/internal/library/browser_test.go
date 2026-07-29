package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSearchMatchesNormalizedFullFolderPathsOnly(t *testing.T) {
	root := createTree(t,
		"Author/Black.Box/chapter.mp3",
		"Blacksmith/chapter.mp3",
		"Loose black box.mp3",
	)
	browser := NewBrowser(resolverFor(t, root))

	got, err := browser.Search(context.Background(), "audiobooks", "black box")
	if err != nil {
		t.Fatal(err)
	}
	if got.RootID != "audiobooks" || got.Query != "black box" {
		t.Fatalf("search result = %#v", got)
	}
	if paths := entryPaths(got.Entries); !slices.Equal(paths, []string{"Author/Black.Box"}) {
		t.Fatalf("paths = %v, want [Author/Black.Box]", paths)
	}

	got, err = browser.Search(context.Background(), "audiobooks", "author black box")
	if err != nil {
		t.Fatal(err)
	}
	if paths := entryPaths(got.Entries); !slices.Equal(
		paths,
		[]string{"Author/Black.Box"},
	) {
		t.Fatalf("paths = %v, want full-path match", paths)
	}
}

func TestSearchReturnsNothingForSymbolOnlyQuery(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")

	got, err := NewBrowser(resolverFor(t, root)).Search(
		context.Background(),
		"audiobooks",
		" ... ",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want none", got.Entries)
	}
}

func TestSearchSortsAndLimitsFolderResults(t *testing.T) {
	paths := make([]string, 105)
	for index := range paths {
		paths[index] = fmt.Sprintf("Match %03d/chapter.mp3", 104-index)
	}
	root := createTree(t, paths...)

	got, err := NewBrowser(resolverFor(t, root)).Search(
		context.Background(),
		"audiobooks",
		"match",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 100 {
		t.Fatalf("entry count = %d, want 100", len(got.Entries))
	}
	if got.Entries[0].RelativePath != "Match 000" ||
		got.Entries[99].RelativePath != "Match 099" {
		t.Fatalf(
			"result bounds = %q ... %q",
			got.Entries[0].RelativePath,
			got.Entries[99].RelativePath,
		)
	}
}

func TestSearchHonorsCancelledContext(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewBrowser(resolverFor(t, root)).Search(ctx, "audiobooks", "book")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context.Canceled", err)
	}
}

func TestSearchDoesNotFollowDirectorySymlinks(t *testing.T) {
	root := createTree(t, "Visible/chapter.mp3")
	outside := createTree(t, "Hidden Match/chapter.mp3")
	link := filepath.Join(root, "Linked Match")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}

	got, err := NewBrowser(resolverFor(t, root)).Search(
		context.Background(),
		"audiobooks",
		"match",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want no symlink or target folders", got.Entries)
	}
}

func TestSearchReturnsFilesystemErrors(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")
	browser := NewBrowser(resolverFor(t, root))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	_, err := browser.Search(context.Background(), "audiobooks", "book")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Search() error = %v, want os.ErrNotExist", err)
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
