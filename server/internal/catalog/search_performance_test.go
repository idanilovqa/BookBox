package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

const (
	searchPerformanceRoot           = "performance"
	searchPerformanceRawCount       = 20_000
	searchPerformanceAudiobookCount = 80_000
	searchPerformanceOptIn          = "BOOKBOX_RUN_SEARCH_PERF"
)

func TestLibrarySearchPlan(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedSearchPerformanceProjection(t, ctx, pool)

	queries := []struct {
		name string
		text string
	}{
		{name: "two character", text: "zx"},
		{name: "trigram typo", text: "xyqtokem"},
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			query := searchPerformanceQuery(test.text)
			statement := buildRawSearchStatement(
				query,
				[]string{searchPerformanceRoot},
				searchCompact(query.Text),
				nil,
			)
			assertIndexedSearchPlan(t, pool, statement)
		})
	}
	for _, test := range []struct {
		name      string
		sort      string
		wantIndex string
	}{
		{
			name: "blank audiobook title sort", sort: BookSortTitle,
			wantIndex: "library_search_documents_title_sort",
		},
		{
			name: "blank audiobook descending year sort", sort: BookSortYear,
			wantIndex: "library_search_documents_year_sort",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := SearchQuery{
				RootIDs: []string{searchPerformanceRoot}, Mode: SearchModeAudiobooks,
				Sort: test.sort, Limit: 100,
			}
			statement, err := buildAudiobookSearchStatement(
				query, []string{searchPerformanceRoot}, "", nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedAudiobookListPlan(t, pool, statement, test.wantIndex, query.Limit)
		})
	}

	repository := NewPostgresRepository(pool)
	typoPage, err := repository.SearchLibrary(ctx, searchPerformanceQuery("xyqtokem"))
	if err != nil {
		t.Fatal(err)
	}
	if !rawPageContainsPath(typoPage, "Shelf/XYQToken000001.m4b") {
		t.Fatalf("typo search did not recall target: %#v", typoPage.Groups)
	}

	if os.Getenv(searchPerformanceOptIn) != "1" {
		t.Logf("latency assertions disabled; set %s=1 to enable", searchPerformanceOptIn)
		return
	}
	if testing.Short() {
		t.Skip("search latency assertions are disabled in short mode")
	}
	t.Run("latency", func(t *testing.T) {
		assertSearchLatency(t, repository, queries)
	})
}

