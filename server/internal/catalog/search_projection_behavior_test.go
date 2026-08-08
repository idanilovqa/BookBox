package catalog

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildSearchProjectionRejectsMalformedMedia(t *testing.T) {
	invalid := []struct {
		name  string
		media map[string]any
	}{
		{"role type", map[string]any{"role": 1}},
		{"role value", map[string]any{"role": "document"}},
		{"negative", map[string]any{"role": FileRoleAudio, "durationMs": int64(-1)}},
		{"fractional", map[string]any{"role": FileRoleAudio, "durationMs": 1.5}},
		{"nan", map[string]any{"role": FileRoleAudio, "durationMs": math.NaN()}},
		{"infinity", map[string]any{"role": FileRoleAudio, "durationMs": math.Inf(1)}},
		{"unsigned overflow", map[string]any{"role": FileRoleAudio, "durationMs": uint64(math.MaxUint64)}},
		{"string duration", map[string]any{"role": FileRoleAudio, "durationMs": "100"}},
		{"boolean duration", map[string]any{"role": FileRoleAudio, "durationMs": true}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			input := oneBookProjectionInput(1_000)
			input.Files[0].Media = test.media
			if _, err := BuildSearchProjection(input); err != ErrInvalidData {
				t.Fatalf("error=%v, want ErrInvalidData", err)
			}
		})
	}
}

func TestBuildSearchProjectionRejectsInvalidActiveRecords(t *testing.T) {
	invalidReview := oneBookProjectionInput(1_000)
	invalidReview.Books[0].ReviewState = "approved"
	if _, err := BuildSearchProjection(invalidReview); err != ErrInvalidData {
		t.Fatalf("invalid review error=%v, want ErrInvalidData", err)
	}

	missingModifiedTime := oneBookProjectionInput(1_000)
	missingModifiedTime.Files[0].ModifiedAt = time.Time{}
	if _, err := BuildSearchProjection(missingModifiedTime); err != ErrInvalidData {
		t.Fatalf("zero modified time error=%v, want ErrInvalidData", err)
	}
}

func TestBuildSearchProjectionRequiresEveryAudioDurationForBookTotal(t *testing.T) {
	input := oneBookProjectionInput(1_000)
	secondID := uuid.New()
	second := projectionFile(secondID, "Book/part2.m4b", FileRoleAudio, 0)
	input.Files = append(input.Files, second)
	input.Assignments[input.Books[0].ID] = append(input.Assignments[input.Books[0].ID], secondID)
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	if duration := audiobookDocument(t, projection, input.Books[0].ID).DurationMS; duration != nil {
		t.Fatalf("duration=%d, want unknown", *duration)
	}

	overflow := oneBookProjectionInput(math.MaxInt64)
	overflowID := uuid.New()
	overflow.Files = append(overflow.Files,
		projectionFile(overflowID, "Book/part2.m4b", FileRoleAudio, 1))
	overflow.Assignments[overflow.Books[0].ID] = append(
		overflow.Assignments[overflow.Books[0].ID], overflowID,
	)
	if _, err := BuildSearchProjection(overflow); err != ErrInvalidData {
		t.Fatalf("overflow error=%v, want ErrInvalidData", err)
	}
}

func TestBuildSearchProjectionDuplicateDurationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right int64
		duplicate   bool
	}{
		{"floor exact", 3_600_000, 3_720_000, true},
		{"floor plus one", 3_600_000, 3_720_001, false},
		{"percent exact", 35_820_000, 36_000_000, true},
		{"percent plus one", 35_820_000, 36_000_001, false},
		{"cap exact", 60_000_000, 60_300_000, true},
		{"cap plus one", 60_000_000, 60_300_001, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, ids := duplicatePairProjection(t, test.left, test.right)
			first := audiobookDocument(t, projection, ids[0])
			second := audiobookDocument(t, projection, ids[1])
			got := first.DuplicateGroupID != uuid.Nil &&
				first.DuplicateGroupID == second.DuplicateGroupID
			if got != test.duplicate {
				t.Fatalf("duplicate=%v, want %v", got, test.duplicate)
			}
		})
	}
}

func TestBuildSearchProjectionUsesAnchorForNonTransitiveDurations(t *testing.T) {
	input := projectionBooksWithDurations(
		[]string{"Book", "Book", "Book"},
		[]int64{1_000_000, 1_120_000, 1_240_000},
	)
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	first := audiobookDocument(t, projection, input.Books[0].ID)
	second := audiobookDocument(t, projection, input.Books[1].ID)
	third := audiobookDocument(t, projection, input.Books[2].ID)
	if first.DuplicateGroupID == uuid.Nil || first.DuplicateGroupID != second.DuplicateGroupID {
		t.Fatalf("first cluster=%s second=%s", first.DuplicateGroupID, second.DuplicateGroupID)
	}
	if third.DuplicateGroupID != uuid.Nil {
		t.Fatalf("third book bridged into anchor cluster: %s", third.DuplicateGroupID)
	}
}

