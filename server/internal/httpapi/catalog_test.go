package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/metadata"
	"github.com/google/uuid"
)

func TestCatalogReviewRoutesOnlyChangeDatabaseAndQueueState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "book.m4b"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := hashSourceTree(t, root)

	bookID := uuid.New()
	fileID := uuid.New()
	matchID := uuid.New()
	modified := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repository := &catalogAPIFake{details: map[uuid.UUID]catalog.BookDetail{
		bookID: {
			Book: catalog.Book{
				ID: bookID, RootID: "audio", SourceFolder: "Book",
				Title: "Local Dune", Authors: []string{"Local Author"},
				GroupingKey: "folder-key", ReviewState: catalog.ReviewNeedsReview,
			},
			Files: []catalog.SourceFile{{
				ID: fileID, RootID: "audio", RelativePath: "Book/book.m4b",
				Size: 5, ModifiedAt: modified,
			}},
		},
	}}
	matcher := &matcherFake{decision: metadata.Decision{
		Candidates: []metadata.ScoredCandidate{{
			Candidate: metadata.Candidate{
				Provider: metadata.ProviderOpenLibrary, ProviderID: "OL123M",
				Title: "Dune", Authors: []string{"Frank Herbert"},
				PublishedDate: "1965", CoverURL: "https://covers.example/dune.jpg",
			},
			Score: 0.94,
		}},
	}}

	search := newJSONRequest(t, http.MethodPost, "/v1/catalog/matches/search", map[string]any{
		"bookId": bookID.String(), "title": "Dune", "author": "Frank Herbert",
	})
	searchResponse := performHandler(matchSearchHandler(repository, matcher), search)
	if searchResponse.Code != http.StatusOK || len(repository.savedMatches) != 1 ||
		matcher.local.Title != "Dune" ||
		len(matcher.local.Authors) != 1 || matcher.local.Authors[0] != "Frank Herbert" {
		t.Fatalf("search = %d %s, matches %#v, local %#v",
			searchResponse.Code, searchResponse.Body, repository.savedMatches, matcher.local)
	}
	if strings.Contains(searchResponse.Body.String(), "providerKey") ||
		strings.Contains(searchResponse.Body.String(), "backupKey") ||
		strings.Contains(searchResponse.Body.String(), root) {
		t.Fatalf("search leaked private data: %s", searchResponse.Body)
	}

	confirm := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/match",
		map[string]string{"matchId": matchID.String()})
	confirm.SetPathValue("bookId", bookID.String())
	confirmResponse := performHandler(confirmMatchHandler(repository), confirm)
	if confirmResponse.Code != http.StatusAccepted ||
		repository.confirmedBook != bookID || repository.confirmedMatch != matchID ||
		len(repository.jobs) != 1 || repository.jobs[0].Kind != catalog.JobPublishCover ||
		repository.jobs[0].Payload["bookId"] != bookID.String() ||
		repository.jobs[0].Payload["matchId"] != matchID.String() {
		t.Fatalf("confirm = %d %s, jobs %#v",
			confirmResponse.Code, confirmResponse.Body, repository.jobs)
	}

	grouping := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/grouping",
		map[string]any{"expectedFiles": []map[string]any{{
			"fileId": fileID.String(), "size": 5, "modifiedAt": modified,
		}}, "groups": []map[string]any{{
			"bookId": bookID.String(), "fileIds": []string{fileID.String()},
		}}})
	grouping.SetPathValue("bookId", bookID.String())
	groupingResponse := performHandler(groupingHandler(repository), grouping)
	var groupingResult struct {
		CatalogGeneration int64 `json:"catalogGeneration"`
	}
	if err := json.Unmarshal(groupingResponse.Body.Bytes(), &groupingResult); err != nil {
		t.Fatal(err)
	}
	if groupingResponse.Code != http.StatusOK || groupingResult.CatalogGeneration != 2 ||
		repository.grouping.RootID != "audio" ||
		repository.grouping.SourceFolder != "Book" ||
		len(repository.grouping.Groups) != 1 ||
		!slices.Equal(repository.grouping.Groups[0].FileIDs, []uuid.UUID{fileID}) ||
		len(repository.grouping.ExpectedFiles) != 1 ||
		repository.grouping.ExpectedFiles[0].FileID != fileID {
		t.Fatalf("grouping = %d %s, saved %#v",
			groupingResponse.Code, groupingResponse.Body, repository.grouping)
	}

	if after := hashSourceTree(t, root); !equalHashSourceTrees(before, after) {
		t.Fatalf("catalog routes changed source tree:\nbefore %#v\nafter  %#v", before, after)
	}
}

