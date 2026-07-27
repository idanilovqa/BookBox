package covers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"netstorage.local/server/internal/catalog"
	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/library"
)

func TestPublisherReplacesOnlyRecognizedCoverAndBacksUpOriginal(t *testing.T) {
	t.Parallel()

	fixture := newPublisherFixture(t, "folder.jpg")
	oldCover := testJPEG(t, 32, 48)
	audio := []byte("untouchable audiobook bytes")
	writeFile(t, filepath.Join(fixture.bookFolder, "folder.jpg"), oldCover)
	writeFile(t, filepath.Join(fixture.bookFolder, "book.m4b"), audio)
	writeFile(t, filepath.Join(fixture.bookFolder, "metadata.json"), []byte(`{"keep":true}`))
	writeFile(t, filepath.Join(fixture.bookFolder, "back.jpg"), testJPEG(t, 16, 24))
	before := snapshotTree(t, fixture.bookFolder)

	oldHash := sha256.Sum256(oldCover)
	if err := os.MkdirAll(fixture.backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixture.backupRoot, formatHash(oldHash)), oldCover)

	event, err := fixture.publisher.Publish(t.Context(), Request{
		BookID:      fixture.bookID,
		Destination: "folder.jpg",
		Candidate:   testPNG(t, 96, 144),
		Provider:    "openlibrary",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	published, err := os.ReadFile(filepath.Join(fixture.bookFolder, "folder.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := Inspect(bytes.NewReader(published), int64(len(published)))
	if err != nil || info.Format != "jpeg" || info.Width != 96 || info.Height != 144 {
		t.Fatalf("published cover = %#v, error %v", info, err)
	}
	gotAudio, err := os.ReadFile(filepath.Join(fixture.bookFolder, "book.m4b"))
	if err != nil || !bytes.Equal(gotAudio, audio) {
		t.Fatalf("audio changed: %v", err)
	}
	after := snapshotTree(t, fixture.bookFolder)
	delete(before, "folder.jpg")
	delete(after, "folder.jpg")
	if !equalSnapshots(before, after) {
		t.Fatalf("non-target source-tree files changed:\nbefore %#v\nafter  %#v", before, after)
	}
	backup, err := os.ReadFile(filepath.Join(fixture.backupRoot, event.BackupKey))
	if err != nil || !bytes.Equal(backup, oldCover) {
		t.Fatalf("backup = %d bytes, error %v", len(backup), err)
	}
	if event.OldSHA256 != formatHash(oldHash) || len(fixture.repository.events) != 1 {
		t.Fatalf("event = %#v, recorded = %d", event, len(fixture.repository.events))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(fixture.backupRoot, event.BackupKey))
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("backup permissions = %v, error %v", info.Mode().Perm(), err)
		}
	}
}

func TestPublisherLeavesOriginalWhenAtomicPublicationFails(t *testing.T) {
	t.Parallel()

	fixture := newPublisherFixture(t, "folder.jpg")
	oldCover := testJPEG(t, 32, 48)
	writeFile(t, filepath.Join(fixture.bookFolder, "folder.jpg"), oldCover)
	before := snapshotTree(t, fixture.bookFolder)
	fixture.publisher.atomicReplace = func(target string, data []byte, mode os.FileMode) error {
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		return errors.New("injected fsync failure")
	}

	if _, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Destination: "folder.jpg", Candidate: testJPEG(t, 96, 144), Provider: "test",
	}); err == nil {
		t.Fatal("Publish() error = nil")
	}
	if after := snapshotTree(t, fixture.bookFolder); !equalSnapshots(before, after) {
		t.Fatalf("source tree changed after failed atomic publication:\nbefore %#v\nafter %#v", before, after)
	}
	if len(fixture.repository.events) != 0 {
		t.Fatal("failed publication recorded an event")
	}
}

