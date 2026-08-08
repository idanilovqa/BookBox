package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
	"github.com/google/uuid"
)

func TestBooksHandlersReturnCardsDetailsAndRelativeFiles(t *testing.T) {
	t.Parallel()

	year := 1965
	bookID := uuid.New()
	fileID := uuid.New()
	confirmedMatchID := uuid.New()
	repository := &catalogAPIFake{
		searchPage: catalog.SearchPage{
			Snapshots: []catalog.SearchSnapshot{{
				RootID: "audio", ScanRunID: uuid.New(),
				CompletedAt: time.Unix(20, 0).UTC(), DocumentCount: 1,
			}},
			Groups:     []catalog.SearchGroup{{ID: uuid.New()}},
			NextCursor: "next-page",
		},
		filters: catalog.FilterSnapshot{
			Authors: []string{"Frank Herbert"}, Genres: []string{"Science Fiction"},
			Years: []int{1965}, Roots: []string{"audio"},
			ReviewStates: []string{catalog.ReviewConfirmed},
		},
		details: map[uuid.UUID]catalog.BookDetail{
			bookID: {
				Book: catalog.Book{
					ID: bookID, RootID: "audio", SourceFolder: "Frank Herbert/Dune",
					Title: "Dune", Authors: []string{"Frank Herbert"},
					Identifiers:      map[string]any{"isbn": "9780441172719"},
					MetadataSource:   catalog.MetadataConfirmed,
					ConfirmedMatchID: &confirmedMatchID,
					ReviewState:      catalog.ReviewConfirmed,
				},
				Files: []catalog.SourceFile{{
					ID: fileID, RootID: "audio",
					RelativePath: `Frank Herbert/Dune/Dune.m4b`,
					Size:         42, ModifiedAt: time.Unix(10, 0).UTC(),
				}},
				Matches: []catalog.MetadataMatch{{
					ID: confirmedMatchID, BookID: bookID,
					Provider: catalog.MetadataOpenLibrary, Confirmed: true,
				}},
			},
		},
	}
	documentID := uuid.New()
	repository.searchPage.Groups[0] = catalog.SearchGroup{
		ID: documentID,
		Documents: []catalog.SearchDocument{{
			ID: documentID, RootID: "audio", EntityKind: catalog.SearchEntityAudiobook,
			BookID: bookID, RelativePath: "Frank Herbert/Dune", DisplayName: "Dune",
			Title: "Dune", Authors: []string{"Frank Herbert"}, PublicationYear: &year,
			Genres: []string{"Science Fiction"}, Series: "Dune",
			Narrators: []string{"Simon Vance"}, ReviewState: catalog.ReviewConfirmed,
			DuplicateCount: 1,
		}},
	}

	listRequest := newJSONRequest(t, "GET",
		"/v1/books?q=dune&author=Frank+Herbert&genre=Science+Fiction&year=1965"+
			"&rootId=audio&reviewState=confirmed&sort=year&limit=20&cursor=cursor-1",
		nil)
	listResponse := performHandler(booksHandler(repository, []config.Root{{ID: "audio"}}), listRequest)
	if listResponse.Code != 200 {
		t.Fatalf("list status = %d, body %s", listResponse.Code, listResponse.Body)
	}
	searchQuery := repository.searchQueries[0]
	if searchQuery.Text != "dune" || searchQuery.Author != "Frank Herbert" ||
		searchQuery.Genre != "Science Fiction" ||
		searchQuery.PublicationYear == nil || *searchQuery.PublicationYear != 1965 ||
		len(searchQuery.RootIDs) != 1 || searchQuery.RootIDs[0] != "audio" ||
		searchQuery.Sort != catalog.BookSortYear || searchQuery.ReviewState != catalog.ReviewConfirmed ||
		searchQuery.Limit != 20 || searchQuery.Cursor != "cursor-1" {
		t.Fatalf("query = %#v", searchQuery)
	}
	var page BooksResponse
	if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Books) != 1 ||
		page.Groups[0].Books[0].ID != bookID.String() ||
		page.Groups[0].Books[0].CoverURL != "/v1/books/"+bookID.String()+"/cover" ||
		page.NextCursor != "next-page" ||
		!slices.Equal(page.Filters.Authors, []string{"Frank Herbert"}) ||
		!slices.Equal(page.Filters.Roots, []string{"audio"}) {
		t.Fatalf("page = %#v", page)
	}

	detailRequest := newJSONRequest(t, "GET", "/v1/books/"+bookID.String(), nil)
	detailRequest.SetPathValue("bookId", bookID.String())
	detailResponse := performHandler(bookDetailHandler(repository), detailRequest)
	if detailResponse.Code != 200 ||
		strings.Contains(detailResponse.Body.String(), `H:\`) ||
		strings.Contains(detailResponse.Body.String(), "backupKey") ||
		strings.Contains(detailResponse.Body.String(), "providerKey") {
		t.Fatalf("detail = %d %s", detailResponse.Code, detailResponse.Body)
	}
	var detail BookDetailResponse
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != bookID.String() || detail.Attribution == nil ||
		detail.Attribution.Provider != catalog.MetadataOpenLibrary ||
		detail.ReviewState != catalog.ReviewConfirmed {
		t.Fatalf("detail = %#v", detail)
	}
	var androidShape struct {
		Attribution *struct {
			Provider    string  `json:"provider"`
			DisplayName *string `json:"displayName"`
			SourceURL   *string `json:"sourceUrl"`
		} `json:"attribution"`
		Identifiers map[string]string `json:"identifiers"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &androidShape); err != nil ||
		androidShape.Attribution == nil ||
		androidShape.Attribution.Provider != catalog.MetadataOpenLibrary ||
		androidShape.Identifiers["isbn"] != "9780441172719" {
		t.Fatalf("Android BookDetailDto compatibility = %#v, error %v", androidShape, err)
	}

	filesRequest := newJSONRequest(t, "GET", "/v1/books/"+bookID.String()+"/files", nil)
	filesRequest.SetPathValue("bookId", bookID.String())
	filesResponse := performHandler(bookFilesHandler(repository), filesRequest)
	if filesResponse.Code != 200 {
		t.Fatalf("files = %d %s", filesResponse.Code, filesResponse.Body)
	}
	var files BookFilesResponse
	if err := json.Unmarshal(filesResponse.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 1 || files.Files[0].ID != fileID.String() ||
		files.Files[0].RelativePath != "Frank Herbert/Dune/Dune.m4b" ||
		filepath.IsAbs(files.Files[0].RelativePath) {
		t.Fatalf("files = %#v", files)
	}
}

