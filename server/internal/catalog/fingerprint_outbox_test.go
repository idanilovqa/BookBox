package catalog

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/media"
)

func TestDrainReadyFingerprintOutboxRequiresSuccessfulPublishedScan(t *testing.T) {
	repository, pool, scan := inventoryRepositoryFixture(t)
	file := inventoryAudioFile("Book/new.mp3")
	if _, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: false,
		Files: []InventoryFile{file},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(context.Background(), scan.ID, ScanResult{
		Success: false, Partial: true, Error: "incomplete",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DrainReadyFingerprintOutbox(context.Background(), time.Now().UTC(), 256); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := pool.QueryRow(context.Background(), `select count(*) from worker_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("partial scan produced %d claimable jobs", jobs)
	}
}

func TestDrainReadyFingerprintOutboxDeduplicatesActiveAndRedeliversDeadAtCurrentPath(t *testing.T) {
	repository, pool, scan := inventoryRepositoryFixture(t)
	file := inventoryAudioFile("Book/old.mp3")
	if _, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{file},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		update scan_runs set status='succeeded', finished_at=now() where id=$1
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update catalog_generations
		set generation=2, inventory_scan_run_id=$1 where root_id='audiobooks'
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update source_files set relative_path='Book/current.mp3' where id=$1
	`, file.ID); err != nil {
		t.Fatal(err)
	}
	insertFingerprintIntent(t, pool, scan.ID, SourceFile{
		ID: file.ID, RootID: "audiobooks", RelativePath: "Book/current.mp3",
		Size: file.Size, ModifiedAt: file.ModifiedAt, FileKind: FileKindAudio,
	})
	key := JobProbeFile + ":" + file.ID.String() + ":" + strconv.FormatInt(file.Size, 10) +
		":" + strconv.FormatInt(file.ModifiedAt.UnixNano(), 10)
	activeJobID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into worker_jobs(
			id,kind,dedupe_key,payload,status,attempts,max_attempts
		) values ($1,$2,$3,'{}'::jsonb,'queued',0,5)
	`, activeJobID, JobProbeFile, key); err != nil {
		t.Fatal(err)
	}
	if dispatched, err := repository.DrainReadyFingerprintOutbox(ctx, time.Now().UTC(), 256); err != nil {
		t.Fatal(err)
	} else if dispatched != 0 {
		t.Fatalf("active delivery dispatched %d duplicate jobs", dispatched)
	}
	if _, err := pool.Exec(ctx, `
		update worker_jobs set status='dead',attempts=max_attempts,last_error='exhausted'
		where id=$1
	`, activeJobID); err != nil {
		t.Fatal(err)
	}
	if dispatched, err := repository.DrainReadyFingerprintOutbox(ctx, time.Now().UTC(), 256); err != nil {
		t.Fatal(err)
	} else if dispatched != 1 {
		t.Fatalf("dead delivery replacement count=%d, want 1", dispatched)
	}
	var path, gotKey, size, nanos string
	if err := pool.QueryRow(ctx, `
		select payload->>'relativePath',dedupe_key,payload->>'size',payload->>'modifiedNanos'
		from worker_jobs where status='queued'
	`).Scan(&path, &gotKey, &size, &nanos); err != nil {
		t.Fatal(err)
	}
	if path != "Book/current.mp3" || gotKey != key || size != strconv.FormatInt(file.Size, 10) ||
		nanos != strconv.FormatInt(file.ModifiedAt.UnixNano(), 10) {
		t.Fatalf("recovered path/key/fingerprint=%q/%q/%q/%q", path, gotKey, size, nanos)
	}
}

func TestActivateFingerprintOutboxPrunesMissingIntent(t *testing.T) {
	repository, pool, scan := inventoryRepositoryFixture(t)
	file := inventoryAudioFile("Book/missing.mp3")
	if _, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{file},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	insertFingerprintIntent(t, pool, scan.ID, SourceFile{
		ID: file.ID, RootID: "audiobooks", RelativePath: file.RelativePath,
		Size: file.Size, ModifiedAt: file.ModifiedAt, FileKind: FileKindAudio,
	})
	if _, err := pool.Exec(ctx, `
		update scan_runs set status='succeeded', finished_at=now() where id=$1
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update catalog_generations set generation=2, inventory_scan_run_id=$1 where root_id='audiobooks'
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update source_files set missing_at=now() where id=$1`, file.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := activateFingerprintOutboxTx(ctx, tx, "audiobooks", scan.ID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var intents, jobs int
	if err := pool.QueryRow(ctx, `select count(*) from fingerprint_job_outbox`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from worker_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || jobs != 0 {
		t.Fatalf("missing file left intents/jobs=%d/%d", intents, jobs)
	}
}

func TestDrainReadyFingerprintOutboxBoundsDispatchWithoutActiveJobStarvation(t *testing.T) {
	repository, pool, scan := inventoryRepositoryFixture(t)
	files := []InventoryFile{
		inventoryAudioFile("Book/01.mp3"),
		inventoryAudioFile("Book/02.mp3"),
		inventoryAudioFile("Book/03.mp3"),
	}
	if _, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true, Files: files,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		update scan_runs set status='succeeded',finished_at=now() where id=$1
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update catalog_generations set inventory_scan_run_id=$1 where root_id='audiobooks'
	`, scan.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := activateFingerprintOutboxTx(ctx, tx, "audiobooks", scan.ID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if dispatched, err := repository.DrainReadyFingerprintOutbox(ctx, now, 2); err != nil {
		t.Fatal(err)
	} else if dispatched != 2 {
		t.Fatalf("first bounded drain=%d, want 2", dispatched)
	}
	if dispatched, err := repository.DrainReadyFingerprintOutbox(ctx, now, 2); err != nil {
		t.Fatal(err)
	} else if dispatched != 1 {
		t.Fatalf("second bounded drain=%d, want remaining 1", dispatched)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `select count(*) from worker_jobs where status='queued'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 3 {
		t.Fatalf("queued fingerprint jobs=%d, want 3", jobs)
	}
}

func TestSaveProbeResultAcknowledgesOutboxByFingerprintNotPath(t *testing.T) {
	modified := inventoryInstant(2)
	stored := storedInventoryFile{
		ID: uuid.New(), Path: "Book/current.mp3", Size: 17, ModifiedAt: modified,
		FileKind: FileKindAudio, ProbeStatus: ProbePending,
	}
	repository, pool, scan := inventoryRepositoryFixture(t, stored)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		insert into fingerprint_job_outbox(
			file_id,root_id,job_kind,relative_path,expected_size,
			expected_modified_at,expected_modified_nanos,scan_run_id,next_dispatch_at
		) values ($1,'audiobooks','probe_file',$2,$3,$4,$5,$6,$7)
	`, stored.ID, stored.Path, stored.Size, postgresTime(modified), modified.UnixNano(),
		scan.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	expected := SourceFile{
		ID: stored.ID, RootID: "audiobooks", RelativePath: "Book/old.mp3",
		Size: stored.Size, ModifiedAt: modified, FileKind: FileKindAudio,
	}
	if _, _, err := repository.SaveProbeResult(ctx, expected, media.Metadata{Title: "Book"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := fingerprintOutboxCount(t, pool, stored.ID); got != 0 {
		t.Fatalf("matching probe result left %d outbox rows", got)
	}
	var status string
	if err := pool.QueryRow(ctx, `select probe_status from source_files where id=$1`, stored.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != ProbeSucceeded {
		t.Fatalf("probe status=%q, want %q", status, ProbeSucceeded)
	}
}

func TestFailedProbeAtOldPathDoesNotAcknowledgeReorganizedOutbox(t *testing.T) {
	modified := inventoryInstant(2)
	stored := storedInventoryFile{
		ID: uuid.New(), Path: "Book/current.mp3", Size: 17, ModifiedAt: modified,
		FileKind: FileKindAudio, ProbeStatus: ProbePending,
	}
	repository, pool, scan := inventoryRepositoryFixture(t, stored)
	insertFingerprintIntent(t, pool, scan.ID, SourceFile{
		ID: stored.ID, RootID: "audiobooks", RelativePath: stored.Path,
		Size: stored.Size, ModifiedAt: modified, FileKind: FileKindAudio,
	})
	expected := SourceFile{
		ID: stored.ID, RootID: "audiobooks", RelativePath: "Book/old.mp3",
		Size: stored.Size, ModifiedAt: modified, FileKind: FileKindAudio,
	}
	if _, _, err := repository.SaveProbeResult(
		context.Background(), expected, media.Metadata{}, "probe_failed",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("old-path failure error=%v, want ErrConflict", err)
	}
	if got := fingerprintOutboxCount(t, pool, stored.ID); got != 1 {
		t.Fatalf("old-path failure left %d outbox rows, want 1", got)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		select probe_status from source_files where id=$1
	`, stored.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != ProbePending {
		t.Fatalf("old-path failure changed status to %q", status)
	}
}

func insertFingerprintIntent(
	t *testing.T,
	pool *pgxpool.Pool,
	scanID uuid.UUID,
	file SourceFile,
) {
	t.Helper()
	kind := JobProbeFile
	if file.FileKind == FileKindCompanion {
		kind = JobInspectCompanion
	}
	if _, err := pool.Exec(context.Background(), `
		insert into fingerprint_job_outbox(
			file_id,root_id,job_kind,relative_path,expected_size,
			expected_modified_at,expected_modified_nanos,scan_run_id,next_dispatch_at
		) values ($1,$2,$3,$4,$5,$6,$7,$8,now())
	`, file.ID, file.RootID, kind, file.RelativePath, file.Size,
		postgresTime(file.ModifiedAt), postgresTime(file.ModifiedAt).UnixNano(), scanID); err != nil {
		t.Fatal(err)
	}
}