func TestCatalogRescanUsesStableDeduplicationKey(t *testing.T) {
	t.Parallel()

	repository := &catalogAPIFake{}
	now := func() time.Time { return time.Date(2026, 7, 26, 12, 34, 45, 0, time.UTC) }
	handler := rescanHandler(repository, []config.Root{{ID: "audio"}}, now)
	requestBody := map[string]string{"rootId": "audio"}
	for range 2 {
		response := performHandler(handler,
			newJSONRequest(t, http.MethodPost, "/v1/catalog/rescan", requestBody))
		if response.Code != http.StatusAccepted {
			t.Fatalf("rescan = %d %s", response.Code, response.Body)
		}
	}
	if len(repository.jobs) != 1 || repository.jobs[0].Kind != catalog.JobScanRoot ||
		repository.jobs[0].Payload["rootId"] != "audio" {
		t.Fatalf("jobs = %#v", repository.jobs)
	}
}

func TestManualMatchSearchCanReplaceConfirmedMetadata(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	repository := &catalogAPIFake{details: map[uuid.UUID]catalog.BookDetail{
		bookID: {
			Book: catalog.Book{
				ID: bookID, RootID: "audio", SourceFolder: "Book",
				GroupingKey: "folder-key", Title: "Existing title",
				MetadataSource: catalog.MetadataConfirmed,
				ReviewState:    catalog.ReviewConfirmed,
			},
		},
	}}
	provider := &manualSearchProvider{candidate: metadata.Candidate{
		Provider: metadata.ProviderOpenLibrary, ProviderID: "OL-new",
		Title: "Replacement title",
	}}
	request := newJSONRequest(t, http.MethodPost, "/v1/catalog/matches/search",
		map[string]string{"bookId": bookID.String()})
	response := performHandler(
		matchSearchHandler(repository, metadata.NewMatcher(provider)),
		request,
	)

	if response.Code != http.StatusOK || provider.calls != 1 ||
		len(repository.savedMatches) != 1 ||
		repository.savedMatches[0].ProviderID != "OL-new" {
		t.Fatalf("response = %d %s, calls = %d, matches = %#v",
			response.Code, response.Body, provider.calls, repository.savedMatches)
	}
}

