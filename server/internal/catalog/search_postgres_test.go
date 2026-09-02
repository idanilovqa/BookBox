package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestFinalizeSuccessfulScanPublishesOneRootSnapshot(t *testing.T) {
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
	fileID := uuid.New()
	file := SourceFile{
		ID: fileID, RootID: "audiobooks", RelativePath: "Frank Herbert/Dune/book.m4b",
		Size: 10, ModifiedAt: time.Now().UTC(),
		Media: map[string]any{"role": FileRoleAudio, "durationMs": int64(3_600_000)},
	}
	if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
		t.Fatal(err)
	}
	books := []InferredBook{{
		RootID: "audiobooks", SourceFolder: "Frank Herbert/Dune", GroupingKey: "dune",
		Title: "Dune", Authors: []string{"Frank Herbert"}, Narrators: []string{"Narrator"},
		MetadataSource: MetadataInferred, Confidence: 0.8, ReviewState: ReviewUnreviewed,
		SourceFileIDs: []uuid.UUID{fileID}, FileRoles: map[uuid.UUID]string{fileID: FileRoleAudio},
	}}
	if err := repository.FinalizeSuccessfulScan(ctx, scan.ID, books); err != nil {
		t.Fatal(err)
	}
	var snapshotScanID uuid.UUID
	var documentCount int
	if err := pool.QueryRow(ctx, `
		select scan_run_id, document_count
		from library_search_snapshots
		where root_id=$1
	`, "audiobooks").Scan(&snapshotScanID, &documentCount); err != nil {
		t.Fatal(err)
	}
	if snapshotScanID != scan.ID || documentCount < 3 {
		t.Fatalf("snapshot scan=%s documents=%d", snapshotScanID, documentCount)
	}
	var status string
	if err := pool.QueryRow(ctx, `select status from scan_runs where id=$1`, scan.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("scan status=%q", status)
	}
}

func TestFinalizeSuccessfulScanRollbackPreservesPriorProjection(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{
		{ID: "audiobooks", DisplayName: "Audiobooks"},
		{ID: "sentinel", DisplayName: "Sentinel"},
	}); err != nil {
		t.Fatal(err)
	}
	sentinelScan, err := repository.BeginScan(ctx, "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	sentinelFile := searchTestFile("sentinel", "Keep/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, sentinelScan.ID, []SourceFile{sentinelFile}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, sentinelScan.ID, []InferredBook{
		searchTestBook("sentinel", "Keep", "keep", sentinelFile.ID),
	}); err != nil {
		t.Fatal(err)
	}
	firstScan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	firstFile := searchTestFile("audiobooks", "Dune/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, firstScan.ID, []SourceFile{firstFile}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, firstScan.ID, []InferredBook{
		searchTestBook("audiobooks", "Dune", "dune", firstFile.ID),
	}); err != nil {
		t.Fatal(err)
	}
	var beforeCount int
	if err := pool.QueryRow(ctx, `select count(*) from library_search_documents where root_id='audiobooks'`).Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	var rootScannedAt time.Time
	if err := pool.QueryRow(ctx, `select scanned_at from library_roots where id='audiobooks'`).Scan(&rootScannedAt); err != nil {
		t.Fatal(err)
	}
	var beforeEvents, sentinelDocuments int
	if err := pool.QueryRow(ctx, `select count(*) from availability_events where root_id='audiobooks'`).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from library_search_documents where root_id='sentinel'`).Scan(&sentinelDocuments); err != nil {
		t.Fatal(err)
	}

	secondScan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	invalidFile := searchTestFile("audiobooks", "Broken/book.m4b", "document")
	if err := repository.UpsertSourceFiles(ctx, secondScan.ID, []SourceFile{invalidFile}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, secondScan.ID, []InferredBook{
		searchTestBook("audiobooks", "Broken", "broken", invalidFile.ID),
	}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("error=%v, want ErrInvalidData", err)
	}
	var snapshotScanID uuid.UUID
	var afterCount int
	if err := pool.QueryRow(ctx, `select scan_run_id from library_search_snapshots where root_id='audiobooks'`).Scan(&snapshotScanID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from library_search_documents where root_id='audiobooks'`).Scan(&afterCount); err != nil {
		t.Fatal(err)
	}
	if snapshotScanID != firstScan.ID || afterCount != beforeCount {
		t.Fatalf("snapshot=%s count=%d, want snapshot=%s count=%d", snapshotScanID, afterCount, firstScan.ID, beforeCount)
	}
	var activeFile, activeBook, activeAssignment, newBook, afterEvents int
	var afterRootScannedAt time.Time
	var secondStatus string
	var afterSentinelScan uuid.UUID
	var afterSentinelDocuments int
	if err := pool.QueryRow(ctx, `select count(*) from source_files where id=$1 and missing_at is null`, firstFile.ID).Scan(&activeFile); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from books where root_id='audiobooks' and grouping_key='dune' and missing_at is null`).Scan(&activeBook); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from book_files where file_id=$1`, firstFile.ID).Scan(&activeAssignment); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from books where root_id='audiobooks' and grouping_key='broken'`).Scan(&newBook); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from availability_events where root_id='audiobooks'`).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select scanned_at from library_roots where id='audiobooks'`).Scan(&afterRootScannedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select status from scan_runs where id=$1`, secondScan.ID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select scan_run_id from library_search_snapshots where root_id='sentinel'`).Scan(&afterSentinelScan); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from library_search_documents where root_id='sentinel'`).Scan(&afterSentinelDocuments); err != nil {
		t.Fatal(err)
	}
	if activeFile != 1 || activeBook != 1 || activeAssignment != 1 || newBook != 0 ||
		afterEvents != beforeEvents || !afterRootScannedAt.Equal(rootScannedAt) ||
		secondStatus != "running" || afterSentinelScan != sentinelScan.ID ||
		afterSentinelDocuments != sentinelDocuments {
		t.Fatalf("rollback state file=%d book=%d assignment=%d new=%d events=%d/%d root=%s/%s scan=%s sentinel=%s/%s docs=%d/%d",
			activeFile, activeBook, activeAssignment, newBook, afterEvents, beforeEvents,
			afterRootScannedAt, rootScannedAt, secondStatus, afterSentinelScan, sentinelScan.ID,
			afterSentinelDocuments, sentinelDocuments)
	}
}

