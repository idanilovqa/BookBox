package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/metadata"
	"github.com/google/uuid"
)

const maxCatalogBodyBytes = 64 << 10

type CatalogRepository interface {
	SearchLibrary(context.Context, catalog.SearchQuery) (catalog.SearchPage, error)
	ListBooks(context.Context, catalog.BookQuery) (catalog.Page[catalog.Book], error)
	ListCatalogFilters(context.Context) (catalog.FilterSnapshot, error)
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
	GetGroupingContext(context.Context, uuid.UUID) (catalog.GroupingContext, error)
	SaveMatches(context.Context, uuid.UUID, []catalog.MetadataMatch) error
	ConfirmMatch(context.Context, uuid.UUID, uuid.UUID) error
	SaveGrouping(context.Context, catalog.GroupingCorrection) error
	Enqueue(context.Context, catalog.WorkerJob) error
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type MetadataMatcher interface {
	Match(context.Context, catalog.InferredBook) (metadata.Decision, error)
}

type MatchSearchRequest struct {
	BookID string `json:"bookId"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	ISBN   string `json:"isbn,omitempty"`
}

type MetadataCandidateResponse struct {
	MatchID        string   `json:"matchId"`
	Provider       string   `json:"provider"`
	ProviderID     string   `json:"providerId"`
	Title          string   `json:"title"`
	Subtitle       string   `json:"subtitle,omitempty"`
	Authors        []string `json:"authors"`
	Publisher      string   `json:"publisher,omitempty"`
	PublishedDate  string   `json:"publishedDate,omitempty"`
	Genres         []string `json:"genres"`
	Subjects       []string `json:"subjects"`
	Series         string   `json:"series,omitempty"`
	SeriesPosition string   `json:"seriesPosition,omitempty"`
	ISBN           string   `json:"isbn,omitempty"`
	CoverURL       string   `json:"coverUrl,omitempty"`
	Score          float64  `json:"score"`
}

type MatchSearchResponse struct {
	BookID     string                      `json:"bookId"`
	Candidates []MetadataCandidateResponse `json:"candidates"`
}

type ConfirmMatchRequest struct {
	MatchID string `json:"matchId"`
}

type GroupingRequest struct {
	Groups []GroupingRequestGroup `json:"groups"`
}

type GroupingRequestGroup struct {
	BookID  string   `json:"bookId,omitempty"`
	FileIDs []string `json:"fileIds"`
}

type GroupingContextResponse struct {
	BookID       string                        `json:"bookId"`
	RootID       string                        `json:"rootId"`
	SourceFolder string                        `json:"sourceFolder"`
	Files        []BookFileResponse            `json:"files"`
	Books        []GroupingContextBookResponse `json:"books"`
}

type GroupingContextBookResponse struct {
	BookID  string   `json:"bookId"`
	Title   string   `json:"title"`
	FileIDs []string `json:"fileIds"`
}

type RescanRequest struct {
	RootID string `json:"rootId"`
}

type acceptedJobResponse struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

func catalogFiltersHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		filters, err := repository.ListCatalogFilters(request.Context())
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, filters)
	})
}

func matchSearchHandler(
	repository CatalogRepository,
	matcher MetadataMatcher,
) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil || matcher == nil {
			writeInternalError(response)
			return
		}
		var input MatchSearchRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		bookID, err := uuid.Parse(input.BookID)
		if err != nil || bookID == uuid.Nil {
			writeStableError(response, http.StatusBadRequest,
				"invalid_book_id", "book identifier is invalid")
			return
		}
		detail, err := repository.GetBook(request.Context(), bookID)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		if detail.Book.ID != bookID || !publicBook(detail.Book) {
			writeInternalError(response)
			return
		}
		title := strings.TrimSpace(input.Title)
		if title == "" {
			title = detail.Book.Title
		}
		author := strings.TrimSpace(input.Author)
		if author == "" && len(detail.Book.Authors) > 0 {
			author = detail.Book.Authors[0]
		}
		isbn := strings.TrimSpace(input.ISBN)
		if len(title) > 500 || len(author) > 500 || len(isbn) > 64 {
			writeInvalidRequest(response)
			return
		}
		local := bookAsInferred(detail.Book)
		// An explicit user search is allowed to replace a previously confirmed
		// match. Matcher normally protects confirmed metadata from automatic
		// enrichment, so present this one request as reviewable input.
		local.MetadataSource = catalog.MetadataInferred
		local.ReviewState = catalog.ReviewUnreviewed
		local.Title = title
		if author == "" {
			local.Authors = []string{}
		} else {
			local.Authors = []string{author}
		}
		if isbn != "" {
			local.Identifiers = map[string]any{"isbn": isbn}
		}
		decision, err := matcher.Match(request.Context(), local)
		if err != nil {
			writeInternalError(response)
			return
		}
		matches := make([]catalog.MetadataMatch, 0, len(decision.Candidates))
		candidates := make([]MetadataCandidateResponse, 0, len(decision.Candidates))
		for _, scored := range decision.Candidates {
			candidate := scored.Candidate
			matchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(
				bookID.String()+"\x00"+candidate.Provider+"\x00"+candidate.ProviderID,
			))
			match := catalog.MetadataMatch{
				ID: matchID, BookID: bookID, Provider: candidate.Provider,
				ProviderID: candidate.ProviderID, Score: scored.Score,
				Payload: candidatePayload(candidate), CoverURL: candidate.CoverURL,
			}
			matches = append(matches, match)
			candidates = append(candidates, candidateResponse(matchID, scored))
		}
		if err := repository.SaveMatches(request.Context(), bookID, matches); err != nil {
			writeCatalogError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, MatchSearchResponse{
			BookID: bookID.String(), Candidates: candidates,
		})
	})
}

func confirmMatchHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		bookID, ok := parsePathBookID(response, request)
		if !ok {
			return
		}
		var input ConfirmMatchRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		matchID, err := uuid.Parse(input.MatchID)
		if err != nil || matchID == uuid.Nil {
			writeStableError(response, http.StatusBadRequest,
				"invalid_match_id", "match identifier is invalid")
			return
		}
		if err := repository.ConfirmMatch(request.Context(), bookID, matchID); err != nil {
			writeCatalogError(response, err)
			return
		}
		enqueued, err := repository.EnqueueDeduplicated(request.Context(), catalog.WorkerJob{
			ID: uuid.New(), Kind: catalog.JobPublishCover,
			Payload: map[string]any{
				"bookId": bookID.String(), "matchId": matchID.String(),
			},
		}, catalog.JobPublishCover+":"+bookID.String()+":"+matchID.String())
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted,
			acceptedJobResponse{JobID: enqueued.ID.String(), Status: enqueued.Status})
	})
}

func groupingContextHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		bookID, ok := parsePathBookID(response, request)
		if !ok {
			return
		}
		grouping, err := repository.GetGroupingContext(request.Context(), bookID)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		if grouping.BookID != bookID || grouping.RootID == "" ||
			catalog.ValidateSourceFolder(grouping.SourceFolder) != nil {
			writeInternalError(response)
			return
		}

		result := GroupingContextResponse{
			BookID: grouping.BookID.String(), RootID: grouping.RootID,
			SourceFolder: grouping.SourceFolder,
			Files:        make([]BookFileResponse, 0, len(grouping.Files)),
			Books:        make([]GroupingContextBookResponse, 0, len(grouping.Books)),
		}
		availableFiles := make(map[uuid.UUID]struct{}, len(grouping.Files))
		for _, file := range grouping.Files {
			if file.ID == uuid.Nil || file.RootID != grouping.RootID ||
				file.MissingAt != nil || file.Size < 0 || file.ModifiedAt.IsZero() ||
				catalog.ValidateRelativePath(file.RelativePath) != nil ||
				!inSourceFolder(file.RelativePath, grouping.SourceFolder) ||
				!isAudioPath(file.RelativePath) {
				writeInternalError(response)
				return
			}
			if _, duplicate := availableFiles[file.ID]; duplicate {
				writeInternalError(response)
				return
			}
			availableFiles[file.ID] = struct{}{}
			result.Files = append(result.Files, BookFileResponse{
				ID: file.ID.String(), RelativePath: file.RelativePath,
				Name: path.Base(file.RelativePath), Size: file.Size,
				ModifiedAt: file.ModifiedAt.UTC(),
			})
		}

		seenBooks := make(map[uuid.UUID]struct{}, len(grouping.Books))
		assignedFiles := make(map[uuid.UUID]struct{})
		requestedBookFound := false
		for _, book := range grouping.Books {
			if book.BookID == uuid.Nil || strings.TrimSpace(book.Title) == "" ||
				len(book.Title) > 1000 ||
				strings.IndexFunc(book.Title, unicode.IsControl) >= 0 {
				writeInternalError(response)
				return
			}
			if _, duplicate := seenBooks[book.BookID]; duplicate {
				writeInternalError(response)
				return
			}
			seenBooks[book.BookID] = struct{}{}
			if book.BookID == bookID {
				requestedBookFound = true
			}
			publicFileIDs := make([]string, 0, len(book.FileIDs))
			for _, fileID := range book.FileIDs {
				if _, exists := availableFiles[fileID]; !exists {
					writeInternalError(response)
					return
				}
				if _, duplicate := assignedFiles[fileID]; duplicate {
					writeInternalError(response)
					return
				}
				assignedFiles[fileID] = struct{}{}
				publicFileIDs = append(publicFileIDs, fileID.String())
			}
			result.Books = append(result.Books, GroupingContextBookResponse{
				BookID: book.BookID.String(), Title: book.Title, FileIDs: publicFileIDs,
			})
		}
		if !requestedBookFound {
			writeInternalError(response)
			return
		}
		writeJSON(response, http.StatusOK, result)
	})
}

func groupingHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		bookID, ok := parsePathBookID(response, request)
		if !ok {
			return
		}
		var input GroupingRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		if len(input.Groups) == 0 || len(input.Groups) > 100 {
			writeInvalidRequest(response)
			return
		}
		folder, err := repository.GetGroupingContext(request.Context(), bookID)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		if folder.BookID != bookID || folder.RootID == "" ||
			catalog.ValidateSourceFolder(folder.SourceFolder) != nil {
			writeInternalError(response)
			return
		}
		allowed := make(map[uuid.UUID]struct{}, len(folder.Files))
		for _, file := range folder.Files {
			if file.ID == uuid.Nil || file.RootID != folder.RootID ||
				file.MissingAt != nil ||
				catalog.ValidateRelativePath(file.RelativePath) != nil ||
				!inSourceFolder(file.RelativePath, folder.SourceFolder) ||
				!isAudioPath(file.RelativePath) {
				writeInternalError(response)
				return
			}
			if _, duplicate := allowed[file.ID]; duplicate {
				writeInternalError(response)
				return
			}
			allowed[file.ID] = struct{}{}
		}
		bookContexts := make(map[uuid.UUID]catalog.GroupingContextBook, len(folder.Books))
		requestedBookFound := false
		for _, book := range folder.Books {
			if book.BookID == uuid.Nil || strings.TrimSpace(book.GroupingKey) == "" {
				writeInternalError(response)
				return
			}
			if _, duplicate := bookContexts[book.BookID]; duplicate {
				writeInternalError(response)
				return
			}
			bookContexts[book.BookID] = book
			if book.BookID == bookID {
				requestedBookFound = true
			}
		}
		if !requestedBookFound || len(allowed) == 0 {
			writeCatalogError(response, catalog.ErrConflict)
			return
		}
		seenBooks := make(map[uuid.UUID]struct{})
		for _, group := range input.Groups {
			if group.BookID == "" {
				continue
			}
			groupBookID, err := uuid.Parse(group.BookID)
			if err != nil || groupBookID == uuid.Nil {
				writeInvalidRequest(response)
				return
			}
			if _, duplicate := seenBooks[groupBookID]; duplicate {
				writeInvalidRequest(response)
				return
			}
			seenBooks[groupBookID] = struct{}{}
			if _, exists := bookContexts[groupBookID]; !exists {
				writeCatalogError(response, catalog.ErrConflict)
				return
			}
		}
		correction := catalog.GroupingCorrection{
			ID: uuid.New(), RootID: folder.RootID,
			SourceFolder: folder.SourceFolder, UpdatedAt: time.Now().UTC(),
			Groups: make([]catalog.GroupingGroup, 0, len(input.Groups)),
		}
		seenFiles := make(map[uuid.UUID]struct{}, len(allowed))
		for _, group := range input.Groups {
			if len(group.FileIDs) == 0 {
				writeInvalidRequest(response)
				return
			}
			fileIDs := make([]uuid.UUID, 0, len(group.FileIDs))
			for _, encoded := range group.FileIDs {
				fileID, err := uuid.Parse(encoded)
				if err != nil || fileID == uuid.Nil {
					writeInvalidRequest(response)
					return
				}
				if _, exists := allowed[fileID]; !exists {
					writeCatalogError(response, catalog.ErrConflict)
					return
				}
				if _, duplicate := seenFiles[fileID]; duplicate {
					writeInvalidRequest(response)
					return
				}
				seenFiles[fileID] = struct{}{}
				fileIDs = append(fileIDs, fileID)
			}
			sort.Slice(fileIDs, func(left, right int) bool {
				return fileIDs[left].String() < fileIDs[right].String()
			})
			var existingBookID *uuid.UUID
			groupingKey := manualGroupingKey(catalog.Book{
				RootID: folder.RootID, SourceFolder: folder.SourceFolder,
			}, fileIDs)
			if group.BookID != "" {
				parsed := uuid.MustParse(group.BookID)
				existingBookID = &parsed
				groupingKey = bookContexts[parsed].GroupingKey
			}
			correction.Groups = append(correction.Groups, catalog.GroupingGroup{
				BookID: existingBookID, GroupingKey: groupingKey, FileIDs: fileIDs,
			})
		}
		if len(seenFiles) != len(allowed) {
			writeCatalogError(response, catalog.ErrConflict)
			return
		}
		if err := repository.SaveGrouping(request.Context(), correction); err != nil {
			writeCatalogError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
}

func rescanHandler(
	repository CatalogRepository,
	roots []config.Root,
	now func() time.Time,
) http.Handler {
	if now == nil {
		now = time.Now
	}
	knownRoots := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		knownRoots[root.ID] = struct{}{}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		var input RescanRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		if _, exists := knownRoots[input.RootID]; !exists {
			writeStableError(response, http.StatusNotFound, "root_not_found", "root not found")
			return
		}
		enqueued, err := repository.EnqueueDeduplicated(request.Context(), catalog.WorkerJob{
			ID: uuid.New(), Kind: catalog.JobScanRoot, RunAfter: now().UTC(),
			Payload: map[string]any{"rootId": input.RootID},
		}, catalog.JobScanRoot+":"+input.RootID)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted,
			acceptedJobResponse{JobID: enqueued.ID.String(), Status: enqueued.Status})
	})
}

func decodeCatalogJSON(
	response http.ResponseWriter,
	request *http.Request,
	destination any,
) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeStableError(response, http.StatusUnsupportedMediaType,
			"unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxCatalogBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeStableError(response, http.StatusRequestEntityTooLarge,
				"request_too_large", "request body is too large")
		} else {
			writeInvalidRequest(response)
		}
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeStableError(response, http.StatusRequestEntityTooLarge,
				"request_too_large", "request body is too large")
		} else {
			writeInvalidRequest(response)
		}
		return false
	}
	return true
}

func parsePathBookID(
	response http.ResponseWriter,
	request *http.Request,
) (uuid.UUID, bool) {
	bookID, err := uuid.Parse(request.PathValue("bookId"))
	if err != nil || bookID == uuid.Nil {
		writeStableError(response, http.StatusBadRequest,
			"invalid_book_id", "book identifier is invalid")
		return uuid.Nil, false
	}
	return bookID, true
}

func bookAsInferred(book catalog.Book) catalog.InferredBook {
	return catalog.InferredBook{
		RootID: book.RootID, SourceFolder: book.SourceFolder,
		GroupingKey: book.GroupingKey, Title: book.Title, Subtitle: book.Subtitle,
		Authors: nonNil(book.Authors), Narrators: nonNil(book.Narrators),
		Series: book.Series, SeriesPosition: book.SeriesPosition,
		Publisher: book.Publisher, PublishedDate: book.PublishedDate,
		PublicationYear: book.PublicationYear, Genres: nonNil(book.Genres),
		ProviderSubjects: nonNil(book.ProviderSubjects),
		Identifiers:      book.Identifiers, MetadataSource: book.MetadataSource,
		Confidence: book.Confidence, ReviewState: book.ReviewState,
	}
}

func candidatePayload(candidate metadata.Candidate) map[string]any {
	return map[string]any{
		"title": candidate.Title, "subtitle": candidate.Subtitle,
		"publisher": candidate.Publisher, "publishedDate": candidate.PublishedDate,
		"authors": nonNil(candidate.Authors), "narrators": nonNil(candidate.Narrators),
		"genres": nonNil(candidate.Genres), "subjects": nonNil(candidate.Subjects),
		"series": candidate.Series, "seriesPosition": candidate.SeriesPosition,
		"isbn": candidate.ISBN,
	}
}

func candidateResponse(
	matchID uuid.UUID,
	scored metadata.ScoredCandidate,
) MetadataCandidateResponse {
	candidate := scored.Candidate
	return MetadataCandidateResponse{
		MatchID: matchID.String(), Provider: candidate.Provider,
		ProviderID: candidate.ProviderID, Title: candidate.Title,
		Subtitle: candidate.Subtitle, Authors: nonNil(candidate.Authors),
		Publisher: candidate.Publisher, PublishedDate: candidate.PublishedDate,
		Genres: nonNil(candidate.Genres), Subjects: nonNil(candidate.Subjects),
		Series: candidate.Series, SeriesPosition: candidate.SeriesPosition,
		ISBN: candidate.ISBN, CoverURL: publicCoverURL(candidate.CoverURL),
		Score: scored.Score,
	}
}

func publicCoverURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if lower == "key" || strings.Contains(lower, "token") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func addAudioFiles(result map[uuid.UUID]struct{}, detail catalog.BookDetail) {
	for _, file := range detail.Files {
		if file.ID != uuid.Nil && file.MissingAt == nil &&
			file.RootID == detail.Book.RootID &&
			inSourceFolder(file.RelativePath, detail.Book.SourceFolder) &&
			isAudioPath(file.RelativePath) {
			result[file.ID] = struct{}{}
		}
	}
}

func isAudioPath(relative string) bool {
	switch strings.ToLower(path.Ext(relative)) {
	case ".m4b", ".m4a", ".mp3", ".aac", ".flac", ".ogg", ".opus", ".wav":
		return true
	default:
		return false
	}
}

func sameBookFolder(left, right catalog.Book) bool {
	return publicBook(right) && left.RootID == right.RootID &&
		left.SourceFolder == right.SourceFolder
}

func manualGroupingKey(book catalog.Book, fileIDs []uuid.UUID) string {
	sorted := append([]uuid.UUID(nil), fileIDs...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].String() < sorted[right].String()
	})
	parts := []string{
		book.RootID, book.SourceFolder,
	}
	for _, id := range sorted {
		parts = append(parts, id.String())
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join(parts, "\x00")))
	return "manual-" + id.String()
}
