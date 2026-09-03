package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestBuildSearchProjectionCarriesReviewReasonAndGeneration(t *testing.T) {
	bookID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fileID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	input := SearchProjectionInput{
		RootID:      "audiobooks",
		ScanRunID:   uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		CompletedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Generation:  CatalogGeneration{RootID: "audiobooks", Value: 7},
		Books: []Book{{
			ID: bookID, RootID: "audiobooks", SourceFolder: "Broken Probe",
			Title: "Broken Probe", ReviewState: ReviewNeedsReview,
		}},
		Files: []SourceFile{
			projectionFile(fileID, "Broken Probe/book.m4b", FileRoleAudio, 0),
		},
		Assignments: map[uuid.UUID][]uuid.UUID{bookID: {fileID}},
		Evidence: map[uuid.UUID]GroupingEvidence{
			bookID: {
				ReasonCode:  "probe_failed",
				Confidence:  0.25,
				ReviewState: ReviewNeedsReview,
				Values:      map[string][]string{"probeError": {"unsupported stream"}},
			},
		},
	}

	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	document := audiobookDocument(t, projection, bookID)
	if document.ReviewState != ReviewNeedsReview || document.ReviewReason != "probe_failed" {
		t.Fatalf("review state=%q reason=%q", document.ReviewState, document.ReviewReason)
	}
	if projection.Snapshot.CatalogGeneration != 7 {
		t.Fatalf("catalog generation=%d, want 7", projection.Snapshot.CatalogGeneration)
	}
}

func TestBuildSearchProjectionOmitsReviewReasonForUnreviewedBooks(t *testing.T) {
	bookID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	fileID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	projection, err := BuildSearchProjection(SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(),
		CompletedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Generation:  CatalogGeneration{RootID: "audiobooks", Value: 2},
		Books: []Book{{
			ID: bookID, RootID: "audiobooks", SourceFolder: "Mixed Shelf",
			Title: "Dune", ReviewState: ReviewUnreviewed,
		}},
		Files: []SourceFile{
			projectionFile(fileID, "Mixed Shelf/Dune.m4b", FileRoleAudio, 0),
		},
		Assignments: map[uuid.UUID][]uuid.UUID{bookID: {fileID}},
		Evidence: map[uuid.UUID]GroupingEvidence{
			bookID: {ReasonCode: "distinct_identity", ReviewState: ReviewUnreviewed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := audiobookDocument(t, projection, bookID)
	if document.ReviewReason != "" {
		t.Fatalf("unreviewed document reason = %q, want empty", document.ReviewReason)
	}
}

func TestBuildSearchProjectionCarriesCorruptedMetadataWarningAcrossGroupingStates(t *testing.T) {
	for _, state := range []string{ReviewConfirmed, ReviewUnreviewed} {
		t.Run(state, func(t *testing.T) {
			input := oneBookProjectionInput(1_000)
			input.Books[0].ReviewState = state
			input.Evidence = map[uuid.UUID]GroupingEvidence{
				input.Books[0].ID: {ReasonCode: ReviewReasonCorrupt, ReviewState: state},
			}
			projection, err := BuildSearchProjection(input)
			if err != nil {
				t.Fatal(err)
			}
			document := audiobookDocument(t, projection, input.Books[0].ID)
			if document.ReviewState != state || document.ReviewReason != ReviewReasonCorrupt {
				t.Fatalf("review state=%q reason=%q", document.ReviewState, document.ReviewReason)
			}
		})
	}
}

func TestBuildSearchProjectionBackfillsLegacyNeedsReviewReason(t *testing.T) {
	input := oneBookProjectionInput(1_000)
	input.Books[0].ReviewState = ReviewNeedsReview
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	document := audiobookDocument(t, projection, input.Books[0].ID)
	if document.ReviewReason != ReviewReasonLegacy {
		t.Fatalf("legacy review reason=%q, want %q", document.ReviewReason, ReviewReasonLegacy)
	}
}

func TestBuildSearchProjectionRequiresEveryActiveAudioExactlyOnce(t *testing.T) {
	firstBookID := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	secondBookID := uuid.MustParse("66666666-6666-4666-8666-666666666666")
	audioID := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	base := SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(),
		CompletedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Generation:  CatalogGeneration{RootID: "audiobooks", Value: 2},
		Books: []Book{
			{ID: firstBookID, RootID: "audiobooks", SourceFolder: "Shelf", Title: "First", ReviewState: ReviewUnreviewed},
			{ID: secondBookID, RootID: "audiobooks", SourceFolder: "Shelf", Title: "Second", ReviewState: ReviewUnreviewed},
		},
		Files: []SourceFile{projectionFile(audioID, "Shelf/book.m4b", FileRoleAudio, 1)},
	}

	for name, assignments := range map[string]map[uuid.UUID][]uuid.UUID{
		"unassigned": {},
		"assigned to two visible books": {
			firstBookID:  {audioID},
			secondBookID: {audioID},
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Assignments = assignments
			if _, err := BuildSearchProjection(input); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("BuildSearchProjection() error=%v, want ErrInvalidData", err)
			}
		})
	}
}