func TestFinalizeSuccessfulScanLocksRootBeforeScan(t *testing.T) {
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
	file := searchTestFile("audiobooks", "Book/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `select id from library_roots where id='audiobooks' for update`); err != nil {
		t.Fatal(err)
	}
	finalized := make(chan error, 1)
	go func() {
		finalized <- repository.FinalizeSuccessfulScan(ctx, scan.ID, []InferredBook{
			searchTestBook("audiobooks", "Book", "book", file.ID),
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case finalizeErr := <-finalized:
			t.Fatalf("finalization returned before waiting on root lock: %v", finalizeErr)
		default:
		}
		var waitingQuery string
		err := pool.QueryRow(ctx, `
			select query from pg_stat_activity
			where datname=current_database() and wait_event_type='Lock'
			  and pid<>pg_backend_pid()
			limit 1
		`).Scan(&waitingQuery)
		if err == nil && strings.TrimSpace(waitingQuery) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalization did not block on the root lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, scanLockErr := probe.Exec(ctx, `select id from scan_runs where id=$1 for update nowait`, scan.ID)
	_ = probe.Rollback(ctx)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	finalizeErr := <-finalized
	if scanLockErr != nil {
		t.Fatalf("scan row was locked before root row: %v", scanLockErr)
	}
	if finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
}

func TestSearchLibraryRequiresEveryRequestedRootSnapshot(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{
		{ID: "alpha", DisplayName: "Alpha"}, {ID: "beta", DisplayName: "Beta"},
	}); err != nil {
		t.Fatal(err)
	}
	alphaScan, err := repository.BeginScan(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	alphaFile := searchTestFile("alpha", "Book/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, alphaScan.ID, []SourceFile{alphaFile}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, alphaScan.ID, []InferredBook{
		searchTestBook("alpha", "Book", "book", alphaFile.ID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"beta", "alpha"}, Mode: SearchModeAudiobooks, Limit: 10,
	}); !errors.Is(err, ErrSearchIndexBuilding) {
		t.Fatalf("error=%v, want ErrSearchIndexBuilding", err)
	}
}

func TestSearchLibraryValidatesModeRootsAndText(t *testing.T) {
	repository := &PostgresRepository{}
	for _, test := range []struct {
		name  string
		query SearchQuery
	}{
		{"empty roots", SearchQuery{Mode: SearchModeRaw, Text: "du", Limit: 10}},
		{"raw multiple roots", SearchQuery{RootIDs: []string{"alpha", "beta"}, Mode: SearchModeRaw, Text: "du", Limit: 10}},
		{"invalid mode", SearchQuery{RootIDs: []string{"alpha"}, Mode: "covers", Text: "du", Limit: 10}},
		{"raw blank", SearchQuery{RootIDs: []string{"alpha"}, Mode: SearchModeRaw, Limit: 10}},
		{"raw one normalized character", SearchQuery{RootIDs: []string{"alpha"}, Mode: SearchModeRaw, Text: " !D! ", Limit: 10}},
		{"audiobook one normalized character", SearchQuery{RootIDs: []string{"alpha"}, Mode: SearchModeAudiobooks, Text: "d", Limit: 10}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := repository.SearchLibrary(context.Background(), test.query); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("error=%v, want ErrInvalidData", err)
			}
		})
	}
}

func TestFuzzySearchGramsPreservesUnicodeAndAllLongQueryCandidates(t *testing.T) {
	short := fuzzySearchGrams("åbcd")
	if got := strings.Join(short, ","); got != "bc,bcd,cd,åb,åbc" {
		t.Fatalf("short grams=%q", got)
	}
	long := fuzzySearchGrams("abcdefghijklmnopqrstuvwxyz")
	if len(long) != 24 || long[0] != "abc" || long[len(long)-1] != "xyz" {
		t.Fatalf("long grams=%#v", long)
	}
	seen := make(map[string]struct{}, len(long))
	for _, gram := range long {
		if _, duplicate := seen[gram]; duplicate {
			t.Fatalf("duplicate sampled gram %q in %#v", gram, long)
		}
		seen[gram] = struct{}{}
	}
	var maximumQuery strings.Builder
	for character := rune(0x1000); character < rune(0x1000+256); character++ {
		maximumQuery.WriteRune(character)
	}
	if grams := fuzzySearchGrams(maximumQuery.String()); len(grams) != 254 {
		t.Fatalf("maximum query grams=%d, want 254", len(grams))
	}
	base := fuzzySearchGrams("abcdefghijklmnopqrstu")
	for name, typo := range map[string]string{
		"early":  "zbcdefghijklmnopqrstu",
		"middle": "abcdefghijzlmnopqrstu",
		"late":   "abcdefghijklmnopqrstz",
	} {
		t.Run(name, func(t *testing.T) {
			if !sharesString(base, fuzzySearchGrams(typo)) {
				t.Fatalf("no shared candidate gram for %q", typo)
			}
		})
	}
}

func sharesString(left []string, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := set[value]; exists {
			return true
		}
	}
	return false
}

