package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/covers"
	"bookbox.local/server/internal/grouping"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/metadata"
	"bookbox.local/server/internal/scanner"
)

const (
	PollInterval       = 2 * time.Second
	ScanInterval       = 6 * time.Hour
	DefaultGracePeriod = 30 * time.Second

	completionTimeout = 5 * time.Second
	bookPageSize      = 100
	jobLeaseDuration  = 15 * time.Minute
)

type Handler interface {
	Handle(context.Context, catalog.WorkerJob) error
}

type HandlerFunc func(context.Context, catalog.WorkerJob) error

func (function HandlerFunc) Handle(
	ctx context.Context,
	job catalog.WorkerJob,
) error {
	return function(ctx, job)
}

type JobRepository interface {
	LeaseRepository
	RecoverExpiredJobs(context.Context, time.Time) error
	DrainReadyFingerprintOutbox(context.Context, time.Time, int) (int, error)
	ClaimJob(context.Context, string, time.Time) (catalog.WorkerJob, error)
	CompleteJob(context.Context, uuid.UUID, uuid.UUID, error) error
	EnqueueDeduplicated(
		context.Context,
		catalog.WorkerJob,
		string,
	) (catalog.WorkerJob, error)
}

type Worker struct {
	Repo         JobRepository
	Roots        []config.Root
	WorkerID     string
	Handlers     map[string]Handler
	Clock        func() time.Time
	Wait         func(context.Context, time.Duration) error
	GracePeriod  time.Duration
	ScanInterval time.Duration
}

func (worker *Worker) Run(ctx context.Context) error {
	if worker == nil || worker.Repo == nil ||
		strings.TrimSpace(worker.WorkerID) == "" {
		return errors.New("worker repository and identity are required")
	}
	clock := worker.Clock
	if clock == nil {
		clock = time.Now
	}
	wait := worker.Wait
	if wait == nil {
		wait = waitFor
	}
	gracePeriod := worker.GracePeriod
	if gracePeriod <= 0 {
		gracePeriod = DefaultGracePeriod
	}
	scanInterval := worker.ScanInterval
	if scanInterval <= 0 {
		scanInterval = ScanInterval
	}

	now := clock().UTC()
	if err := worker.Repo.RecoverExpiredJobs(ctx, now); err != nil {
		return fmt.Errorf("recover expired worker jobs: %w", err)
	}
	if err := worker.scheduleScans(ctx, now); err != nil {
		return fmt.Errorf("schedule startup scans: %w", err)
	}
	if _, err := worker.Repo.DrainReadyFingerprintOutbox(ctx, now, 256); err != nil {
		return fmt.Errorf("drain fingerprint outbox: %w", err)
	}
	nextScan := now.Add(scanInterval)

	for {
		if ctx.Err() != nil {
			return nil
		}
		now = clock().UTC()
		if _, err := worker.Repo.DrainReadyFingerprintOutbox(ctx, now, 256); err != nil {
			return fmt.Errorf("drain fingerprint outbox: %w", err)
		}
		if !now.Before(nextScan) {
			if err := worker.scheduleScans(ctx, now); err != nil {
				return fmt.Errorf("schedule periodic scans: %w", err)
			}
			for !nextScan.After(now) {
				nextScan = nextScan.Add(scanInterval)
			}
		}

		job, err := worker.Repo.ClaimJob(ctx, worker.WorkerID, now)
		if errors.Is(err, catalog.ErrNoJob) {
			if wait(ctx, PollInterval) != nil && ctx.Err() == nil {
				return errors.New("worker idle wait failed")
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claim worker job: %w", err)
		}

		handler := worker.Handlers[job.Kind]
		var jobErr error
		if handler == nil {
			jobErr = fmt.Errorf("unsupported job kind %q", job.Kind)
		} else {
			jobErr = runWithGrace(ctx, gracePeriod, func(operationCtx context.Context) error {
				return runWithLease(
					operationCtx, worker.Repo, job, jobLeaseDuration,
					func(handlerCtx context.Context) error {
						return handler.Handle(handlerCtx, job)
					},
				)
			})
		}
		completionCtx, cancel := context.WithTimeout(context.Background(), completionTimeout)
		completeErr := worker.Repo.CompleteJob(
			completionCtx,
			job.ID,
			job.LeaseToken,
			jobErr,
		)
		cancel()
		if completeErr != nil {
			return fmt.Errorf("complete worker job: %w", completeErr)
		}
	}
}

func (worker *Worker) scheduleScans(ctx context.Context, now time.Time) error {
	for _, root := range worker.Roots {
		if strings.TrimSpace(root.ID) == "" {
			return errors.New("scan root ID is required")
		}
		job := catalog.WorkerJob{
			ID:       uuid.New(),
			Kind:     catalog.JobScanRoot,
			Payload:  map[string]any{"rootId": root.ID, "relativePath": "", "full": true},
			RunAfter: now,
		}
		if _, err := worker.Repo.EnqueueDeduplicated(
			ctx,
			job,
			"scan_root:"+root.ID+":",
		); err != nil {
			return err
		}
	}
	return nil
}

func waitFor(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runWithGrace(
	parent context.Context,
	gracePeriod time.Duration,
	operation func(context.Context) error,
) error {
	operationCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		select {
		case <-parent.Done():
			timer := time.NewTimer(gracePeriod)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-finished:
			}
		case <-finished:
		}
	}()
	err := operation(operationCtx)
	close(finished)
	return err
}

