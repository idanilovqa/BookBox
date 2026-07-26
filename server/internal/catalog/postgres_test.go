package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"netstorage.local/server/internal/database"
	"netstorage.local/server/internal/testpostgres"
)

func TestValidateRelativePathRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{
		"",
		".",
		"..",
		"/Book/file.m4b",
		`Book\file.m4b`,
		"Book/../file.m4b",
		"Book/./file.m4b",
		"Book//file.m4b",
		"Book/\x00file.m4b",
		"C:/Book/file.m4b",
	} {
		if err := ValidateRelativePath(path); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("ValidateRelativePath(%q) = %v, want ErrInvalidPath", path, err)
		}
	}
	for _, path := range []string{"Book", "Book/file.m4b", "Series/Book 2/file.mp3"} {
		if err := ValidateRelativePath(path); err != nil {
			t.Errorf("ValidateRelativePath(%q) = %v", path, err)
		}
	}
}

func TestBookCursorRejectsTrailingDataAndRetryDelayCaps(t *testing.T) {
	cursorWithTrailingData := base64.RawURLEncoding.EncodeToString([]byte(
		`{"value":"dune","id":"00000000-0000-0000-0000-000000000001"}{}`,
	))
	if _, err := decodeBookCursor(cursorWithTrailingData); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("decodeBookCursor(trailing data) = %v, want ErrInvalidPage", err)
	}
	if got := retryDelay(100); got != 6*time.Hour {
		t.Fatalf("retryDelay(100) = %v, want 6h", got)
	}
}