func TestSearchLibraryFindsRawSingletonsWithGramAndTrigramText(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{
		{ID: "audiobooks", DisplayName: "Audiobooks"},
		{ID: "other", DisplayName: "Other"},
	}); err != nil {
		t.Fatal(err)
	}
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	file := searchTestFile("audiobooks", "Frank Herbert/Dune/book.m4b", FileRoleAudio)
	loose := searchTestFile("audiobooks", "Dune Loose/copy.jpg", FileRoleCover)
	fuzzyBeginning := searchTestFile(
		"audiobooks", "Fuzzy/XYQToken-"+strings.Repeat("LongMetadata", 10)+".jpg", FileRoleCover,
	)
	fuzzyEnd := searchTestFile(
		"audiobooks", "Fuzzy/"+strings.Repeat("LongMetadata", 10)+"-EndingToken.jpg", FileRoleCover,
	)
	if err := repository.UpsertSourceFiles(
		ctx, scan.ID, []SourceFile{file, loose, fuzzyBeginning, fuzzyEnd},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update source_files
		set file_kind='cover', probe_status='not_applicable'
		where id = any($1)
	`, []uuid.UUID{loose.ID, fuzzyBeginning.ID, fuzzyEnd.ID}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, scan.ID, []InferredBook{
		searchTestBook("audiobooks", "Frank Herbert/Dune", "dune", file.ID),
	}); err != nil {
		t.Fatal(err)
	}
	otherScan, err := repository.BeginScan(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	otherFile := searchTestFile("other", "Other/Dune/book.m4b", FileRoleAudio)
	if err := repository.UpsertSourceFiles(ctx, otherScan.ID, []SourceFile{otherFile}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, otherScan.ID, []InferredBook{
		searchTestBook("other", "Other/Dune", "dune", otherFile.ID),
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]SearchEntityKind{
		"Dune Loose":         SearchEntityFolder,
		"Frank Herbert":      SearchEntityFolder,
		"Frank Herbert/Dune": SearchEntityFolder,
	}
	for _, text := range []string{"du", "dune"} {
		t.Run(text, func(t *testing.T) {
			page, err := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeRaw, Text: text, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Snapshots) != 1 || page.Snapshots[0].RootID != "audiobooks" || len(page.Groups) != len(want) {
				t.Fatalf("page=%#v", page)
			}
			seen := make(map[string]SearchEntityKind)
			for _, group := range page.Groups {
				if group.Duplicate || len(group.Documents) != 1 || group.ID != group.Documents[0].ID {
					t.Fatalf("raw group=%#v", group)
				}
				document := group.Documents[0]
				if document.RootID != "audiobooks" || document.EntityKind == SearchEntityAudiobook {
					t.Fatalf("raw document leaked root/kind: %#v", document)
				}
				seen[document.RelativePath] = document.EntityKind
				if strings.HasPrefix(document.RelativePath, "Dune Loose") &&
					(document.BookID != uuid.Nil || document.DuplicateGroupID != uuid.Nil ||
						document.ReviewState != "" || document.PublicationYear != nil) {
					t.Fatalf("unassigned nullable fields=%#v", document)
				}
				if document.RelativePath == "Dune Loose" &&
					(document.Size != nil || document.ModifiedAt != nil) {
					t.Fatalf("unassigned folder file fields=%#v", document)
				}
			}
			if !mapsEqual(seen, want) {
				t.Fatalf("paths=%#v, want %#v", seen, want)
			}
		})
	}
	for name, test := range map[string]struct {
		text string
		path string
	}{
		"typo near beginning": {text: "zyqtoken", path: "Fuzzy"},
		"typo near end":       {text: "endingtokem", path: "Fuzzy"},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeRaw, Text: test.text, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !searchPageContainsPath(page, test.path) {
				t.Fatalf("query %q did not recall %q: %#v", test.text, test.path, page.Groups)
			}
		})
	}
}

func TestSearchLibraryReturnsSortedSnapshotsAndRootAttributedAudiobooks(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	}); err != nil {
		t.Fatal(err)
	}
	scanIDs := make(map[string]uuid.UUID)
	for _, rootID := range []string{"beta", "alpha"} {
		scan, err := repository.BeginScan(ctx, rootID)
		if err != nil {
			t.Fatal(err)
		}
		scanIDs[rootID] = scan.ID
		file := searchTestFile(rootID, rootID+"/book.m4b", FileRoleAudio)
		if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
			t.Fatal(err)
		}
		book := searchTestBook(rootID, rootID, rootID, file.ID)
		book.Title = strings.ToUpper(rootID[:1]) + rootID[1:]
		if err := repository.FinalizeSuccessfulScan(ctx, scan.ID, []InferredBook{book}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"beta", "alpha"},
		Mode:    SearchModeAudiobooks,
		Sort:    BookSortTitle,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Snapshots) != 2 ||
		page.Snapshots[0].RootID != "alpha" ||
		page.Snapshots[0].ScanRunID != scanIDs["alpha"] ||
		page.Snapshots[1].RootID != "beta" ||
		page.Snapshots[1].ScanRunID != scanIDs["beta"] {
		t.Fatalf("snapshots=%#v", page.Snapshots)
	}
	if got := flattenedTitles(page); strings.Join(got, ",") != "Alpha,Beta" {
		t.Fatalf("titles=%v", got)
	}
	if len(page.Groups) != 2 {
		t.Fatalf("groups=%#v", page.Groups)
	}
	for _, group := range page.Groups {
		for _, document := range group.Documents {
			unexpectedRoot := document.RootID != "alpha" && document.RootID != "beta"
			if unexpectedRoot || strings.ToLower(document.Title) != document.RootID {
				t.Fatalf("document=%#v", document)
			}
		}
	}
}

func TestSearchLibraryGroupsAudiobooksWithinEachRoot(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, rootID := range []string{"alpha", "beta"} {
		publishSearchDefinitions(t, repository, rootID, []searchBookDefinition{
			{title: "Dune", folder: "Copy One", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_000},
			{title: "Dune", folder: "Copy Two", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_001},
		})
	}
	query := SearchQuery{
		RootIDs: []string{"beta", "alpha"}, Mode: SearchModeAudiobooks,
		Sort: BookSortTitle, Limit: 10,
	}
	first, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Groups) != 2 || len(second.Groups) != 2 {
		t.Fatalf("groups first=%#v second=%#v", first.Groups, second.Groups)
	}
	seenRoots := make(map[string]uuid.UUID)
	for index, group := range first.Groups {
		if !group.Duplicate || group.ID == uuid.Nil || len(group.Documents) != 2 {
			t.Fatalf("group=%#v", group)
		}
		rootID := group.Documents[0].RootID
		for _, document := range group.Documents {
			if document.RootID != rootID || document.DuplicateGroupID != group.ID {
				t.Fatalf("cross-root or incomplete group=%#v", group)
			}
		}
		seenRoots[rootID] = group.ID
		if second.Groups[index].ID != group.ID {
			t.Fatalf("unstable group IDs first=%s second=%s", group.ID, second.Groups[index].ID)
		}
	}
	if len(seenRoots) != 2 || seenRoots["alpha"] == seenRoots["beta"] {
		t.Fatalf("root-scoped groups=%#v", seenRoots)
	}
}

func TestSearchLibraryFiltersSortsAndRanksAudiobooks(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "audiobooks", DisplayName: "Audiobooks"}}); err != nil {
		t.Fatal(err)
	}
	year1965, year1969, year1990, year2001 := 1965, 1969, 1990, 2001
	publishSearchDefinitions(t, repository, "audiobooks", []searchBookDefinition{
		{title: "Dune", folder: "Exact One", author: "Frank Herbert", narrator: "Narrator", genre: "Science Fiction", review: ReviewConfirmed, year: &year1965, duration: 3_600_000},
		{title: "Dune", folder: "Exact Two", author: "Frank Herbert", narrator: "Narrator", genre: "Science Fiction", review: ReviewConfirmed, year: &year1965, duration: 3_600_001},
		{title: "Dune Messiah", folder: "Prefix", author: "Frank Herbert", narrator: "Other", genre: "Science Fiction", review: ReviewUnreviewed, year: &year1969, duration: 4_000_000},
		{title: "Filename Result", folder: "Shelf", filename: "dune-recording.m4b", author: "Path Author", narrator: "Other", genre: "Reference", review: ReviewUnreviewed, year: &year1990, duration: 5_000_000},
		{title: "Metadata Result", folder: "Metadata", author: "Dune Writer", narrator: "Other", genre: "Fantasy", review: ReviewNeedsReview, year: &year2001, duration: 6_000_000},
		{title: "Trigram Result", folder: "Trigram", author: "", narrator: "Other", genre: "", review: ReviewUnreviewed, duration: 7_000_000},
	})
	if _, err := pool.Exec(ctx, `
		update library_search_documents
		set search_compact='dunx', search_grams=catalog_search_grams('dunx'),
		    search_terms='dunx', compact_name='dunx', compact_path='trigram', compact_metadata=''
		where root_id='audiobooks' and entity_kind='audiobook' and title='Trigram Result'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		update library_search_documents
		set created_at=case title
		        when 'Filename Result' then '2026-01-06'::timestamptz
		        when 'Metadata Result' then '2026-01-05'::timestamptz
		        else '2026-01-01'::timestamptz end,
		    recently_scanned_at=case title
		        when 'Trigram Result' then '2026-02-06'::timestamptz
		        when 'Metadata Result' then '2026-02-05'::timestamptz
		        when 'Filename Result' then '2026-02-03'::timestamptz
		        else '2026-02-01'::timestamptz end
		where root_id='audiobooks' and entity_kind='audiobook'
	`); err != nil {
		t.Fatal(err)
	}

	filtered, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
		Author: "Frank Herbert", Genre: "Science Fiction", ReviewState: ReviewConfirmed,
		PublicationYear: &year1965, Sort: BookSortTitle, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Groups) != 1 || !filtered.Groups[0].Duplicate ||
		len(filtered.Groups[0].Documents) != 2 {
		t.Fatalf("filtered groups=%#v", filtered.Groups)
	}

	for _, test := range []struct {
		sort      string
		wantFirst string
		wantLast  string
	}{
		{BookSortTitle, "Dune", "Trigram Result"},
		{BookSortAuthor, "Trigram Result", "Filename Result"},
		{BookSortYear, "Metadata Result", "Trigram Result"},
		{BookSortRecentlyAdded, "Filename Result", "Trigram Result"},
		{BookSortRecentlyScanned, "Trigram Result", "Dune Messiah"},
	} {
		t.Run(test.sort, func(t *testing.T) {
			page, searchErr := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
				Sort: test.sort, Limit: 20,
			})
			if searchErr != nil {
				t.Fatal(searchErr)
			}
			titles := flattenedTitles(page)
			if len(titles) != 6 || titles[0] != test.wantFirst || titles[len(titles)-1] != test.wantLast {
				t.Fatalf("sort=%s titles=%v", test.sort, titles)
			}
			repeated, repeatErr := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
				Sort: test.sort, Limit: 20,
			})
			if repeatErr != nil || !sameDocumentOrder(page, repeated) {
				t.Fatalf("nondeterministic sort=%s repeatErr=%v", test.sort, repeatErr)
			}
		})
	}

	ranked, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
		Text: "dune", Sort: BookSortTitle, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRanks := []string{"Dune", "Dune", "Dune Messiah", "Filename Result", "Metadata Result", "Trigram Result"}
	if got := flattenedTitles(ranked); strings.Join(got, "|") != strings.Join(wantRanks, "|") {
		t.Fatalf("ranked=%v, want %v", got, wantRanks)
	}
}

func TestSearchLibraryOrdersDuplicateMembersAndHydratesFilenameMatch(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "audiobooks", DisplayName: "Audiobooks"}}); err != nil {
		t.Fatal(err)
	}
	publishSearchDefinitions(t, repository, "audiobooks", []searchBookDefinition{
		{title: "Dune", folder: "A Later", filename: "needle-copy.m4b", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_001},
		{title: "Dune", folder: "Z Earlier", filename: "book.m4b", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_000},
	})
	page, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
		Text: "needle", Sort: BookSortTitle, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Groups) != 1 || !page.Groups[0].Duplicate || len(page.Groups[0].Documents) != 2 {
		t.Fatalf("groups=%#v", page.Groups)
	}
	first, second := page.Groups[0].Documents[0], page.Groups[0].Documents[1]
	if first.DurationMS == nil || second.DurationMS == nil ||
		*first.DurationMS != 3_600_000 || *second.DurationMS != 3_600_001 ||
		first.RelativePath != "Z Earlier" || second.RelativePath != "A Later" {
		t.Fatalf("members=%#v", page.Groups[0].Documents)
	}
}

func TestSearchLibraryRejectsCompactEmptyFilters(t *testing.T) {
	for _, query := range []SearchQuery{
		{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Author: " -- ", Sort: BookSortTitle, Limit: 10},
		{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Genre: " / ", Sort: BookSortTitle, Limit: 10},
	} {
		if _, _, err := prepareSearchQuery(query); !errors.Is(err, ErrInvalidData) {
			t.Fatalf("query=%#v error=%v, want ErrInvalidData", query, err)
		}
	}
}

func TestSearchLibraryRejectsCorruptDuplicateGroups(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
	}{
		{
			name: "wrong duplicate count",
			mutate: `update library_search_documents set duplicate_count=3
			         where root_id='audiobooks' and entity_kind='audiobook'
			           and relative_path='Copy One'`,
		},
		{
			name: "incomplete group identity",
			mutate: `update library_search_documents set duplicate_group_id=null
			         where root_id='audiobooks' and entity_kind='audiobook'
			           and relative_path='Copy Two'`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := testpostgres.Open(t)
			ctx := context.Background()
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			repository := NewPostgresRepository(pool)
			if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "audiobooks", DisplayName: "Audiobooks"}}); err != nil {
				t.Fatal(err)
			}
			publishSearchDefinitions(t, repository, "audiobooks", []searchBookDefinition{
				{title: "Dune", folder: "Copy One", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_000},
				{title: "Dune", folder: "Copy Two", author: "Frank Herbert", narrator: "Narrator", duration: 3_600_001},
			})
			if _, err := pool.Exec(ctx, test.mutate); err != nil {
				t.Fatal(err)
			}
			_, err := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
				Sort: BookSortTitle, Limit: 10,
			})
			if !errors.Is(err, ErrInvalidData) {
				t.Fatalf("error=%v, want ErrInvalidData", err)
			}
		})
	}
}

func TestSearchLibraryPaginatesWholeAudiobookGroups(t *testing.T) {
	repository, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Alpha", folder: "Alpha", author: "Author", narrator: "One", duration: 1_000_000},
		{title: "Bravo", folder: "Bravo One", author: "Author", narrator: "Same", duration: 2_000_000},
		{title: "Bravo", folder: "Bravo Two", author: "Author", narrator: "Same", duration: 2_000_001},
		{title: "Charlie", folder: "Charlie", author: "Author", narrator: "Three", duration: 3_000_000},
	})
	query := SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
		Sort: BookSortTitle, Limit: 2,
	}
	first, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" || first.NextCursor != repeated.NextCursor ||
		strings.Join(flattenedTitles(first), ",") != "Alpha" {
		t.Fatalf("first=%#v repeated cursor=%q", first, repeated.NextCursor)
	}
	query.Cursor = first.NextCursor
	second, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if second.NextCursor == "" || strings.Join(flattenedTitles(second), ",") != "Bravo,Bravo" {
		t.Fatalf("second=%#v", second)
	}
	tampered := query
	tampered.Cursor = mutateSearchCursor(t, second.NextCursor, func(value map[string]any) {
		value["d"] = second.Groups[0].Documents[1].ID.String()
	})
	if _, err := repository.SearchLibrary(ctx, tampered); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("duplicate nonleader cursor error=%v, want ErrInvalidPage", err)
	}
	query.Cursor = second.NextCursor
	third, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if third.NextCursor != "" || strings.Join(flattenedTitles(third), ",") != "Charlie" {
		t.Fatalf("third=%#v", third)
	}
	if got := append(append(flattenedTitles(first), flattenedTitles(second)...), flattenedTitles(third)...); strings.Join(got, ",") != "Alpha,Bravo,Bravo,Charlie" {
		t.Fatalf("all pages=%v", got)
	}
}

func TestSearchLibraryReturnsOversizeFirstGroupThenProgresses(t *testing.T) {
	repository, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Alpha", folder: "Alpha One", author: "Author", narrator: "Same", duration: 2_000_000},
		{title: "Alpha", folder: "Alpha Two", author: "Author", narrator: "Same", duration: 2_000_001},
		{title: "Alpha", folder: "Alpha Three", author: "Author", narrator: "Same", duration: 2_000_002},
		{title: "Bravo", folder: "Bravo", author: "Author", narrator: "Other", duration: 3_000_000},
	})
	query := SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Sort: BookSortTitle, Limit: 2}
	first, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Groups) != 1 || len(first.Groups[0].Documents) != 3 || first.NextCursor == "" {
		t.Fatalf("first=%#v", first)
	}
	query.Cursor = first.NextCursor
	second, err := repository.SearchLibrary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(flattenedTitles(second), ",") != "Bravo" || second.NextCursor != "" {
		t.Fatalf("second=%#v", second)
	}
}

func TestSearchLibraryNonblankAudiobookRankingIgnoresListingSort(t *testing.T) {
	yearOld, yearNew := 1900, 2000
	repository, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Dune", folder: "A Path", author: "Author", narrator: "One", year: &yearOld, duration: 1_000_000},
		{title: "Dune", folder: "Z Path", author: "Author", narrator: "Two", year: &yearNew, duration: 2_000_000},
	})
	if _, err := repository.pool.Exec(ctx, `
		update library_search_documents
		set search_compact='dune', search_grams=catalog_search_grams('dune')
		where root_id='audiobooks'
	`); err != nil {
		t.Fatal(err)
	}
	page, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
		Text: "dune", Sort: BookSortYear, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, group := range page.Groups {
		paths = append(paths, group.Documents[0].RelativePath)
	}
	if strings.Join(paths, "|") != "A Path|Z Path" {
		t.Fatalf("paths=%v, want text-ranking tie breakers instead of year sort", paths)
	}
}

func TestSearchCursorBindsQueryAndSnapshotsAndRejectsMalformedTokens(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "alpha", DisplayName: "Alpha"}, {ID: "beta", DisplayName: "Beta"}}); err != nil {
		t.Fatal(err)
	}
	for _, rootID := range []string{"alpha", "beta"} {
		publishSearchDefinitions(t, repository, rootID, []searchBookDefinition{
			{title: "Alpha", folder: "Alpha", author: "Author", narrator: "One", genre: "Genre", review: ReviewUnreviewed, duration: 1_000_000},
			{title: "Bravo", folder: "Bravo", author: "Author", narrator: "Two", genre: "Genre", review: ReviewUnreviewed, duration: 2_000_000},
		})
	}
	query := SearchQuery{RootIDs: []string{"beta", "alpha"}, Mode: SearchModeAudiobooks, Sort: BookSortTitle, Limit: 1}
	first, err := repository.SearchLibrary(ctx, query)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	for name, mutate := range map[string]func(SearchQuery) SearchQuery{
		"text":   func(q SearchQuery) SearchQuery { q.Text = "alpha"; return q },
		"author": func(q SearchQuery) SearchQuery { q.Author = "Author"; return q },
		"genre":  func(q SearchQuery) SearchQuery { q.Genre = "Genre"; return q },
		"review": func(q SearchQuery) SearchQuery { q.ReviewState = ReviewUnreviewed; return q },
		"year":   func(q SearchQuery) SearchQuery { value := 2000; q.PublicationYear = &value; return q },
		"sort":   func(q SearchQuery) SearchQuery { q.Sort = BookSortAuthor; return q },
		"roots":  func(q SearchQuery) SearchQuery { q.RootIDs = []string{"alpha"}; return q },
		"limit":  func(q SearchQuery) SearchQuery { q.Limit = 2; return q },
	} {
		t.Run(name, func(t *testing.T) {
			changed := mutate(query)
			changed.Cursor = first.NextCursor
			if _, err := repository.SearchLibrary(ctx, changed); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("error=%v, want ErrInvalidPage", err)
			}
		})
	}

	for name, cursor := range map[string]string{
		"base64":         "%%%",
		"unknown":        mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["unknown"] = true }),
		"version":        mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["v"] = 2 }),
		"nil document":   mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["d"] = uuid.Nil.String() }),
		"wrong document": mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["d"] = uuid.New().String() }),
		"nil group":      mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["g"] = uuid.Nil.String() }),
		"wrong group":    mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["g"] = uuid.New().String() }),
		"negative root":  mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["r"] = -1 }),
		"unknown root":   mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["r"] = 99 }),
		"trailing":       first.NextCursor + base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		"oversize":       base64.RawURLEncoding.EncodeToString(make([]byte, 4097)),
		"scope digest":   mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["q"] = "bad" }),
		"snapshot digest": mutateSearchCursor(t, first.NextCursor, func(value map[string]any) {
			value["s"] = "bad"
		}),
		"wrong kind": mutateSearchCursor(t, first.NextCursor, func(value map[string]any) { value["k"] = string(SearchEntityFile) }),
	} {
		t.Run(name, func(t *testing.T) {
			changed := query
			changed.Cursor = cursor
			if _, err := repository.SearchLibrary(ctx, changed); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("error=%v, want ErrInvalidPage", err)
			}
		})
	}

	publishSearchDefinitions(t, repository, "beta", []searchBookDefinition{
		{title: "Alpha", folder: "Alpha", author: "Author", narrator: "One", duration: 1_000_000},
	})
	query.Cursor = first.NextCursor
	if _, err := repository.SearchLibrary(ctx, query); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("stale cursor error=%v, want ErrInvalidPage", err)
	}
}

func TestSearchLibraryReturnsMatchingFoldersAndKeepsRootFiles(t *testing.T) {
	repository, ctx, _ := rawSearchFixture(t)
	page, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dune",
		Sort: BookSortTitle, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]SearchEntityKind{
		"Dune": SearchEntityFolder, "Dune Messiah": SearchEntityFolder,
		"Shelf": SearchEntityFolder, "Metadata": SearchEntityFolder,
		"Typo": SearchEntityFolder, "root-dune.m4b": SearchEntityFile,
	}
	got := make(map[string]SearchEntityKind)
	for _, group := range page.Groups {
		if group.Duplicate || len(group.Documents) != 1 || group.ID != group.Documents[0].ID {
			t.Fatalf("raw group=%#v", group)
		}
		document := group.Documents[0]
		if _, exists := got[document.DisplayName]; exists {
			t.Fatalf("duplicate result for %q", document.DisplayName)
		}
		got[document.DisplayName] = document.EntityKind
	}
	if !mapsEqual(got, want) {
		t.Fatalf("results=%v, want %v", got, want)
	}
}

func TestSearchLibraryUsesFilesOnlyForDistinctBooksInSharedFolder(t *testing.T) {
	repository, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Standalone Dune", folder: "Shared", filename: "Standalone Dune.m4b", author: "Author", duration: 1},
		{title: "Foundation", folder: "Shared", filename: "Foundation.m4b", author: "Other", duration: 2},
		{title: "Dune Messiah", folder: "Author/Dune Messiah", filename: "part-01.mp3", author: "Author", duration: 3},
	})

	shared, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeRaw,
		Text: "standalone dune", Sort: BookSortTitle, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.Groups) != 1 || len(shared.Groups[0].Documents) != 1 {
		t.Fatalf("shared results=%#v", shared.Groups)
	}
	sharedDocument := shared.Groups[0].Documents[0]
	if sharedDocument.EntityKind != SearchEntityFile ||
		sharedDocument.RelativePath != "Shared/Standalone Dune.m4b" {
		t.Fatalf("shared result=%#v", sharedDocument)
	}

	dedicated, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"audiobooks"}, Mode: SearchModeRaw,
		Text: "dune messiah", Sort: BookSortTitle, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundDedicated := false
	for _, group := range dedicated.Groups {
		if len(group.Documents) != 1 || group.Documents[0].EntityKind != SearchEntityFolder {
			t.Fatalf("dedicated result is not a folder: %#v", group)
		}
		foundDedicated = foundDedicated || group.Documents[0].RelativePath == "Author/Dune Messiah"
	}
	if !foundDedicated {
		t.Fatalf("dedicated book folder missing from %#v", dedicated.Groups)
	}
}

func TestBuildRawSearchStatementResolvesNestedFilesBeforePagination(t *testing.T) {
	statement := buildRawSearchStatement(SearchQuery{Limit: 25}, []string{"raw"}, "dune", nil)
	for _, fragment := range []string{
		"resolved_targets as (",
		"regexp_replace(m.relative_path,'/[^/]+$','')",
		"parent.book_id is distinct from m.book_id",
		"not exists (",
		"'book:'||m.book_id::text",
		"distinct on (root_id,resolution_key)",
	} {
		if !strings.Contains(statement.SQL, fragment) {
			t.Fatalf("statement does not contain %q:\n%s", fragment, statement.SQL)
		}
	}
}

func TestRawSearchParentValidationRejectsMissingNestedParent(t *testing.T) {
	if err := validateRawSearchParentRows(1, nil); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("error=%v, want ErrInvalidData", err)
	}
	if err := validateRawSearchParentRows(0, nil); err != nil {
		t.Fatalf("valid projection error=%v", err)
	}
	probe := errors.New("query failed")
	if err := validateRawSearchParentRows(0, probe); !errors.Is(err, probe) {
		t.Fatalf("query error=%v, want %v", err, probe)
	}
}

func TestFuzzySearchPredicateRequiresGeneralAndWordBoundarySimilarity(t *testing.T) {
	predicate := fuzzyTextPredicate("$7")
	for _, fragment := range []string{
		"strpos(search_compact,$7)>0",
		"word_similarity($7,search_terms)>=0.4",
		"strict_word_similarity($7,search_terms)>=0.25",
		"catalog_search_spelling_match($7,search_terms)",
	} {
		if !strings.Contains(predicate, fragment) {
			t.Fatalf("predicate %q does not contain %q", predicate, fragment)
		}
	}
	if strings.Contains(predicate, "search_compact)>=0.3") {
		t.Fatalf("predicate retains permissive fuzzy threshold: %q", predicate)
	}
	if score := fuzzyTextScore("$7"); score != "word_similarity($7,search_terms)::float8" {
		t.Fatalf("score=%q", score)
	}
}

func TestSearchStatementsReuseFuzzyPredicateForPagesAndCursors(t *testing.T) {
	raw := buildRawSearchStatement(SearchQuery{Limit: 25}, []string{"raw"}, "sanderson", nil)
	audiobooks, err := buildAudiobookSearchStatement(
		SearchQuery{Limit: 25, Sort: BookSortTitle},
		[]string{"audiobooks"},
		"sanderson",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"raw page":       raw.SQL,
		"audiobook page": audiobooks.SQL,
	} {
		for _, fragment := range []string{"search_terms)>=0.4", "search_terms)>=0.25"} {
			if !strings.Contains(statement, fragment) {
				t.Fatalf("%s does not contain %q:\n%s", name, fragment, statement)
			}
		}
	}
}

func TestSearchLibraryPaginatesRawWithBoundAndStaleCursor(t *testing.T) {
	repository, ctx, _ := rawSearchFixture(t)
	query := SearchQuery{
		RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dune",
		Sort: BookSortTitle, Limit: 2,
	}
	var names []string
	var cursors []string
	for {
		page, err := repository.SearchLibrary(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range page.Groups {
			names = append(names, group.Documents[0].DisplayName)
		}
		if page.NextCursor == "" {
			break
		}
		cursors = append(cursors, page.NextCursor)
		query.Cursor = page.NextCursor
	}
	got := make(map[string]bool, len(names))
	for _, name := range names {
		got[name] = true
	}
	if len(names) != 6 || len(got) != 6 || !got["Dune"] || !got["Dune Messiah"] ||
		!got["Shelf"] || !got["Metadata"] || !got["Typo"] || !got["root-dune.m4b"] || len(cursors) != 2 {
		t.Fatalf("names=%v cursors=%v", names, cursors)
	}
	repeated, err := repository.SearchLibrary(ctx, SearchQuery{
		RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dune",
		Sort: BookSortTitle, Limit: 2,
	})
	if err != nil || repeated.NextCursor != cursors[0] {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}

	for name, cursor := range map[string]string{
		"base64":       "%%%",
		"unknown":      mutateSearchCursor(t, cursors[0], func(value map[string]any) { value["unknown"] = true }),
		"nil document": mutateSearchCursor(t, cursors[0], func(value map[string]any) { value["d"] = uuid.Nil.String() }),
		"wrong group":  mutateSearchCursor(t, cursors[0], func(value map[string]any) { value["g"] = uuid.New().String() }),
		"wrong kind":   mutateSearchCursor(t, cursors[0], func(value map[string]any) { value["k"] = string(SearchEntityAudiobook) }),
	} {
		t.Run(name, func(t *testing.T) {
			changed := query
			changed.Cursor = cursor
			if _, err := repository.SearchLibrary(ctx, changed); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("error=%v, want ErrInvalidPage", err)
			}
		})
	}
	for name, changed := range map[string]SearchQuery{
		"text":  {RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dun", Sort: BookSortTitle, Limit: 2, Cursor: cursors[0]},
		"limit": {RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dune", Sort: BookSortTitle, Limit: 3, Cursor: cursors[0]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.SearchLibrary(ctx, changed); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("error=%v, want ErrInvalidPage", err)
			}
		})
	}

	newScan, err := repository.BeginScan(ctx, "raw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, newScan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		update library_search_snapshots
		set scan_run_id=$2, completed_at=now()
		where root_id=$1
	`, "raw", newScan.ID); err != nil {
		t.Fatal(err)
	}
	stale := query
	stale.Cursor = cursors[0]
	if _, err := repository.SearchLibrary(ctx, stale); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("stale error=%v, want ErrInvalidPage", err)
	}
}