func TestBuildSearchProjectionPropagatesDistinctDuplicateGroupsToRawAncestors(t *testing.T) {
	input := projectionBooksWithDurations(
		[]string{"Alpha", "Alpha", "Beta", "Beta"},
		[]int64{3_600_000, 3_600_001, 7_200_000, 7_200_001},
	)
	for index := range input.Files {
		group := "A"
		if index >= 2 {
			group = "B"
		}
		input.Files[index].RelativePath = "Shared/" + group + "/" + input.Books[index].ID.String() + ".m4b"
	}
	unassignedID := uuid.New()
	input.Files = append(input.Files,
		projectionFile(unassignedID, "Shared/Loose/loose.m4b", FileRoleAudio, 1_000),
		SourceFile{ID: uuid.New(), RootID: "audiobooks", RelativePath: "Shared/notes.txt", Size: 1, ModifiedAt: time.Now().UTC(), Media: map[string]any{}},
		SourceFile{ID: uuid.New(), RootID: "audiobooks", RelativePath: "Shared/book.nfo", Size: 1, ModifiedAt: time.Now().UTC(), Media: map[string]any{"role": FileRoleSidecar}},
	)
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	alpha := findSearchDocument(t, projection, SearchEntityFolder, "Shared/A")
	if alpha.DuplicateGroupID == uuid.Nil || alpha.DuplicateCount != 2 {
		t.Fatalf("alpha folder duplicate state=%#v", alpha)
	}
	shared := findSearchDocument(t, projection, SearchEntityFolder, "Shared")
	if shared.DuplicateGroupID != uuid.Nil || shared.DuplicateCount != 4 {
		t.Fatalf("shared folder duplicate state=%#v", shared)
	}
	loose := findSearchDocument(t, projection, SearchEntityFile, "Shared/Loose/loose.m4b")
	if loose.BookID != uuid.Nil {
		t.Fatalf("unassigned raw file book=%s", loose.BookID)
	}
	for _, document := range projection.Documents {
		if document.RelativePath == "Shared/notes.txt" || document.RelativePath == "Shared/book.nfo" {
			t.Fatalf("unrecognized file was indexed: %#v", document)
		}
	}
}

func TestBuildSearchProjectionRejectsCanonicallyEquivalentFolderPaths(t *testing.T) {
	input := SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Files: []SourceFile{
			projectionFile(uuid.New(), "Caf\u00e9/one.m4b", FileRoleAudio, 1_000),
			projectionFile(uuid.New(), "Cafe\u0301/two.m4b", FileRoleAudio, 1_000),
		},
	}
	if _, err := BuildSearchProjection(input); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("BuildSearchProjection(canonical folder collision) = %v, want ErrInvalidData", err)
	}
}

func TestBuildSearchProjectionUsesScanCompletionForRecentlyScanned(t *testing.T) {
	input := oneBookProjectionInput(1_000)
	input.CompletedAt = time.Date(2026, 8, 8, 18, 30, 0, 0, time.UTC)
	input.Files[0].ModifiedAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	document := audiobookDocument(t, projection, input.Books[0].ID)
	if document.RecentlyScannedAt == nil || !document.RecentlyScannedAt.Equal(input.CompletedAt) {
		t.Fatalf("RecentlyScannedAt = %v, want %v", document.RecentlyScannedAt, input.CompletedAt)
	}
}

func TestBuildSearchProjectionChoosesEquivalentMetadataDeterministically(t *testing.T) {
	input := oneBookProjectionInput(1_000)
	input.Books[0].Authors = []string{"author", "Author"}
	first, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Books[0].Authors = []string{"Author", "author"}
	second, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Documents, second.Documents) {
		t.Fatalf("equivalent metadata order changed projection:\nfirst=%#v\nsecond=%#v", first.Documents, second.Documents)
	}
}

func oneBookProjectionInput(duration int64) SearchProjectionInput {
	bookID, fileID := uuid.New(), uuid.New()
	return SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Books: []Book{{
			ID: bookID, RootID: "audiobooks", SourceFolder: "Book", Title: "Book",
			Authors: []string{"Author"}, Narrators: []string{"Narrator"}, ReviewState: ReviewUnreviewed,
		}},
		Files:       []SourceFile{projectionFile(fileID, "Book/book.m4b", FileRoleAudio, duration)},
		Assignments: map[uuid.UUID][]uuid.UUID{bookID: {fileID}},
	}
}

func duplicatePairProjection(t *testing.T, left, right int64) (SearchProjection, []uuid.UUID) {
	t.Helper()
	input := projectionBooksWithDurations([]string{"Book", "Book"}, []int64{left, right})
	projection, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	return projection, []uuid.UUID{input.Books[0].ID, input.Books[1].ID}
}

func projectionBooksWithDurations(titles []string, durations []int64) SearchProjectionInput {
	input := SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Assignments: make(map[uuid.UUID][]uuid.UUID, len(titles)),
	}
	for index, title := range titles {
		bookID := uuid.MustParse([]string{
			"11111111-1111-1111-1111-111111111111",
			"22222222-2222-2222-2222-222222222222",
			"33333333-3333-3333-3333-333333333333",
			"44444444-4444-4444-4444-444444444444",
		}[index])
		fileID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(bookID.String()))
		input.Books = append(input.Books, Book{
			ID: bookID, RootID: "audiobooks", SourceFolder: bookID.String(), Title: title,
			Authors: []string{"Author"}, Narrators: []string{"Narrator"}, ReviewState: ReviewUnreviewed,
		})
		input.Files = append(input.Files,
			projectionFile(fileID, bookID.String()+"/book.m4b", FileRoleAudio, durations[index]))
		input.Assignments[bookID] = []uuid.UUID{fileID}
	}
	return input
}

func findSearchDocument(t *testing.T, projection SearchProjection, kind SearchEntityKind, relativePath string) SearchDocument {
	t.Helper()
	for _, document := range projection.Documents {
		if document.EntityKind == kind && document.RelativePath == relativePath {
			return document
		}
	}
	t.Fatalf("missing %s %q", kind, relativePath)
	return SearchDocument{}
}
