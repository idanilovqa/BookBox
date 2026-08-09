package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestApplyInventoryQueuesOnlyNewOrChangedFilesAndPreservesProbeState(t *testing.T) {
	first := inventoryInstant(1)
	second := inventoryInstant(2)
	probedAt := inventoryInstant(3)
	sameID := uuid.New()
	failedID := uuid.New()
	changedID := uuid.New()
	companionID := uuid.New()
	repository, pool, scan := inventoryRepositoryFixture(t,
		storedInventoryFile{
			ID: sameID, Path: "same.mp3", Size: 10, ModifiedAt: first,
			FileKind: "audio", ProbeStatus: "succeeded", ProbeVersion: 7,
			ProbedAt: &probedAt, Media: map[string]any{"title": "Preserved"},
		},
		storedInventoryFile{
			ID: failedID, Path: "unchanged-failed.mp3", Size: 9, ModifiedAt: first,
			FileKind: "audio", ProbeStatus: "failed", ProbeVersion: 3,
			ProbeError: "old failure", ProbedAt: &probedAt,
		},
		storedInventoryFile{
			ID: changedID, Path: "changed.mp3", Size: 10, ModifiedAt: first,
			FileKind: "audio", ProbeStatus: "failed", ProbeVersion: 5,
			ProbeError: "stale failure", ProbedAt: &probedAt,
			Media: map[string]any{"title": "Stale"},
		},
		storedInventoryFile{
			ID: companionID, Path: "book.nfo", Size: 4, ModifiedAt: first,
			FileKind: "companion", CompanionKind: "nfo",
			ProbeStatus: "succeeded", ProbeVersion: 2,
			ProbedAt: &probedAt, Media: map[string]any{"title": "Old companion"},
		},
	)

	newAudioID := uuid.New()
	coverID := uuid.New()
	ebookID := uuid.New()
	delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID,
		RootID:    "audiobooks",
		Complete:  true,
		Files: []InventoryFile{
			inventoryFile(sameID, "same.mp3", "audio", "", 10, first),
			inventoryFile(failedID, "unchanged-failed.mp3", "audio", "", 9, first),
			inventoryFile(changedID, "changed.mp3", "audio", "", 11, second),
			inventoryFile(newAudioID, "new.mp3", "audio", "", 12, second),
			inventoryFile(companionID, "book.nfo", "companion", "nfo", 5, second),
			inventoryFile(coverID, "cover.jpg", "cover", "", 20, second),
			inventoryFile(ebookID, "book.epub", "companion", "ebook", 30, second),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryDeltaPaths(t, delta.ProbeFiles, "changed.mp3", "new.mp3")
	assertInventoryDeltaPaths(t, delta.InspectCompanions, "book.nfo")
	assertInventoryDeltaPaths(t, delta.UnchangedFiles, "same.mp3", "unchanged-failed.mp3")
	if len(delta.MissingFileIDs) != 0 {
		t.Fatalf("missing IDs=%v, want none", delta.MissingFileIDs)
	}

	var sameStatus string
	var sameVersion int
	var sameMedia map[string]any
	var sameProbedAt *time.Time
	if err := pool.QueryRow(context.Background(), `
		select probe_status, probe_version, media, probed_at
		from source_files where id=$1
	`, sameID).Scan(&sameStatus, &sameVersion, &sameMedia, &sameProbedAt); err != nil {
		t.Fatal(err)
	}
	if sameStatus != "succeeded" || sameVersion != 7 || sameMedia["title"] != "Preserved" ||
		sameProbedAt == nil || !sameProbedAt.Equal(probedAt) {
		t.Fatalf("unchanged probe state=%q version=%d media=%v probedAt=%v", sameStatus, sameVersion, sameMedia, sameProbedAt)
	}
	var failedStatus, failedError string
	var failedVersion int
	if err := pool.QueryRow(context.Background(), `
		select probe_status, probe_version, probe_error
		from source_files where id=$1
	`, failedID).Scan(&failedStatus, &failedVersion, &failedError); err != nil {
		t.Fatal(err)
	}
	if failedStatus != "failed" || failedVersion != 3 || failedError != "old failure" {
		t.Fatalf("unchanged failure state=%q version=%d error=%q", failedStatus, failedVersion, failedError)
	}

	for _, fileID := range []uuid.UUID{changedID, companionID, newAudioID} {
		var status string
		var version int
		var media map[string]any
		var probeError *string
		var currentProbedAt *time.Time
		if err := pool.QueryRow(context.Background(), `
			select probe_status, probe_version, media, probe_error, probed_at
			from source_files where id=$1
		`, fileID).Scan(&status, &version, &media, &probeError, &currentProbedAt); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || version != 0 || len(media) != 0 ||
			probeError != nil || currentProbedAt != nil {
			t.Fatalf("pending file %s state=%q version=%d media=%v error=%v probedAt=%v", fileID, status, version, media, probeError, currentProbedAt)
		}
	}

	for fileID, wantKind := range map[uuid.UUID]string{coverID: "cover", ebookID: "companion"} {
		var kind, status string
		if err := pool.QueryRow(context.Background(), `
			select file_kind, probe_status from source_files where id=$1
		`, fileID).Scan(&kind, &status); err != nil {
			t.Fatal(err)
		}
		if kind != wantKind || status != "not_applicable" {
			t.Fatalf("file %s kind/status=%q/%q, want %q/not_applicable", fileID, kind, status, wantKind)
		}
	}
}

func TestApplyInventoryMissingBehavior(t *testing.T) {
	t.Run("partial inventory preserves unseen files", func(t *testing.T) {
		repository, pool, scan := inventoryRepositoryFixture(t,
			storedAudioInventoryFile("seen.mp3"),
			storedAudioInventoryFile("unseen.mp3"),
		)
		delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
			ScanRunID: scan.ID, RootID: "audiobooks", Complete: false,
			Files: []InventoryFile{inventoryAudioFile("seen.mp3")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(delta.MissingFileIDs) != 0 || inventoryMissingCount(t, pool) != 0 {
			t.Fatalf("partial delta=%v missing rows=%d", delta.MissingFileIDs, inventoryMissingCount(t, pool))
		}
	})

	t.Run("complete full inventory marks every unseen file missing", func(t *testing.T) {
		unseen := storedAudioInventoryFile("unseen.mp3")
		repository, pool, scan := inventoryRepositoryFixture(t,
			storedAudioInventoryFile("seen.mp3"), unseen,
		)
		delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
			ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
			Files: []InventoryFile{inventoryAudioFile("seen.mp3")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(delta.MissingFileIDs, []uuid.UUID{unseen.ID}) || inventoryMissingCount(t, pool) != 1 {
			t.Fatalf("full delta=%v missing rows=%d", delta.MissingFileIDs, inventoryMissingCount(t, pool))
		}
	})

	t.Run("complete scoped inventory marks only its unseen subtree missing", func(t *testing.T) {
		inside := storedAudioInventoryFile("Author/Book/unseen.mp3")
		outside := storedAudioInventoryFile("Author/Other/unseen.mp3")
		repository, pool, scan := inventoryRepositoryFixture(t,
			storedAudioInventoryFile("Author/Book/seen.mp3"), inside, outside,
		)
		delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
			ScanRunID: scan.ID, RootID: "audiobooks", ScopePath: "Author/Book", Complete: true,
			Files: []InventoryFile{inventoryAudioFile("Author/Book/seen.mp3")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(delta.MissingFileIDs, []uuid.UUID{inside.ID}) {
			t.Fatalf("scoped missing IDs=%v, want %v", delta.MissingFileIDs, inside.ID)
		}
		var outsideMissing bool
		if err := pool.QueryRow(context.Background(), `
			select missing_at is not null from source_files where id=$1
		`, outside.ID).Scan(&outsideMissing); err != nil {
			t.Fatal(err)
		}
		if outsideMissing {
			t.Fatal("scoped inventory marked a sibling subtree missing")
		}
	})
}

func TestApplyInventoryRestoresStoredIDAndQueuesCurrentFingerprint(t *testing.T) {
	stored := storedAudioInventoryFile("restored.mp3")
	missingAt := inventoryInstant(5)
	stored.MissingAt = &missingAt
	repository, pool, scan := inventoryRepositoryFixture(t, stored)

	delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{{
			ID: uuid.New(), RootID: "audiobooks", RelativePath: stored.Path,
			FileKind: "audio", Size: stored.Size + 1, ModifiedAt: inventoryInstant(6),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.ProbeFiles) != 1 || delta.ProbeFiles[0].ID != stored.ID {
		t.Fatalf("restored probe files=%#v, want stored ID %s", delta.ProbeFiles, stored.ID)
	}
	var actualID uuid.UUID
	var stillMissing bool
	if err := pool.QueryRow(context.Background(), `
		select id, missing_at is not null from source_files
		where root_id='audiobooks' and relative_path=$1
	`, stored.Path).Scan(&actualID, &stillMissing); err != nil {
		t.Fatal(err)
	}
	if actualID != stored.ID || stillMissing {
		t.Fatalf("restored ID/missing=%s/%v, want %s/false", actualID, stillMissing, stored.ID)
	}
}

func TestApplyInventoryReturnsDeterministicSortedDeltas(t *testing.T) {
	repository, _, scan := inventoryRepositoryFixture(t)
	delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{
			inventoryFile(uuid.New(), "z.mp3", "audio", "", 1, inventoryInstant(1)),
			inventoryFile(uuid.New(), "a.nfo", "companion", "nfo", 1, inventoryInstant(1)),
			inventoryFile(uuid.New(), "b.mp3", "audio", "", 1, inventoryInstant(1)),
			inventoryFile(uuid.New(), "a.cue", "companion", "cue", 1, inventoryInstant(1)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryDeltaPaths(t, delta.ProbeFiles, "b.mp3", "z.mp3")
	assertInventoryDeltaPaths(t, delta.InspectCompanions, "a.cue", "a.nfo")
	if !slices.Equal(delta.RegroupScopes, []string{""}) {
		t.Fatalf("regroup scopes=%v, want root scope", delta.RegroupScopes)
	}
}

func TestApplyInventoryValidatesAtomically(t *testing.T) {
	tests := []struct {
		name  string
		files []InventoryFile
	}{
		{
			name: "unsafe path",
			files: []InventoryFile{
				inventoryAudioFile("valid.mp3"),
				inventoryAudioFile("../escape.mp3"),
			},
		},
		{
			name: "duplicate path",
			files: []InventoryFile{
				inventoryAudioFile("duplicate.mp3"),
				inventoryAudioFile("duplicate.mp3"),
			},
		},
		{
			name: "wrong root",
			files: []InventoryFile{
				inventoryAudioFile("valid.mp3"),
				{ID: uuid.New(), RootID: "other", RelativePath: "other.mp3", FileKind: "audio", Size: 1, ModifiedAt: inventoryInstant(1)},
			},
		},
		{
			name: "file outside scope",
			files: []InventoryFile{
				inventoryAudioFile("Other/file.mp3"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, pool, scan := inventoryRepositoryFixture(t)
			update := InventoryUpdate{
				ScanRunID: scan.ID, RootID: "audiobooks", Complete: true, Files: test.files,
			}
			if test.name == "file outside scope" {
				update.ScopePath = "Book"
			}
			_, err := repository.ApplyInventory(context.Background(), update)
			if !errors.Is(err, ErrInvalidData) && !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("ApplyInventory() error=%v, want validation error", err)
			}
			var count int
			if queryErr := pool.QueryRow(context.Background(), `select count(*) from source_files`).Scan(&count); queryErr != nil {
				t.Fatal(queryErr)
			}
			if count != 0 {
				t.Fatalf("invalid inventory wrote %d rows", count)
			}
		})
	}
}

func TestApplyInventoryRejectsInactiveScanWithoutWriting(t *testing.T) {
	repository, pool, scan := inventoryRepositoryFixture(t)
	if err := repository.FinishScan(context.Background(), scan.ID, ScanResult{
		Partial: true,
		Error:   "inventory interrupted",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{inventoryAudioFile("late.mp3")},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyInventory(inactive scan)=%v, want ErrConflict", err)
	}
	var count int
	if queryErr := pool.QueryRow(context.Background(), `select count(*) from source_files`).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatalf("inactive inventory wrote %d rows", count)
	}
}

func TestIncrementCatalogGenerationIsMonotonicAndRejectsUnknownRoot(t *testing.T) {
	repository, _, _ := inventoryRepositoryFixture(t)
	ctx := context.Background()

	first, err := repository.IncrementCatalogGeneration(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.IncrementCatalogGeneration(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if first.RootID != "audiobooks" || first.Value != 2 ||
		second.RootID != "audiobooks" || second.Value != 3 ||
		second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("generations first=%#v second=%#v, want values 2 then 3", first, second)
	}
	if _, err := repository.IncrementCatalogGeneration(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("IncrementCatalogGeneration(unknown)=%v, want ErrNotFound", err)
	}
}

type storedInventoryFile struct {
	ID            uuid.UUID
	Path          string
	Size          int64
	ModifiedAt    time.Time
	FileKind      string
	CompanionKind string
	ProbeStatus   string
	ProbeVersion  int
	ProbeError    string
	ProbedAt      *time.Time
	Media         map[string]any
	MissingAt     *time.Time
}

func inventoryRepositoryFixture(
	t *testing.T,
	files ...storedInventoryFile,
) (*PostgresRepository, *pgxpool.Pool, ScanRun) {
	t.Helper()
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "audiobooks", DisplayName: "Audiobooks"}}); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		mediaValues := file.Media
		if mediaValues == nil {
			mediaValues = map[string]any{}
		}
		media, err := json.Marshal(mediaValues)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			insert into source_files(
				id, root_id, relative_path, size, modified_at, media, missing_at,
				file_kind, companion_kind, probe_status, probe_version, probe_error, probed_at
			) values (
				$1, 'audiobooks', $2, $3, $4, $5::jsonb, $6,
				$7, nullif($8, ''), $9, $10, nullif($11, ''), $12
			)
		`, file.ID, file.Path, file.Size, file.ModifiedAt, string(media), file.MissingAt,
			file.FileKind, file.CompanionKind, file.ProbeStatus, file.ProbeVersion,
			file.ProbeError, file.ProbedAt); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	return repository, pool, scan
}

func storedAudioInventoryFile(relativePath string) storedInventoryFile {
	return storedInventoryFile{
		ID: uuid.New(), Path: relativePath, Size: 1, ModifiedAt: inventoryInstant(1),
		FileKind: "audio", ProbeStatus: "succeeded", ProbeVersion: 1,
		Media: map[string]any{"title": relativePath},
	}
}

func inventoryAudioFile(relativePath string) InventoryFile {
	return inventoryFile(uuid.New(), relativePath, "audio", "", 1, inventoryInstant(1))
}

func inventoryFile(
	id uuid.UUID,
	relativePath string,
	fileKind string,
	companionKind string,
	size int64,
	modifiedAt time.Time,
) InventoryFile {
	return InventoryFile{
		ID: id, RootID: "audiobooks", RelativePath: relativePath,
		FileKind: fileKind, CompanionKind: companionKind,
		Size: size, ModifiedAt: modifiedAt,
	}
}

func inventoryInstant(hour int) time.Time {
	return time.Date(2026, time.August, 8, hour, 0, 0, 0, time.UTC)
}

func inventoryMissingCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		select count(*) from source_files where missing_at is not null
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertInventoryDeltaPaths(t *testing.T, files []SourceFile, want ...string) {
	t.Helper()
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.RelativePath
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths=%v, want %v", paths, want)
	}
}
