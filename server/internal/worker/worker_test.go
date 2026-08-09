package worker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/covers"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
	"bookbox.local/server/internal/metadata"
	"bookbox.local/server/internal/scanner"
)

func TestWorkerSchedulesStartupAndSixHourScans(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	repository := &jobRepositoryFake{}
	waits := 0
	runtime := &Worker{
		Repo: repository,
		Roots: []config.Root{
			{ID: "audio-one"},
			{ID: "audio-two"},
		},
		WorkerID: "test-worker",
		Clock:    func() time.Time { return now },
		Wait: func(context.Context, time.Duration) error {
			waits++
			if waits == 1 {
				now = now.Add(ScanInterval)
				return nil
			}
			cancel()
			return context.Canceled
		},
	}

	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := repository.enqueuedRoots, []string{
		"audio-one", "audio-two", "audio-one", "audio-two",
	}; !slices.Equal(got, want) {
		t.Fatalf("scheduled roots = %v, want %v", got, want)
	}
	if repository.claims != 2 {
		t.Fatalf("claims = %d, want 2 idle polls", repository.claims)
	}
}

func TestWorkerRecoversExpiredJobsBeforeSchedulingStartupScans(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	repository := &jobRepositoryFake{}
	runtime := &Worker{
		Repo: repository, Roots: []config.Root{{ID: "audio"}},
		WorkerID: "test-worker", Clock: func() time.Time { return now },
		Wait: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	}

	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if repository.recoveries != 1 {
		t.Fatalf("recoveries=%d, want one", repository.recoveries)
	}
	if len(repository.events) < 2 ||
		repository.events[0] != "recover" || repository.events[1] != "enqueue" {
		t.Fatalf("startup events=%v, want recovery before scan enqueue", repository.events)
	}
}

func TestWorkerUsesConfiguredScanInterval(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	repository := &jobRepositoryFake{}
	configured := 5 * time.Second
	waits := 0
	runtime := &Worker{
		Repo:         repository,
		Roots:        []config.Root{{ID: "audio"}},
		WorkerID:     "test-worker",
		ScanInterval: configured,
		Clock:        func() time.Time { return now },
		Wait: func(context.Context, time.Duration) error {
			waits++
			if waits == 1 {
				now = now.Add(configured)
				return nil
			}
			cancel()
			return context.Canceled
		},
	}

	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := repository.enqueuedRoots, []string{"audio", "audio"}; !slices.Equal(got, want) {
		t.Fatalf("scheduled roots = %v, want %v", got, want)
	}
}