func TestSearchLibraryPaginatesRawWithEmptyCompactName(t *testing.T) {
	repository, ctx, _ := rawSearchFixture(t)
	if _, err := repository.pool.Exec(ctx, `
		update library_search_documents
		set display_name='---', compact_name=''
		where root_id='raw' and relative_path='Dune'
	`); err != nil {
		t.Fatal(err)
	}
	query := SearchQuery{
		RootIDs: []string{"raw"}, Mode: SearchModeRaw, Text: "dune",
		Sort: BookSortTitle, Limit: 1,
	}
	first, err := repository.SearchLibrary(ctx, query)
	if err != nil || first.NextCursor == "" || first.Groups[0].Documents[0].DisplayName != "---" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	query.Cursor = first.NextCursor
	second, err := repository.SearchLibrary(ctx, query)
	if err != nil || len(second.Groups) != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestSearchLibraryCursorAllowsLongPublishedSortValues(t *testing.T) {
	for _, test := range []struct {
		name        string
		sort        string
		definitions []searchBookDefinition
	}{
		{
			name: "title and normalized name", sort: BookSortTitle,
			definitions: []searchBookDefinition{
				{title: "A" + strings.Repeat("x", 300), folder: "A", author: "Author", narrator: "One", duration: 1},
				{title: "Z", folder: "Z", author: "Author", narrator: "Two", duration: 2},
			},
		},
		{
			name: "author", sort: BookSortAuthor,
			definitions: []searchBookDefinition{
				{title: "A", folder: "A", author: "A" + strings.Repeat("x", 300), narrator: "One", duration: 1},
				{title: "Z", folder: "Z", author: "Z", narrator: "Two", duration: 2},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, ctx := searchPaginationFixture(t, test.definitions)
			query := SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Sort: test.sort, Limit: 1}
			first, err := repository.SearchLibrary(ctx, query)
			if err != nil || first.NextCursor == "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}
			query.Cursor = first.NextCursor
			second, err := repository.SearchLibrary(ctx, query)
			if err != nil || len(second.Groups) != 1 {
				t.Fatalf("second=%#v err=%v", second, err)
			}
		})
	}
}

