package database

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bookbox.local/server/internal/testpostgres"
	"bookbox.local/server/migrations"
)

func TestMigrateCreatesCatalogTablesOnce(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()

	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- Migrate(ctx, pool)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, table := range []string{
		"library_roots",
		"scan_runs",
		"source_files",
		"books",
		"book_files",
		"grouping_corrections",
		"metadata_matches",
		"cover_events",
		"worker_jobs",
		"availability_events",
		"library_search_snapshots",
		"library_search_documents",
	} {
		var exists bool
		if err := pool.QueryRow(
			ctx,
			`select to_regclass('public.' || $1) is not null`,
			table,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s exists=%v err=%v", table, exists, err)
		}
	}

	var applied int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 11 {
		t.Fatalf("schema migration rows = %d, want 11", applied)
	}

	for _, constraint := range []string{
		"book_grouping_evidence_reason_code_check",
		"library_search_documents_review_state_reason_check",
	} {
		var allowsCorruptedMetadata bool
		if err := pool.QueryRow(ctx, `
			select position('corrupted_metadata' in pg_get_constraintdef(oid)) > 0
			from pg_constraint
			where conname=$1
		`, constraint).Scan(&allowsCorruptedMetadata); err != nil {
			t.Fatal(err)
		}
		if !allowsCorruptedMetadata {
			t.Fatalf("constraint %s rejects corrupted metadata warnings", constraint)
		}
	}

	var rejectsCrossRootScan bool
	if err := pool.QueryRow(ctx, `
		select exists (
			select 1
			from pg_constraint
			where conrelid='source_files'::regclass
			  and contype='f'
			  and pg_get_constraintdef(oid) like '%(scan_run_id, root_id)%'
		)
	`).Scan(&rejectsCrossRootScan); err != nil {
		t.Fatal(err)
	}
	if !rejectsCrossRootScan {
		t.Fatal("source_files lacks a composite scan/root foreign key")
	}

	if _, err := pool.Exec(ctx, `
		insert into library_roots(id, display_name)
		values ('one','One'), ('two','Two')
	`); err != nil {
		t.Fatal(err)
	}
	scanID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into scan_runs(id, root_id, started_at, status)
		values ($1, 'one', $2, 'running')
	`, scanID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into source_files(
			id, root_id, relative_path, size, modified_at, scan_run_id
		)
		values ($1, 'two', 'Book/file.m4b', 1, $2, $3)
	`, uuid.New(), time.Now().UTC(), scanID); err == nil {
		t.Fatal("source_files accepted a scan run belonging to another root")
	}
}

