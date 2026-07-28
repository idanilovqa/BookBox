package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bookbox.local/server/internal/config"
)

func TestResolveAcceptsPathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	bookPath := filepath.Join(root, "Author One", "Book One")
	if err := os.MkdirAll(bookPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := resolverFor(t, root)
	got, err := resolver.Resolve("audiobooks", "Author One/Book One")
	if err != nil {
		t.Fatal(err)
	}
	if got.RootID != "audiobooks" || got.Relative != "Author One/Book One" || got.Absolute != bookPath {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestResolveRejectsEscapes(t *testing.T) {
	resolver := resolverFor(t, t.TempDir())
	for _, path := range []string{
		"../secret",
		"/absolute",
		`C:\Windows`,
		`\\server\share`,
		`folder\file.mp3`,
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := resolver.Resolve("audiobooks", path); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("Resolve(%q) error = %v, want ErrPathEscape", path, err)
			}
		})
	}
}

func TestResolveTreatsPercentNamesLiterally(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"100% Complete", "%20", "%2e%2e"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := resolverFor(t, root).Resolve("audiobooks", name)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", name, err)
		}
		if got.Relative != name {
			t.Fatalf("Resolve(%q).Relative = %q", name, got.Relative)
		}
	}
}

func TestResolveRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "file.mp3")
	if err := os.WriteFile(outsideFile, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, outside, filepath.Join(root, "escape"))

	resolver := resolverFor(t, root)
	if _, err := resolver.Resolve("audiobooks", "escape/file.mp3"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("Resolve() error = %v, want ErrPathEscape", err)
	}
}

func TestResolveAllowsSymlinkThatStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, target, filepath.Join(root, "inside"))

	got, err := resolverFor(t, root).Resolve("audiobooks", "inside")
	if err != nil {
		t.Fatal(err)
	}
	if got.Relative != "target" || got.Absolute != target {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestResolveRejectsUnknownRoot(t *testing.T) {
	resolver := resolverFor(t, t.TempDir())
	if _, err := resolver.Resolve("missing", ""); !errors.Is(err, ErrUnknownRoot) {
		t.Fatalf("Resolve() error = %v, want ErrUnknownRoot", err)
	}
}

func resolverFor(t *testing.T, root string) *Resolver {
	t.Helper()
	resolver, err := NewResolver([]config.Root{{
		ID:            "audiobooks",
		Name:          "Audiobooks",
		ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func requireSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}
