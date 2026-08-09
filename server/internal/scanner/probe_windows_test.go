//go:build windows

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/media"
)

func TestWindowsProbeReceivesOnlyConfinedCanonicalAbsolutePath(t *testing.T) {
	root := createTree(t, "Book/chapter.m4b")
	probe := &recordingPathProbe{}
	result, err := New(probe, Options{}).Scan(t.Context(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].ProbeError != "" {
		t.Fatalf("files = %#v", result.Files)
	}
	if probe.path == "" || !filepath.IsAbs(probe.path) {
		t.Fatalf("probe path = %q, want absolute path", probe.path)
	}
	canonical, err := filepath.EvalSymlinks(probe.path)
	if err != nil || !strings.EqualFold(canonical, probe.path) {
		t.Fatalf("probe path = %q, canonical = %q, err = %v", probe.path, canonical, err)
	}
	relative, err := filepath.Rel(root, probe.path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("probe path escaped root: path=%q relative=%q err=%v", probe.path, relative, err)
	}
}

func TestWindowsProbeAcceptsUnchangedFileIdentity(t *testing.T) {
	root := createTree(t, "Book/chapter.m4b")
	result, err := New(staticProbe{}, Options{}).Scan(t.Context(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].ProbeError != "" ||
		result.Files[0].Metadata.Title != "Book" {
		t.Fatalf("files = %#v", result.Files)
	}
}

func TestWindowsProbeRejectsFileReplacementDuringProbe(t *testing.T) {
	root := createTree(t, "Book/chapter.m4b")
	source := filepath.Join(root, "Book", "chapter.m4b")
	modified := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, modified, modified); err != nil {
		t.Fatal(err)
	}
	probe := &replacingPathProbe{replacement: []byte("Book/chapter.m4b")}
	result, err := New(probe, Options{}).Scan(t.Context(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !probe.called || len(result.Files) != 1 || result.Files[0].ProbeError != "probe_failed" ||
		result.Files[0].Metadata != (media.Metadata{}) {
		t.Fatalf("replacement was accepted: files=%#v", result.Files)
	}
}

func TestWindowsProbeChecksReplacementBeforeReturningUnsupportedMedia(t *testing.T) {
	root := createTree(t, "Book/chapter.m4b")
	probe := &replacingPathProbe{
		replacement: []byte("Book/chapter.m4b"),
		err:         media.ErrUnsupportedMedia,
	}
	result, err := New(probe, Options{}).Scan(t.Context(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !probe.called || len(result.Files) != 1 || result.Files[0].ProbeError != "probe_failed" {
		t.Fatalf("replacement escaped identity check: files=%#v", result.Files)
	}
}

func TestWindowsScannerNeverProbesOutsideRoot(t *testing.T) {
	root := createTree(t, "Book/chapter.m4b")
	outside := filepath.Join(t.TempDir(), "outside.m4b")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link.m4b")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("creating Windows symlink requires developer mode: %v", err)
	}
	probe := &recordingPathProbe{}
	if _, err := New(probe, Options{}).Scan(t.Context(), config.Root{
		ID: "audiobooks", ContainerPath: root,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(probe.path, outside) {
		t.Fatalf("scanner probed outside root: %q", probe.path)
	}
}

type replacingPathProbe struct {
	replacement []byte
	called      bool
	err         error
}

func (probe *replacingPathProbe) Probe(_ context.Context, path string) (media.Metadata, error) {
	probe.called = true
	info, err := os.Stat(path)
	if err != nil {
		return media.Metadata{}, err
	}
	if err := os.Remove(path); err != nil {
		return media.Metadata{}, err
	}
	if err := os.WriteFile(path, probe.replacement, 0o600); err != nil {
		return media.Metadata{}, err
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		return media.Metadata{}, err
	}
	return media.Metadata{Title: "Replacement", AudioCodec: "aac"}, probe.err
}