func seedSearchPerformanceProjection(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	indexDefinitions := dropSearchPerformanceIndexes(t, ctx, pool)
	scanID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	if _, err := pool.Exec(ctx, `
		insert into library_roots(id, display_name, scanned_at)
		values ($1, 'Performance', '2026-08-08T00:00:00Z')
	`, searchPerformanceRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into scan_runs(id, root_id, started_at, finished_at, status)
		values ($1, $2, '2026-08-07T23:59:00Z', '2026-08-08T00:00:00Z', 'succeeded')
	`, scanID, searchPerformanceRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		with generated as (
			select n,
			       md5('search-performance-document-' || n::text)::uuid as document_id,
			       case when n % 10 = 0 then 'folder' else 'file' end as entity_kind,
			       case
			         when n % 1000 = 0 then 'ZXMarker' || lpad(n::text, 6, '0')
			         when n % 1000 = 1 then 'XYQToken' || lpad(n::text, 6, '0')
			         else 'ArchiveVolume' || lpad(n::text, 6, '0')
			       end as base_name
			from generate_series(1, $2::integer) n
		), shaped as (
			select *,
			       'Shelf/' || base_name || case when entity_kind = 'file' then '.m4b' else '' end as relative_path,
			       base_name || case when entity_kind = 'file' then '.m4b' else '' end as display_name
			from generated
		), normalized as (
			select *,
			       catalog_search_compact(display_name) as compact_name,
			       catalog_search_compact(relative_path) as compact_path,
			       catalog_search_compact(display_name || ' ' || relative_path) as search_compact
			from shaped
		)
		insert into library_search_documents(
			root_id, document_id, entity_kind, relative_path, display_name,
			size, modified_at, title, authors, compact_authors, series, narrators,
			genres, compact_genres, filenames, duplicate_count, compact_name,
			compact_path, compact_metadata, search_compact, search_terms, search_grams
		)
		select $1, document_id, entity_kind, relative_path, display_name,
		       case when entity_kind = 'file' then n::bigint else null end,
		       case when entity_kind = 'file' then '2026-08-08T00:00:00Z'::timestamptz else null end,
		       '', '{}'::text[], '{}'::text[], '', '{}'::text[], '{}'::text[], '{}'::text[],
		       case when entity_kind = 'file' then array[display_name] else '{}'::text[] end,
		       1, compact_name, compact_path, '', search_compact,
		       lower(display_name || ' ' || relative_path), catalog_search_grams(search_compact)
		from normalized
	`, searchPerformanceRoot, searchPerformanceRawCount); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into books(
			id, root_id, source_folder, grouping_key, title, authors,
			publication_year, metadata_source, confidence, review_state,
			created_at, updated_at
		)
		select md5('search-performance-book-' || n::text)::uuid,
		       $1,
		       'Audiobooks/Book' || lpad(n::text, 6, '0'),
		       'performance-' || lpad(n::text, 6, '0'),
		       'Performance Book ' || lpad(n::text, 6, '0'),
		       array['Author ' || lpad((n % 1000)::text, 4, '0')],
		       1900 + (n % 125), 'inferred', 1, 'unreviewed',
		       '2026-01-01T00:00:00Z'::timestamptz + n * interval '1 second',
		       '2026-08-08T00:00:00Z'::timestamptz
		from generate_series(1, $2::integer) n
	`, searchPerformanceRoot, searchPerformanceAudiobookCount); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		with generated as (
			select n,
			       md5('search-performance-book-' || n::text)::uuid as book_id,
			       md5('search-performance-audiobook-document-' || n::text)::uuid as document_id,
			       'Audiobooks/Book' || lpad(n::text, 6, '0') as relative_path,
			       'Performance Book ' || lpad(n::text, 6, '0') as display_name,
			       case when n % 1000 in (2, 3)
			         then md5('search-performance-duplicate-' || (n / 1000)::text)::uuid
			         else null
			       end as duplicate_group_id,
			       case when n % 1000 in (2, 3) then 2 else 1 end as duplicate_count
			from generate_series(1, $2::integer) n
		), normalized as (
			select *,
			       catalog_search_compact(display_name) as compact_name,
			       catalog_search_compact(relative_path) as compact_path,
			       catalog_search_compact(display_name || ' Author ' || (n % 1000)::text) as compact_metadata
			from generated
		), indexed as (
			select *,
			       catalog_search_compact(display_name || ' ' || relative_path || ' ' || compact_metadata) as search_compact
			from normalized
		)
		insert into library_search_documents(
			root_id, document_id, entity_kind, book_id, relative_path, display_name,
			title, authors, compact_authors, series, narrators, genres, compact_genres,
			review_state, publication_year, created_at, recently_scanned_at, filenames,
			duration_ms, duplicate_group_id, duplicate_count, compact_name, compact_path,
			compact_metadata, search_compact, search_terms, search_grams
		)
		select $1, document_id, 'audiobook', book_id, relative_path, display_name,
		       display_name, array['Author ' || lpad((n % 1000)::text, 4, '0')],
		       array[catalog_search_compact('Author ' || lpad((n % 1000)::text, 4, '0'))],
		       '', '{}'::text[], '{}'::text[], '{}'::text[], 'unreviewed',
		       1900 + (n % 125),
		       '2026-01-01T00:00:00Z'::timestamptz + n * interval '1 second',
		       '2026-08-08T00:00:00Z'::timestamptz,
		       array['book' || lpad(n::text, 6, '0') || '.m4b'], 3600000,
		       duplicate_group_id, duplicate_count, compact_name, compact_path,
		       compact_metadata, search_compact,
		       lower(display_name || ' ' || relative_path), catalog_search_grams(search_compact)
		from indexed
	`, searchPerformanceRoot, searchPerformanceAudiobookCount); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into library_search_snapshots(root_id, scan_run_id, completed_at, document_count)
		values ($1, $2, '2026-08-08T00:00:00Z', $3)
	`, searchPerformanceRoot, scanID,
		searchPerformanceRawCount+searchPerformanceAudiobookCount); err != nil {
		t.Fatal(err)
	}
	for _, definition := range indexDefinitions {
		if _, err := pool.Exec(ctx, definition); err != nil {
			t.Fatalf("recreate search performance index: %v\n%s", err, definition)
		}
	}
	if _, err := pool.Exec(ctx, "analyze library_search_documents"); err != nil {
		t.Fatal(err)
	}
}

func dropSearchPerformanceIndexes(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		select indexname, indexdef
		from pg_indexes
		where schemaname=current_schema()
		  and tablename='library_search_documents'
		  and indexname<>'library_search_documents_pkey'
		order by indexname
	`)
	if err != nil {
		t.Fatal(err)
	}
	type indexDefinition struct {
		name       string
		definition string
	}
	var indexes []indexDefinition
	for rows.Next() {
		var index indexDefinition
		if err := rows.Scan(&index.name, &index.definition); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	definitions := make([]string, 0, len(indexes))
	for _, index := range indexes {
		if _, err := pool.Exec(ctx, "drop index "+pgx.Identifier{index.name}.Sanitize()); err != nil {
			t.Fatalf("drop search performance index %s: %v", index.name, err)
		}
		definitions = append(definitions, index.definition)
	}
	return definitions
}

