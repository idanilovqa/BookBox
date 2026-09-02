package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"bookbox.local/server/internal/config"
)

func TestInventoryRecognizesAudioCoverAndGroupingCompanionsWithoutProbe(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/01.mp3":       "audio",
		"Author/Book/02.M4B":       "audio",
		"Author/Book/cover.jpg":    "image",
		"Author/Book/FOLDER.PNG":   "image",
		"Author/Book/book.cue":     "cue",
		"Author/Book/book.nfo":     "nfo",
		"Author/Book/book.opf":     "opf",
		"Author/Book/book.m3u":     "playlist",
		"Author/Book/book.m3u8":    "playlist",
		"Author/Book/book.epub":    "ebook",
		"Author/Book/book.fb2":     "ebook",
		"Author/Book/book.pdf":     "ebook",
		"Author/Book/book.mobi":    "ebook",
		"Author/Book/book.azw":     "ebook",
		"Author/Book/book.azw3":    "ebook",
		"Author/Book/tool.exe":     "ignored",
		"Author/Book/VIDEO_TS.IFO": "ignored",
		"Author/Book/notes.txt":    "ignored",
	})

	// The legacy scanner remains constructible with a media probe during the
	// transition. Inventory has its own probe-free entry point and must not
	// consult that probe, even for deliberately invalid audio bytes.
	if legacy := New(panicProbe{}, Options{}); legacy == nil {
		t.Fatal("legacy scanner is nil")
	}
	result, err := NewInventory(Options{}).Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryKinds(t, result.Files, map[string]string{
		"Author/Book/01.mp3":     "audio",
		"Author/Book/02.M4B":     "audio",
		"Author/Book/cover.jpg":  "cover",
		"Author/Book/FOLDER.PNG": "cover",
		"Author/Book/book.cue":   "companion:cue",
		"Author/Book/book.nfo":   "companion:nfo",
		"Author/Book/book.opf":   "companion:opf",
		"Author/Book/book.m3u":   "companion:playlist",
		"Author/Book/book.m3u8":  "companion:playlist",
		"Author/Book/book.epub":  "companion:ebook",
		"Author/Book/book.fb2":   "companion:ebook",
		"Author/Book/book.pdf":   "companion:ebook",
		"Author/Book/book.mobi":  "companion:ebook",
		"Author/Book/book.azw":   "companion:ebook",
		"Author/Book/book.azw3":  "companion:ebook",
	})
}

func TestInventoryPathScopeTraversesOnlyRequestedSubtree(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/Disc 1/01.mp3":   "one",
		"Author/Book/Disc 1/book.cue": "cue",
		"Author/Book/Disc 2/02.mp3":   "two",
		"Other/Book/01.mp3":           "other",
	})
	scope, err := PathScope("Author/Book/Disc 1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewInventory(Options{}).Scan(context.Background(), root, scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.RootID != root.ID || result.Scope != scope {
		t.Fatalf("inventory identity = root %q scope %#v, want root %q scope %#v",
			result.RootID, result.Scope, root.ID, scope)
	}
	assertInventoryPaths(t, result.Files,
		"Author/Book/Disc 1/01.mp3",
		"Author/Book/Disc 1/book.cue",
	)
}

func TestInventoryPathScopeRejectsUnsafeOrAmbiguousPaths(t *testing.T) {
	for _, relative := range []string{
		"", ".", "..", "../Book", "Author/../Book", "/Author/Book",
		`C:\Author\Book`, `Author\Book`, "Author//Book", "Author/./Book",
	} {
		t.Run(relative, func(t *testing.T) {
			if scope, err := PathScope(relative); err == nil {
				t.Fatalf("PathScope(%q) = %#v, want error", relative, scope)
			}
		})
	}
}

func TestInventoryScanRejectsInconsistentConstructedScopes(t *testing.T) {
	root := inventoryFixture(t, map[string]string{"Author/Book/01.mp3": "audio"})
	for _, scope := range []Scope{
		{},
		{Full: true, RelativePath: "Author/Book"},
		{RelativePath: "Author/../Book"},
	} {
		result, err := NewInventory(Options{}).Scan(context.Background(), root, scope)
		if err == nil {
			t.Fatalf("Scan(scope=%#v) = %#v, nil error", scope, result)
		}
		if result.RootID != root.ID || result.Scope != scope || len(result.Files) != 0 {
			t.Fatalf("invalid-scope result = %#v", result)
		}
	}
}

func TestInventoryRejectsPathScopeSymlinkAliases(t *testing.T) {
	root := inventoryFixture(t, map[string]string{"Author/Book/01.mp3": "audio"})
	alias := filepath.Join(root.ContainerPath, "Alias")
	if err := os.Symlink(filepath.Join(root.ContainerPath, "Author"), alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating Windows symlink requires developer mode: %v", err)
		}
		t.Fatal(err)
	}
	scope, err := PathScope("Alias/Book")
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewInventory(Options{}).Scan(context.Background(), root, scope)
	if err == nil {
		t.Fatalf("Scan(symlink alias) = %#v, nil error", result)
	}
	if len(result.Files) != 0 {
		t.Fatalf("symlink-alias result exposed files: %#v", result.Files)
	}
}

func TestInventoryFullScopePreservesHiddenExclusionAndImageCandidateRules(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Keep/book.mp3":          "audio",
		"Keep/front.webp":        "cover",
		"Keep/front-cover.jpg":   "image candidate",
		"Keep/coverlet.jpg":      "image candidate",
		"Keep/.hidden/book.mp3":  "hidden",
		".hidden/book.mp3":       "hidden",
		"Excluded/book.mp3":      "excluded",
		"Nested/Ignore/book.m4b": "excluded",
	})
	result, err := NewInventory(Options{Exclusions: []string{
		"Excluded/**", "Nested/Ignore/**",
	}}).Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryPaths(t, result.Files,
		"Keep/book.mp3", "Keep/coverlet.jpg", "Keep/front-cover.jpg", "Keep/front.webp",
	)
}