func TestSearchCursorUsesBoundedDigestsForManyRoots(t *testing.T) {
	rootIDs := make([]string, 1000)
	snapshots := make([]SearchSnapshot, 1000)
	for index := range rootIDs {
		rootIDs[index] = fmt.Sprintf("root-%04d", index)
		snapshots[index] = SearchSnapshot{RootID: rootIDs[index], ScanRunID: uuid.New()}
	}
	rootIDs[0] = "root-" + strings.Repeat("x", 6000)
	snapshots[0].RootID = rootIDs[0]
	query := SearchQuery{RootIDs: rootIDs, Mode: SearchModeAudiobooks, Sort: BookSortTitle, Limit: 10}
	document := SearchDocument{
		ID: uuid.New(), RootID: rootIDs[0], EntityKind: SearchEntityAudiobook,
		RelativePath: strings.Repeat("path", 1500), CompactName: strings.Repeat("name", 1500),
	}
	cursor := newSearchCursor(query, rootIDs, "", snapshots, document, document.ID, 0, 0,
		strings.Repeat("sort", 1500), document.CompactName)
	encoded, err := encodeSearchCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) >= 4096 {
		t.Fatalf("decoded cursor size=%d err=%v", len(raw), err)
	}
}

type rawSearchDocumentSeed struct {
	kind                                                                 SearchEntityKind
	path, name, compactName, compactPath, compactMetadata, searchCompact string
	searchTerms                                                          string
	duplicateGroupID                                                     uuid.UUID
	duplicateCount                                                       int
}