func TestCatalogHandlersApplyBodyUUIDAndGroupingValidation(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	fileID := uuid.New()
	modifiedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repository := &catalogAPIFake{details: map[uuid.UUID]catalog.BookDetail{
		bookID: {
			Book: catalog.Book{
				ID: bookID, RootID: "audio", SourceFolder: "Book", GroupingKey: "folder-key",
			},
			Files: []catalog.SourceFile{{
				ID: fileID, RootID: "audio", RelativePath: "Book/book.m4b",
				Size: 1, ModifiedAt: modifiedAt,
			}},
		},
	}}

	invalidMatch := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/match",
		map[string]string{"matchId": "not-a-uuid"})
	invalidMatch.SetPathValue("bookId", bookID.String())
	if response := performHandler(confirmMatchHandler(repository), invalidMatch); response.Code != 400 || !strings.Contains(response.Body.String(), `"code":"invalid_match_id"`) {
		t.Fatalf("invalid match = %d %s", response.Code, response.Body)
	}

	oversized := newJSONRequest(t, http.MethodPost, "/v1/catalog/matches/search", nil)
	oversized.Header.Set("Content-Type", "application/json")
	oversized.Body = ioNopCloser{Reader: strings.NewReader(
		`{"bookId":"` + bookID.String() + `","title":"` +
			strings.Repeat("x", maxCatalogBodyBytes) + `"}`,
	)}
	if response := performHandler(matchSearchHandler(repository, &matcherFake{}), oversized); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d %s", response.Code, response.Body)
	}
	for name, body := range map[string]string{
		"unknown field": `{"bookId":"` + bookID.String() + `","unknown":true}`,
		"trailing JSON": `{"bookId":"` + bookID.String() + `"} {}`,
	} {
		request := newJSONRequest(t, http.MethodPost, "/v1/catalog/matches/search", nil)
		request.Header.Set("Content-Type", "application/json")
		request.Body = ioNopCloser{Reader: strings.NewReader(body)}
		if response := performHandler(matchSearchHandler(repository, &matcherFake{}), request); response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", name, response.Code, response.Body)
		}
	}

	repository.err = catalog.ErrConflict
	grouping := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/grouping",
		map[string]any{
			"expectedFiles": []map[string]any{{
				"fileId": fileID.String(), "size": int64(1), "modifiedAt": modifiedAt,
			}},
			"groups": []map[string]any{{
				"bookId": bookID.String(), "fileIds": []string{fileID.String()},
			}},
		})
	grouping.SetPathValue("bookId", bookID.String())
	if response := performHandler(groupingHandler(repository), grouping); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"catalog_conflict"`) {
		t.Fatalf("grouping conflict = %d %s", response.Code, response.Body)
	}
}

func TestGroupingRejectsCrossFolderDuplicateAndIncompleteAssignments(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	firstFile := uuid.New()
	secondFile := uuid.New()
	otherBookID := uuid.New()
	otherFile := uuid.New()
	modified := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	details := map[uuid.UUID]catalog.BookDetail{
		bookID: {
			Book: catalog.Book{
				ID: bookID, RootID: "audio", SourceFolder: "Book", GroupingKey: "folder-key",
			},
			Files: []catalog.SourceFile{
				{ID: firstFile, RootID: "audio", RelativePath: "Book/one.m4b", Size: 1, ModifiedAt: modified},
				{ID: secondFile, RootID: "audio", RelativePath: "Book/two.m4b", Size: 2, ModifiedAt: modified},
			},
		},
		otherBookID: {
			Book: catalog.Book{
				ID: otherBookID, RootID: "other", SourceFolder: "Elsewhere",
				GroupingKey: "other-key",
			},
			Files: []catalog.SourceFile{{
				ID: otherFile, RootID: "other", RelativePath: "Elsewhere/book.m4b",
			}},
		},
	}
	tests := []struct {
		name   string
		groups []map[string]any
		status int
	}{
		{
			name: "duplicate",
			groups: []map[string]any{{
				"bookId":  bookID.String(),
				"fileIds": []string{firstFile.String(), firstFile.String()},
			}},
			status: http.StatusBadRequest,
		},
		{
			name: "incomplete",
			groups: []map[string]any{{
				"bookId": bookID.String(), "fileIds": []string{firstFile.String()},
			}},
			status: http.StatusConflict,
		},
		{
			name: "cross root folder",
			groups: []map[string]any{
				{"bookId": bookID.String(), "fileIds": []string{firstFile.String(), secondFile.String()}},
				{"bookId": otherBookID.String(), "fileIds": []string{otherFile.String()}},
			},
			status: http.StatusConflict,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := &catalogAPIFake{details: details}
			request := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/grouping",
				map[string]any{
					"expectedFiles": []map[string]any{
						{"fileId": firstFile.String(), "size": 1, "modifiedAt": modified},
						{"fileId": secondFile.String(), "size": 2, "modifiedAt": modified},
					},
					"groups": test.groups,
				})
			request.SetPathValue("bookId", bookID.String())
			response := performHandler(groupingHandler(repository), request)
			if response.Code != test.status {
				t.Fatalf("response = %d %s", response.Code, response.Body)
			}
			if repository.grouping.ID != uuid.Nil {
				t.Fatal("invalid grouping reached repository mutation")
			}
		})
	}
}

func TestGroupingContextReturnsSubtreeEvidenceRolesCompanionsAndCapability(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	siblingID := uuid.New()
	firstFileID := uuid.New()
	looseFileID := uuid.New()
	companionID := uuid.New()
	coverID := uuid.New()
	modified := time.Date(2026, 7, 26, 12, 34, 56, 0, time.UTC)
	repository := &catalogAPIFake{groupingContext: catalog.GroupingContext{
		BookID: bookID, RootID: "audio", ScopePath: "Omnibus",
		ReviewReason: "probe_failed", CanReorganize: true,
		Evidence: map[string][]string{
			catalog.FileRoleAudio:   {"Omnibus/Disc 1/First.m4b"},
			catalog.FileRoleSidecar: {"Omnibus/book.cue"},
		},
		Files: []catalog.GroupingContextFile{
			{
				SourceFile: catalog.SourceFile{ID: firstFileID, RootID: "audio", RelativePath: "Omnibus/Disc 1/First.m4b",
					Size: 101, ModifiedAt: modified, SHA256: strings.Repeat("a", 64),
					FileKind: catalog.FileKindAudio, Media: map[string]any{"absolutePath": "H:/Audiobooks/Omnibus/First.m4b"}},
				Role: catalog.FileRoleAudio,
			},
			{
				SourceFile: catalog.SourceFile{ID: looseFileID, RootID: "audio", RelativePath: "Omnibus/Disc 2/Loose.flac",
					Size: 202, ModifiedAt: modified, FileKind: catalog.FileKindAudio}, Role: catalog.FileRoleAudio,
			},
			{SourceFile: catalog.SourceFile{ID: companionID, RootID: "audio", RelativePath: "Omnibus/book.cue", Size: 12, ModifiedAt: modified, FileKind: catalog.FileKindCompanion, CompanionKind: catalog.CompanionCue}, Role: catalog.FileRoleSidecar, CompanionKind: catalog.CompanionCue},
			{SourceFile: catalog.SourceFile{ID: coverID, RootID: "audio", RelativePath: "Omnibus/cover.jpg", Size: 22, ModifiedAt: modified, FileKind: catalog.FileKindCover}, Role: catalog.FileRoleCover, Shared: true},
		},
		Books: []catalog.GroupingContextBook{
			{BookID: bookID, Title: "First", FileIDs: []uuid.UUID{firstFileID, companionID, coverID}},
			{BookID: siblingID, Title: "Second", FileIDs: []uuid.UUID{}},
		},
	}}
	request := newJSONRequest(
		t,
		http.MethodGet,
		"/v1/books/"+bookID.String()+"/grouping",
		nil,
	)
	request.SetPathValue("bookId", bookID.String())
	response := performHandler(groupingContextHandler(repository), request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	var body struct {
		BookID        string              `json:"bookId"`
		RootID        string              `json:"rootId"`
		ScopePath     string              `json:"scopePath"`
		ReviewReason  string              `json:"reviewReason"`
		Evidence      map[string][]string `json:"evidence"`
		CanReorganize bool                `json:"canReorganize"`
		Files         []struct {
			ID            string    `json:"id"`
			RelativePath  string    `json:"relativePath"`
			Name          string    `json:"name"`
			Size          int64     `json:"size"`
			ModifiedAt    time.Time `json:"modifiedAt"`
			Role          string    `json:"role"`
			CompanionKind string    `json:"companionKind"`
			Shared        bool      `json:"shared"`
		} `json:"files"`
		Books []struct {
			BookID  string   `json:"bookId"`
			Title   string   `json:"title"`
			FileIDs []string `json:"fileIds"`
		} `json:"books"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BookID != bookID.String() || body.RootID != "audio" ||
		body.ScopePath != "Omnibus" || body.ReviewReason != "probe_failed" ||
		!body.CanReorganize ||
		!slices.Equal(body.Evidence[catalog.FileRoleSidecar], []string{"Omnibus/book.cue"}) ||
		len(body.Files) != 4 ||
		body.Files[0].ID != firstFileID.String() ||
		body.Files[0].RelativePath != "Omnibus/Disc 1/First.m4b" ||
		body.Files[0].Name != "First.m4b" ||
		body.Files[0].Size != 101 ||
		!body.Files[0].ModifiedAt.Equal(modified) ||
		body.Files[0].Role != catalog.FileRoleAudio ||
		body.Files[1].ID != looseFileID.String() ||
		body.Files[2].CompanionKind != catalog.CompanionCue ||
		body.Files[2].Role != catalog.FileRoleSidecar || body.Files[2].Shared ||
		body.Files[3].Role != catalog.FileRoleCover || !body.Files[3].Shared ||
		len(body.Books) != 2 ||
		body.Books[0].BookID != bookID.String() ||
		!slices.Equal(body.Books[0].FileIDs, []string{firstFileID.String(), companionID.String(), coverID.String()}) ||
		body.Books[1].BookID != siblingID.String() ||
		body.Books[1].FileIDs == nil ||
		len(body.Books[1].FileIDs) != 0 {
		t.Fatalf("grouping context = %#v", body)
	}
	encoded := response.Body.String()
	for _, private := range []string{
		"H:/", "absolutePath", "sha256", "media", "scanRunId", "missingAt",
	} {
		if strings.Contains(encoded, private) {
			t.Fatalf("grouping context leaked %q: %s", private, encoded)
		}
	}
}