func TestMigrateBackfillsOnlyNeverEnqueuedPendingFingerprints(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	for version, name := range []string{
		"0001_catalog.sql",
		"0002_worker_job_deduplication.sql",
		"0003_library_search.sql",
		"0004_staged_catalog.sql",
		"0005_reorganization_payload.sql",
		"0006_library_search_review_invariant.sql",
	} {
		contents, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx,
			`insert into schema_migrations(version) values ($1)`, version+1,
		); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}

	const rootID = "pending-backfill"
	if _, err := pool.Exec(ctx,
		`insert into library_roots(id, display_name) values ($1, 'Pending backfill')`, rootID,
	); err != nil {
		t.Fatal(err)
	}
	publishedScan := uuid.New()
	runningScan := uuid.New()
	partialScan := uuid.New()
	failedScan := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into scan_runs(id,root_id,started_at,finished_at,status,error) values
			($1,$5,'2026-08-08T11:00:00Z','2026-08-08T11:30:00Z','succeeded',null),
			($2,$5,'2026-08-08T12:00:00Z',null,'running',null),
			($3,$5,'2026-08-08T10:00:00Z','2026-08-08T10:15:00Z','partial','incomplete'),
			($4,$5,'2026-08-08T09:00:00Z','2026-08-08T09:05:00Z','failed','unavailable')
	`, publishedScan, runningScan, partialScan, failedScan, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into catalog_generations(root_id,generation,inventory_scan_run_id)
		values ($1,2,$2)
	`, rootID, publishedScan); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 8, 8, 12, 0, 0, 123000, time.UTC)
	pendingAudio := uuid.New()
	pendingCompanion := uuid.New()
	missingAudio := uuid.New()
	processedAudio := uuid.New()
	deadHistoryAudio := uuid.New()
	runningAudio := uuid.New()
	partialAudio := uuid.New()
	failedAudio := uuid.New()
	invalidTimestampAudio := uuid.New()
	for _, file := range []struct {
		id                        uuid.UUID
		scanID                    uuid.UUID
		path, kind, ckind, status string
		missing                   bool
	}{
		{pendingAudio, publishedScan, "Book/new.mp3", "audio", "", "pending", false},
		{pendingCompanion, publishedScan, "Book/book.nfo", "companion", "nfo", "pending", false},
		{missingAudio, publishedScan, "Book/missing.mp3", "audio", "", "pending", true},
		{processedAudio, publishedScan, "Book/done.mp3", "audio", "", "succeeded", false},
		{deadHistoryAudio, publishedScan, "Book/dead.mp3", "audio", "", "pending", false},
		{runningAudio, runningScan, "Book/running.mp3", "audio", "", "pending", false},
		{partialAudio, partialScan, "Book/partial.mp3", "audio", "", "pending", false},
		{failedAudio, failedScan, "Book/failed.mp3", "audio", "", "pending", false},
	} {
		if _, err := pool.Exec(ctx, `
			insert into source_files(
				id, root_id, relative_path, size, modified_at, file_kind,
				companion_kind, probe_status, scan_run_id, missing_at
			) values ($1,$2,$3,17,$4::timestamptz,$5,nullif($6,''),$7,$8,
			          case when $9::boolean then $4::timestamptz else null end)
		`, file.id, rootID, file.path, modified, file.kind, file.ckind, file.status,
			file.scanID, file.missing); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		insert into source_files(
			id,root_id,relative_path,size,modified_at,file_kind,probe_status,scan_run_id
		) values ($1,$2,'Book/future.mp3',17,'2500-01-01T00:00:00Z','audio','pending',$3)
	`, invalidTimestampAudio, rootID, publishedScan); err != nil {
		t.Fatal(err)
	}
	modifiedNanos := modified.UnixNano()
	deadKey := "probe_file:" + deadHistoryAudio.String() + ":17:" + strconv.FormatInt(modifiedNanos, 10)
	if _, err := pool.Exec(ctx, `
		insert into worker_jobs(
			id,kind,dedupe_key,payload,status,attempts,max_attempts,last_error
		) values ($1,'probe_file',$2,'{}'::jsonb,'dead',5,5,'exhausted')
	`, uuid.New(), deadKey); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	if err := pool.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 10 {
		t.Fatalf("schema migration rows=%d, want 10", migrationCount)
	}

	type fingerprintIntent struct {
		fileID, scanID                      uuid.UUID
		root, kind, relativePath            string
		expectedSize, expectedModifiedNanos int64
	}
	rows, err := pool.Query(ctx, `
		select file_id,scan_run_id,root_id,job_kind,relative_path,
		       expected_size,expected_modified_nanos
		from fingerprint_job_outbox
		order by job_kind,relative_path
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []fingerprintIntent
	for rows.Next() {
		var intent fingerprintIntent
		if err := rows.Scan(&intent.fileID, &intent.scanID, &intent.root, &intent.kind,
			&intent.relativePath, &intent.expectedSize, &intent.expectedModifiedNanos); err != nil {
			t.Fatal(err)
		}
		got = append(got, intent)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []fingerprintIntent{
		{
			fileID: pendingCompanion, scanID: publishedScan, root: rootID,
			kind: "inspect_companion", relativePath: "Book/book.nfo",
			expectedSize: 17, expectedModifiedNanos: modifiedNanos,
		},
		{
			fileID: deadHistoryAudio, scanID: publishedScan, root: rootID,
			kind: "probe_file", relativePath: "Book/dead.mp3",
			expectedSize: 17, expectedModifiedNanos: modifiedNanos,
		},
		{
			fileID: pendingAudio, scanID: publishedScan, root: rootID,
			kind: "probe_file", relativePath: "Book/new.mp3",
			expectedSize: 17, expectedModifiedNanos: modifiedNanos,
		},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backfilled outbox=%#v, want %#v", got, want)
	}

	var queuedJobs, allJobs int
	if err := pool.QueryRow(ctx, `
		select count(*) filter (where status='queued'),count(*) from worker_jobs
	`).Scan(&queuedJobs, &allJobs); err != nil {
		t.Fatal(err)
	}
	if queuedJobs != 0 || allJobs != 1 {
		t.Fatalf("migration left queued/all worker jobs=%d/%d, want 0/1", queuedJobs, allJobs)
	}

	var invalidStatus, invalidError string
	var invalidProbedAt *time.Time
	if err := pool.QueryRow(ctx, `
		select probe_status,coalesce(probe_error,''),probed_at
		from source_files where id=$1
	`, invalidTimestampAudio).Scan(&invalidStatus, &invalidError, &invalidProbedAt); err != nil {
		t.Fatal(err)
	}
	if invalidStatus != "failed" || invalidError != "fingerprint_timestamp_out_of_range" ||
		invalidProbedAt == nil {
		t.Fatalf("invalid timestamp sanitized status/error/probed_at=%q/%q/%v",
			invalidStatus, invalidError, invalidProbedAt)
	}
}

func TestMigrateEnforcesLibrarySearchDocumentReviewInvariant(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	bookID := seedSearchReviewInvariantCatalog(t, pool, ctx, "fresh-review-invariant")
	assertLibrarySearchDocumentReviewInvariant(t, pool, ctx, "fresh-review-invariant", bookID)
}

func TestMigrateUpgradesExistingStagedCatalogWithReviewInvariant(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	for version, name := range []string{
		"0001_catalog.sql",
		"0002_worker_job_deduplication.sql",
		"0003_library_search.sql",
		"0004_staged_catalog.sql",
		"0005_reorganization_payload.sql",
	} {
		contents, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx,
			`insert into schema_migrations(version) values ($1)`, version+1,
		); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}

	const rootID = "upgrade-review-invariant"
	bookID := seedSearchReviewInvariantCatalog(t, pool, ctx, rootID)
	if _, err := pool.Exec(ctx,
		`update books set review_state='needs_review' where id=$1`, bookID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into book_grouping_evidence(book_id, root_id, reason_code, evidence)
		values ($1, $2, 'probe_failed', '{}'::jsonb)
	`, bookID, rootID); err != nil {
		t.Fatal(err)
	}
	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", nil, nil, true)
	insertSearchReviewDocument(t, pool, ctx, rootID, nil, "folder", "unreviewed", "legacy-folder-review", true)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var documents, repairedAudiobooks, normalizedRawDocuments int
	if err := pool.QueryRow(ctx,
		`select count(*),
		        count(*) filter (
		            where entity_kind='audiobook'
		              and review_state='needs_review'
		              and review_reason='probe_failed'
		        ),
		        count(*) filter (
		            where entity_kind in ('folder','file')
		              and review_state is null
		              and review_reason is null
		        )
		 from library_search_documents
		 where root_id=$1`, rootID,
	).Scan(&documents, &repairedAudiobooks, &normalizedRawDocuments); err != nil {
		t.Fatal(err)
	}
	if documents != 2 {
		t.Fatalf("upgraded search documents=%d, want 2 preserved", documents)
	}
	if repairedAudiobooks != 1 || normalizedRawDocuments != 1 {
		t.Fatalf(
			"upgraded review semantics repaired audiobooks=%d normalized raw=%d, want 1/1",
			repairedAudiobooks,
			normalizedRawDocuments,
		)
	}
	assertLibrarySearchDocumentReviewInvariant(t, pool, ctx, rootID, bookID)
}

func seedSearchReviewInvariantCatalog(
	t *testing.T,
	pool interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	ctx context.Context,
	rootID string,
) uuid.UUID {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`insert into library_roots(id, display_name) values ($1, 'Review invariant')`, rootID,
	); err != nil {
		t.Fatal(err)
	}
	bookID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into books(
			id, root_id, source_folder, grouping_key, title,
			metadata_source, confidence, review_state
		) values ($1, $2, 'Book', $3, 'Book', 'inferred', 1, 'unreviewed')
	`, bookID, rootID, bookID.String()); err != nil {
		t.Fatal(err)
	}
	return bookID
}