type Scanner interface {
	Scan(context.Context, config.Root) (scanner.Result, error)
}

type InventoryScanner interface {
	Scan(context.Context, config.Root, scanner.Scope) (scanner.InventoryResult, error)
}

type ScanRepository interface {
	BeginScan(context.Context, string) (catalog.ScanRun, error)
	UpsertSourceFiles(context.Context, uuid.UUID, []catalog.SourceFile) error
	FinalizeSuccessfulScan(context.Context, uuid.UUID, []catalog.InferredBook) error
	FinishScan(context.Context, uuid.UUID, catalog.ScanResult) error
	ListBooks(context.Context, catalog.BookQuery) (catalog.Page[catalog.Book], error)
	EnqueueDeduplicated(
		context.Context,
		catalog.WorkerJob,
		string,
	) (catalog.WorkerJob, error)
}

type ScanRootHandler struct {
	Repo         ScanRepository
	Scanner      Scanner
	Inventory    InventoryScanner
	Roots        map[string]config.Root
	ValidateRoot func(string) error
	LoadTree     func(context.Context, string, string) (grouping.Tree, error)
	InferTree    func(grouping.Tree) grouping.Result
}

func (handler *ScanRootHandler) Handle(
	ctx context.Context,
	job catalog.WorkerJob,
) error {
	if handler == nil || handler.Repo == nil ||
		(handler.Scanner == nil && handler.Inventory == nil) ||
		job.Kind != catalog.JobScanRoot {
		return errors.New("invalid scan job")
	}
	rootID, err := requiredPayloadString(job.Payload, "rootId")
	if err != nil {
		return err
	}
	root, exists := handler.Roots[rootID]
	if !exists {
		return errors.New("scan root is not configured")
	}
	validateRoot := handler.ValidateRoot
	if validateRoot == nil {
		validateRoot = library.ValidateLocalRoot
	}
	if err := validateRoot(root.ContainerPath); err != nil {
		return fmt.Errorf("validate scan root: %w", err)
	}
	if handler.Inventory != nil {
		return handler.handleInventory(ctx, job, root)
	}
	run, err := handler.Repo.BeginScan(ctx, rootID)
	if err != nil {
		return fmt.Errorf("begin scan: %w", err)
	}

	result, scanErr := handler.Scanner.Scan(ctx, root)
	if scanErr != nil {
		finishErr := handler.Repo.FinishScan(ctx, run.ID, catalog.ScanResult{
			Success: false,
			Error:   "source scan failed",
		})
		if finishErr != nil {
			return errors.Join(scanErr, fmt.Errorf("finish failed scan: %w", finishErr))
		}
		return fmt.Errorf("scan source: %w", scanErr)
	}
	if len(result.Warnings) != 0 {
		finishErr := handler.Repo.FinishScan(ctx, run.ID, catalog.ScanResult{
			Success: false,
			Partial: true,
			Error:   "source scan was incomplete",
		})
		if finishErr != nil {
			return fmt.Errorf("finish partial scan: %w", finishErr)
		}
		return errors.New("source scan was incomplete")
	}

	files := make([]catalog.SourceFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, sourceFile(run.ID, file))
	}
	if err := handler.Repo.UpsertSourceFiles(ctx, run.ID, files); err != nil {
		return handler.failScan(ctx, run.ID, "source file indexing failed", err)
	}
	var books []catalog.InferredBook
	for _, folder := range result.Folders {
		books = append(books, grouping.Infer(folder)...)
	}
	if err := handler.Repo.FinalizeSuccessfulScan(ctx, run.ID, books); err != nil {
		return handler.failScan(ctx, run.ID, "search projection publication failed", err)
	}
	if err := handler.queueIncompleteBooks(ctx, rootID); err != nil {
		return fmt.Errorf("queue metadata enrichment: %w", err)
	}
	return nil
}