func TestPublishSearchProjectionRejectsStaleGenerationAndPreservesPriorSnapshot(t *testing.T) {
	repository, pool, ctx, scan := task6RepositoryFixture(t)
	prior := task6Projection(t, "audiobooks", scan.ID, 1, "Prior Title")
	if err := repository.PublishSearchProjection(ctx, prior); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.IncrementCatalogGeneration(ctx, "audiobooks"); err != nil {
		t.Fatal(err)
	}

	stale := task6Projection(t, "audiobooks", scan.ID, 1, "Stale Replacement")
	if err := repository.PublishSearchProjection(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("PublishSearchProjection(stale)=%v, want ErrConflict", err)
	}

	var scanID uuid.UUID
	var generation int64
	var title string
	if err := pool.QueryRow(ctx, `
		select snapshots.scan_run_id, snapshots.catalog_generation, documents.display_name
		from library_search_snapshots snapshots
		join library_search_documents documents on documents.root_id=snapshots.root_id
		where snapshots.root_id='audiobooks' and documents.entity_kind='folder'
	`).Scan(&scanID, &generation, &title); err != nil {
		t.Fatal(err)
	}
	if scanID != prior.Snapshot.ScanRunID || generation != 1 || title != "Prior Title" {
		t.Fatalf("preserved snapshot scan=%s generation=%d title=%q", scanID, generation, title)
	}
	if current := task6CatalogGeneration(t, pool, ctx); current != 2 {
		t.Fatalf("current generation=%d, want 2", current)
	}
}