func TestWorkerStopsClaimsAndCancelsCurrentHandlerAfterGracePeriod(t *testing.T) {
	leaseUntil := time.Now().Add(15 * time.Minute)
	job := catalog.WorkerJob{
		ID: uuid.New(), Kind: catalog.JobEnrichBook, LeaseToken: uuid.New(),
		LeaseUntil: &leaseUntil,
	}
	repository := &jobRepositoryFake{claimed: []catalog.WorkerJob{job}}
	started := make(chan struct{})
	finished := make(chan struct{})
	handler := HandlerFunc(func(ctx context.Context, _ catalog.WorkerJob) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Worker{
		Repo: repository, WorkerID: "test-worker",
		Handlers:    map[string]Handler{catalog.JobEnrichBook: handler},
		Clock:       time.Now,
		GracePeriod: 10 * time.Millisecond,
		Wait: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled after grace period")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if repository.claims != 1 {
		t.Fatalf("claims after shutdown = %d, want 1", repository.claims)
	}
	if len(repository.completions) != 1 ||
		!errors.Is(repository.completions[0], context.Canceled) {
		t.Fatalf("completions = %#v", repository.completions)
	}
}

func TestScanRootHandlerIndexesAndQueuesIncompleteBooks(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	fileID := uuid.New()
	bookID := uuid.New()
	repository := &scanRepositoryFake{
		scan: catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
		page: catalog.Page[catalog.Book]{Items: []catalog.Book{{
			ID: bookID, RootID: root.ID, SourceFolder: "Book",
			Title: "Book", ReviewState: catalog.ReviewNeedsReview,
		}}},
	}
	scanResult := scanner.Result{
		RootID: root.ID,
		Files: []scanner.File{{
			ID: fileID, RootID: root.ID, RelativePath: "Book/book.m4b",
			Name: "book.m4b", Folder: "Book", Kind: scanner.KindAudio,
			Size: 123, ModifiedAt: time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
			Metadata: media.Metadata{Title: "Book", Author: "Author"},
		}},
		Folders: []scanner.Folder{{
			RootID: root.ID, RelativePath: "Book",
			Files: []scanner.File{{
				ID: fileID, RootID: root.ID, RelativePath: "Book/book.m4b",
				Name: "book.m4b", Folder: "Book", Kind: scanner.KindAudio,
				Metadata: media.Metadata{Title: "Book", Author: "Author"},
			}},
		}},
	}
	handler := &ScanRootHandler{
		Repo: repository, Scanner: scannerFake{result: scanResult},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if len(repository.files) != 1 ||
		repository.files[0].RelativePath != "Book/book.m4b" ||
		repository.files[0].Media["title"] != "Book" ||
		len(repository.inferred) != 1 ||
		repository.finalizeCalls != 1 ||
		repository.finalizedRunID != repository.scan.ID ||
		len(repository.finishResults) != 0 ||
		!slices.Equal(repository.events, []string{"upsert", "finalize", "list", "enqueue"}) ||
		!slices.Equal(repository.enrichedBooks, []uuid.UUID{bookID}) {
		t.Fatalf("scan writes files=%#v inferred=%#v finalized=%d run=%s finishes=%#v events=%v enrichment=%#v",
			repository.files, repository.inferred, repository.finalizeCalls,
			repository.finalizedRunID, repository.finishResults, repository.events,
			repository.enrichedBooks)
	}
}

func TestScanRootRejectsUnavailableRootBeforeBeginScan(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: `Z:\Unavailable`}
	repository := &scanRepositoryFake{}
	handler := &ScanRootHandler{
		Repo: repository, Scanner: scannerFake{},
		Roots:        map[string]config.Root{root.ID: root},
		ValidateRoot: func(string) error { return library.ErrRootUnavailable },
	}

	err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	})
	if !errors.Is(err, library.ErrRootUnavailable) || repository.beginCalls != 0 ||
		repository.finalizeCalls != 0 || repository.upsertCalled ||
		len(repository.finishResults) != 0 {
		t.Fatalf("unavailable root mutated catalog: err=%v repo=%+v", err, repository)
	}
}

func TestScanRootMarksScanFailedWhenProjectionPublicationFails(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	finalizeErr := errors.New("projection failed")
	repository := &scanRepositoryFake{
		scan:        catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
		finalizeErr: finalizeErr,
	}
	handler := &ScanRootHandler{
		Repo: repository,
		Scanner: scannerFake{result: scanner.Result{
			RootID: root.ID,
		}},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	})
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("Handle() error=%v", err)
	}
	if repository.finalizeCalls != 1 || repository.finalizedRunID != repository.scan.ID {
		t.Fatalf("finalized=%d run=%s", repository.finalizeCalls, repository.finalizedRunID)
	}
	if len(repository.finishResults) != 1 || repository.failedScans != 1 ||
		repository.finished.Success || repository.finished.Partial ||
		repository.finished.Error != "search projection publication failed" {
		t.Fatalf("finishes=%#v failed=%d", repository.finishResults, repository.failedScans)
	}
	if len(repository.enrichedBooks) != 0 ||
		!slices.Equal(repository.events, []string{"upsert", "finalize", "finish"}) {
		t.Fatalf("events=%v enrichment=%v", repository.events, repository.enrichedBooks)
	}
}

func TestScanRootJoinsProjectionAndFailedScanCompletionErrors(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	finalizeErr := errors.New("projection failed")
	finishErr := errors.New("finish failed")
	repository := &scanRepositoryFake{
		scan:        catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
		finalizeErr: finalizeErr,
		finishErr:   finishErr,
	}
	handler := &ScanRootHandler{
		Repo: repository,
		Scanner: scannerFake{result: scanner.Result{
			RootID: root.ID,
		}},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	})
	if !errors.Is(err, finalizeErr) || !errors.Is(err, finishErr) {
		t.Fatalf("Handle() error=%v, want joined finalize and finish errors", err)
	}
	if repository.finalizeCalls != 1 || len(repository.finishResults) != 1 ||
		len(repository.enrichedBooks) != 0 ||
		!slices.Equal(repository.events, []string{"upsert", "finalize", "finish"}) {
		t.Fatalf("finalized=%d finishes=%#v events=%v enrichment=%v",
			repository.finalizeCalls, repository.finishResults,
			repository.events, repository.enrichedBooks)
	}
}

