package database

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	if applied != 3 {
		t.Fatalf("schema migration rows = %d, want 3", applied)
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