func TestInventoryKeepsStableIDsWhileRefreshingChangedFingerprints(t *testing.T) {
	root := inventoryFixture(t, map[string]string{"Author/Book/01.mp3": "one"})
	inventory := NewInventory(Options{})
	first, err := inventory.Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 {
		t.Fatalf("first files = %#v", first.Files)
	}

	absolute := filepath.Join(root.ContainerPath, "Author", "Book", "01.mp3")
	changedAt := time.Date(2026, 8, 9, 12, 34, 56, 0, time.UTC)
	if err := os.WriteFile(absolute, []byte("changed and longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(absolute, changedAt, changedAt); err != nil {
		t.Fatal(err)
	}
	second, err := inventory.Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 1 {
		t.Fatalf("second files = %#v", second.Files)
	}
	before, after := first.Files[0], second.Files[0]
	if before.ID != after.ID {
		t.Fatalf("stable ID changed from %s to %s", before.ID, after.ID)
	}
	if before.Size == after.Size || !after.ModifiedAt.Equal(changedAt) {
		t.Fatalf("fingerprint did not refresh: before=%#v after=%#v", before, after)
	}
	if after.RootID != root.ID || after.RelativePath != "Author/Book/01.mp3" ||
		after.Name != "01.mp3" || after.Folder != "Author/Book" {
		t.Fatalf("inventory file identity = %#v", after)
	}
}

func TestInventoryOmitsFileChangedWhileItsFingerprintIsCaptured(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/01.mp3": "stable",
		"Author/Book/02.mp3": "before",
	})
	changedPath := filepath.Join(root.ContainerPath, "Author", "Book", "02.mp3")
	changedAt := time.Date(2026, 8, 9, 13, 14, 15, 0, time.UTC)
	inventory := NewInventory(Options{})
	inventory.afterFingerprint = func(relative string) {
		if relative != "Author/Book/02.mp3" {
			return
		}
		if err := os.WriteFile(changedPath, []byte("changed and longer"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(changedPath, changedAt, changedAt); err != nil {
			t.Fatal(err)
		}
	}

	result, err := inventory.Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryPaths(t, result.Files, "Author/Book/01.mp3")
	if !reflect.DeepEqual(result.Warnings, []Warning{{
		RelativePath: "Author/Book/02.mp3",
		Operation:    "changed",
	}}) {
		t.Fatalf("inventory warnings = %#v, want one changed-file warning", result.Warnings)
	}
}

func TestInventoryOmitsPathAtomicallyReplacedWhileItsFingerprintIsCaptured(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/01.mp3": "stable",
		"Author/Book/02.mp3": "before",
	})
	changedPath := filepath.Join(root.ContainerPath, "Author", "Book", "02.mp3")
	replacementPath := changedPath + ".replacement"
	originalInfo, err := os.Stat(changedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacementPath, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}

	inventory := NewInventory(Options{})
	inventory.afterFingerprint = func(relative string) {
		if relative != "Author/Book/02.mp3" {
			return
		}
		if err := os.Rename(replacementPath, changedPath); err != nil {
			t.Fatal(err)
		}
	}

	result, err := inventory.Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryPaths(t, result.Files, "Author/Book/01.mp3")
	if !reflect.DeepEqual(result.Warnings, []Warning{{
		RelativePath: "Author/Book/02.mp3",
		Operation:    "changed",
	}}) {
		t.Fatalf("inventory warnings = %#v, want one changed-file warning", result.Warnings)
	}
}

func TestInventorySkipsSymlinkEscapes(t *testing.T) {
	root := inventoryFixture(t, map[string]string{"Keep/book.mp3": "audio"})
	outside := filepath.Join(t.TempDir(), "outside.mp3")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(root.ContainerPath, "outside.mp3")
	if err := os.Symlink(outside, fileLink); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating Windows symlink requires developer mode: %v", err)
		}
		t.Fatal(err)
	}
	directoryLink := filepath.Join(root.ContainerPath, "Linked")
	if err := os.Symlink(filepath.Dir(outside), directoryLink); err != nil {
		t.Fatal(err)
	}

	result, err := NewInventory(Options{}).Scan(context.Background(), root, FullScope())
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryPaths(t, result.Files, "Keep/book.mp3")
}

func TestInventoryCanceledTraversalReturnsNoPartialResult(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/01.mp3": "one",
		"Author/Book/02.mp3": "two",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewInventory(Options{}).Scan(ctx, root, FullScope())
	if err == nil {
		t.Fatalf("Scan(canceled) = %#v, nil error", result)
	}
	if result.RootID != root.ID || len(result.Files) != 0 {
		t.Fatalf("canceled result = %#v", result)
	}
}

func inventoryFixture(t *testing.T, files map[string]string) config.Root {
	t.Helper()
	root := t.TempDir()
	paths := make([]string, 0, len(files))
	for relative := range files {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(files[relative]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return config.Root{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root}
}

func assertInventoryKinds(
	t *testing.T,
	files []InventoryFile,
	want map[string]string,
) {
	t.Helper()
	got := make(map[string]string, len(files))
	for _, file := range files {
		kind := string(file.Kind)
		if file.CompanionKind != "" {
			kind += ":" + file.CompanionKind
		}
		got[file.RelativePath] = kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory kinds = %#v, want %#v", got, want)
	}
}

func assertInventoryPaths(t *testing.T, files []InventoryFile, want ...string) {
	t.Helper()
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.RelativePath)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory paths = %v, want %v", got, want)
	}
}