func assertLibrarySearchDocumentReviewInvariant(
	t *testing.T,
	pool interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	ctx context.Context,
	rootID string,
	bookID uuid.UUID,
) {
	t.Helper()
	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", "needs_review", "probe_failed", true)
	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", "confirmed", nil, true)
	insertSearchReviewDocument(t, pool, ctx, rootID, nil, "folder", nil, nil, true)
	insertSearchReviewDocument(t, pool, ctx, rootID, nil, "file", nil, nil, true)

	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", nil, nil, false)
	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", "needs_review", nil, false)
	insertSearchReviewDocument(t, pool, ctx, rootID, bookID, "audiobook", "unreviewed", "probe_failed", false)
	insertSearchReviewDocument(t, pool, ctx, rootID, nil, "folder", "unreviewed", nil, false)
	insertSearchReviewDocument(t, pool, ctx, rootID, nil, "file", nil, "probe_failed", false)
}

func insertSearchReviewDocument(
	t *testing.T,
	pool interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	ctx context.Context,
	rootID string,
	bookID any,
	entityKind string,
	reviewState any,
	reviewReason any,
	wantAccepted bool,
) {
	t.Helper()
	documentID := uuid.New()
	_, err := pool.Exec(ctx, `
		insert into library_search_documents(
			root_id, document_id, entity_kind, book_id, relative_path, display_name,
			compact_name, compact_path, compact_metadata, search_compact,
			search_terms, search_grams, review_state, review_reason
		) values (
			$1, $2, $3, $4, 'Book', 'Book',
			'book', 'book', '', 'book', 'book', array['bo'], $5, $6
		)
	`, rootID, documentID, entityKind, bookID, reviewState, reviewReason)
	if wantAccepted && err != nil {
		t.Fatalf("valid %s review pair %v/%v rejected: %v", entityKind, reviewState, reviewReason, err)
	}
	if !wantAccepted && err == nil {
		t.Fatalf("invalid %s review pair %v/%v accepted", entityKind, reviewState, reviewReason)
	}
}