type searchExplainDocument struct {
	Plan searchExplainNode `json:"Plan"`
}

type searchExplainNode struct {
	NodeType     string              `json:"Node Type"`
	RelationName string              `json:"Relation Name"`
	IndexName    string              `json:"Index Name"`
	PlanRows     int64               `json:"Plan Rows"`
	Plans        []searchExplainNode `json:"Plans"`
}

func assertIndexedSearchPlan(
	t *testing.T,
	pool *pgxpool.Pool,
	statement rawSearchStatement,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var encoded []byte
	if err := pool.QueryRow(
		ctx,
		"explain (format json) "+statement.SQL,
		statement.Arguments...,
	).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var documents []searchExplainDocument
	if err := json.Unmarshal(encoded, &documents); err != nil {
		t.Fatalf("decode EXPLAIN JSON: %v\n%s", err, encoded)
	}
	if len(documents) != 1 {
		t.Fatalf("EXPLAIN documents=%d, want 1\n%s", len(documents), encoded)
	}
	var sequential bool
	var projectionGIN bool
	walkSearchExplainPlan(documents[0].Plan, func(node searchExplainNode) {
		if node.RelationName == "library_search_documents" &&
			(node.NodeType == "Seq Scan" || node.NodeType == "Parallel Seq Scan") {
			sequential = true
		}
		if node.NodeType == "Bitmap Index Scan" &&
			(node.IndexName == "library_search_documents_grams" ||
				node.IndexName == "library_search_documents_trgm") {
			projectionGIN = true
		}
	})
	if sequential || !projectionGIN {
		t.Fatalf("indexed projection plan sequential=%v gin=%v\n%s", sequential, projectionGIN, encoded)
	}
}

func assertBoundedAudiobookListPlan(
	t *testing.T,
	pool *pgxpool.Pool,
	statement rawSearchStatement,
	wantIndex string,
	limit int,
) {
	t.Helper()
	documents, encoded := loadSearchExplainPlan(t, pool, statement)
	var sequential bool
	var expectedIndex bool
	var duplicateIndex bool
	var unboundedMaterialization []string
	walkSearchExplainPlan(documents[0].Plan, func(node searchExplainNode) {
		if node.RelationName == "library_search_documents" &&
			(node.NodeType == "Seq Scan" || node.NodeType == "Parallel Seq Scan") {
			sequential = true
		}
		if node.IndexName == wantIndex {
			expectedIndex = true
		}
		if node.IndexName == "library_search_documents_duplicate_group" {
			duplicateIndex = true
		}
		if (node.NodeType == "Sort" || node.NodeType == "Incremental Sort" ||
			node.NodeType == "WindowAgg") && node.PlanRows > int64(limit*10) {
			unboundedMaterialization = append(
				unboundedMaterialization,
				fmt.Sprintf("%s(%d)", node.NodeType, node.PlanRows),
			)
		}
	})
	if sequential || !expectedIndex || !duplicateIndex || len(unboundedMaterialization) != 0 {
		t.Fatalf(
			"bounded audiobook plan sequential=%v index=%v duplicate_index=%v materialization=%v\n%s",
			sequential, expectedIndex, duplicateIndex, unboundedMaterialization, encoded,
		)
	}
}

