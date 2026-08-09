package workerlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const liveLogName = "catalog-worker.log"

func TestOpenRejectsInvalidPolicy(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name     string
		maxBytes int64
		backups  int
	}{
		{name: "zero size", maxBytes: 0, backups: 1},
		{name: "negative size", maxBytes: -1, backups: 1},
		{name: "zero backups", maxBytes: 32, backups: 0},
		{name: "negative backups", maxBytes: 32, backups: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, err := Open(directory, test.maxBytes, test.backups)
			if writer != nil {
				_ = writer.Close()
			}
			if !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("Open() error = %v, want ErrInvalidPolicy", err)
			}
		})
	}
}

func TestWriterRotatesNewestFirstAndBoundsBackupCount(t *testing.T) {
	directory := t.TempDir()
	writer, err := Open(directory, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"one\n", "two\n", "three\n", "four\n", "five\n"} {
		if _, err := writer.Write([]byte(entry)); err != nil {
			t.Fatalf("Write(%q): %v", entry, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContents(t, filepath.Join(directory, liveLogName), "five\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".1"), "four\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".2"), "three\n")
	if _, err := os.Lstat(filepath.Join(directory, liveLogName+".3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup beyond retention: %v", err)
	}
}

func TestWriterRotatesMoreThanNineBackupsNumerically(t *testing.T) {
	directory := t.TempDir()
	writer, err := Open(directory, 4, 12)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 14; index++ {
		entry := fmt.Sprintf("%03d\n", index)
		if _, err := writer.Write([]byte(entry)); err != nil {
			t.Fatalf("Write(%q): %v", entry, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContents(t, filepath.Join(directory, liveLogName), "013\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".1"), "012\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".9"), "004\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".10"), "003\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".11"), "002\n")
	assertFileContents(t, filepath.Join(directory, liveLogName+".12"), "001\n")
	if _, err := os.Lstat(filepath.Join(directory, liveLogName+".13")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup beyond retention: %v", err)
	}
}

func TestWriterAppendsAfterReopen(t *testing.T) {
	directory := t.TempDir()
	writer, err := Open(directory, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err = Open(directory, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("-beta")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, filepath.Join(directory, liveLogName), "alpha-beta")
}

func TestWriterRecoversAfterTransientRotationReopenFailure(t *testing.T) {
	directory := t.TempDir()
	originalOpen := openLiveFileForWriter
	openCalls := 0
	openLiveFileForWriter = func(directory string) (*os.File, int64, error) {
		openCalls++
		if openCalls == 2 {
			return nil, 0, errors.New("injected transient reopen failure")
		}
		return originalOpen(directory)
	}
	t.Cleanup(func() { openLiveFileForWriter = originalOpen })

	writer, err := Open(directory, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("full")); err != nil {
		t.Fatal(err)
	}
	if written, err := writer.Write([]byte("x")); written != 0 || err == nil {
		t.Fatalf("rotation Write() = (%d, %v), want (0, transient error)", written, err)
	}
	if written, err := writer.Write([]byte("y")); written != 1 || err != nil {
		t.Fatalf("recovery Write() = (%d, %v), want (1, nil)", written, err)
	}

	assertFileContents(t, filepath.Join(directory, liveLogName+".1"), "full")
	assertFileContents(t, filepath.Join(directory, liveLogName), "y")
}

func TestWriterCloseIsIdempotentAndRejectsFurtherWrites(t *testing.T) {
	writer, err := Open(t.TempDir(), 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if written, err := writer.Write([]byte("late")); written != 0 || err == nil {
		t.Fatalf("Write() after Close = (%d, %v), want (0, error)", written, err)
	}
}

func TestWriterRejectsEntryLargerThanFileLimit(t *testing.T) {
	directory := t.TempDir()
	writer, err := Open(directory, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if written, err := writer.Write([]byte("123456789")); written != 0 || !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("Write() = (%d, %v), want (0, ErrEntryTooLarge)", written, err)
	}
	assertFileContents(t, filepath.Join(directory, liveLogName), "")
}

func TestOpenRejectsSymlinkedDirectory(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if writer, err := Open(link, 32, 2); err == nil {
		_ = writer.Close()
		t.Fatal("Open() accepted a symlinked directory")
	}
}

func TestOpenRejectsDirectoryBelowSymlinkedParent(t *testing.T) {
	targetParent := t.TempDir()
	if err := os.Mkdir(filepath.Join(targetParent, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(t.TempDir(), "linked-parent")
	if err := os.Symlink(targetParent, linkedParent); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if writer, err := Open(filepath.Join(linkedParent, "logs"), 32, 2); err == nil {
		_ = writer.Close()
		t.Fatal("Open() accepted a directory below a symlinked parent")
	}
}

func TestOpenRejectsUnsafeLiveFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target.log")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("file symlinks unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			test.setup(t, filepath.Join(directory, liveLogName))
			if writer, err := Open(directory, 32, 2); err == nil {
				_ = writer.Close()
				t.Fatal("Open() accepted an unsafe live file")
			}
		})
	}
}

func TestOpenRejectsUnsafeBackupTarget(t *testing.T) {
	for _, suffix := range []string{".1", ".2"} {
		t.Run(suffix, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, liveLogName+suffix), 0o700); err != nil {
				t.Fatal(err)
			}
			if writer, err := Open(directory, 32, 2); err == nil {
				_ = writer.Close()
				t.Fatalf("Open() accepted unsafe backup %s", suffix)
			}
		})
	}
}

func TestOpenRemovesSafeNumericBackupsBeyondReducedRetention(t *testing.T) {
	directory := t.TempDir()
	for _, suffix := range []string{".3", ".10", ".12"} {
		if err := os.WriteFile(filepath.Join(directory, liveLogName+suffix), []byte(suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writer, err := Open(directory, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".3", ".10", ".12"} {
		if _, err := os.Lstat(filepath.Join(directory, liveLogName+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale backup %s remains: %v", suffix, err)
		}
	}
}

func TestOpenRejectsUnsafeNumericBackupBeyondReducedRetention(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "stale-target.log")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("file symlinks unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			stalePath := filepath.Join(directory, liveLogName+".10")
			test.setup(t, stalePath)
			if writer, err := Open(directory, 32, 2); !errors.Is(err, ErrUnsafeTarget) {
				if writer != nil {
					_ = writer.Close()
				}
				t.Fatalf("Open() error = %v, want ErrUnsafeTarget", err)
			}
			if _, err := os.Lstat(stalePath); err != nil {
				t.Fatalf("unsafe stale backup was mutated: %v", err)
			}
		})
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s contents = %q, want %q", filepath.Base(path), contents, want)
	}
}