func TestPublisherRollsBackWhenEventRecordingFails(t *testing.T) {
	t.Parallel()

	for _, destination := range []string{"folder.jpg", ""} {
		destination := destination
		t.Run(destination, func(t *testing.T) {
			t.Parallel()
			fixture := newPublisherFixture(t, destination)
			if destination != "" {
				writeFile(t, filepath.Join(fixture.bookFolder, destination), testJPEG(t, 32, 48))
			}
			writeFile(t, filepath.Join(fixture.bookFolder, "book.m4b"), []byte("audio"))
			before := snapshotTree(t, fixture.bookFolder)
			fixture.repository.recordError = errors.New("database unavailable")

			if _, err := fixture.publisher.Publish(t.Context(), Request{
				BookID: fixture.bookID, Destination: destination, Candidate: testJPEG(t, 96, 144), Provider: "test",
			}); err == nil {
				t.Fatal("Publish() error = nil")
			}
			if after := snapshotTree(t, fixture.bookFolder); !equalSnapshots(before, after) {
				t.Fatalf("source tree changed after event failure:\nbefore %#v\nafter %#v", before, after)
			}
		})
	}
}

func TestPublisherCreatesOnlyCoverJPGWhenArtworkIsMissing(t *testing.T) {
	t.Parallel()

	fixture := newPublisherFixture(t, "")
	writeFile(t, filepath.Join(fixture.bookFolder, "book.m4b"), []byte("audio"))
	event, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Candidate: testPNG(t, 96, 144), Provider: "openlibrary",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if event.Destination != "cover.jpg" || event.Action != "created" || event.BackupKey != "" {
		t.Fatalf("event = %#v", event)
	}
	if _, err := os.Stat(filepath.Join(fixture.bookFolder, "cover.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherRejectsUnsafeOrNonImprovingDestinations(t *testing.T) {
	t.Parallel()

	fixture := newPublisherFixture(t, "folder.jpg")
	writeFile(t, filepath.Join(fixture.bookFolder, "folder.jpg"), testJPEG(t, 64, 96))
	writeFile(t, filepath.Join(fixture.bookFolder, "book.m4b"), []byte("audio"))

	for _, destination := range []string{"../folder.jpg", "book.m4b", "poster.jpg", "nested/cover.jpg"} {
		_, err := fixture.publisher.Publish(t.Context(), Request{
			BookID: fixture.bookID, Destination: destination, Candidate: testJPEG(t, 128, 192), Provider: "test",
		})
		if err == nil {
			t.Fatalf("destination %q accepted", destination)
		}
	}
	if _, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Destination: "folder.jpg", Candidate: testJPEG(t, 70, 100), Provider: "test",
	}); !errors.Is(err, ErrNoImprovement) {
		t.Fatalf("non-improving image error = %v", err)
	}
}

func TestPublisherRejectsUnknownBooksAndSymlinkDestinations(t *testing.T) {
	t.Parallel()

	fixture := newPublisherFixture(t, "cover.jpg")
	fixture.repository.details = nil
	if _, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Destination: "cover.jpg", Candidate: testJPEG(t, 64, 96), Provider: "test",
	}); err == nil {
		t.Fatal("unknown book accepted")
	}

	fixture = newPublisherFixture(t, "cover.jpg")
	detail := fixture.repository.details[fixture.bookID]
	detail.Book.SourceFolder = "Unknown/Folder"
	fixture.repository.details[fixture.bookID] = detail
	if _, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Destination: "cover.jpg", Candidate: testJPEG(t, 64, 96), Provider: "test",
	}); err == nil {
		t.Fatal("unknown folder accepted")
	}

	fixture = newPublisherFixture(t, "cover.jpg")
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	writeFile(t, outside, testJPEG(t, 32, 48))
	if err := os.Symlink(outside, filepath.Join(fixture.bookFolder, "cover.jpg")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := fixture.publisher.Publish(t.Context(), Request{
		BookID: fixture.bookID, Destination: "cover.jpg", Candidate: testJPEG(t, 96, 144), Provider: "test",
	}); err == nil {
		t.Fatal("symlink destination accepted")
	}
}