func TestScanRootEnrichmentFailureDoesNotRollbackSuccessfulFinalization(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	listErr := errors.New("list failed")
	repository := &scanRepositoryFake{
		scan:    catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
		listErr: listErr,
	}
	handler := &ScanRootHandler{
		Repo: repository,
		Scanner: scannerFake{result: scanner.Result{
			RootID: root.ID,
		}},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	})
	if !errors.Is(err, listErr) || !strings.Contains(err.Error(), "queue metadata enrichment") {
		t.Fatalf("Handle() error=%v", err)
	}
	if repository.finalizeCalls != 1 || len(repository.finishResults) != 0 ||
		!slices.Equal(repository.events, []string{"upsert", "finalize", "list"}) {
		t.Fatalf("finalized=%d finishes=%#v events=%v",
			repository.finalizeCalls, repository.finishResults, repository.events)
	}
}

func TestScanRootPartialResultDoesNotFinalizeProjection(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	repository := &scanRepositoryFake{
		scan: catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
	}
	handler := &ScanRootHandler{
		Repo: repository,
		Scanner: scannerFake{result: scanner.Result{
			RootID: root.ID,
			Warnings: []scanner.Warning{{
				RelativePath: "Unreadable", Operation: "read directory",
			}},
		}},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Handle() error=%v", err)
	}
	if repository.finalizeCalls != 0 || repository.upsertCalled ||
		len(repository.finishResults) != 1 || !repository.finished.Partial ||
		repository.finished.Success {
		t.Fatalf("finalized=%d upsert=%v finishes=%#v",
			repository.finalizeCalls, repository.upsertCalled, repository.finishResults)
	}
}

func TestScanRootHandlerFailureFinishesScanAndPreservesCatalog(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	repository := &scanRepositoryFake{
		scan: catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
	}
	handler := &ScanRootHandler{
		Repo: repository, Scanner: scannerFake{err: errors.New("source unavailable")},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	}); err == nil {
		t.Fatal("scan failure returned nil")
	}
	if repository.finished.Success || repository.finished.Error == "" {
		t.Fatalf("finished = %#v, want failed result", repository.finished)
	}
	if repository.upsertCalled || repository.replaceCalled || repository.finalizeCalls != 0 {
		t.Fatalf("failed scan changed catalog: upsert=%v replace=%v finalized=%d",
			repository.upsertCalled, repository.replaceCalled, repository.finalizeCalls)
	}
}