func TestGroupingAcceptsEveryPresentFolderAudioIncludingUnassignedFiles(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	assignedID := uuid.New()
	unassignedID := uuid.New()
	companionID := uuid.New()
	coverID := uuid.New()
	modified := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repository := &catalogAPIFake{
		details: map[uuid.UUID]catalog.BookDetail{
			bookID: {
				Book: catalog.Book{
					ID: bookID, RootID: "audio", SourceFolder: "Omnibus",
					GroupingKey: "first", Title: "First",
				},
				Files: []catalog.SourceFile{{
					ID: assignedID, RootID: "audio", RelativePath: "Omnibus/First.m4b",
				}},
			},
		},
		groupingContext: catalog.GroupingContext{
			BookID: bookID, RootID: "audio", ScopePath: "Omnibus",
			Files: []catalog.GroupingContextFile{
				{SourceFile: catalog.SourceFile{ID: assignedID, RootID: "audio", RelativePath: "Omnibus/Disc 1/First.m4b", Size: 10, ModifiedAt: modified, FileKind: catalog.FileKindAudio}, Role: catalog.FileRoleAudio},
				{SourceFile: catalog.SourceFile{ID: unassignedID, RootID: "audio", RelativePath: "Omnibus/Disc 2/Loose.flac", Size: 20, ModifiedAt: modified, FileKind: catalog.FileKindAudio}, Role: catalog.FileRoleAudio},
				{SourceFile: catalog.SourceFile{ID: companionID, RootID: "audio", RelativePath: "Omnibus/book.cue", Size: 30, ModifiedAt: modified, FileKind: catalog.FileKindCompanion, CompanionKind: catalog.CompanionCue}, Role: catalog.FileRoleSidecar, CompanionKind: catalog.CompanionCue},
				{SourceFile: catalog.SourceFile{ID: coverID, RootID: "audio", RelativePath: "Omnibus/cover.jpg", Size: 40, ModifiedAt: modified, FileKind: catalog.FileKindCover}, Role: catalog.FileRoleCover, Shared: true},
			},
			Books: []catalog.GroupingContextBook{{
				BookID: bookID, Title: "First", GroupingKey: "first",
				FileIDs: []uuid.UUID{assignedID},
			}},
		},
	}
	duplicateShared := newJSONRequest(
		t,
		http.MethodPut,
		"/v1/books/"+bookID.String()+"/grouping",
		map[string]any{
			"expectedFiles": []map[string]any{
				{"fileId": assignedID.String(), "size": 10, "modifiedAt": modified},
				{"fileId": unassignedID.String(), "size": 20, "modifiedAt": modified},
				{"fileId": companionID.String(), "size": 30, "modifiedAt": modified},
				{"fileId": coverID.String(), "size": 40, "modifiedAt": modified},
			},
			"groups": []map[string]any{
				{"bookId": bookID.String(), "fileIds": []string{assignedID.String(), companionID.String(), coverID.String(), coverID.String()}},
				{"fileIds": []string{unassignedID.String(), coverID.String()}},
			},
		},
	)
	duplicateShared.SetPathValue("bookId", bookID.String())
	if response := performHandler(groupingHandler(repository), duplicateShared); response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate shared response = %d %s", response.Code, response.Body)
	}
	request := newJSONRequest(
		t,
		http.MethodPut,
		"/v1/books/"+bookID.String()+"/grouping",
		map[string]any{
			"expectedFiles": []map[string]any{
				{"fileId": assignedID.String(), "size": 10, "modifiedAt": modified},
				{"fileId": unassignedID.String(), "size": 20, "modifiedAt": modified},
				{"fileId": companionID.String(), "size": 30, "modifiedAt": modified},
				{"fileId": coverID.String(), "size": 40, "modifiedAt": modified},
			},
			"groups": []map[string]any{
				{"bookId": bookID.String(), "fileIds": []string{assignedID.String(), companionID.String(), coverID.String()}},
				{"fileIds": []string{unassignedID.String(), coverID.String()}},
			},
		},
	)
	request.SetPathValue("bookId", bookID.String())
	response := performHandler(groupingHandler(repository), request)
	var result struct {
		CatalogGeneration int64 `json:"catalogGeneration"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.CatalogGeneration != 2 ||
		len(repository.grouping.Groups) != 2 || len(repository.grouping.ExpectedFiles) != 4 ||
		!slices.Contains(repository.grouping.Groups[0].FileIDs, companionID) ||
		!slices.Contains(repository.grouping.Groups[0].FileIDs, coverID) ||
		!slices.Contains(repository.grouping.Groups[1].FileIDs, coverID) {
		t.Fatalf("response = %d %s, grouping = %#v",
			response.Code, response.Body, repository.grouping)
	}
}

func TestGroupingRejectsStaleFileFingerprintBeforeSave(t *testing.T) {
	t.Parallel()
	bookID, fileID := uuid.New(), uuid.New()
	modified := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repository := &catalogAPIFake{groupingContext: catalog.GroupingContext{
		BookID: bookID, RootID: "audio", ScopePath: "Book",
		Files: []catalog.GroupingContextFile{{
			SourceFile: catalog.SourceFile{ID: fileID, RootID: "audio", RelativePath: "Book/book.m4b", Size: 10, ModifiedAt: modified, FileKind: catalog.FileKindAudio},
			Role:       catalog.FileRoleAudio,
		}},
		Books: []catalog.GroupingContextBook{{BookID: bookID, Title: "Book", GroupingKey: "book", FileIDs: []uuid.UUID{fileID}}},
	}}
	request := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/grouping", map[string]any{
		"expectedFiles": []map[string]any{{"fileId": fileID.String(), "size": 9, "modifiedAt": modified}},
		"groups":        []map[string]any{{"bookId": bookID.String(), "fileIds": []string{fileID.String()}}},
	})
	request.SetPathValue("bookId", bookID.String())
	response := performHandler(groupingHandler(repository), request)
	if response.Code != http.StatusConflict || repository.grouping.ID != uuid.Nil {
		t.Fatalf("response=%d %s saved=%#v", response.Code, response.Body, repository.grouping)
	}
}

func TestConfirmMatchRetryRecoversFromQueueFailure(t *testing.T) {
	t.Parallel()

	bookID := uuid.New()
	matchID := uuid.New()
	repository := &catalogAPIFake{enqueueErr: errors.New("queue unavailable")}
	request := func() *http.Request {
		value := newJSONRequest(t, http.MethodPut, "/v1/books/"+bookID.String()+"/match",
			map[string]string{"matchId": matchID.String()})
		value.SetPathValue("bookId", bookID.String())
		return value
	}
	if response := performHandler(confirmMatchHandler(repository), request()); response.Code != http.StatusInternalServerError {
		t.Fatalf("first response = %d %s", response.Code, response.Body)
	}
	repository.enqueueErr = nil
	if response := performHandler(confirmMatchHandler(repository), request()); response.Code != http.StatusAccepted {
		t.Fatalf("retry response = %d %s", response.Code, response.Body)
	}
	if repository.confirmCalls != 2 || len(repository.jobs) != 1 {
		t.Fatalf("retry jobs = %#v, confirms = %d", repository.jobs, repository.confirmCalls)
	}
}

func TestManualGroupingKeyIgnoresGroupOrder(t *testing.T) {
	t.Parallel()

	book := catalog.Book{RootID: "audio", SourceFolder: "Book"}
	first := uuid.New()
	second := uuid.New()
	left := manualGroupingKey(book, []uuid.UUID{first, second})
	right := manualGroupingKey(book, []uuid.UUID{second, first})
	if left != right {
		t.Fatalf("keys = %q and %q", left, right)
	}
}

func TestCatalogRouteSurfaceIsAuthenticatedAndReadOnlyForSourceFiles(t *testing.T) {
	t.Parallel()

	expected := map[string]bool{
		"GET /v1/books":                   true,
		"GET /v1/books/{bookId}":          true,
		"GET /v1/books/{bookId}/files":    true,
		"GET /v1/books/{bookId}/cover":    true,
		"GET /v1/books/{bookId}/grouping": true,
		"GET /v1/catalog/filters":         true,
		"POST /v1/catalog/matches/search": true,
		"PUT /v1/books/{bookId}/match":    true,
		"PUT /v1/books/{bookId}/grouping": true,
		"POST /v1/catalog/rescan":         true,
	}
	for _, registered := range routes(Dependencies{}) {
		key := registered.method + " " + registered.path
		if expected[key] {
			if registered.public {
				t.Fatalf("%s is public", key)
			}
			delete(expected, key)
		}
		lower := strings.ToLower(key)
		for _, forbidden := range []string{
			"upload", "delete", "rename", "move", "tag", "metadata/edit", "cover/publish",
		} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("forbidden source mutation route %q", key)
			}
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing routes = %#v", expected)
	}
	for _, route := range []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/v1/books"},
		{method: http.MethodGet, target: "/v1/books/" + uuid.New().String() + "/grouping"},
		{method: http.MethodGet, target: "/v1/catalog/filters"},
		{method: http.MethodPost, target: "/v1/catalog/matches/search"},
	} {
		response := performHandler(New(Dependencies{}),
			newJSONRequest(t, route.method, route.target, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status = %d", route.target, response.Code)
		}
	}
}

type manualSearchProvider struct {
	candidate metadata.Candidate
	calls     int
}

func (provider *manualSearchProvider) Search(
	context.Context,
	metadata.Query,
) ([]metadata.Candidate, error) {
	provider.calls++
	return []metadata.Candidate{provider.candidate}, nil
}

type matcherFake struct {
	local    catalog.InferredBook
	decision metadata.Decision
	err      error
}

func (matcher *matcherFake) Match(
	_ context.Context,
	local catalog.InferredBook,
) (metadata.Decision, error) {
	matcher.local = local
	return matcher.decision, matcher.err
}

type ioNopCloser struct {
	*strings.Reader
}

func (ioNopCloser) Close() error { return nil }

var _ MetadataMatcher = (*matcherFake)(nil)
