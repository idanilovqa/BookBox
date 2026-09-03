package httpapi

import (
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"github.com/google/uuid"
)

const defaultSearchPageSize = 50

type SearchSnapshotResponse struct {
	RootID            string    `json:"rootId"`
	ScanRunID         string    `json:"scanRunId"`
	CatalogGeneration int64     `json:"catalogGeneration"`
	CompletedAt       time.Time `json:"completedAt"`
}

type BookSearchGroupResponse struct {
	GroupID   string             `json:"groupId"`
	Duplicate bool               `json:"duplicate"`
	Books     []BookCardResponse `json:"books"`
}

type RawSearchEntryResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	RelativePath     string     `json:"relativePath"`
	Kind             string     `json:"kind"`
	Size             *int64     `json:"size,omitempty"`
	ModifiedAt       *time.Time `json:"modifiedAt,omitempty"`
	BookID           string     `json:"bookId,omitempty"`
	DuplicateGroupID string     `json:"duplicateGroupId,omitempty"`
	DuplicateBook    bool       `json:"duplicateBook"`
}

type RawSearchResponse struct {
	RootID     string                   `json:"rootId"`
	Query      string                   `json:"query"`
	Snapshot   SearchSnapshotResponse   `json:"snapshot"`
	Entries    []RawSearchEntryResponse `json:"entries"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

func requestedRootIDs(request *http.Request, roots []config.Root) ([]string, error) {
	values := request.URL.Query()["rootId"]
	if len(values) == 0 {
		for _, root := range roots {
			values = append(values, root.ID)
		}
	}
	configured := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		configured[root.ID] = struct{}{}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := configured[value]; value == "" || !ok {
			return nil, catalog.ErrInvalidData
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, catalog.ErrInvalidData
	}
	return result, nil
}

func parseSearchLimit(value string) (int, error) {
	if value == "" {
		return defaultSearchPageSize, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		return 0, catalog.ErrInvalidPage
	}
	return limit, nil
}

func shapeSnapshots(values []catalog.SearchSnapshot, roots map[string]struct{}) ([]SearchSnapshotResponse, bool) {
	if len(values) != len(roots) {
		return nil, false
	}
	result := make([]SearchSnapshotResponse, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	previousRoot := ""
	for _, value := range values {
		_, requested := roots[value.RootID]
		_, duplicate := seen[value.RootID]
		if !requested || duplicate || value.RootID <= previousRoot || value.ScanRunID == uuid.Nil ||
			value.CatalogGeneration <= 0 || value.CompletedAt.IsZero() || value.DocumentCount < 0 {
			return nil, false
		}
		seen[value.RootID] = struct{}{}
		previousRoot = value.RootID
		result = append(result, SearchSnapshotResponse{
			RootID: value.RootID, ScanRunID: value.ScanRunID.String(),
			CatalogGeneration: value.CatalogGeneration, CompletedAt: value.CompletedAt.UTC(),
		})
	}
	return result, true
}

func shapeBookGroups(values []catalog.SearchGroup, roots map[string]struct{}) ([]BookSearchGroupResponse, bool) {
	result := make([]BookSearchGroupResponse, 0, len(values))
	seen := make(map[uuid.UUID]struct{})
	for _, group := range values {
		if group.ID == uuid.Nil || len(group.Documents) == 0 || group.Duplicate != (len(group.Documents) > 1) {
			return nil, false
		}
		response := BookSearchGroupResponse{GroupID: group.ID.String(), Duplicate: group.Duplicate, Books: make([]BookCardResponse, 0, len(group.Documents))}
		groupRoot := group.Documents[0].RootID
		for _, document := range group.Documents {
			_, rootOK := roots[document.RootID]
			valid := rootOK && document.RootID == groupRoot && document.ID != uuid.Nil && document.BookID != uuid.Nil && document.EntityKind == catalog.SearchEntityAudiobook && catalog.ValidateSourceFolder(document.RelativePath) == nil && strings.TrimSpace(document.Title) != "" && document.DuplicateCount == len(group.Documents) && (document.DurationMS == nil || *document.DurationMS >= 0) && validReviewStateReason(document.ReviewState, document.ReviewReason)
			if group.Duplicate {
				valid = valid && document.DuplicateGroupID == group.ID
			} else {
				valid = valid && document.DuplicateGroupID == uuid.Nil && group.ID == document.ID
			}
			if _, duplicate := seen[document.BookID]; !valid || duplicate {
				return nil, false
			}
			seen[document.BookID] = struct{}{}
			response.Books = append(response.Books, searchBookCard(document))
		}
		result = append(result, response)
	}
	return result, true
}

func searchBookCard(document catalog.SearchDocument) BookCardResponse {
	return BookCardResponse{ID: document.BookID.String(), RootID: document.RootID, Title: document.Title, Authors: nonNil(document.Authors), PublicationYear: document.PublicationYear, Genres: nonNil(document.Genres), Series: document.Series, SeriesPosition: document.SeriesPosition, Narrators: nonNil(document.Narrators), CoverURL: "/v1/books/" + document.BookID.String() + "/cover", ReviewState: document.ReviewState, ReviewReason: document.ReviewReason, SourceFolder: document.RelativePath, DurationMS: document.DurationMS}
}

func validReviewStateReason(state, reason string) bool {
	if reason != strings.TrimSpace(reason) {
		return false
	}
	switch state {
	case catalog.ReviewUnreviewed:
		return reason == ""
	case catalog.ReviewConfirmed:
		return reason == "" || reason == catalog.ReviewReasonCorrupt
	case catalog.ReviewNeedsReview:
		switch reason {
		case "conflicting_title", "conflicting_author", "weak_loose_files", "probe_failed", "reorganization_incomplete", catalog.ReviewReasonLegacy, catalog.ReviewReasonCorrupt:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func shapeRawEntries(values []catalog.SearchGroup, rootID string) ([]RawSearchEntryResponse, bool) {
	result := make([]RawSearchEntryResponse, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, group := range values {
		if len(group.Documents) != 1 || group.Duplicate {
			return nil, false
		}
		document := group.Documents[0]
		validKind := document.EntityKind == catalog.SearchEntityFolder || document.EntityKind == catalog.SearchEntityFile
		if document.ID == uuid.Nil || group.ID != document.ID || document.RootID != rootID || !validKind || catalog.ValidateRelativePath(document.RelativePath) != nil || document.DuplicateCount < 1 {
			return nil, false
		}
		if _, duplicate := seen[document.ID]; duplicate {
			return nil, false
		}
		seen[document.ID] = struct{}{}
		if (document.EntityKind == catalog.SearchEntityFolder && (document.Size != nil || document.ModifiedAt != nil)) || (document.EntityKind == catalog.SearchEntityFile && (document.Size == nil || document.ModifiedAt == nil)) || (document.Size != nil && *document.Size < 0) || (document.ModifiedAt != nil && document.ModifiedAt.IsZero()) {
			return nil, false
		}
		if document.DisplayName == "" || strings.ContainsAny(document.DisplayName, `/\`) || document.DisplayName != path.Base(document.RelativePath) {
			return nil, false
		}
		entry := RawSearchEntryResponse{ID: document.ID.String(), Name: document.DisplayName, RelativePath: document.RelativePath, Kind: string(document.EntityKind), Size: document.Size, DuplicateBook: document.DuplicateCount > 1}
		if document.ModifiedAt != nil {
			modified := document.ModifiedAt.UTC()
			entry.ModifiedAt = &modified
		}
		if document.EntityKind == catalog.SearchEntityFile && document.BookID != uuid.Nil {
			entry.BookID = document.BookID.String()
		}
		if document.DuplicateGroupID != uuid.Nil {
			if document.DuplicateCount < 2 {
				return nil, false
			}
			entry.DuplicateGroupID = document.DuplicateGroupID.String()
		}
		result = append(result, entry)
	}
	return result, true
}

func configuredRoot(roots []config.Root, rootID string) bool {
	for _, root := range roots {
		if root.ID == rootID {
			return true
		}
	}
	return false
}

func writeSearchError(response http.ResponseWriter, err error) {
	if errors.Is(err, catalog.ErrSearchIndexBuilding) {
		writeStableError(response, http.StatusServiceUnavailable, "search_index_building", "search index is building")
		return
	}
	writeCatalogError(response, err)
}