func TestScanRootHandlerQueuesIncompleteBooksAcrossAllPages(t *testing.T) {
	root := config.Root{ID: "audio", Name: "Audio", ContainerPath: "/library/audio"}
	firstID := uuid.New()
	secondID := uuid.New()
	repository := &scanRepositoryFake{
		scan: catalog.ScanRun{ID: uuid.New(), RootID: root.ID, Status: "running"},
		pages: []catalog.Page[catalog.Book]{
			{
				Items: []catalog.Book{{
					ID: firstID, RootID: root.ID, Title: "First",
					ReviewState: catalog.ReviewNeedsReview,
				}},
				NextCursor: "page-two",
			},
			{Items: []catalog.Book{{
				ID: secondID, RootID: root.ID, Title: "Second",
				ReviewState: catalog.ReviewUnreviewed,
			}}},
		},
	}
	handler := &ScanRootHandler{
		Repo: repository,
		Scanner: scannerFake{result: scanner.Result{
			RootID: root.ID,
		}},
		Roots: map[string]config.Root{root.ID: root}, ValidateRoot: acceptRoot,
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobScanRoot, Payload: map[string]any{"rootId": root.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := repository.enrichedBooks, []uuid.UUID{firstID, secondID}; !slices.Equal(got, want) {
		t.Fatalf("enriched books = %v, want %v", got, want)
	}
	if got, want := repository.listCursors, []string{"", "page-two"}; !slices.Equal(got, want) {
		t.Fatalf("list cursors = %v, want %v", got, want)
	}
}

func TestEnrichBookProviderFailureLeavesLocalMetadataUntouched(t *testing.T) {
	bookID := uuid.New()
	repository := &enrichRepositoryFake{detail: catalog.BookDetail{Book: catalog.Book{
		ID: bookID, RootID: "audio", SourceFolder: "Book", GroupingKey: "folder",
		Title: "Local title", Authors: []string{"Local author"},
		MetadataSource: catalog.MetadataLocal, ReviewState: catalog.ReviewUnreviewed,
	}}}
	handler := &EnrichBookHandler{
		Repo: repository, Matcher: matcherFake{err: errors.New("provider unavailable")},
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": bookID.String()},
	}); err == nil {
		t.Fatal("provider failure returned nil")
	}
	if repository.saved || repository.confirmed {
		t.Fatalf("provider failure mutated metadata: %#v", repository)
	}
}

func TestEnrichBookStoresCandidatesAutoConfirmsAndQueuesCover(t *testing.T) {
	bookID := uuid.New()
	candidate := metadata.Candidate{
		Provider: metadata.ProviderOpenLibrary, ProviderID: "/works/OL1W",
		Title: "Provider title", Authors: []string{"Provider author"},
		CoverURL: "https://covers.example/large.jpg",
	}
	repository := &enrichRepositoryFake{detail: catalog.BookDetail{Book: catalog.Book{
		ID: bookID, RootID: "audio", SourceFolder: "Book", GroupingKey: "folder",
		Title: "Local title", Authors: []string{"Local author"},
		MetadataSource: catalog.MetadataLocal, ReviewState: catalog.ReviewUnreviewed,
	}}}
	handler := &EnrichBookHandler{
		Repo: repository,
		Matcher: matcherFake{decision: metadata.Decision{
			Candidates: []metadata.ScoredCandidate{{Candidate: candidate, Score: 0.95}},
			Selected:   &candidate, ReviewState: catalog.ReviewConfirmed,
		}},
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": bookID.String()},
	}); err != nil {
		t.Fatal(err)
	}
	if len(repository.matches) != 1 || !repository.saved || !repository.confirmed ||
		repository.confirmedMatch != repository.matches[0].ID ||
		len(repository.jobs) != 1 ||
		repository.jobs[0].Kind != catalog.JobPublishCover {
		t.Fatalf("enrichment = %#v", repository)
	}
}

