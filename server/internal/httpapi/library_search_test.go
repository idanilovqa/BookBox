package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"github.com/google/uuid"
)

func TestBooksSearchUsesAllConfiguredRootsAndSerializesGroups(t *testing.T) {
	t.Parallel()
	completed := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	firstBook, secondBook := uuid.New(), uuid.New()
	firstDocument := uuid.New()
	groupID := uuid.New()
	repository := &catalogAPIFake{searchPage: catalog.SearchPage{
		Snapshots: []catalog.SearchSnapshot{
			{RootID: "alpha", ScanRunID: uuid.New(), CompletedAt: completed, DocumentCount: 1},
			{RootID: "beta", ScanRunID: uuid.New(), CompletedAt: completed.Add(time.Minute), DocumentCount: 2},
		},
		Groups: []catalog.SearchGroup{
			{ID: firstDocument, Documents: []catalog.SearchDocument{{
				ID: firstDocument, RootID: "alpha", EntityKind: catalog.SearchEntityAudiobook,
				BookID: firstBook, RelativePath: "Dune", DisplayName: "Dune", Title: "Dune",
				ReviewState: catalog.ReviewNeedsReview, DuplicateCount: 1,
			}}},
			{ID: groupID, Duplicate: true, Documents: []catalog.SearchDocument{
				{ID: uuid.New(), RootID: "beta", EntityKind: catalog.SearchEntityAudiobook,
					BookID: secondBook, RelativePath: "Dune/Copy One", DisplayName: "Dune copy one",
					Title: "Dune", ReviewState: catalog.ReviewConfirmed,
					DuplicateGroupID: groupID, DuplicateCount: 2},
				{ID: uuid.New(), RootID: "beta", EntityKind: catalog.SearchEntityAudiobook,
					BookID: uuid.New(), RelativePath: "Dune/Copy Two", DisplayName: "Dune copy two",
					Title: "Dune", ReviewState: catalog.ReviewConfirmed,
					DuplicateGroupID: groupID, DuplicateCount: 2},
			}},
		},
		NextCursor: "opaque-next",
	}}

	request := newJSONRequest(t, http.MethodGet, "/v1/books?q=dune&limit=3", nil)
	response := performHandler(booksHandler(repository, []config.Root{{ID: "beta"}, {ID: "alpha"}}), request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	if len(repository.searchQueries) != 1 {
		t.Fatalf("search calls = %d", len(repository.searchQueries))
	}
	query := repository.searchQueries[0]
	if query.Mode != catalog.SearchModeAudiobooks || query.Text != "dune" || query.Limit != 3 ||
		len(query.RootIDs) != 2 || query.RootIDs[0] != "beta" || query.RootIDs[1] != "alpha" {
		t.Fatalf("query = %#v", query)
	}
	var page BooksResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Groups) != 2 || page.Groups[0].Duplicate ||
		!page.Groups[1].Duplicate || len(page.Groups[1].Books) != 2 ||
		len(page.Snapshots) != 2 || page.NextCursor != "opaque-next" {
		t.Fatalf("page = %#v", page)
	}
}