func loadSearchExplainPlan(
	t *testing.T,
	pool *pgxpool.Pool,
	statement rawSearchStatement,
) ([]searchExplainDocument, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var encoded []byte
	if err := pool.QueryRow(
		ctx,
		"explain (format json) "+statement.SQL,
		statement.Arguments...,
	).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var documents []searchExplainDocument
	if err := json.Unmarshal(encoded, &documents); err != nil {
		t.Fatalf("decode EXPLAIN JSON: %v\n%s", err, encoded)
	}
	if len(documents) != 1 {
		t.Fatalf("EXPLAIN documents=%d, want 1\n%s", len(documents), encoded)
	}
	return documents, encoded
}

func walkSearchExplainPlan(node searchExplainNode, visit func(searchExplainNode)) {
	visit(node)
	for _, child := range node.Plans {
		walkSearchExplainPlan(child, visit)
	}
}

func assertSearchLatency(
	t *testing.T,
	repository *PostgresRepository,
	queries []struct {
		name string
		text string
	},
) {
	t.Helper()
	coldStarted := time.Now()
	if _, err := repository.SearchLibrary(t.Context(), searchPerformanceQuery("zx")); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(coldStarted)
	if cold >= 150*time.Millisecond {
		t.Fatalf("cold raw search=%s, want <150ms", cold)
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			query := searchPerformanceQuery(test.text)
			for range 10 {
				if _, err := repository.SearchLibrary(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			durations := make([]time.Duration, 100)
			for index := range durations {
				started := time.Now()
				page, err := repository.SearchLibrary(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Groups) == 0 {
					t.Fatal("performance query returned no groups")
				}
				durations[index] = time.Since(started)
			}
			slices.Sort(durations)
			p95 := durations[94]
			t.Logf("cold=%s min=%s median=%s p95=%s max=%s",
				cold, durations[0], durations[49], p95, durations[len(durations)-1])
			if p95 >= 50*time.Millisecond {
				t.Fatalf("raw search p95=%s, want <50ms", p95)
			}
		})
	}
	t.Run("blank audiobook title", func(t *testing.T) {
		query := SearchQuery{
			RootIDs: []string{searchPerformanceRoot}, Mode: SearchModeAudiobooks,
			Sort: BookSortTitle, Limit: 100,
		}
		for range 10 {
			if _, err := repository.SearchLibrary(t.Context(), query); err != nil {
				t.Fatal(err)
			}
		}
		durations := make([]time.Duration, 100)
		for index := range durations {
			started := time.Now()
			page, err := repository.SearchLibrary(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Groups) == 0 {
				t.Fatal("blank audiobook performance query returned no groups")
			}
			for _, group := range page.Groups {
				for _, document := range group.Documents {
					if document.RootID != searchPerformanceRoot ||
						document.EntityKind != SearchEntityAudiobook {
						t.Fatalf("blank audiobook result escaped root/kind: %#v", document)
					}
				}
			}
			durations[index] = time.Since(started)
		}
		slices.Sort(durations)
		p95 := durations[94]
		t.Logf("min=%s median=%s p95=%s max=%s",
			durations[0], durations[49], p95, durations[len(durations)-1])
		if p95 >= 50*time.Millisecond {
			t.Fatalf("blank audiobook search p95=%s, want <50ms", p95)
		}
	})
}

func searchPerformanceQuery(text string) SearchQuery {
	return SearchQuery{
		RootIDs: []string{searchPerformanceRoot}, Mode: SearchModeRaw,
		Text: text, Sort: BookSortTitle, Limit: 100,
	}
}

func rawPageContainsPath(page SearchPage, relativePath string) bool {
	for _, group := range page.Groups {
		for _, document := range group.Documents {
			if document.RelativePath == relativePath &&
				document.RootID == searchPerformanceRoot &&
				document.EntityKind != SearchEntityAudiobook {
				return true
			}
		}
	}
	return false
}