func TestPublishCoverDownloadsConfirmedMatchAndQueuesRefreshScan(t *testing.T) {
	bookID := uuid.New()
	matchID := uuid.New()
	repository := &publishRepositoryFake{detail: catalog.BookDetail{
		Book: catalog.Book{
			ID: bookID, RootID: "audio", SourceFolder: "Book",
			ConfirmedMatchID: &matchID,
		},
		Matches: []catalog.MetadataMatch{{
			ID: matchID, BookID: bookID, Provider: catalog.MetadataOpenLibrary,
			CoverURL: "https://covers.example/large.jpg", Confirmed: true,
		}},
	}}
	downloader := &downloaderFake{data: []byte("image")}
	publisher := &publisherFake{}
	handler := &PublishCoverHandler{
		Repo: repository, Downloader: downloader, Publisher: publisher,
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobPublishCover,
		Payload: map[string]any{
			"bookId": bookID.String(), "matchId": matchID.String(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if downloader.rawURL != "https://covers.example/large.jpg" ||
		publisher.request.BookID != bookID ||
		publisher.request.Destination != "" ||
		string(publisher.request.Candidate) != "image" ||
		len(repository.jobs) != 1 ||
		repository.jobs[0].Kind != catalog.JobScanRoot ||
		repository.jobs[0].Payload["rootId"] != "audio" {
		t.Fatalf("publication downloader=%#v publisher=%#v jobs=%#v",
			downloader, publisher, repository.jobs)
	}
}

func TestPublishCoverTreatsNoImprovementAsSuccessfulNoOp(t *testing.T) {
	bookID := uuid.New()
	matchID := uuid.New()
	repository := &publishRepositoryFake{detail: catalog.BookDetail{
		Book: catalog.Book{
			ID: bookID, RootID: "audio", SourceFolder: "Book",
			ConfirmedMatchID: &matchID,
		},
		Matches: []catalog.MetadataMatch{{
			ID: matchID, BookID: bookID, Provider: catalog.MetadataOpenLibrary,
			CoverURL: "https://covers.example/large.jpg", Confirmed: true,
		}},
	}}
	handler := &PublishCoverHandler{
		Repo:       repository,
		Downloader: &downloaderFake{data: []byte("image")},
		Publisher:  &publisherFake{err: covers.ErrNoImprovement},
	}

	if err := handler.Handle(t.Context(), catalog.WorkerJob{
		Kind: catalog.JobPublishCover,
		Payload: map[string]any{
			"bookId": bookID.String(), "matchId": matchID.String(),
		},
	}); err != nil {
		t.Fatalf("no-improvement result = %v, want successful no-op", err)
	}
	if len(repository.jobs) != 0 {
		t.Fatalf("no-improvement queued refresh jobs: %#v", repository.jobs)
	}
}

type jobRepositoryFake struct {
	claimed       []catalog.WorkerJob
	claims        int
	completions   []error
	enqueuedRoots []string
	recoveries    int
	events        []string
}

func (repository *jobRepositoryFake) RecoverExpiredJobs(
	context.Context,
	time.Time,
) error {
	repository.recoveries++
	repository.events = append(repository.events, "recover")
	return nil
}

func (repository *jobRepositoryFake) ClaimJob(
	context.Context,
	string,
	time.Time,
) (catalog.WorkerJob, error) {
	repository.claims++
	repository.events = append(repository.events, "claim")
	if len(repository.claimed) == 0 {
		return catalog.WorkerJob{}, catalog.ErrNoJob
	}
	job := repository.claimed[0]
	repository.claimed = repository.claimed[1:]
	return job, nil
}

func (repository *jobRepositoryFake) CompleteJob(
	_ context.Context,
	_ uuid.UUID,
	_ uuid.UUID,
	err error,
) error {
	repository.completions = append(repository.completions, err)
	return nil
}

func (repository *jobRepositoryFake) RenewJob(
	_ context.Context,
	_ uuid.UUID,
	_ uuid.UUID,
	now time.Time,
) (time.Time, error) {
	return now.Add(15 * time.Minute), nil
}

func (repository *jobRepositoryFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	_ string,
) (catalog.WorkerJob, error) {
	repository.events = append(repository.events, "enqueue")
	repository.enqueuedRoots = append(
		repository.enqueuedRoots,
		job.Payload["rootId"].(string),
	)
	return job, nil
}

func acceptRoot(string) error { return nil }

type scannerFake struct {
	result scanner.Result
	err    error
}

func (fake scannerFake) Scan(context.Context, config.Root) (scanner.Result, error) {
	return fake.result, fake.err
}

type scanRepositoryFake struct {
	scan           catalog.ScanRun
	files          []catalog.SourceFile
	inferred       []catalog.InferredBook
	finished       catalog.ScanResult
	finishResults  []catalog.ScanResult
	page           catalog.Page[catalog.Book]
	pages          []catalog.Page[catalog.Book]
	listCursors    []string
	enrichedBooks  []uuid.UUID
	events         []string
	upsertCalled   bool
	replaceCalled  bool
	finalizeCalls  int
	finalizedRunID uuid.UUID
	finalizeErr    error
	finishErr      error
	listErr        error
	failedScans    int
	beginCalls     int
}

func (repository *scanRepositoryFake) BeginScan(
	context.Context,
	string,
) (catalog.ScanRun, error) {
	repository.beginCalls++
	return repository.scan, nil
}

func (repository *scanRepositoryFake) UpsertSourceFiles(
	_ context.Context,
	_ uuid.UUID,
	files []catalog.SourceFile,
) error {
	repository.upsertCalled = true
	repository.files = files
	repository.events = append(repository.events, "upsert")
	return nil
}

func (repository *scanRepositoryFake) ReplaceInferredBooks(
	_ context.Context,
	_ string,
	books []catalog.InferredBook,
) error {
	repository.replaceCalled = true
	repository.inferred = books
	repository.events = append(repository.events, "replace")
	return nil
}

func (repository *scanRepositoryFake) FinalizeSuccessfulScan(
	_ context.Context,
	runID uuid.UUID,
	books []catalog.InferredBook,
) error {
	repository.finalizeCalls++
	repository.finalizedRunID = runID
	repository.inferred = books
	repository.events = append(repository.events, "finalize")
	return repository.finalizeErr
}

func (repository *scanRepositoryFake) FinishScan(
	_ context.Context,
	_ uuid.UUID,
	result catalog.ScanResult,
) error {
	repository.finished = result
	repository.finishResults = append(repository.finishResults, result)
	repository.events = append(repository.events, "finish")
	if !result.Success {
		repository.failedScans++
	}
	return repository.finishErr
}

func (repository *scanRepositoryFake) ListBooks(
	_ context.Context,
	query catalog.BookQuery,
) (catalog.Page[catalog.Book], error) {
	repository.listCursors = append(repository.listCursors, query.Cursor)
	repository.events = append(repository.events, "list")
	if repository.listErr != nil {
		return catalog.Page[catalog.Book]{}, repository.listErr
	}
	if len(repository.pages) != 0 {
		page := repository.pages[0]
		repository.pages = repository.pages[1:]
		return page, nil
	}
	return repository.page, nil
}

func (repository *scanRepositoryFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	_ string,
) (catalog.WorkerJob, error) {
	bookID, _ := uuid.Parse(job.Payload["bookId"].(string))
	repository.enrichedBooks = append(repository.enrichedBooks, bookID)
	repository.events = append(repository.events, "enqueue")
	return job, nil
}

type matcherFake struct {
	decision metadata.Decision
	err      error
}

func (fake matcherFake) Match(
	context.Context,
	catalog.InferredBook,
) (metadata.Decision, error) {
	return fake.decision, fake.err
}

type enrichRepositoryFake struct {
	detail         catalog.BookDetail
	matches        []catalog.MetadataMatch
	jobs           []catalog.WorkerJob
	confirmedMatch uuid.UUID
	saved          bool
	confirmed      bool
}

func (repository *enrichRepositoryFake) GetBook(
	context.Context,
	uuid.UUID,
) (catalog.BookDetail, error) {
	return repository.detail, nil
}

func (repository *enrichRepositoryFake) SaveMatches(
	_ context.Context,
	_ uuid.UUID,
	matches []catalog.MetadataMatch,
) error {
	repository.saved = true
	repository.matches = matches
	return nil
}

func (repository *enrichRepositoryFake) ConfirmMatch(
	_ context.Context,
	_ uuid.UUID,
	matchID uuid.UUID,
) error {
	repository.confirmed = true
	repository.confirmedMatch = matchID
	return nil
}

func (repository *enrichRepositoryFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	_ string,
) (catalog.WorkerJob, error) {
	repository.jobs = append(repository.jobs, job)
	return job, nil
}

type publishRepositoryFake struct {
	detail catalog.BookDetail
	jobs   []catalog.WorkerJob
}

func (repository *publishRepositoryFake) GetBook(
	context.Context,
	uuid.UUID,
) (catalog.BookDetail, error) {
	return repository.detail, nil
}

func (repository *publishRepositoryFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	_ string,
) (catalog.WorkerJob, error) {
	repository.jobs = append(repository.jobs, job)
	return job, nil
}

type downloaderFake struct {
	rawURL string
	data   []byte
}

func (downloader *downloaderFake) Fetch(
	_ context.Context,
	rawURL string,
) (covers.ImageInfo, []byte, error) {
	downloader.rawURL = rawURL
	return covers.ImageInfo{Width: 1200, Height: 1800}, downloader.data, nil
}

type publisherFake struct {
	request covers.Request
	err     error
}

func (publisher *publisherFake) Publish(
	_ context.Context,
	request covers.Request,
) (catalog.CoverEvent, error) {
	publisher.request = request
	return catalog.CoverEvent{ID: uuid.New()}, publisher.err
}
