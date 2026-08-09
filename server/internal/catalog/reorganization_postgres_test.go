package catalog

import (
	"context"
	"encoding/json"
	"testing"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
	"github.com/google/uuid"
)

func TestCommitReorganizationAtomicallyPersistsGroupingAndFollowups(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID: "audiobooks", DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}

	audioID, sidecarID := uuid.New(), uuid.New()
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	modified := inventoryInstant(1)
	if _, err := repository.ApplyInventory(ctx, InventoryUpdate{
		ScanRunID: scan.ID, RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{
			inventoryFile(audioID, "Incoming/Book/book.m4b", "audio", "", 100, modified),
			inventoryFile(sidecarID, "Incoming/Book/book.nfo", "companion", "nfo", 10, modified),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, scan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	book := inferredBook("Book", "Author", 2026, audioID)
	book.SourceFolder = "Incoming/Book"
	book.GroupingKey = "book"
	book.SourceFileIDs = []uuid.UUID{audioID, sidecarID}
	book.FileRoles = map[uuid.UUID]string{sidecarID: FileRoleSidecar}
	if err := repository.ReplaceInferredBooks(ctx, "audiobooks", []InferredBook{book}); err != nil {
		t.Fatal(err)
	}
	var bookID uuid.UUID
	if err := pool.QueryRow(ctx, `select id from books where root_id='audiobooks' and missing_at is null`).Scan(&bookID); err != nil {
		t.Fatal(err)
	}

	preview, err := repository.BuildReorganizationPreview(ctx, ReorganizationRequest{
		BookID: bookID, CatalogGeneration: 1, Actor: "owner",
		Groups: []GroupingGroup{{
			BookID: &bookID, GroupingKey: "book", FileIDs: []uuid.UUID{audioID, sidecarID},
		}},
		TargetFolders: map[string]string{"book": "Author/Book"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.QueueReorganization(ctx, preview.OperationID, "owner"); err != nil {
		t.Fatal(err)
	}
	operation, err := repository.LoadReorganization(ctx, preview.OperationID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetReorganizationStatus(ctx, operation.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitReorganizationGrouping(ctx, operation); err != nil {
		t.Fatal(err)
	}

	var generation int64
	var status, folder, reviewState, reason string
	var groupingLocked bool
	if err := pool.QueryRow(ctx, `select generation from catalog_generations where root_id='audiobooks'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 2 {
		t.Fatalf("generation = %d, want 2", generation)
	}
	if err := pool.QueryRow(ctx, `select status from reorganization_operations where id=$1`, operation.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("operation status = %q, want succeeded", status)
	}
	if err := pool.QueryRow(ctx, `select source_folder,grouping_locked,review_state from books where id=$1`, bookID).Scan(&folder, &groupingLocked, &reviewState); err != nil {
		t.Fatal(err)
	}
	if folder != "Author/Book" || !groupingLocked || reviewState != ReviewConfirmed {
		t.Fatalf("book state = folder %q locked %t review %q", folder, groupingLocked, reviewState)
	}
	if err := pool.QueryRow(ctx, `select reason_code from book_grouping_evidence where book_id=$1`, bookID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "manual" {
		t.Fatalf("evidence reason = %q, want manual", reason)
	}

	rows, err := pool.Query(ctx, `select kind,payload from worker_jobs where kind in ('scan_root','publish_search') order by kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	jobs := make(map[string]map[string]any)
	for rows.Next() {
		var kind string
		var encoded []byte
		if err := rows.Scan(&kind, &encoded); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		jobs[kind] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[JobScanRoot]["full"] != true ||
		jobs[JobPublishSearch]["catalogGeneration"] != float64(2) {
		t.Fatalf("follow-up jobs = %#v", jobs)
	}
}
