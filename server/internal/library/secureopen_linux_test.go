//go:build linux

package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"bookbox.local/server/internal/config"

	"golang.org/x/sys/unix"
)

func TestLinuxSecureOpenRejectsSymlinkAndFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "chapter.mp3"), []byte("inside"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if handle, err := openRegularBeneath(root, "link/chapter.mp3"); err == nil {
		_ = handle.Close()
		t.Fatal("descriptor-relative opener followed an ancestor symlink")
	}

	fifo := filepath.Join(root, "book.pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver([]config.Root{{
		ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		handle, _, openErr := resolver.OpenRegular("audiobooks", "book.pipe")
		if handle != nil {
			_ = handle.Close()
		}
		done <- openErr
	}()
	select {
	case openErr := <-done:
		if openErr == nil {
			t.Fatal("OpenRegular accepted a FIFO")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OpenRegular blocked while opening a FIFO")
	}
}