func TestBooksSearchAcceptsRepeatedRootsAndMapsIndexBuilding(t *testing.T) {
	t.Parallel()
	repository := &catalogAPIFake{err: catalog.ErrSearchIndexBuilding}
	request := newJSONRequest(t, http.MethodGet,
		"/v1/books?rootId=beta&rootId=alpha&q=dune", nil)
	response := performHandler(booksHandler(repository,
		[]config.Root{{ID: "alpha"}, {ID: "beta"}}), request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"search_index_building"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	if len(repository.searchQueries) != 1 ||
		len(repository.searchQueries[0].RootIDs) != 2 ||
		repository.searchQueries[0].RootIDs[0] != "beta" ||
		repository.searchQueries[0].RootIDs[1] != "alpha" {
		t.Fatalf("queries = %#v", repository.searchQueries)
	}
}

func TestSearchSnapshotsMustBeRootSorted(t *testing.T) {
	t.Parallel()
	completed := time.Now().UTC()
	values := []catalog.SearchSnapshot{
		{RootID: "beta", ScanRunID: uuid.New(), CompletedAt: completed},
		{RootID: "alpha", ScanRunID: uuid.New(), CompletedAt: completed},
	}
	if _, ok := shapeSnapshots(values, map[string]struct{}{"alpha": {}, "beta": {}}); ok {
		t.Fatal("shapeSnapshots accepted unsorted snapshots")
	}
}

func TestRawSearchUsesCatalogAndSerializesFilesAndFolders(t *testing.T) {
	t.Parallel()
	completed := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	fileSize := int64(42)
	modified := completed.Add(-time.Hour)
	bookID, duplicateID := uuid.New(), uuid.New()
	folderID, fileID := uuid.New(), uuid.New()
	repository := &catalogAPIFake{searchPage: catalog.SearchPage{
		Snapshots: []catalog.SearchSnapshot{{
			RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: completed, DocumentCount: 2,
		}},
		Groups: []catalog.SearchGroup{
			{ID: folderID, Documents: []catalog.SearchDocument{{
				ID: folderID, RootID: "audiobooks", EntityKind: catalog.SearchEntityFolder,
				BookID: bookID, RelativePath: "Frank Herbert/Dune", DisplayName: "Dune", DuplicateCount: 2,
			}}},
			{ID: fileID, Documents: []catalog.SearchDocument{{
				ID: fileID, RootID: "audiobooks", EntityKind: catalog.SearchEntityFile,
				BookID: bookID, RelativePath: "Frank Herbert/Dune/Dune.m4b", DisplayName: "Dune.m4b",
				Size: &fileSize, ModifiedAt: &modified, DuplicateGroupID: duplicateID, DuplicateCount: 2,
			}}},
		},
		NextCursor: "raw-next",
	}}
	response := performHandler(folderSearchHandler(repository, []config.Root{{ID: "audiobooks"}}),
		newJSONRequest(t, http.MethodGet,
			"/v1/browse/search?root=audiobooks&q=dune&limit=20&cursor=raw-cursor", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	query := repository.searchQueries[0]
	if query.Mode != catalog.SearchModeRaw || len(query.RootIDs) != 1 ||
		query.RootIDs[0] != "audiobooks" || query.Text != "dune" ||
		query.Limit != 20 || query.Cursor != "raw-cursor" {
		t.Fatalf("query = %#v", query)
	}
	var page RawSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.RootID != "audiobooks" || page.Query != "dune" || page.Snapshot.RootID != "audiobooks" ||
		len(page.Entries) != 2 || page.Entries[0].Kind != "folder" || page.Entries[0].Size != nil ||
		!page.Entries[0].DuplicateBook || page.Entries[0].DuplicateGroupID != "" || page.Entries[0].BookID != "" ||
		page.Entries[1].Kind != "file" || page.Entries[1].Size == nil || *page.Entries[1].Size != 42 ||
		!page.Entries[1].DuplicateBook || page.Entries[1].BookID != bookID.String() ||
		page.NextCursor != "raw-next" {
		t.Fatalf("page = %#v", page)
	}
}

func TestBooksSearchRejectsDuplicateGroupsSpanningRoots(t *testing.T) {
	t.Parallel()
	groupID := uuid.New()
	repository := &catalogAPIFake{searchPage: catalog.SearchPage{
		Snapshots: []catalog.SearchSnapshot{
			{RootID: "alpha", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC()},
			{RootID: "beta", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC()},
		},
		Groups: []catalog.SearchGroup{{ID: groupID, Duplicate: true, Documents: []catalog.SearchDocument{
			{ID: uuid.New(), RootID: "alpha", EntityKind: catalog.SearchEntityAudiobook, BookID: uuid.New(), RelativePath: "One", Title: "Book", DuplicateGroupID: groupID, DuplicateCount: 2},
			{ID: uuid.New(), RootID: "beta", EntityKind: catalog.SearchEntityAudiobook, BookID: uuid.New(), RelativePath: "Two", Title: "Book", DuplicateGroupID: groupID, DuplicateCount: 2},
		}}},
	}}
	response := performHandler(booksHandler(repository, []config.Root{{ID: "alpha"}, {ID: "beta"}}),
		newJSONRequest(t, http.MethodGet, "/v1/books?q=book", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func TestSearchHandlersRejectUnsafeProjectionDocuments(t *testing.T) {
	t.Parallel()
	repository := &catalogAPIFake{searchPage: catalog.SearchPage{
		Snapshots: []catalog.SearchSnapshot{{
			RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(), DocumentCount: 1,
		}},
		Groups: []catalog.SearchGroup{{ID: uuid.New(), Documents: []catalog.SearchDocument{{
			ID: uuid.New(), RootID: "audiobooks", EntityKind: catalog.SearchEntityFile,
			RelativePath: `C:\secret\book.m4b`, DisplayName: "book.m4b", DuplicateCount: 1,
		}}}},
	}}
	response := performHandler(folderSearchHandler(repository, []config.Root{{ID: "audiobooks"}}),
		newJSONRequest(t, http.MethodGet, "/v1/browse/search?root=audiobooks&q=book", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}
