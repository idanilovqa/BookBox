package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
)

func TestManifestPreservesSelectedFolderAndHashesFiles(t *testing.T) {
	root := createTransferTree(t,
		"Author/Book/chapter1.mp3",
		"Author/Book/cover.jpg",
	)
	builder := newTestBuilder(t, root)
	got, err := builder.Build(context.Background(), Request{
		Selections: []Selection{{
			RootID:       "audiobooks",
			RelativePath: "Author/Book",
			Kind:         "folder",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.TotalBytes == 0 || len(got.Files) != 2 {
		t.Fatalf("manifest = %#v", got)
	}
	paths := []string{got.Files[0].DestinationPath, got.Files[1].DestinationPath}
	if want := []string{"Book/chapter1.mp3", "Book/cover.jpg"}; !slices.Equal(paths, want) {
		t.Fatalf("destination paths = %v, want %v", paths, want)
	}
	for _, file := range got.Files {
		if file.FileID == "" || len(file.SHA256) != 64 ||
			file.SourcePath == "" || file.ModifiedAt.IsZero() {
			t.Fatalf("file = %#v", file)
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(root)) {
		t.Fatalf("manifest leaked absolute root %q: %s", root, encoded)
	}
}

func TestManifestUsesRootIDAsRootSelectionDestination(t *testing.T) {
	root := createTransferTree(t, "chapter.mp3")
	got, err := newTestBuilder(t, root).Build(context.Background(), Request{
		Selections: []Selection{{RootID: "audiobooks", Kind: "folder"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].DestinationPath != "audiobooks/chapter.mp3" {
		t.Fatalf("files = %#v", got.Files)
	}
}

func TestManifestNormalizesOverlapWithOuterAncestorWinning(t *testing.T) {
	root := createTransferTree(t, "Author/Book/chapter.mp3")
	got, err := newTestBuilder(t, root).Build(context.Background(), Request{
		Selections: []Selection{
			{RootID: "audiobooks", RelativePath: "Author/Book", Kind: "folder"},
			{RootID: "audiobooks", RelativePath: "Author", Kind: "folder"},
			{RootID: "audiobooks", RelativePath: "Author/Book", Kind: "folder"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 ||
		got.Files[0].DestinationPath != "Author/Book/chapter.mp3" {
		t.Fatalf("files = %#v", got.Files)
	}
}

func TestManifestRejectsCaseInsensitiveDestinationCollision(t *testing.T) {
	root := createTransferTree(t,
		"A/Book/first.mp3",
		"B/book/second.mp3",
	)
	_, err := newTestBuilder(t, root).Build(context.Background(), Request{
		Selections: []Selection{
			{RootID: "audiobooks", RelativePath: "A/Book", Kind: "folder"},
			{RootID: "audiobooks", RelativePath: "B/book", Kind: "folder"},
		},
	})
	if !errors.Is(err, ErrDestinationCollision) {
		t.Fatalf("Build() error = %v, want ErrDestinationCollision", err)
	}
}

func TestManifestRejectsInvalidSelectionsAndSymlinks(t *testing.T) {
	root := createTransferTree(t, "Book/chapter.mp3")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.mp3"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(outside, "secret.mp3"),
		filepath.Join(root, "Book", "escape.mp3"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	builder := newTestBuilder(t, root)

	for name, request := range map[string]Request{
		"non-folder": {
			Selections: []Selection{{
				RootID: "audiobooks", RelativePath: "Book", Kind: "file",
			}},
		},
		"symlink": {
			Selections: []Selection{{
				RootID: "audiobooks", RelativePath: "Book", Kind: "folder",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := builder.Build(context.Background(), request)
			if name == "non-folder" && !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("Build() error = %v, want ErrInvalidSelection", err)
			}
			if name == "symlink" && !errors.Is(err, ErrUnsupportedFile) {
				t.Fatalf("Build() error = %v, want ErrUnsupportedFile", err)
			}
		})
	}
}

func TestManifestHonorsCancelledContext(t *testing.T) {
	root := createTransferTree(t, "Book/chapter.mp3")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestBuilder(t, root).Build(ctx, Request{
		Selections: []Selection{{
			RootID: "audiobooks", RelativePath: "Book", Kind: "folder",
		}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context.Canceled", err)
	}
}

func TestManifestUsesStableFileIDsAndFreshManifestIDs(t *testing.T) {
	root := createTransferTree(t, "Book/chapter.mp3")
	builder := newTestBuilder(t, root)
	request := Request{Selections: []Selection{{
		RootID: "audiobooks", RelativePath: "Book", Kind: "folder",
	}}}
	first, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("manifest IDs unexpectedly match: %q", first.ID)
	}
	if len(first.Files) != 1 || len(second.Files) != 1 ||
		first.Files[0].FileID != second.Files[0].FileID {
		t.Fatalf("file IDs are not stable: %#v %#v", first.Files, second.Files)
	}
}

func newTestBuilder(t *testing.T, root string) *Builder {
	t.Helper()
	resolver, err := library.NewResolver([]config.Root{{
		ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewFileSigner(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return NewBuilder(resolver, signer)
}

func createTransferTree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