func TestBookCoverServesRecognizedLocalArtworkWithoutChangingSourceTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	bookFolder := filepath.Join(root, "Book")
	if err := os.MkdirAll(bookFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	cover := []byte("\xff\xd8\xff\xe0local-cover")
	audio := []byte("audio-is-read-only")
	if err := os.WriteFile(filepath.Join(bookFolder, "cover.jpg"), cover, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bookFolder, "book.m4b"), audio, 0o600); err != nil {
		t.Fatal(err)
	}
	before := hashSourceTree(t, root)
	resolver, err := library.NewResolver([]config.Root{{ID: "audio", ContainerPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	bookID := uuid.New()
	repository := &catalogAPIFake{details: map[uuid.UUID]catalog.BookDetail{
		bookID: {
			Book: catalog.Book{ID: bookID, RootID: "audio", SourceFolder: "Book"},
			Files: []catalog.SourceFile{
				{ID: uuid.New(), RootID: "audio", RelativePath: "Book/book.m4b",
					Size: int64(len(audio))},
			},
		},
	}}
	request := newJSONRequest(t, "GET", "/v1/books/"+bookID.String()+"/cover", nil)
	request.SetPathValue("bookId", bookID.String())
	response := performHandler(bookCoverHandler(repository, resolver), request)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), cover) ||
		response.Header().Get("ETag") == "" ||
		!strings.Contains(response.Header().Get("Cache-Control"), "private") {
		t.Fatalf("cover = %d %#v %q", response.Code, response.Header(), response.Body.Bytes())
	}
	if after := hashSourceTree(t, root); !equalHashSourceTrees(before, after) {
		t.Fatalf("source tree changed:\nbefore %#v\nafter  %#v", before, after)
	}

	firstETag := response.Header().Get("ETag")
	replacement := []byte("\xff\xd8\xff\xe0replacement-cover")
	coverPath := filepath.Join(bookFolder, "cover.jpg")
	if err := os.WriteFile(coverPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(coverPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	afterPublish := hashSourceTree(t, root)
	replacementRequest := newJSONRequest(t, "GET", "/v1/books/"+bookID.String()+"/cover", nil)
	replacementRequest.SetPathValue("bookId", bookID.String())
	replacementResponse := performHandler(
		bookCoverHandler(repository, resolver),
		replacementRequest,
	)
	if replacementResponse.Code != 200 ||
		!bytes.Equal(replacementResponse.Body.Bytes(), replacement) ||
		replacementResponse.Header().Get("ETag") == firstETag {
		t.Fatalf("replacement cover = %d %#v %q",
			replacementResponse.Code, replacementResponse.Header(),
			replacementResponse.Body.Bytes())
	}
	if after := hashSourceTree(t, root); !equalHashSourceTrees(afterPublish, after) {
		t.Fatalf("replacement read changed source tree:\nbefore %#v\nafter  %#v",
			afterPublish, after)
	}
}

func TestBookHandlersRejectInvalidUUIDsWithStableErrors(t *testing.T) {
	t.Parallel()

	request := newJSONRequest(t, "GET", "/v1/books/not-a-uuid", nil)
	request.SetPathValue("bookId", "not-a-uuid")
	response := performHandler(bookDetailHandler(&catalogAPIFake{}), request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), `"code":"invalid_book_id"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

type catalogAPIFake struct {
	page            catalog.Page[catalog.Book]
	filters         catalog.FilterSnapshot
	query           catalog.BookQuery
	details         map[uuid.UUID]catalog.BookDetail
	savedMatches    []catalog.MetadataMatch
	confirmedBook   uuid.UUID
	confirmedMatch  uuid.UUID
	grouping        catalog.GroupingCorrection
	groupingContext catalog.GroupingContext
	jobs            []catalog.WorkerJob
	jobIDs          map[uuid.UUID]struct{}
	dedupeJobs      map[string]catalog.WorkerJob
	err             error
	searchPage      catalog.SearchPage
	searchQueries   []catalog.SearchQuery
	enqueueErr      error
	lastEnqueueID   uuid.UUID
	confirmCalls    int
}

func (repository *catalogAPIFake) SearchLibrary(
	_ context.Context,
	query catalog.SearchQuery,
) (catalog.SearchPage, error) {
	repository.searchQueries = append(repository.searchQueries, query)
	return repository.searchPage, repository.err
}

func (repository *catalogAPIFake) ListCatalogFilters(
	context.Context,
) (catalog.FilterSnapshot, error) {
	return repository.filters, repository.err
}

func (repository *catalogAPIFake) ListBooks(
	_ context.Context,
	query catalog.BookQuery,
) (catalog.Page[catalog.Book], error) {
	repository.query = query
	return repository.page, repository.err
}

func (repository *catalogAPIFake) GetBook(
	_ context.Context,
	id uuid.UUID,
) (catalog.BookDetail, error) {
	if repository.err != nil {
		return catalog.BookDetail{}, repository.err
	}
	detail, ok := repository.details[id]
	if !ok {
		return catalog.BookDetail{}, catalog.ErrNotFound
	}
	return detail, nil
}

func (repository *catalogAPIFake) GetGroupingContext(
	_ context.Context,
	bookID uuid.UUID,
) (catalog.GroupingContext, error) {
	if repository.err != nil {
		return catalog.GroupingContext{}, repository.err
	}
	if repository.groupingContext.BookID != uuid.Nil {
		return repository.groupingContext, nil
	}
	detail, exists := repository.details[bookID]
	if !exists {
		return catalog.GroupingContext{}, catalog.ErrNotFound
	}
	result := catalog.GroupingContext{
		BookID: bookID, RootID: detail.Book.RootID,
		SourceFolder: detail.Book.SourceFolder,
		Files:        []catalog.SourceFile{},
		Books:        []catalog.GroupingContextBook{},
	}
	seenFiles := make(map[uuid.UUID]struct{})
	for id, candidate := range repository.details {
		if candidate.Book.RootID != result.RootID ||
			candidate.Book.SourceFolder != result.SourceFolder ||
			candidate.Book.MissingAt != nil {
			continue
		}
		sibling := catalog.GroupingContextBook{
			BookID: id, Title: candidate.Book.Title,
			GroupingKey: candidate.Book.GroupingKey, FileIDs: []uuid.UUID{},
		}
		for _, file := range candidate.Files {
			if file.MissingAt != nil || file.RootID != result.RootID ||
				!inSourceFolder(file.RelativePath, result.SourceFolder) ||
				!isAudioPath(file.RelativePath) {
				continue
			}
			sibling.FileIDs = append(sibling.FileIDs, file.ID)
			if _, duplicate := seenFiles[file.ID]; !duplicate {
				seenFiles[file.ID] = struct{}{}
				result.Files = append(result.Files, file)
			}
		}
		result.Books = append(result.Books, sibling)
	}
	return result, nil
}

func (repository *catalogAPIFake) SaveMatches(
	_ context.Context,
	_ uuid.UUID,
	matches []catalog.MetadataMatch,
) error {
	repository.savedMatches = append([]catalog.MetadataMatch(nil), matches...)
	return repository.err
}

func (repository *catalogAPIFake) ConfirmMatch(
	_ context.Context,
	bookID uuid.UUID,
	matchID uuid.UUID,
) error {
	repository.confirmedBook = bookID
	repository.confirmedMatch = matchID
	repository.confirmCalls++
	return repository.err
}

func (repository *catalogAPIFake) SaveGrouping(
	_ context.Context,
	grouping catalog.GroupingCorrection,
) error {
	repository.grouping = grouping
	return repository.err
}

func (repository *catalogAPIFake) Enqueue(
	_ context.Context,
	job catalog.WorkerJob,
) error {
	repository.lastEnqueueID = job.ID
	if repository.enqueueErr != nil {
		return repository.enqueueErr
	}
	if repository.jobIDs == nil {
		repository.jobIDs = make(map[uuid.UUID]struct{})
	}
	if _, exists := repository.jobIDs[job.ID]; exists {
		return catalog.ErrConflict
	}
	repository.jobIDs[job.ID] = struct{}{}
	repository.jobs = append(repository.jobs, job)
	return repository.err
}

func (repository *catalogAPIFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	key string,
) (catalog.WorkerJob, error) {
	repository.lastEnqueueID = job.ID
	if repository.enqueueErr != nil {
		return catalog.WorkerJob{}, repository.enqueueErr
	}
	if repository.dedupeJobs == nil {
		repository.dedupeJobs = make(map[string]catalog.WorkerJob)
	}
	if active, exists := repository.dedupeJobs[key]; exists {
		return active, nil
	}
	job.Status = catalog.JobQueued
	repository.dedupeJobs[key] = job
	repository.jobs = append(repository.jobs, job)
	return job, repository.err
}

func hashSourceTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func equalHashSourceTrees(left, right map[string][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for path, digest := range left {
		if right[path] != digest {
			return false
		}
	}
	return true
}

func newJSONRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func performHandler(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var _ CatalogRepository = (*catalogAPIFake)(nil)