func TestMigrateCreatesStagedCatalogAndSearchSchema(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for _, relation := range []string{
		"library_search_snapshots",
		"library_search_documents",
		"catalog_generations",
		"book_grouping_evidence",
		"reorganization_operations",
		"reorganization_moves",
		"library_search_documents_trgm",
		"library_search_documents_grams",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`select to_regclass('public.' || $1) is not null`, relation,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("relation %s exists=%v err=%v", relation, exists, err)
		}
	}

	for table, expectedColumns := range map[string][]string{
		"source_files": {
			"file_kind", "companion_kind", "probe_status", "probe_version",
			"probe_error", "probed_at",
		},
		"library_search_snapshots":  {"catalog_generation"},
		"library_search_documents":  {"review_reason"},
		"reorganization_operations": {"groups", "target_folders"},
	} {
		var columns int
		if err := pool.QueryRow(ctx, `
			select count(*)
			from information_schema.columns
			where table_schema='public'
			  and table_name=$1
			  and column_name = any($2)
		`, table, expectedColumns).Scan(&columns); err != nil {
			t.Fatalf("count %s staged columns: %v", table, err)
		}
		if columns != len(expectedColumns) {
			t.Fatalf("%s staged columns=%d, want %d", table, columns, len(expectedColumns))
		}
	}

	for _, kind := range []string{
		"scan_root", "probe_file", "inspect_companion", "regroup_scope",
		"publish_search", "enrich_book", "publish_cover", "reorganize_files",
	} {
		if _, err := pool.Exec(ctx, `
			insert into worker_jobs(id, kind, payload)
			values ($1, $2, '{}'::jsonb)
		`, uuid.New(), kind); err != nil {
			t.Fatalf("worker job kind %q rejected: %v", kind, err)
		}
	}
}

