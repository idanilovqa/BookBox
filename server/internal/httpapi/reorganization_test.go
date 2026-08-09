package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"github.com/google/uuid"
)

func TestReorganizationPreviewRequiresFlagCapabilityAndWritableRoot(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	for _, test := range []struct {
		name       string
		enabled    bool
		capability bool
		writeError error
	}{
		{name: "flag disabled", capability: true},
		{name: "capability absent", enabled: true},
		{name: "root not writable", enabled: true, capability: true, writeError: errors.New("C:\\private\\root denied")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &reorganizationAPIFake{}
			access := ReorganizationAccess{
				Enabled: test.enabled,
				HasCapability: func(actor, capability string) bool {
					return actor == "owner" && capability == "library:reorganize" && test.capability
				},
				ProbeBookRootWritable: func(context.Context, uuid.UUID) error { return test.writeError },
			}
			request := newJSONRequest(t, http.MethodPost,
				"/v1/books/"+bookID.String()+"/reorganization/preview", map[string]any{
					"catalogGeneration": 8,
					"groups":            []map[string]any{{"groupingKey": "book", "fileIds": []string{uuid.NewString()}}},
					"targetFolders":     map[string]string{"book": "Author/Book"},
				})
			request.SetPathValue("bookId", bookID.String())
			request = request.WithContext(context.WithValue(request.Context(), accessSubjectContextKey{}, "owner"))
			response := performHandler(reorganizationPreviewHandler(repository, access), request)

			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"library_reorganization_disabled"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body)
			}
			if repository.previewCalls != 0 {
				t.Fatal("unauthorized preview reached catalog repository")
			}
			if strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "denied") {
				t.Fatalf("response leaked root error: %s", response.Body)
			}
		})
	}
}