func TestPublisherRejectsMissingBooksBlankProvidersAndUnsafeBackupRootsBeforeWriting(t *testing.T) {
	t.Parallel()

	t.Run("blank provider", func(t *testing.T) {
		t.Parallel()
		fixture := newPublisherFixture(t, "")
		before := snapshotTree(t, fixture.root)
		if _, err := fixture.publisher.Publish(t.Context(), Request{
			BookID: fixture.bookID, Candidate: testJPEG(t, 96, 144), Provider: " ",
		}); err == nil {
			t.Fatal("blank provider accepted")
		}
		if after := snapshotTree(t, fixture.root); !equalSnapshots(before, after) {
			t.Fatal("blank provider changed source root")
		}
	})

	t.Run("missing book", func(t *testing.T) {
		t.Parallel()
		fixture := newPublisherFixture(t, "")
		detail := fixture.repository.details[fixture.bookID]
		missingAt := time.Now()
		detail.Book.MissingAt = &missingAt
		fixture.repository.details[fixture.bookID] = detail
		before := snapshotTree(t, fixture.root)
		if _, err := fixture.publisher.Publish(t.Context(), Request{
			BookID: fixture.bookID, Candidate: testJPEG(t, 96, 144), Provider: "test",
		}); err == nil {
			t.Fatal("missing book accepted")
		}
		if after := snapshotTree(t, fixture.root); !equalSnapshots(before, after) {
			t.Fatal("missing book changed source root")
		}
	})

	for _, test := range []struct {
		name       string
		backupRoot func(publisherFixture) string
	}{
		{name: "relative", backupRoot: func(publisherFixture) string { return "relative-backup" }},
		{name: "inside source root", backupRoot: func(fixture publisherFixture) string {
			return filepath.Join(fixture.root, ".netstorage-backups")
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublisherFixture(t, "")
			fixture.publisher = NewPublisher(fixture.resolver, fixture.repository, test.backupRoot(fixture))
			before := snapshotTree(t, fixture.root)
			if _, err := fixture.publisher.Publish(t.Context(), Request{
				BookID: fixture.bookID, Candidate: testJPEG(t, 96, 144), Provider: "test",
			}); err == nil {
				t.Fatal("unsafe backup root accepted")
			}
			if after := snapshotTree(t, fixture.root); !equalSnapshots(before, after) {
				t.Fatal("unsafe backup root changed source root")
			}
		})
	}
}

type publisherFixture struct {
	publisher  *Publisher
	repository *fakeCoverRepository
	bookID     uuid.UUID
	root       string
	bookFolder string
	backupRoot string
	resolver   *library.Resolver
}

func newPublisherFixture(t *testing.T, _ string) publisherFixture {
	t.Helper()
	root := t.TempDir()
	bookFolder := filepath.Join(root, "Author", "Book")
	if err := os.MkdirAll(bookFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver, err := library.NewResolver([]config.Root{{ID: "audio", ContainerPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	bookID := uuid.New()
	repository := &fakeCoverRepository{details: map[uuid.UUID]catalog.BookDetail{
		bookID: {Book: catalog.Book{ID: bookID, RootID: "audio", SourceFolder: "Author/Book"}},
	}}
	backupRoot := filepath.Join(t.TempDir(), "backups")
	return publisherFixture{
		publisher:  NewPublisher(resolver, repository, backupRoot),
		repository: repository,
		bookID:     bookID,
		root:       root,
		bookFolder: bookFolder,
		backupRoot: backupRoot,
		resolver:   resolver,
	}
}

type fakeCoverRepository struct {
	details     map[uuid.UUID]catalog.BookDetail
	events      []catalog.CoverEvent
	recordError error
}

func (repository *fakeCoverRepository) GetBook(_ context.Context, id uuid.UUID) (catalog.BookDetail, error) {
	detail, ok := repository.details[id]
	if !ok {
		return catalog.BookDetail{}, catalog.ErrNotFound
	}
	return detail, nil
}

func (repository *fakeCoverRepository) RecordCoverEvent(_ context.Context, event catalog.CoverEvent) error {
	if repository.recordError != nil {
		return repository.recordError
	}
	repository.events = append(repository.events, event)
	return nil
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func formatHash(hash [32]byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(hash)*2)
	for index, value := range hash {
		result[index*2] = digits[value>>4]
		result[index*2+1] = digits[value&0xf]
	}
	return string(result)
}

func snapshotTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func equalSnapshots(left, right map[string][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for path, hash := range left {
		if right[path] != hash {
			return false
		}
	}
	return true
}
