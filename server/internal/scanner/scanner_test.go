package scanner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/media"
)

type staticProbe struct{}

func (staticProbe) Probe(context.Context, string) (media.Metadata, error) {
	return media.Metadata{Title: "Book", Author: "Author", AudioCodec: "aac"}, nil
}

type panicProbe struct{}

func (panicProbe) Probe(context.Context, string) (media.Metadata, error) {
	panic("inventory called the legacy media probe")
}

func TestLegacyScannerCompatibilityStillProbesAudio(t *testing.T) {
	root := createTree(t, "Book/chapter.mp3")
	result, err := New(staticProbe{}, Options{}).Scan(
		context.Background(),
		config.Root{ID: "audiobooks", ContainerPath: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Metadata.Title != "Book" ||
		result.Files[0].Metadata.AudioCodec != "aac" {
		t.Fatalf("legacy scanner result = %#v", result.Files)
	}
}

func TestScanIncludesSupportedAudioAndRecognizedCovers(t *testing.T) {
	root := createTree(t,
		"Book/a.m4b", "Book/b.MP3", "Book/c.m4a", "Book/d.aac",
		"Book/e.flac", "Book/f.ogg", "Book/g.opus", "Book/h.wav",
		"Book/cover.jpg", "Book/FOLDER.PNG", "Book/front.WebP",
		"Book/notes.txt", "Book/download.part", "Book/render.tmp", "Book/.hidden.mp3",
	)

	got, err := New(staticProbe{}, Options{}).Scan(
		context.Background(),
		config.Root{ID: "audiobooks", ContainerPath: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertPaths(t, got,
		"Book/FOLDER.PNG", "Book/a.m4b", "Book/b.MP3", "Book/c.m4a",
		"Book/cover.jpg", "Book/d.aac", "Book/e.flac", "Book/f.ogg",
		"Book/front.WebP", "Book/g.opus", "Book/h.wav",
	)
}

func TestScanSkipsConfiguredExclusionsAndLinks(t *testing.T) {
	root := createTree(t, "Keep/book.m4b", "Excluded/book.m4b", "Nested/Ignore/book.mp3")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "Keep"), filepath.Join(root, "Linked")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(root, "Keep", "book.m4b"),
			filepath.Join(root, "linked-book.m4b"),
		); err != nil {
			t.Fatal(err)
		}
	}

	got, err := New(staticProbe{}, Options{
		Exclusions: []string{"Excluded/**", "Nested/Ignore/**"},
	}).Scan(context.Background(), config.Root{ID: "audiobooks", ContainerPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertPaths(t, got, "Keep/book.m4b")
}

func TestScanIsDeterministicAndDoesNotMutateSource(t *testing.T) {
	root := createTree(t, "Z/book.mp3", "A/book.m4b", "A/cover.jpg")
	before := snapshotTree(t, root)
	subject := New(staticProbe{}, Options{})

	first, err := subject.Scan(context.Background(), config.Root{ID: "audiobooks", ContainerPath: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := subject.Scan(context.Background(), config.Root{ID: "audiobooks", ContainerPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("scan differs across runs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("scanner mutated source:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestScanToleratesFileDisappearingDuringProbe(t *testing.T) {
	root := createTree(t, "Book/a.m4b", "Book/b.m4b")
	probe := &removingProbe{remove: filepath.Join(root, "Book", "b.m4b")}

	got, err := New(probe, Options{}).Scan(context.Background(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPaths(t, got, "Book/a.m4b")
}

func TestScanNeverPassesSourcePathToPathBasedProbe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux worker uses procfs for a confined descriptor fallback")
	}
	root := createTree(t, "Book/a.m4b")
	probe := &recordingPathProbe{}
	_, err := New(probe, Options{}).Scan(context.Background(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "Book", "a.m4b")
	if probe.path == "" || probe.path == sourcePath ||
		!strings.HasPrefix(probe.path, "/proc/") {
		t.Fatalf("probe path = %q, want a procfs descriptor path distinct from %q",
			probe.path, sourcePath)
	}
}

type recordingPathProbe struct {
	path string
}

func (p *recordingPathProbe) Probe(_ context.Context, path string) (media.Metadata, error) {
	p.path = path
	return media.Metadata{Title: "Book", AudioCodec: "aac"}, nil
}

type removingProbe struct {
	remove string
	done   bool
}

func (p *removingProbe) Probe(context.Context, string) (media.Metadata, error) {
	if !p.done {
		p.done = true
		if err := os.Remove(p.remove); err != nil && !os.IsNotExist(err) {
			return media.Metadata{}, err
		}
	}
	return media.Metadata{Title: "Book", AudioCodec: "aac"}, nil
}

func createTree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range paths {
		full := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertPaths(t *testing.T, result Result, want ...string) {
	t.Helper()
	got := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		got = append(got, file.RelativePath)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s|%s|%d|%d",
			filepath.ToSlash(relative), info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += "|" + fmt.Sprintf("%x", sha256.Sum256(content))
		}
		snapshot = append(snapshot, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(snapshot)
	return snapshot
}

func TestRecognizedNamesAreExact(t *testing.T) {
	root := createTree(t,
		"Book/coverlet.jpg", "Book/front-cover.jpg", "Book/folder.jpeg",
		"Book/cover.gif", "Book/actual.mp3",
	)
	got, err := New(staticProbe{}, Options{}).Scan(context.Background(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPaths(t, got, "Book/actual.mp3", "Book/folder.jpeg")
	for _, file := range got.Files {
		if strings.Contains(file.RelativePath, "coverlet") {
			t.Fatalf("accepted non-recognized cover: %s", file.RelativePath)
		}
	}
}