func TestMigrateUpgradesExistingSearchProjectionToStagedCatalog(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	for version, name := range []string{
		"0001_catalog.sql",
		"0002_worker_job_deduplication.sql",
		"0003_library_search.sql",
	} {
		contents, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx,
			`insert into schema_migrations(version) values ($1)`, version+1,
		); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}

	rootID := "existing"
	scanID := uuid.New()
	documentID := uuid.New()
	coverFileID := uuid.New()
	bookID := uuid.New()
	if _, err := pool.Exec(ctx,
		`insert into library_roots(id, display_name) values ($1, 'Existing')`, rootID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into scan_runs(id, root_id, started_at, finished_at, status)
		values ($2, $1, now() - interval '1 minute', now(), 'succeeded')
	`, rootID, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into library_search_snapshots(root_id, scan_run_id, completed_at, document_count)
		values ($1, $2, now(), 1)
	`, rootID, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into library_search_documents(
			root_id, document_id, entity_kind, relative_path, display_name,
			compact_name, compact_path, compact_metadata,
			search_compact, search_terms, search_grams
		) values (
			$1, $2, 'folder', 'Existing', 'Existing',
			'existing', 'existing', '', 'existing', 'existing', array['ex','exi']
		)
	`, rootID, documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into source_files(
			id, root_id, relative_path, size, modified_at, media, scan_run_id
		) values ($1, $2, 'Existing/cover.jpg', 12, now(), '{}'::jsonb, $3)
	`, coverFileID, rootID, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into books(
			id, root_id, source_folder, grouping_key, title,
			metadata_source, confidence, review_state
		) values ($1, $2, 'Existing', 'existing', 'Existing', 'inferred', 1, 'unreviewed')
	`, bookID, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into book_files(book_id, file_id, root_id, role)
		values ($1, $2, $3, 'cover')
	`, bookID, coverFileID, rootID); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var snapshotGeneration int64
	if err := pool.QueryRow(ctx, `
		select catalog_generation
		from library_search_snapshots
		where root_id=$1
	`, rootID).Scan(&snapshotGeneration); err != nil {
		t.Fatal(err)
	}
	if snapshotGeneration != 1 {
		t.Fatalf("snapshot catalog generation=%d, want 1", snapshotGeneration)
	}

	var generation int64
	var inventoryScanID uuid.UUID
	if err := pool.QueryRow(ctx, `
		select generation, inventory_scan_run_id
		from catalog_generations
		where root_id=$1
	`, rootID).Scan(&generation, &inventoryScanID); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || inventoryScanID != scanID {
		t.Fatalf("catalog generation=%d inventory scan=%s, want 1/%s", generation, inventoryScanID, scanID)
	}

	var reviewReason *string
	if err := pool.QueryRow(ctx, `
		select review_reason
		from library_search_documents
		where root_id=$1 and document_id=$2
	`, rootID, documentID).Scan(&reviewReason); err != nil {
		t.Fatal(err)
	}
	if reviewReason != nil {
		t.Fatalf("review reason=%q, want null", *reviewReason)
	}

	var fileKind, probeStatus string
	if err := pool.QueryRow(ctx, `
		select file_kind, probe_status
		from source_files
		where id=$1
	`, coverFileID).Scan(&fileKind, &probeStatus); err != nil {
		t.Fatal(err)
	}
	if fileKind != "cover" || probeStatus != "not_applicable" {
		t.Fatalf("legacy cover kind/status=%q/%q, want cover/not_applicable", fileKind, probeStatus)
	}

	for relation := range map[string]struct{}{
		"library_search_documents_author_filter": {},
		"library_search_documents_title_sort":    {},
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`select to_regclass('public.' || $1) is not null`, relation,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("existing relation %s preserved=%v err=%v", relation, exists, err)
		}
	}
	for _, column := range []string{"compact_authors", "genres", "publication_year"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			select exists (
				select 1 from information_schema.columns
				where table_schema='public'
				  and table_name='library_search_documents'
				  and column_name=$1
			)
		`, column).Scan(&exists); err != nil || !exists {
			t.Fatalf("existing column %s preserved=%v err=%v", column, exists, err)
		}
	}
}

func TestMigrateUpgradesVersionOneWorkerJobsForDeduplication(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	versionOne, err := migrations.Files.ReadFile("0001_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(versionOne)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into schema_migrations(version) values (1)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var columnExists bool
	if err := pool.QueryRow(ctx, `
		select exists (
			select 1
			from information_schema.columns
			where table_schema='public'
			  and table_name='worker_jobs'
			  and column_name='dedupe_key'
		)
	`).Scan(&columnExists); err != nil {
		t.Fatal(err)
	}
	var indexExists bool
	if err := pool.QueryRow(ctx, `
		select to_regclass('public.worker_jobs_active_dedupe') is not null
	`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !columnExists || !indexExists {
		t.Fatalf("dedupe upgrade column=%v index=%v", columnExists, indexExists)
	}
}

func TestMigrateCreatesIndexedLibrarySearchProjection(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for _, index := range []string{
		"library_search_documents_trgm",
		"library_search_documents_grams",
		"library_search_documents_page",
		"library_search_documents_audiobook_group",
		"library_search_documents_duplicate_group",
		"library_search_documents_review_filter",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`select to_regclass('public.' || $1) is not null`, index,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s exists=%v err=%v", index, exists, err)
		}
	}

	for column, dataType := range map[string]string{
		"genres":              "ARRAY",
		"review_state":        "text",
		"publication_year":    "integer",
		"created_at":          "timestamp with time zone",
		"recently_scanned_at": "timestamp with time zone",
	} {
		var actual string
		if err := pool.QueryRow(ctx, `
			select data_type
			from information_schema.columns
			where table_schema = 'public'
			  and table_name = 'library_search_documents'
			  and column_name = $1
		`, column).Scan(&actual); err != nil {
			t.Fatalf("column %s: %v", column, err)
		}
		if actual != dataType {
			t.Fatalf("column %s data_type=%q, want %q", column, actual, dataType)
		}
	}

	var compact string
	var grams []string
	if err := pool.QueryRow(ctx, `
		select catalog_search_compact(' Black.Box 42 '),
		       catalog_search_grams('blackbox')
	`).Scan(&compact, &grams); err != nil {
		t.Fatal(err)
	}
	if compact != "blackbox42" || !slices.Contains(grams, "bl") ||
		!slices.Contains(grams, "bla") {
		t.Fatalf("compact=%q grams=%v", compact, grams)
	}
}

func TestCatalogSearchNormalizationAndGramsSupportUnicode(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var composed, decomposed string
	var grams []string
	if err := pool.QueryRow(ctx, `
		select catalog_search_compact(' Ångström—東京 42 '),
		       catalog_search_compact(E' A\u030Angstro\u0308m—東京 42 '),
		       catalog_search_grams('東京本')
	`).Scan(&composed, &decomposed, &grams); err != nil {
		t.Fatal(err)
	}
	if composed != "ångström東京42" || decomposed != composed {
		t.Fatalf("composed=%q decomposed=%q", composed, decomposed)
	}
	if want := []string{"京本", "東京", "東京本"}; !slices.Equal(grams, want) {
		t.Fatalf("grams=%v, want %v", grams, want)
	}
}

func TestLibrarySearchDocumentsRejectCrossRootBook(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		insert into library_roots(id, display_name)
		values ('one', 'One'), ('two', 'Two')
	`); err != nil {
		t.Fatal(err)
	}
	bookID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into books(
			id, root_id, source_folder, grouping_key, title,
			metadata_source, confidence, review_state
		)
		values ($1, 'one', '', 'book', 'Book', 'inferred', 1, 'unreviewed')
	`, bookID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		insert into library_search_documents(
			root_id, document_id, entity_kind, book_id, relative_path,
			display_name, compact_name, compact_path, compact_metadata,
			search_compact, search_terms, search_grams
		)
		values (
			'two', $1, 'audiobook', $2, '',
			'Book', 'book', '', '', 'book', 'book', array['bo', 'boo']
		)
	`, uuid.New(), bookID); err == nil {
		t.Fatal("library_search_documents accepted a book belonging to another root")
	}
}