func (handler *ScanRootHandler) failScan(
	ctx context.Context,
	runID uuid.UUID,
	message string,
	cause error,
) error {
	finishErr := handler.Repo.FinishScan(ctx, runID, catalog.ScanResult{
		Success: false,
		Error:   message,
	})
	if finishErr != nil {
		return errors.Join(cause, fmt.Errorf("finish failed scan: %w", finishErr))
	}
	return cause
}

func (handler *ScanRootHandler) queueIncompleteBooks(
	ctx context.Context,
	rootID string,
) error {
	return queueIncompleteBooks(ctx, handler.Repo, rootID)
}

type incompleteBookRepository interface {
	ListBooks(context.Context, catalog.BookQuery) (catalog.Page[catalog.Book], error)
	EnqueueDeduplicated(
		context.Context,
		catalog.WorkerJob,
		string,
	) (catalog.WorkerJob, error)
}

func queueIncompleteBooks(
	ctx context.Context,
	repository incompleteBookRepository,
	rootID string,
) error {
	cursor := ""
	for {
		page, err := repository.ListBooks(ctx, catalog.BookQuery{
			RootID: rootID,
			Sort:   catalog.BookSortTitle,
			Limit:  bookPageSize,
			Cursor: cursor,
		})
		if err != nil {
			return err
		}
		for _, book := range page.Items {
			if book.ReviewState == catalog.ReviewConfirmed ||
				book.MetadataSource == catalog.MetadataConfirmed ||
				book.MissingAt != nil {
				continue
			}
			job := catalog.WorkerJob{
				ID:      uuid.New(),
				Kind:    catalog.JobEnrichBook,
				Payload: map[string]any{"bookId": book.ID.String()},
			}
			if _, err := repository.EnqueueDeduplicated(
				ctx,
				job,
				"enrich_book:"+book.ID.String(),
			); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		if page.NextCursor == cursor {
			return errors.New("book pagination cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func sourceFile(scanRunID uuid.UUID, file scanner.File) catalog.SourceFile {
	mediaValues := map[string]any{
		"role":        string(file.Kind),
		"durationMs":  file.Metadata.Duration.Milliseconds(),
		"title":       file.Metadata.Title,
		"album":       file.Metadata.Album,
		"albumArtist": file.Metadata.AlbumArtist,
		"author":      file.Metadata.Author,
		"artist":      file.Metadata.Artist,
		"narrator":    file.Metadata.Narrator,
		"genre":       file.Metadata.Genre,
		"date":        file.Metadata.Date,
		"series":      file.Metadata.Series,
		"track":       file.Metadata.Track,
		"disc":        file.Metadata.Disc,
		"identifiers": file.Metadata.Identifiers,
		"audioCodec":  file.Metadata.AudioCodec,
		"width":       file.Metadata.Width,
		"height":      file.Metadata.Height,
	}
	if file.ProbeError != "" {
		mediaValues["probeError"] = file.ProbeError
	}
	return catalog.SourceFile{
		ID:           file.ID,
		RootID:       file.RootID,
		RelativePath: file.RelativePath,
		Size:         file.Size,
		ModifiedAt:   file.ModifiedAt,
		Media:        mediaValues,
		ScanRunID:    &scanRunID,
	}
}

type Matcher interface {
	Match(context.Context, catalog.InferredBook) (metadata.Decision, error)
}

type EnrichRepository interface {
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
	SaveMatches(context.Context, uuid.UUID, []catalog.MetadataMatch) error
	ConfirmMatch(context.Context, uuid.UUID, uuid.UUID) error
	EnqueueDeduplicated(
		context.Context,
		catalog.WorkerJob,
		string,
	) (catalog.WorkerJob, error)
}

type EnrichBookHandler struct {
	Repo    EnrichRepository
	Matcher Matcher
}

func (handler *EnrichBookHandler) Handle(
	ctx context.Context,
	job catalog.WorkerJob,
) error {
	if handler == nil || handler.Repo == nil || handler.Matcher == nil ||
		job.Kind != catalog.JobEnrichBook {
		return errors.New("invalid enrichment job")
	}
	bookID, err := requiredPayloadUUID(job.Payload, "bookId")
	if err != nil {
		return err
	}
	detail, err := handler.Repo.GetBook(ctx, bookID)
	if err != nil {
		return fmt.Errorf("load enrichment book: %w", err)
	}
	if detail.Book.ID != bookID || detail.Book.MissingAt != nil {
		return errors.New("enrichment book is unavailable")
	}
	if detail.Book.ReviewState == catalog.ReviewConfirmed ||
		detail.Book.MetadataSource == catalog.MetadataConfirmed {
		return nil
	}
	decision, err := handler.Matcher.Match(ctx, inferredFromBook(detail.Book))
	if err != nil {
		return fmt.Errorf("match book metadata: %w", err)
	}
	matches := make([]catalog.MetadataMatch, 0, len(decision.Candidates))
	for _, scored := range decision.Candidates {
		matches = append(matches, metadataMatch(bookID, scored))
	}
	if err := handler.Repo.SaveMatches(ctx, bookID, matches); err != nil {
		return fmt.Errorf("save metadata matches: %w", err)
	}
	if decision.Selected == nil {
		return nil
	}
	match, found := selectedMatch(matches, *decision.Selected)
	if !found {
		return errors.New("selected metadata match was not persisted")
	}
	if err := handler.Repo.ConfirmMatch(ctx, bookID, match.ID); err != nil {
		return fmt.Errorf("confirm metadata match: %w", err)
	}
	if strings.TrimSpace(match.CoverURL) == "" {
		return nil
	}
	publish := catalog.WorkerJob{
		ID:   uuid.New(),
		Kind: catalog.JobPublishCover,
		Payload: map[string]any{
			"bookId":  bookID.String(),
			"matchId": match.ID.String(),
		},
	}
	if _, err := handler.Repo.EnqueueDeduplicated(
		ctx,
		publish,
		"publish_cover:"+bookID.String()+":"+match.ID.String(),
	); err != nil {
		return fmt.Errorf("queue cover publication: %w", err)
	}
	return nil
}

func inferredFromBook(book catalog.Book) catalog.InferredBook {
	return catalog.InferredBook{
		RootID:           book.RootID,
		SourceFolder:     book.SourceFolder,
		GroupingKey:      book.GroupingKey,
		Title:            book.Title,
		Subtitle:         book.Subtitle,
		Authors:          append([]string(nil), book.Authors...),
		Narrators:        append([]string(nil), book.Narrators...),
		Series:           book.Series,
		SeriesPosition:   book.SeriesPosition,
		Publisher:        book.Publisher,
		PublishedDate:    book.PublishedDate,
		PublicationYear:  book.PublicationYear,
		Genres:           append([]string(nil), book.Genres...),
		ProviderSubjects: append([]string(nil), book.ProviderSubjects...),
		Identifiers:      book.Identifiers,
		MetadataSource:   book.MetadataSource,
		Confidence:       book.Confidence,
		ReviewState:      book.ReviewState,
	}
}

func metadataMatch(
	bookID uuid.UUID,
	scored metadata.ScoredCandidate,
) catalog.MetadataMatch {
	candidate := scored.Candidate
	matchID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(bookID.String()+"\x00"+candidate.Provider+"\x00"+candidate.ProviderID),
	)
	return catalog.MetadataMatch{
		ID:         matchID,
		BookID:     bookID,
		Provider:   candidate.Provider,
		ProviderID: candidate.ProviderID,
		Score:      scored.Score,
		CoverURL:   candidate.CoverURL,
		Payload: map[string]any{
			"title":          candidate.Title,
			"subtitle":       candidate.Subtitle,
			"publisher":      candidate.Publisher,
			"publishedDate":  candidate.PublishedDate,
			"authors":        candidate.Authors,
			"narrators":      candidate.Narrators,
			"genres":         candidate.Genres,
			"subjects":       candidate.Subjects,
			"series":         candidate.Series,
			"seriesPosition": candidate.SeriesPosition,
			"isbn":           candidate.ISBN,
		},
	}
}

func selectedMatch(
	matches []catalog.MetadataMatch,
	selected metadata.Candidate,
) (catalog.MetadataMatch, bool) {
	for _, match := range matches {
		if match.Provider == selected.Provider &&
			match.ProviderID == selected.ProviderID {
			return match, true
		}
	}
	return catalog.MetadataMatch{}, false
}

type CoverDownloader interface {
	Fetch(context.Context, string) (covers.ImageInfo, []byte, error)
}

type CoverPublisher interface {
	Publish(context.Context, covers.Request) (catalog.CoverEvent, error)
}

type PublishRepository interface {
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
	EnqueueDeduplicated(
		context.Context,
		catalog.WorkerJob,
		string,
	) (catalog.WorkerJob, error)
}

type PublishCoverHandler struct {
	Repo       PublishRepository
	Downloader CoverDownloader
	Publisher  CoverPublisher
}

func (handler *PublishCoverHandler) Handle(
	ctx context.Context,
	job catalog.WorkerJob,
) error {
	if handler == nil || handler.Repo == nil || handler.Downloader == nil ||
		handler.Publisher == nil || job.Kind != catalog.JobPublishCover {
		return errors.New("invalid cover publication job")
	}
	bookID, err := requiredPayloadUUID(job.Payload, "bookId")
	if err != nil {
		return err
	}
	matchID, err := requiredPayloadUUID(job.Payload, "matchId")
	if err != nil {
		return err
	}
	detail, err := handler.Repo.GetBook(ctx, bookID)
	if err != nil {
		return fmt.Errorf("load cover book: %w", err)
	}
	if detail.Book.ID != bookID || detail.Book.MissingAt != nil ||
		detail.Book.ConfirmedMatchID == nil ||
		*detail.Book.ConfirmedMatchID != matchID {
		return errors.New("cover match is no longer confirmed")
	}
	match, found := confirmedMatch(detail.Matches, bookID, matchID)
	if !found || strings.TrimSpace(match.CoverURL) == "" {
		return errors.New("confirmed cover URL is unavailable")
	}
	_, image, err := handler.Downloader.Fetch(ctx, match.CoverURL)
	if err != nil {
		return fmt.Errorf("download cover: %w", err)
	}
	if _, err := handler.Publisher.Publish(ctx, covers.Request{
		BookID:      bookID,
		Destination: "",
		Candidate:   image,
		Provider:    match.Provider,
	}); err != nil {
		if errors.Is(err, covers.ErrNoImprovement) {
			return nil
		}
		return fmt.Errorf("publish cover: %w", err)
	}
	refresh := catalog.WorkerJob{
		ID:      uuid.New(),
		Kind:    catalog.JobScanRoot,
		Payload: map[string]any{"rootId": detail.Book.RootID},
	}
	if _, err := handler.Repo.EnqueueDeduplicated(
		ctx,
		refresh,
		"scan_root:"+detail.Book.RootID,
	); err != nil {
		return fmt.Errorf("queue post-publication scan: %w", err)
	}
	return nil
}

func confirmedMatch(
	matches []catalog.MetadataMatch,
	bookID uuid.UUID,
	matchID uuid.UUID,
) (catalog.MetadataMatch, bool) {
	for _, match := range matches {
		if match.ID == matchID && match.BookID == bookID && match.Confirmed {
			return match, true
		}
	}
	return catalog.MetadataMatch{}, false
}

func requiredPayloadString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("job payload %s is required", key)
	}
	return value, nil
}

func requiredPayloadUUID(payload map[string]any, key string) (uuid.UUID, error) {
	value, err := requiredPayloadString(payload, key)
	if err != nil {
		return uuid.Nil, err
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, fmt.Errorf("job payload %s is invalid", key)
	}
	return parsed, nil
}