func rawSearchFixture(t *testing.T) (*PostgresRepository, context.Context, uuid.UUID) {
	t.Helper()
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "raw", DisplayName: "Raw"}}); err != nil {
		t.Fatal(err)
	}
	scan, err := repository.BeginScan(ctx, "raw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, scan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	duplicateGroupID := uuid.New()
	seeds := []rawSearchDocumentSeed{
		{kind: SearchEntityFolder, path: "Dune", name: "Dune", compactName: "dune", compactPath: "dune", searchCompact: "dune", duplicateCount: 1},
		{kind: SearchEntityFolder, path: "Dune Messiah", name: "Dune Messiah", compactName: "dunemessiah", compactPath: "dunemessiah", searchCompact: "dunemessiah", duplicateCount: 1},
		{kind: SearchEntityFolder, path: "Shelf", name: "Shelf", compactName: "shelf", compactPath: "shelf", searchCompact: "shelf", duplicateCount: 1},
		{kind: SearchEntityFolder, path: "Metadata", name: "Metadata", compactName: "metadata", compactPath: "metadata", searchCompact: "metadata", duplicateCount: 1},
		{kind: SearchEntityFolder, path: "Typo", name: "Typo", compactName: "typo", compactPath: "typo", searchCompact: "typo", duplicateCount: 1},
		{kind: SearchEntityFile, path: "Shelf/my-dune-copy.m4b", name: "my-dune-copy.m4b", compactName: "mydunecopym4b", compactPath: "shelfmydunecopym4b", searchCompact: "mydunecopym4b", duplicateGroupID: duplicateGroupID, duplicateCount: 2},
		{kind: SearchEntityFile, path: "Shelf/dune-extra.mp3", name: "dune-extra.mp3", compactName: "duneextramp3", compactPath: "shelfduneextramp3", searchCompact: "duneextramp3", duplicateCount: 1},
		{kind: SearchEntityFile, path: "Metadata/metadata.m4b", name: "metadata.m4b", compactName: "metadatam4b", compactPath: "metadatametadatam4b", compactMetadata: "dunewriter", searchCompact: "metadatam4bdunewriter", duplicateCount: 1},
		{kind: SearchEntityFile, path: "Typo/typo.m4b", name: "typo.m4b", compactName: "typom4b", compactPath: "typotypom4b", searchCompact: "dunx", searchTerms: "dunx", duplicateCount: 1},
		{kind: SearchEntityFile, path: "root-dune.m4b", name: "root-dune.m4b", compactName: "rootdunem4b", compactPath: "rootdunem4b", searchCompact: "rootdunem4b", duplicateCount: 1},
	}
	for _, seed := range seeds {
		searchTerms := seed.searchTerms
		if searchTerms == "" {
			searchTerms = seed.name
		}
		if _, err := pool.Exec(ctx, `
			insert into library_search_documents(
				root_id,document_id,entity_kind,relative_path,display_name,
				duplicate_group_id,duplicate_count,compact_name,compact_path,
				compact_metadata,search_compact,search_terms,search_grams
			) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,catalog_search_grams($11))
		`, "raw", uuid.New(), seed.kind, seed.path, seed.name,
			nullableUUID(seed.duplicateGroupID), seed.duplicateCount, seed.compactName,
			seed.compactPath, seed.compactMetadata, seed.searchCompact, searchTerms); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		insert into library_search_snapshots(root_id,scan_run_id,completed_at,document_count)
		values ('raw',$1,now(),$2)
	`, scan.ID, len(seeds)); err != nil {
		t.Fatal(err)
	}
	return repository, ctx, duplicateGroupID
}

func searchPaginationFixture(t *testing.T, definitions []searchBookDefinition) (*PostgresRepository, context.Context) {
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
	publishSearchDefinitions(t, repository, "audiobooks", definitions)
	return repository, ctx
}

func mutateSearchCursor(t *testing.T, encoded string, mutate func(map[string]any)) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type searchBookDefinition struct {
	title, folder, filename, author, narrator, genre, review string
	year                                                     *int
	duration                                                 int64
}

func publishSearchDefinitions(t *testing.T, repository *PostgresRepository, rootID string, definitions []searchBookDefinition) {
	t.Helper()
	ctx := context.Background()
	scan, err := repository.BeginScan(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]SourceFile, 0, len(definitions))
	books := make([]InferredBook, 0, len(definitions))
	for index, definition := range definitions {
		filename := definition.filename
		if filename == "" {
			filename = "book.m4b"
		}
		file := searchTestFile(rootID, definition.folder+"/"+filename, FileRoleAudio)
		file.Media["durationMs"] = definition.duration
		book := searchTestBook(rootID, definition.folder, fmt.Sprintf("book-%d", index), file.ID)
		book.Title = definition.title
		book.Authors = []string{definition.author}
		book.Narrators = []string{definition.narrator}
		book.Genres = []string{definition.genre}
		book.PublicationYear = definition.year
		if definition.review != "" {
			book.ReviewState = definition.review
		}
		files = append(files, file)
		books = append(books, book)
	}
	if err := repository.UpsertSourceFiles(ctx, scan.ID, files); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeSuccessfulScan(ctx, scan.ID, books); err != nil {
		t.Fatal(err)
	}
}

func sameDocumentOrder(left, right SearchPage) bool {
	leftDocuments := flattenDocumentIDs(left)
	rightDocuments := flattenDocumentIDs(right)
	if len(leftDocuments) != len(rightDocuments) {
		return false
	}
	for index := range leftDocuments {
		if leftDocuments[index] != rightDocuments[index] {
			return false
		}
	}
	return true
}

func flattenDocumentIDs(page SearchPage) []uuid.UUID {
	var result []uuid.UUID
	for _, group := range page.Groups {
		for _, document := range group.Documents {
			result = append(result, document.ID)
		}
	}
	return result
}

func flattenedTitles(page SearchPage) []string {
	var result []string
	for _, group := range page.Groups {
		for _, document := range group.Documents {
			result = append(result, document.Title)
		}
	}
	return result
}

func mapsEqual(left, right map[string]SearchEntityKind) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func searchPageContainsPath(page SearchPage, relativePath string) bool {
	for _, group := range page.Groups {
		for _, document := range group.Documents {
			if document.RelativePath == relativePath {
				return true
			}
		}
	}
	return false
}

func searchTestFile(rootID, relativePath, role string) SourceFile {
	return SourceFile{
		ID:     uuid.NewSHA1(uuid.NameSpaceOID, []byte(rootID+"\x00"+relativePath)),
		RootID: rootID, RelativePath: relativePath, Size: 10,
		ModifiedAt: time.Now().UTC(),
		Media:      map[string]any{"role": role, "durationMs": int64(3_600_000)},
	}
}

func searchTestBook(rootID, folder, key string, fileID uuid.UUID) InferredBook {
	return InferredBook{
		RootID: rootID, SourceFolder: folder, GroupingKey: key, Title: "Dune",
		Authors: []string{"Frank Herbert"}, Narrators: []string{"Narrator"},
		MetadataSource: MetadataInferred, Confidence: 0.8, ReviewState: ReviewUnreviewed,
		SourceFileIDs: []uuid.UUID{fileID}, FileRoles: map[uuid.UUID]string{fileID: FileRoleAudio},
	}
}
