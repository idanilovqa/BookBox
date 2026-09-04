package worker

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestWorkerCustomMaintenanceRunsAtStartupAndPeriodically(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Now()
	calls, waits := 0, 0
	repo := &jobRepositoryFake{}
	runtime := &Worker{Repo: repo, WorkerID: "metadata", Clock: func() time.Time { return now }, Schedule: func(context.Context, time.Time) error { calls++; return nil }, SkipFingerprintOutbox: true, Wait: func(context.Context, time.Duration) error {
		waits++
		if waits == 1 {
			now = now.Add(ScanInterval)
			return nil
		}
		cancel()
		return context.Canceled
	}}
	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(repo.enqueuedJobs) != 0 || repo.outboxDrains != 0 {
		t.Fatalf("maintenance calls=%d scans=%d drains=%d", calls, len(repo.enqueuedJobs), repo.outboxDrains)
	}
}
func TestQueueMetadataEnrichmentIncludesExistingBooks(t *testing.T) {
	id := uuid.New()
	repo := &coverMaintenanceRepository{stagedRepositoryFake: stagedRepositoryFake{books: []catalog.Book{{ID: id, RootID: "audio", Title: "M01: The Final Empire"}}}}
	if err := QueueMetadataEnrichment(t.Context(), repo, []config.Root{{ID: "audio"}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.dedupeKeys) != 1 || repo.dedupeKeys[0] != enrichmentDedupeKey(repo.books[0]) {
		t.Fatalf("keys=%v", repo.dedupeKeys)
	}
}
func TestEnrichmentRetryRepairsCoverQueueAfterConfirmation(t *testing.T) {
	id, matchID := uuid.New(), uuid.New()
	repo := &enrichRepositoryFake{detail: catalog.BookDetail{
		Book:    catalog.Book{ID: id, Title: "The Final Empire", ReviewState: catalog.ReviewConfirmed, ConfirmedMatchID: &matchID},
		Matches: []catalog.MetadataMatch{{ID: matchID, BookID: id, Confirmed: true, CoverURL: "https://covers.openlibrary.org/b/id/1-L.jpg"}},
	}}
	handler := &EnrichBookHandler{Repo: repo, Matcher: &recordingMatcherFake{}}
	if err := handler.Handle(t.Context(), catalog.WorkerJob{Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": id.String()}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.jobs) != 1 || repo.jobs[0].Kind != catalog.JobPublishCover {
		t.Fatalf("cover queue not repaired: %+v", repo.jobs)
	}
}

type coverMaintenanceRepository struct {
	stagedRepositoryFake
	detail catalog.BookDetail
}

func (r *coverMaintenanceRepository) GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error) {
	return r.detail, nil
}
func TestMaintenanceRecoversConfirmedBookMissingCover(t *testing.T) {
	id, matchID := uuid.New(), uuid.New()
	book := catalog.Book{ID: id, RootID: "audio", Title: "The Final Empire", ReviewState: catalog.ReviewConfirmed, ConfirmedMatchID: &matchID}
	repo := &coverMaintenanceRepository{
		stagedRepositoryFake: stagedRepositoryFake{books: []catalog.Book{book}},
		detail:               catalog.BookDetail{Book: book, Matches: []catalog.MetadataMatch{{ID: matchID, BookID: id, Confirmed: true, CoverURL: "https://covers.openlibrary.org/b/id/1-L.jpg"}}},
	}
	if err := QueueMetadataEnrichment(t.Context(), repo, []config.Root{{ID: "audio"}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.jobs) != 1 || repo.jobs[0].Kind != catalog.JobPublishCover {
		t.Fatalf("missing cover was not queued: %+v", repo.jobs)
	}
}

func TestEnrichmentDedupeKeyUsesCleanSearchTitle(t *testing.T) {
	book := catalog.Book{ID: uuid.New(), Title: "(#11) Reaper Man", Authors: []string{"Terry Pratchett"}}
	dirtyKey := enrichmentDedupeKey(book)
	book.Title = "Reaper Man"
	if cleanKey := enrichmentDedupeKey(book); cleanKey != dirtyKey {
		t.Fatalf("equivalent search identities differ: %s != %s", dirtyKey, cleanKey)
	}
	book.Title = "Guards! Guards!"
	if enrichmentDedupeKey(book) == dirtyKey {
		t.Fatal("different books must not share an enrichment key")
	}
}

func TestMeaningfulEnrichmentTitleRejectsShortNumericChapter(t *testing.T) {
	for _, title := range []string{"01", "9", "123"} {
		if meaningfulEnrichmentTitle(title) {
			t.Fatalf("chapter counter %q is meaningful", title)
		}
	}
	if !meaningfulEnrichmentTitle("1984") {
		t.Fatal("four-digit book title was rejected")
	}
}

type seriesMaintenanceRepository struct {
	coverMaintenanceRepository
	filledSeries, filledPosition string
}

func (r *seriesMaintenanceRepository) FillMissingBookSeries(_ context.Context, _ uuid.UUID, _ time.Time, series, position string) (bool, error) {
	r.filledSeries = series
	r.filledPosition = position
	return true, nil
}
func TestMaintenanceBackfillsConfirmedSeries(t *testing.T) {
	book := catalog.Book{ID: uuid.New(), RootID: "audio", Title: "The Great Hunt (The Wheel of Time Book 2)", ReviewState: catalog.ReviewConfirmed}
	repo := &seriesMaintenanceRepository{coverMaintenanceRepository: coverMaintenanceRepository{stagedRepositoryFake: stagedRepositoryFake{books: []catalog.Book{book}}}}
	if err := QueueMetadataEnrichment(t.Context(), repo, []config.Root{{ID: "audio"}}); err != nil {
		t.Fatal(err)
	}
	if repo.filledSeries != "The Wheel of Time" || repo.filledPosition != "2" {
		t.Fatalf("series not backfilled: %+v", repo)
	}
}

func TestMaintenanceCorrectsAuthorDecoratedSeries(t *testing.T) {
	book := catalog.Book{
		ID: uuid.New(), RootID: "audio", Title: "The Dark Talent",
		Authors: []string{"Brandon Sanderson"},
		Series:  "Brandon Sanderson - Alcatraz vs The Evil Librarians", SeriesPosition: "5",
		SourceFolder: "Brandon Sanderson/Brandon Sanderson - Alcatraz vs The Evil Librarians/5 Dark Talent",
	}
	repo := &seriesMaintenanceRepository{coverMaintenanceRepository: coverMaintenanceRepository{stagedRepositoryFake: stagedRepositoryFake{books: []catalog.Book{book}}}}
	if err := QueueMetadataEnrichment(t.Context(), repo, []config.Root{{ID: "audio"}}); err != nil {
		t.Fatal(err)
	}
	if repo.filledSeries != "Alcatraz vs The Evil Librarians" || repo.filledPosition != "5" {
		t.Fatalf("series not corrected: %+v", repo)
	}
}

func TestMaintenanceSearchesForCoverWhenConfirmedURLIsEmpty(t *testing.T) {
	id, mid := uuid.New(), uuid.New()
	b := catalog.Book{ID: id, RootID: "audio", Title: "Reaper Man", ReviewState: catalog.ReviewConfirmed, ConfirmedMatchID: &mid}
	r := &coverMaintenanceRepository{stagedRepositoryFake: stagedRepositoryFake{books: []catalog.Book{b}}, detail: catalog.BookDetail{Book: b, Matches: []catalog.MetadataMatch{{ID: mid, BookID: id, Confirmed: true}}}}
	if err := QueueMetadataEnrichment(t.Context(), r, []config.Root{{ID: "audio"}}); err != nil {
		t.Fatal(err)
	}
	if len(r.jobs) != 1 || r.jobs[0].Kind != catalog.JobEnrichBook {
		t.Fatalf("no cover search: %+v", r.jobs)
	}
}
