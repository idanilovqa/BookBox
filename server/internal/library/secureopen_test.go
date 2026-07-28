package library

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"bookbox.local/server/internal/config"
)

func TestOpenRegularReadsConfinedFileAndRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "Book")
	if err := os.MkdirAll(book, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "chapter.mp3"), []byte("inside"), 0o640); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver([]config.Root{{
		ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	handle, resolved, err := resolver.OpenRegular("audiobooks", "Book/chapter.mp3")
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(handle)
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read error = %v, close error = %v", readErr, closeErr)
	}
	if string(content) != "inside" || resolved.Relative != "Book/chapter.mp3" {
		t.Fatalf("content = %q, resolved = %#v", content, resolved)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.mp3"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if handle, _, err := resolver.OpenRegular("audiobooks", "link/secret.mp3"); err == nil {
		_ = handle.Close()
		t.Fatal("OpenRegular followed an ancestor symlink")
	} else if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("OpenRegular symlink error = %v, want ErrPathEscape", err)
	}
	if err := os.Symlink(book, filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if handle, _, err := resolver.OpenRegular("audiobooks", "inside-link/chapter.mp3"); err == nil {
		_ = handle.Close()
		t.Fatal("OpenRegular followed an in-root ancestor symlink")
	} else if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("OpenRegular in-root symlink error = %v, want ErrPathEscape", err)
	}
}