func TestReorganizationPreviewReturnsOnlyRelativeActorBoundMoves(t *testing.T) {
	t.Parallel()

	bookID, operationID := uuid.New(), uuid.New()
	repository := &reorganizationAPIFake{preview: catalog.ReorganizationPreview{
		OperationID: operationID, RootID: "audio", CatalogGeneration: 8,
		Moves: []catalog.ReorganizationMove{{Order: 1, SourcePath: "Incoming/book.m4b", DestinationPath: "Author/Book/book.m4b", Role: "audio"}},
	}}
	request := newJSONRequest(t, http.MethodPost,
		"/v1/books/"+bookID.String()+"/reorganization/preview", map[string]any{
			"catalogGeneration": 8,
			"groups":            []map[string]any{{"groupingKey": "book", "fileIds": []string{uuid.NewString()}}},
			"targetFolders":     map[string]string{"book": "Author/Book"},
		})
	request.SetPathValue("bookId", bookID.String())
	request = request.WithContext(context.WithValue(request.Context(), accessSubjectContextKey{}, "owner"))
	response := performHandler(reorganizationPreviewHandler(repository, permittedReorganizationAccess()), request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	if repository.actor != "owner" || repository.bookID != bookID {
		t.Fatalf("preview actor/book = %q/%s", repository.actor, repository.bookID)
	}
	if strings.Contains(response.Body.String(), `C:\`) || strings.Contains(response.Body.String(), "/library/") {
		t.Fatalf("preview exposed an absolute path: %s", response.Body)
	}
}

func TestReorganizationConfirmIsActorBoundAndMapsConflicts(t *testing.T) {
	t.Parallel()

	bookID, operationID := uuid.New(), uuid.New()
	for _, test := range []struct {
		name, actor, code string
		err               error
		status            int
	}{
		{name: "different actor", actor: "intruder", err: catalog.ErrReorganizationStale, status: http.StatusConflict, code: "reorganization_stale"},
		{name: "stale generation or fingerprint", actor: "owner", err: catalog.ErrReorganizationStale, status: http.StatusConflict, code: "reorganization_stale"},
		{name: "destination appeared", actor: "owner", err: catalog.ErrDestinationConflict, status: http.StatusConflict, code: "destination_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &reorganizationAPIFake{queueError: test.err}
			request := newJSONRequest(t, http.MethodPost,
				"/v1/books/"+bookID.String()+"/reorganization/confirm",
				map[string]string{"operationId": operationID.String()})
			request.SetPathValue("bookId", bookID.String())
			request = request.WithContext(context.WithValue(request.Context(), accessSubjectContextKey{}, test.actor))
			response := performHandler(reorganizationConfirmHandler(repository, permittedReorganizationAccess()), request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "operation") && strings.Contains(response.Body.String(), operationID.String()) {
				t.Fatalf("response leaked operation detail: %s", response.Body)
			}
		})
	}
}

func TestReorganizationConfirmQueuesTheSameActorsPreview(t *testing.T) {
	t.Parallel()

	bookID, operationID := uuid.New(), uuid.New()
	repository := &reorganizationAPIFake{}
	request := newJSONRequest(t, http.MethodPost,
		"/v1/books/"+bookID.String()+"/reorganization/confirm",
		map[string]string{"operationId": operationID.String()})
	request.SetPathValue("bookId", bookID.String())
	request = request.WithContext(context.WithValue(request.Context(), accessSubjectContextKey{}, "owner"))
	response := performHandler(reorganizationConfirmHandler(repository, permittedReorganizationAccess()), request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	if repository.queuedOperation != operationID || repository.queuedActor != "owner" {
		t.Fatalf("queued operation/actor = %s/%q", repository.queuedOperation, repository.queuedActor)
	}
}

func TestGroupingContextPublishesGenerationStableKeysAndDerivedCapability(t *testing.T) {
	t.Parallel()

	bookID, fileID := uuid.New(), uuid.New()
	repository := &catalogAPIFake{groupingContext: catalog.GroupingContext{
		BookID: bookID, RootID: "audio", ScopePath: "Author/Book", CatalogGeneration: 9,
		Files: []catalog.GroupingContextFile{{
			SourceFile: catalog.SourceFile{ID: fileID, RootID: "audio", RelativePath: "Author/Book/book.m4b", FileKind: catalog.FileKindAudio, Size: 4, ModifiedAt: time.Unix(10, 0).UTC()},
			Role:       catalog.FileRoleAudio,
		}},
		Books: []catalog.GroupingContextBook{{BookID: bookID, Title: "Book", GroupingKey: "stable-book", FileIDs: []uuid.UUID{fileID}}},
	}}
	request := newJSONRequest(t, http.MethodGet, "/v1/books/"+bookID.String()+"/grouping", nil)
	request.SetPathValue("bookId", bookID.String())
	request = request.WithContext(context.WithValue(request.Context(), accessSubjectContextKey{}, "owner"))
	response := performHandler(groupingContextHandler(repository, permittedReorganizationAccess()), request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"catalogGeneration":9`) ||
		!strings.Contains(response.Body.String(), `"groupingKey":"stable-book"`) ||
		!strings.Contains(response.Body.String(), `"canReorganize":true`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

type reorganizationAPIFake struct {
	preview         catalog.ReorganizationPreview
	previewCalls    int
	actor           string
	bookID          uuid.UUID
	queueError      error
	queuedOperation uuid.UUID
	queuedActor     string
}

func (repository *reorganizationAPIFake) BuildReorganizationPreview(_ context.Context, request catalog.ReorganizationRequest) (catalog.ReorganizationPreview, error) {
	repository.previewCalls++
	repository.actor, repository.bookID = request.Actor, request.BookID
	return repository.preview, nil
}

func (repository *reorganizationAPIFake) QueueReorganization(_ context.Context, operationID uuid.UUID, actor string) (catalog.WorkerJob, error) {
	repository.queuedOperation, repository.queuedActor = operationID, actor
	return catalog.WorkerJob{ID: uuid.New(), Kind: catalog.JobReorganizeFiles}, repository.queueError
}

func permittedReorganizationAccess() ReorganizationAccess {
	return ReorganizationAccess{
		Enabled:               true,
		HasCapability:         func(_, capability string) bool { return capability == "library:reorganize" },
		ProbeBookRootWritable: func(context.Context, uuid.UUID) error { return nil },
	}
}