func TestPostgresRepositoryPreservesConfirmedMetadataAndCorrections(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID:          "audiobooks",
		DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}

	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	files := []SourceFile{
		sourceFile("Dune", "Dune/chapter01.mp3"),
		sourceFile("Dune", "Dune/chapter02.mp3"),
		sourceFile("Dune", "Dune/cover.jpg"),
		sourceFile("Nested", "Dune/Subbook/nested.m4b"),
		sourceFile("Dune Messiah", "Dune Messiah/book.m4b"),
		sourceFile("Children of Dune", "Children of Dune/book.m4b"),
	}
	if err := repository.UpsertSourceFiles(ctx, scan.ID, files); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, scan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}

	books := []InferredBook{
		inferredBook("Dune", "Frank Herbert", 1965, files[0].ID),
		inferredBook("Dune Messiah", "Frank Herbert", 1969, files[4].ID),
		inferredBook("Children of Dune", "Frank Herbert", 1976, files[5].ID),
	}
	books[0].SourceFileIDs = []uuid.UUID{files[0].ID, files[1].ID, files[2].ID}
	books[0].FileRoles = map[uuid.UUID]string{files[2].ID: FileRoleCover}
	if err := repository.ReplaceInferredBooks(ctx, "audiobooks", books); err != nil {
		t.Fatal(err)
	}

	first, err := repository.ListBooks(ctx, BookQuery{
		Author:          "Frank Herbert",
		PublicationYear: pointer(1965),
		Limit:           2,
		Sort:            BookSortTitle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Title != "Dune" {
		t.Fatalf("filtered books = %#v", first.Items)
	}

	allFirst, err := repository.ListBooks(ctx, BookQuery{Limit: 2, Sort: BookSortTitle})
	if err != nil {
		t.Fatal(err)
	}
	if len(allFirst.Items) != 2 || allFirst.NextCursor == "" {
		t.Fatalf("first page = %#v", allFirst)
	}
	allSecond, err := repository.ListBooks(ctx, BookQuery{
		Limit:  2,
		Sort:   BookSortTitle,
		Cursor: allFirst.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(allSecond.Items) != 1 || allSecond.Items[0].ID == allFirst.Items[0].ID ||
		allSecond.Items[0].ID == allFirst.Items[1].ID {
		t.Fatalf("second page = %#v after %#v", allSecond, allFirst)
	}

	dune := findBook(t, allFirst.Items, allSecond.Items, "Dune")
	match := MetadataMatch{
		ID:         uuid.New(),
		BookID:     dune.ID,
		Provider:   "open_library",
		ProviderID: "OL123M",
		Score:      0.98,
		Payload:    map[string]any{"title": "Dune"},
	}
	if err := repository.SaveMatches(ctx, dune.ID, []MetadataMatch{match}); err != nil {
		t.Fatal(err)
	}
	otherBook := findBook(t, allFirst.Items, allSecond.Items, "Dune Messiah")
	otherMatch := MetadataMatch{
		ID:         uuid.New(),
		BookID:     otherBook.ID,
		Provider:   "open_library",
		ProviderID: "OL456M",
		Score:      0.97,
		Payload:    map[string]any{"title": "Dune Messiah"},
	}
	if err := repository.SaveMatches(ctx, otherBook.ID, []MetadataMatch{otherMatch}); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmMatch(ctx, dune.ID, otherMatch.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("ConfirmMatch(cross-book) = %v, want ErrConflict", err)
	}
	if err := repository.ConfirmMatch(ctx, dune.ID, match.ID); err != nil {
		t.Fatal(err)
	}
	correction := GroupingCorrection{
		ID:           uuid.New(),
		RootID:       "audiobooks",
		SourceFolder: "Dune",
		Groups: []GroupingGroup{{
			BookID:      &dune.ID,
			GroupingKey: "folder",
			FileIDs:     []uuid.UUID{files[0].ID, files[1].ID},
		}},
	}
	crossBookCorrection := correction
	crossBookCorrection.ID = uuid.New()
	crossBookCorrection.Groups = append([]GroupingGroup(nil), correction.Groups...)
	crossBookCorrection.Groups[0].BookID = &otherBook.ID
	if err := repository.SaveGrouping(
		ctx,
		crossBookCorrection,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("SaveGrouping(cross-book) = %v, want ErrConflict", err)
	}
	for name, groups := range map[string][]GroupingGroup{
		"omitted audio": {{
			BookID:      &dune.ID,
			GroupingKey: "folder",
			FileIDs:     []uuid.UUID{files[0].ID},
		}},
		"duplicate file": {
			{
				BookID:      &dune.ID,
				GroupingKey: "folder",
				FileIDs:     []uuid.UUID{files[0].ID},
			},
			{
				GroupingKey: "second",
				FileIDs:     []uuid.UUID{files[0].ID, files[1].ID},
			},
		},
		"non-audio": {{
			BookID:      &dune.ID,
			GroupingKey: "folder",
			FileIDs:     []uuid.UUID{files[0].ID, files[1].ID, files[2].ID},
		}},
		"mismatched grouping key": {{
			BookID:      &dune.ID,
			GroupingKey: "different",
			FileIDs:     []uuid.UUID{files[0].ID, files[1].ID},
		}},
		"duplicate grouping key": {
			{
				GroupingKey: "same",
				FileIDs:     []uuid.UUID{files[0].ID},
			},
			{
				GroupingKey: "same",
				FileIDs:     []uuid.UUID{files[1].ID},
			},
		},
	} {
		t.Run("reject grouping "+name, func(t *testing.T) {
			invalid := correction
			invalid.ID = uuid.New()
			invalid.Groups = groups
			if err := repository.SaveGrouping(ctx, invalid); err == nil {
				t.Fatalf("SaveGrouping(%s) accepted invalid partition", name)
			}
		})
	}
	if err := repository.SaveGrouping(ctx, correction); err != nil {
		t.Fatal(err)
	}

	replacement := books
	replacement[0].Title = "Folder title that must not replace confirmed metadata"
	replacement[0].GroupingKey = "changed-inference"
	if err := repository.ReplaceInferredBooks(ctx, "audiobooks", replacement); err != nil {
		t.Fatal(err)
	}
	detail, err := repository.GetBook(ctx, dune.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Book.Title != "Dune" || !detail.Book.GroupingLocked ||
		detail.Book.ConfirmedMatchID == nil || *detail.Book.ConfirmedMatchID != match.ID {
		t.Fatalf("preserved book = %#v", detail.Book)
	}
	if len(detail.Matches) != 1 || !detail.Matches[0].Confirmed {
		t.Fatalf("matches = %#v", detail.Matches)
	}
	if len(detail.Files) != 3 {
		t.Fatalf(
			"corrected book files = %#v, want two audio files plus reattached shared cover",
			detail.Files,
		)
	}
	var presentDuneBooks int
	var presentDuneID uuid.UUID
	if err := pool.QueryRow(ctx, `
		select count(*)
		from books
		where root_id='audiobooks' and source_folder='Dune' and missing_at is null
	`).Scan(&presentDuneBooks); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select id
		from books
		where root_id='audiobooks' and source_folder='Dune' and missing_at is null
		order by id
		limit 1
	`).Scan(&presentDuneID); err != nil {
		t.Fatal(err)
	}
	if presentDuneBooks != 1 || presentDuneID != dune.ID {
		t.Fatalf("present corrected Dune books = %d id=%s, want one id=%s",
			presentDuneBooks, presentDuneID, dune.ID)
	}

	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID:          "other",
		DisplayName: "Other",
	}}); err != nil {
		t.Fatal(err)
	}
	otherScan, err := repository.BeginScan(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	otherFile := sourceFile("Other", "Other/book.m4b")
	otherFile.RootID = "other"
	if err := repository.UpsertSourceFiles(ctx, otherScan.ID, []SourceFile{otherFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into book_files(book_id, file_id, root_id, role)
		values ($1,$2,'audiobooks','audio')
	`, dune.ID, otherFile.ID); err == nil {
		t.Fatal("book_files accepted a source file belonging to another root")
	}

	var groups []byte
	if err := pool.QueryRow(
		ctx,
		`select groups from grouping_corrections where root_id=$1 and source_folder=$2`,
		"audiobooks",
		"Dune",
	).Scan(&groups); err != nil || len(groups) == 0 {
		t.Fatalf("grouping correction groups = %q, err=%v", groups, err)
	}
}

func TestPostgresRepositoryScanMissingAndWorkerLeaseLifecycle(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if _, err := repository.BeginScan(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BeginScan(unknown) = %v, want ErrNotFound", err)
	}
	if err := repository.SyncRoots(ctx, []LibraryRoot{{ID: "audiobooks", DisplayName: "Audio"}}); err != nil {
		t.Fatal(err)
	}

	old := sourceFile("Old", "Old/book.m4b")
	first, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertSourceFiles(ctx, first.ID, []SourceFile{old}); err != nil {
		t.Fatal(err)
	}
	newer, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	newerObservation := sourceFile("New", "New/book.m4b")
	if err := repository.UpsertSourceFiles(
		ctx,
		newer.ID,
		[]SourceFile{old, newerObservation},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, newer.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(
		ctx,
		first.ID,
		ScanResult{Success: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("FinishScan(superseded after newer success) = %v, want ErrConflict", err)
	}
	assertFileMissing(t, pool, old.ID, false)
	assertFileMissing(t, pool, newerObservation.ID, false)

	failed, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, failed.ID, ScanResult{
		Success: false,
		Error:   "probe failed",
	}); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, pool, old.ID, false)

	successful, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, successful.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, pool, old.ID, true)

	job := WorkerJob{
		ID:          uuid.New(),
		Kind:        JobScanRoot,
		Payload:     map[string]any{"rootId": "audiobooks"},
		MaxAttempts: 2,
	}
	if err := repository.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimJob(ctx, "worker-a", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID || claimed.Status != JobRunning || claimed.LockedBy != "worker-a" {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if _, err := repository.ClaimJob(ctx, "worker-b", time.Now().UTC()); !errors.Is(err, ErrNoJob) {
		t.Fatalf("second ClaimJob() = %v, want ErrNoJob", err)
	}
	reclaimedAt := claimed.LeaseUntil.Add(time.Second)
	repository.now = func() time.Time { return reclaimedAt }
	if err := repository.CompleteJob(
		ctx,
		job.ID,
		claimed.LeaseToken,
		nil,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired CompleteJob() = %v, want ErrConflict", err)
	}
	reclaimed, err := repository.ClaimJob(ctx, "worker-b", reclaimedAt)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.ID != job.ID || reclaimed.Attempts != 1 ||
		reclaimed.LeaseToken == claimed.LeaseToken {
		t.Fatalf("reclaimed job = %#v after %#v", reclaimed, claimed)
	}
	if err := repository.CompleteJob(
		ctx,
		job.ID,
		claimed.LeaseToken,
		errors.New("stale claimant"),
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CompleteJob() = %v, want ErrConflict", err)
	}
	if err := repository.CompleteJob(
		ctx,
		job.ID,
		reclaimed.LeaseToken,
		errors.New("permanent"),
	); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := pool.QueryRow(
		ctx,
		`select status, attempts from worker_jobs where id=$1`,
		job.ID,
	).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != JobDead || attempts != 2 {
		t.Fatalf("dead state = %q attempts=%d", status, attempts)
	}
	if _, err := repository.ClaimJob(ctx, "worker-c", reclaimedAt.Add(time.Hour)); !errors.Is(err, ErrNoJob) {
		t.Fatalf("ClaimJob(dead) = %v, want ErrNoJob", err)
	}
}

func TestPostgresRepositorySupportsRootLevelBookAndGrouping(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID:          "audiobooks",
		DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	file := sourceFile("Root Book", "root-book.m4b")
	if err := repository.UpsertSourceFiles(ctx, scan.ID, []SourceFile{file}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, scan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	book := inferredBook("Root Book", "Author", 2020, file.ID)
	book.SourceFolder = ""
	book.GroupingKey = "root-book"
	if err := repository.ReplaceInferredBooks(ctx, "audiobooks", []InferredBook{book}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListBooks(ctx, BookQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceFolder != "" {
		t.Fatalf("root-level books = %#v", page.Items)
	}
	if err := repository.SaveGrouping(ctx, GroupingCorrection{
		ID:           uuid.New(),
		RootID:       "audiobooks",
		SourceFolder: "",
		Groups: []GroupingGroup{{
			BookID:      &page.Items[0].ID,
			GroupingKey: "root-book",
			FileIDs:     []uuid.UUID{file.ID},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresGroupingSplitMergeAndIndependentReapplication(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID:          "audiobooks",
		DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}
	firstFile := sourceFile("Anthology", "Anthology/First Book.m4b")
	secondFile := sourceFile("Anthology", "Anthology/Second Book.m4b")
	scan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertSourceFiles(
		ctx,
		scan.ID,
		[]SourceFile{firstFile, secondFile},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, scan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	initial := inferredBook("Anthology", "Local Author", 2020, firstFile.ID)
	initial.SourceFolder = "Anthology"
	initial.SourceFileIDs = []uuid.UUID{firstFile.ID, secondFile.ID}
	if err := repository.ReplaceInferredBooks(
		ctx,
		"audiobooks",
		[]InferredBook{initial},
	); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListBooks(ctx, BookQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("initial books = %#v, err=%v", page.Items, err)
	}
	originalID := page.Items[0].ID
	split := GroupingCorrection{
		ID:           uuid.New(),
		RootID:       "audiobooks",
		SourceFolder: "Anthology",
		Groups: []GroupingGroup{
			{
				BookID:      &originalID,
				GroupingKey: "folder",
				FileIDs:     []uuid.UUID{firstFile.ID},
			},
			{
				GroupingKey: "second-book",
				FileIDs:     []uuid.UUID{secondFile.ID},
			},
		},
	}
	if err := repository.SaveGrouping(ctx, split); err != nil {
		t.Fatal(err)
	}
	var storedGroupsJSON []byte
	if err := pool.QueryRow(ctx, `
		select groups
		from grouping_corrections
		where root_id='audiobooks' and source_folder='Anthology'
	`).Scan(&storedGroupsJSON); err != nil {
		t.Fatal(err)
	}
	var storedGroups []GroupingGroup
	if err := json.Unmarshal(storedGroupsJSON, &storedGroups); err != nil {
		t.Fatal(err)
	}
	if len(storedGroups) != 2 || storedGroups[1].BookID == nil ||
		*storedGroups[1].BookID == uuid.Nil {
		t.Fatalf("stored split groups = %#v", storedGroups)
	}
	splitBookID := *storedGroups[1].BookID
	var originalTitle, splitTitle string
	if err := pool.QueryRow(ctx, `
		select
		    (select title from books where id=$1),
		    (select title from books where id=$2)
	`, originalID, splitBookID).Scan(&originalTitle, &splitTitle); err != nil {
		t.Fatal(err)
	}
	if splitTitle != "Second Book" || splitTitle == originalTitle {
		t.Fatalf("split titles original=%q new=%q; want a distinguishable audio identity",
			originalTitle, splitTitle)
	}
	assertPresentBookAndAudioAssignments(t, pool, "Anthology", 2, 2)

	thirdFile := sourceFile("Anthology", "Anthology/New Arrival.m4b")
	secondScan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertSourceFiles(
		ctx,
		secondScan.ID,
		[]SourceFile{firstFile, secondFile, thirdFile},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(
		ctx,
		secondScan.ID,
		ScanResult{Success: true},
	); err != nil {
		t.Fatal(err)
	}
	reinferred := inferredBook("Anthology", "Local Author", 2020, firstFile.ID)
	reinferred.SourceFolder = "Anthology"
	reinferred.SourceFileIDs = []uuid.UUID{firstFile.ID, secondFile.ID, thirdFile.ID}
	if err := repository.ReplaceInferredBooks(
		ctx,
		"audiobooks",
		[]InferredBook{reinferred},
	); err != nil {
		t.Fatal(err)
	}
	assertPresentBookAndAudioAssignments(t, pool, "Anthology", 3, 3)
	var preservedOriginal, preservedSplit bool
	if err := pool.QueryRow(ctx, `
		select
		    exists(select 1 from books where id=$1 and missing_at is null),
		    exists(select 1 from books where id=$2 and missing_at is null)
	`, originalID, splitBookID).Scan(&preservedOriginal, &preservedSplit); err != nil {
		t.Fatal(err)
	}
	if !preservedOriginal || !preservedSplit {
		t.Fatalf("new file destroyed corrected groups original=%v split=%v",
			preservedOriginal, preservedSplit)
	}

	thirdScan, err := repository.BeginScan(ctx, "audiobooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertSourceFiles(
		ctx,
		thirdScan.ID,
		[]SourceFile{firstFile, thirdFile},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishScan(ctx, thirdScan.ID, ScanResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	remaining := inferredBook("Anthology", "Local Author", 2020, firstFile.ID)
	remaining.SourceFolder = "Anthology"
	remaining.SourceFileIDs = []uuid.UUID{firstFile.ID, thirdFile.ID}
	if err := repository.ReplaceInferredBooks(
		ctx,
		"audiobooks",
		[]InferredBook{remaining},
	); err != nil {
		t.Fatal(err)
	}
	assertPresentBookAndAudioAssignments(t, pool, "Anthology", 2, 2)
	var originalPresent, splitMissing bool
	if err := pool.QueryRow(ctx, `
		select
		    exists(select 1 from books where id=$1 and missing_at is null),
		    exists(select 1 from books where id=$2 and missing_at is not null)
	`, originalID, splitBookID).Scan(&originalPresent, &splitMissing); err != nil {
		t.Fatal(err)
	}
	if !originalPresent || !splitMissing {
		t.Fatalf("broken group handling originalPresent=%v splitMissing=%v",
			originalPresent, splitMissing)
	}

	merge := GroupingCorrection{
		ID:           uuid.New(),
		RootID:       "audiobooks",
		SourceFolder: "Anthology",
		Groups: []GroupingGroup{{
			BookID:      &originalID,
			GroupingKey: "folder",
			FileIDs:     []uuid.UUID{firstFile.ID, thirdFile.ID},
		}},
	}
	if err := repository.SaveGrouping(ctx, merge); err != nil {
		t.Fatal(err)
	}
	assertPresentBookAndAudioAssignments(t, pool, "Anthology", 1, 2)
}

func assertPresentBookAndAudioAssignments(
	t *testing.T,
	pool *pgxpool.Pool,
	folder string,
	wantBooks int,
	wantAudio int,
) {
	t.Helper()
	var books, audio, distinctAudio int
	if err := pool.QueryRow(context.Background(), `
		select
		    count(distinct b.id),
		    count(bf.file_id),
		    count(distinct bf.file_id)
		from books b
		left join book_files bf on bf.book_id=b.id
		where b.root_id='audiobooks'
		  and b.source_folder=$1
		  and b.missing_at is null
	`, folder).Scan(&books, &audio, &distinctAudio); err != nil {
		t.Fatal(err)
	}
	if books != wantBooks || audio != wantAudio || distinctAudio != wantAudio {
		t.Fatalf(
			"folder %q active books=%d audio assignments=%d distinct=%d; want %d/%d/%d",
			folder,
			books,
			audio,
			distinctAudio,
			wantBooks,
			wantAudio,
			wantAudio,
		)
	}
}

func sourceFile(folder, path string) SourceFile {
	return SourceFile{
		ID:           uuid.New(),
		RootID:       "audiobooks",
		RelativePath: path,
		Size:         1024,
		ModifiedAt:   time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		Media:        map[string]any{"folder": folder},
	}
}

func inferredBook(title, author string, year int, fileID uuid.UUID) InferredBook {
	return InferredBook{
		RootID:          "audiobooks",
		SourceFolder:    title,
		GroupingKey:     "folder",
		Title:           title,
		Authors:         []string{author},
		PublicationYear: &year,
		MetadataSource:  MetadataInferred,
		Confidence:      0.80,
		ReviewState:     ReviewUnreviewed,
		SourceFileIDs:   []uuid.UUID{fileID},
	}
}

func findBook(t *testing.T, first, second []Book, title string) Book {
	t.Helper()
	for _, book := range append(append([]Book{}, first...), second...) {
		if book.Title == title {
			return book
		}
	}
	t.Fatalf("book %q not found", title)
	return Book{}
}

func assertFileMissing(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, want bool) {
	t.Helper()
	var missing bool
	if err := pool.QueryRow(
		context.Background(),
		`select missing_at is not null from source_files where id=$1`,
		id,
	).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != want {
		t.Fatalf("file %s missing = %v, want %v", id, missing, want)
	}
}

func pointer[T any](value T) *T {
	return &value
}