func TestFinalizeInventoryAndPublishRollsBackBooksGenerationAndProjection(t *testing.T) {
	repository, pool, ctx, firstScan := task6RepositoryFixture(t)
	firstFile := searchTestFile("audiobooks", "Dune/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, firstScan.ID, []SourceFile{firstFile}); err != nil {
		t.Fatal(err)
	}
	first, err := repository.FinalizeInventoryAndPublish(ctx, InventoryFinalization{
		ScanRunID: firstScan.ID,
		RootID:    "audiobooks",
		Books:     []InferredBook{searchTestBook("audiobooks", "Dune", "dune", firstFile.ID)},
		Evidence: map[string]GroupingEvidence{
			"dune": {ReasonCode: "folder_default", Confidence: 0.8, ReviewState: ReviewUnreviewed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Value != 2 {
		t.Fatalf("first published generation=%d, want 2", first.Value)
	}

	secondScan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	brokenFile := searchTestFile("audiobooks", "Broken/book.m4b", "document")
	if err := repository.UpsertSourceFiles(ctx, secondScan.ID, []SourceFile{brokenFile}); err != nil {
		t.Fatal(err)
	}
	_, err = repository.FinalizeInventoryAndPublish(ctx, InventoryFinalization{
		ScanRunID: secondScan.ID,
		RootID:    "audiobooks",
		Books:     []InferredBook{searchTestBook("audiobooks", "Broken", "broken", brokenFile.ID)},
		Evidence: map[string]GroupingEvidence{
			"broken": {ReasonCode: "folder_default", Confidence: 0.8, ReviewState: ReviewUnreviewed},
		},
	})
	if !errors.Is(err, ErrInvalidData) {
		t.Fatalf("FinalizeInventoryAndPublish(broken)=%v, want ErrInvalidData", err)
	}

	var generation, snapshotGeneration int64
	var snapshotScan uuid.UUID
	var scanStatus string
	var duneBooks, brokenBooks int
	if err := pool.QueryRow(ctx, `select generation from catalog_generations where root_id='audiobooks'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select scan_run_id, catalog_generation
		from library_search_snapshots where root_id='audiobooks'
	`).Scan(&snapshotScan, &snapshotGeneration); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select status from scan_runs where id=$1`, secondScan.ID).Scan(&scanStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from books where root_id='audiobooks' and grouping_key='dune' and missing_at is null`).Scan(&duneBooks); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from books where root_id='audiobooks' and grouping_key='broken'`).Scan(&brokenBooks); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || snapshotGeneration != 2 || snapshotScan != firstScan.ID ||
		scanStatus != "running" || duneBooks != 1 || brokenBooks != 0 {
		t.Fatalf("rollback generation=%d snapshot=%d/%s scan=%q dune=%d broken=%d",
			generation, snapshotGeneration, snapshotScan, scanStatus, duneBooks, brokenBooks)
	}
}

func TestApplyGroupingRefinementUsesCurrentGenerationAndRollsBackFailures(t *testing.T) {
	repository, pool, ctx, scan := task6RepositoryFixture(t)
	file := searchTestFile("audiobooks", "Dune/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
		t.Fatal(err)
	}
	initial, err := repository.FinalizeInventoryAndPublish(ctx, InventoryFinalization{
		ScanRunID: scan.ID, RootID: "audiobooks",
		Books: []InferredBook{searchTestBook("audiobooks", "Dune", "dune", file.ID)},
		Evidence: map[string]GroupingEvidence{
			"dune": {ReasonCode: "folder_default", Confidence: 0.8, ReviewState: ReviewUnreviewed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	refinedBook := searchTestBook("audiobooks", "Dune", "dune", file.ID)
	refinedBook.Title = "Dune Refined"
	refined, err := repository.ApplyGroupingRefinement(ctx, GroupingRefinement{
		RootID: "audiobooks", ScopePath: "Dune", ExpectedGeneration: initial.Value,
		Books: []InferredBook{refinedBook},
		Evidence: map[string]GroupingEvidence{
			"dune": {ReasonCode: "distinct_identity", Confidence: 0.95, ReviewState: ReviewUnreviewed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if refined.Value != initial.Value+1 {
		t.Fatalf("refined generation=%d, want %d", refined.Value, initial.Value+1)
	}
	assertTask6RefinementState(t, pool, ctx, refined.Value, initial.Value, "Dune Refined", "Dune")
	projection, err := repository.LoadSearchProjection(ctx, "audiobooks", refined.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSearchProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	assertTask6RefinementState(t, pool, ctx, refined.Value, refined.Value, "Dune Refined", "Dune Refined")

	staleBook := refinedBook
	staleBook.Title = "Stale Title"
	if _, err := repository.ApplyGroupingRefinement(ctx, GroupingRefinement{
		RootID: "audiobooks", ScopePath: "Dune", ExpectedGeneration: initial.Value,
		Books: []InferredBook{staleBook},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyGroupingRefinement(stale)=%v, want ErrConflict", err)
	}
	assertTask6RefinementState(t, pool, ctx, refined.Value, refined.Value, "Dune Refined", "Dune Refined")

	invalidBook := refinedBook
	invalidBook.Title = "Must Roll Back"
	if _, err := repository.ApplyGroupingRefinement(ctx, GroupingRefinement{
		RootID: "audiobooks", ScopePath: "Dune", ExpectedGeneration: refined.Value,
		Books: []InferredBook{invalidBook},
		Evidence: map[string]GroupingEvidence{
			"dune": {ReasonCode: "unknown_reason", Confidence: 1, ReviewState: ReviewUnreviewed},
		},
	}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ApplyGroupingRefinement(invalid evidence)=%v, want ErrInvalidData", err)
	}
	assertTask6RefinementState(t, pool, ctx, refined.Value, refined.Value, "Dune Refined", "Dune Refined")
}

func TestApplyGroupingRefinementCommitsWhenProjectionBuildFails(t *testing.T) {
	repository, pool, ctx, scan := task6RepositoryFixture(t)
	file := searchTestFile("audiobooks", "Dune/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
		t.Fatal(err)
	}
	initial, err := repository.FinalizeInventoryAndPublish(ctx, InventoryFinalization{
		ScanRunID: scan.ID, RootID: "audiobooks",
		Books: []InferredBook{searchTestBook("audiobooks", "Dune", "dune", file.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	orphanID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into source_files(id, root_id, relative_path, size, modified_at, media, file_kind)
		values ($1, 'audiobooks', 'Loose/orphan.m4b', 1, now(), '{"role":"audio","durationMs":1}'::jsonb, 'audio')
	`, orphanID); err != nil {
		t.Fatal(err)
	}
	refinedBook := searchTestBook("audiobooks", "Dune", "dune", file.ID)
	refinedBook.Title = "Dune Refined"
	refined, err := repository.ApplyGroupingRefinement(ctx, GroupingRefinement{
		RootID: "audiobooks", ScopePath: "Dune", ExpectedGeneration: initial.Value,
		Books: []InferredBook{refinedBook},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LoadSearchProjection(ctx, "audiobooks", refined.Value); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("LoadSearchProjection()=%v, want ErrInvalidData", err)
	}
	assertTask6RefinementState(t, pool, ctx, refined.Value, initial.Value, "Dune Refined", "Dune")
}

func task6RepositoryFixture(t *testing.T) (*PostgresRepository, *pgxpool.Pool, context.Context, ScanRun) {
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
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	return repository, pool, ctx, scan
}

func task6Projection(t *testing.T, rootID string, scanID uuid.UUID, generation int64, title string) SearchProjection {
	t.Helper()
	fileID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(title+" file"))
	projection, err := BuildSearchProjection(SearchProjectionInput{
		RootID: rootID, ScanRunID: scanID, CompletedAt: time.Now().UTC(),
		Generation: CatalogGeneration{RootID: rootID, Value: generation},
		Files: []SourceFile{{
			ID: fileID, RootID: rootID, RelativePath: title + "/cover.jpg", Size: 1,
			ModifiedAt: time.Now().UTC(),
			Media:      map[string]any{"role": FileRoleCover},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func task6CatalogGeneration(t *testing.T, pool *pgxpool.Pool, ctx context.Context) int64 {
	t.Helper()
	var generation int64
	if err := pool.QueryRow(ctx, `select generation from catalog_generations where root_id='audiobooks'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func assertTask6RefinementState(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	wantGeneration int64,
	wantSnapshotGeneration int64,
	wantCatalogTitle string,
	wantPublishedTitle string,
) {
	t.Helper()
	var generation, snapshotGeneration int64
	var catalogTitle, publishedTitle string
	if err := pool.QueryRow(ctx, `select generation from catalog_generations where root_id='audiobooks'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select title from books where root_id='audiobooks' and grouping_key='dune' and missing_at is null
	`).Scan(&catalogTitle); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select snapshots.catalog_generation, documents.title
		from library_search_snapshots snapshots
		join library_search_documents documents on documents.root_id=snapshots.root_id
		where snapshots.root_id='audiobooks' and documents.entity_kind='audiobook'
	`).Scan(&snapshotGeneration, &publishedTitle); err != nil {
		t.Fatal(err)
	}
	if generation != wantGeneration || snapshotGeneration != wantSnapshotGeneration ||
		catalogTitle != wantCatalogTitle || publishedTitle != wantPublishedTitle {
		t.Fatalf("generation=%d snapshot=%d catalog=%q published=%q, want %d/%d/%q/%q",
			generation, snapshotGeneration, catalogTitle, publishedTitle,
			wantGeneration, wantSnapshotGeneration, wantCatalogTitle, wantPublishedTitle)
	}
}