func TestLibrarySearchProjectionStoresNormalizedCatalogFilters(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for _, column := range []string{"compact_authors", "compact_genres"} {
		var dataType string
		var nullable string
		if err := pool.QueryRow(ctx, `
			select data_type, is_nullable
			from information_schema.columns
			where table_schema = 'public'
			  and table_name = 'library_search_documents'
			  and column_name = $1
		`, column).Scan(&dataType, &nullable); err != nil {
			t.Fatalf("column %s: %v", column, err)
		}
		if dataType != "ARRAY" || nullable != "NO" {
			t.Fatalf("column %s data_type=%q nullable=%q", column, dataType, nullable)
		}
	}

	for _, index := range []string{
		"library_search_documents_author_filter",
		"library_search_documents_genre_filter",
	} {
		assertSearchIndex(
			t, pool, ctx, index,
			"USING gin (compact_",
			"WHERE (entity_kind = 'audiobook'::text)",
		)
	}
}

func TestLibrarySearchProjectionIndexesEveryCatalogSortAndFilter(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for index, keys := range map[string]string{
		"library_search_documents_root_filter":   "(root_id)",
		"library_search_documents_review_filter": "(review_state)",
		"library_search_documents_year_filter":   "(publication_year)",
	} {
		assertSearchIndex(t, pool, ctx, index, keys, "WHERE (entity_kind = 'audiobook'::text)")
	}
	for index, keys := range map[string]string{
		"library_search_documents_title_sort":   "(root_id, lower(title), compact_name, relative_path, document_id)",
		"library_search_documents_author_sort":  "(root_id, lower(COALESCE(authors[1], ''::text)), compact_name, relative_path, document_id)",
		"library_search_documents_year_sort":    "(root_id, COALESCE(publication_year, '-2147483648'::integer) DESC, compact_name, relative_path, document_id)",
		"library_search_documents_added_sort":   "(root_id, COALESCE(created_at, '1970-01-01 00:00:00+00'::timestamp with time zone) DESC, compact_name, relative_path, document_id)",
		"library_search_documents_scanned_sort": "(root_id, COALESCE(recently_scanned_at, '1970-01-01 00:00:00+00'::timestamp with time zone) DESC, compact_name, relative_path, document_id)",
	} {
		assertSearchIndex(
			t, pool, ctx, index, keys,
			"entity_kind = 'audiobook'::text", "duplicate_group_id IS NULL",
		)
	}
	assertSearchIndex(
		t, pool, ctx, "library_search_documents_audiobook_group",
		"(root_id, COALESCE(duplicate_group_id, document_id))",
		"WHERE (entity_kind = 'audiobook'::text)",
	)
	assertSearchIndex(
		t, pool, ctx, "library_search_documents_duplicate_group",
		"(root_id, duplicate_group_id)",
		"entity_kind = 'audiobook'::text", "duplicate_group_id IS NOT NULL",
	)
}

func assertSearchIndex(
	t *testing.T,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	ctx context.Context,
	index string,
	wants ...string,
) {
	t.Helper()
	var definition string
	if err := pool.QueryRow(ctx, `
		select coalesce(pg_get_indexdef(to_regclass('public.' || $1)), '')
	`, index).
		Scan(&definition); err != nil {
		t.Fatalf("index %s: %v", index, err)
	}
	for _, want := range wants {
		if !strings.Contains(definition, want) {
			t.Fatalf("index %s definition=%q, want %q", index, definition, want)
		}
	}
}

func TestMigrateIndexesCompletedEnrichmentJobLookup(t *testing.T) {
	pool := testpostgres.Open(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), "set local enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), `
		explain select id, status from worker_jobs
		where kind='enrich_book' and dedupe_key='finished-book'
		order by created_at desc, id desc limit 1
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "worker_jobs_dedupe_history") || strings.Contains(plan, "Sort") {
		t.Fatalf("terminal-job lookup needs an ordered history index: %s", plan)
	}
}
