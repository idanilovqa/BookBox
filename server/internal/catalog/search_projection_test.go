package catalog

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildSearchProjectionCreatesAudiobookAndRawDocuments(t *testing.T) {
	bookID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	audioID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	coverID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	projection, err := BuildSearchProjection(SearchProjectionInput{
		RootID:      "audiobooks",
		ScanRunID:   uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		CompletedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Books: []Book{{
			ID: bookID, RootID: "audiobooks", SourceFolder: "Frank Herbert/Dune",
			Title: "Dune", Authors: []string{"Frank Herbert"},
			Narrators: []string{"Narrator A"}, ReviewState: ReviewUnreviewed,
		}},
		Files: []SourceFile{
			projectionFile(audioID, "Frank Herbert/Dune/book.m4b", FileRoleAudio, 3_600_000),
			projectionFile(coverID, "Frank Herbert/Dune/cover.jpg", FileRoleCover, 0),
		},
		Assignments: map[uuid.UUID][]uuid.UUID{bookID: {audioID, coverID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertSearchDocument(t, projection, SearchEntityAudiobook, "Frank Herbert/Dune")
	assertSearchDocument(t, projection, SearchEntityFile, "Frank Herbert/Dune/book.m4b")
	assertSearchDocument(t, projection, SearchEntityFile, "Frank Herbert/Dune/cover.jpg")
	assertSearchDocument(t, projection, SearchEntityFolder, "Frank Herbert")
	assertSearchDocument(t, projection, SearchEntityFolder, "Frank Herbert/Dune")
	for _, document := range projection.Documents {
		if document.EntityKind == SearchEntityFolder && document.RelativePath == "" {
			t.Fatal("projection created a synthetic root folder")
		}
	}
}

func TestBuildSearchProjectionValidatesPathsAndMediaNumbers(t *testing.T) {
	base := SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Books: []Book{{
			ID: uuid.New(), RootID: "audiobooks", SourceFolder: "Book",
			Title: "Book", Authors: []string{"Author"}, Narrators: []string{"Narrator"},
			ReviewState: ReviewUnreviewed,
		}},
	}
	fileID := uuid.New()
	base.Files = []SourceFile{projectionFile(fileID, "Book/book.m4b", FileRoleAudio, 1)}
	base.Assignments = map[uuid.UUID][]uuid.UUID{base.Books[0].ID: {fileID}}

	invalidPath := base
	invalidPath.Books = append([]Book(nil), base.Books...)
	invalidPath.Books[0].SourceFolder = "Book/../Other"
	if _, err := BuildSearchProjection(invalidPath); err != ErrInvalidData {
		t.Fatalf("invalid source folder error=%v, want ErrInvalidData", err)
	}

	integralJSON := base
	integralJSON.Files = append([]SourceFile(nil), base.Files...)
	integralJSON.Files[0].Media = map[string]any{
		"role": FileRoleAudio, "durationMs": json.Number("3600000.0"),
	}
	projection, err := BuildSearchProjection(integralJSON)
	if err != nil {
		t.Fatalf("integral json duration: %v", err)
	}
	document := audiobookDocument(t, projection, base.Books[0].ID)
	if document.DurationMS == nil || *document.DurationMS != 3_600_000 {
		t.Fatalf("duration=%v, want 3600000", document.DurationMS)
	}

	floatOverflow := base
	floatOverflow.Files = append([]SourceFile(nil), base.Files...)
	floatOverflow.Files[0].Media = map[string]any{
		"role": FileRoleAudio, "durationMs": float64(math.MaxInt64),
	}
	if _, err := BuildSearchProjection(floatOverflow); err != ErrInvalidData {
		t.Fatalf("overflow duration error=%v, want ErrInvalidData", err)
	}
}

func TestBuildSearchProjectionDuplicateIdentityKeepsAuthorNarratorBoundary(t *testing.T) {
	firstID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	secondID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	firstFile := uuid.NewSHA1(uuid.NameSpaceOID, []byte("first"))
	secondFile := uuid.NewSHA1(uuid.NameSpaceOID, []byte("second"))
	projection, err := BuildSearchProjection(SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Books: []Book{
			{ID: firstID, RootID: "audiobooks", SourceFolder: "one", Title: "Book", Authors: []string{"a"}, Narrators: []string{"b", "c"}, ReviewState: ReviewUnreviewed},
			{ID: secondID, RootID: "audiobooks", SourceFolder: "two", Title: "Book", Authors: []string{"a", "b"}, Narrators: []string{"c"}, ReviewState: ReviewUnreviewed},
		},
		Files: []SourceFile{
			projectionFile(firstFile, "one/book.m4b", FileRoleAudio, 3_600_000),
			projectionFile(secondFile, "two/book.m4b", FileRoleAudio, 3_600_000),
		},
		Assignments: map[uuid.UUID][]uuid.UUID{firstID: {firstFile}, secondID: {secondFile}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bookID := range []uuid.UUID{firstID, secondID} {
		if document := audiobookDocument(t, projection, bookID); document.DuplicateGroupID != uuid.Nil {
			t.Fatalf("book %s grouped across author/narrator boundary: %#v", bookID, document)
		}
	}
}

func TestBuildSearchProjectionIsStableAcrossInputOrderAndPinsUUIDv5(t *testing.T) {
	bookID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fileID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	input := SearchProjectionInput{
		RootID: "root-a", ScanRunID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		CompletedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Books: []Book{{
			ID: bookID, RootID: "root-a", SourceFolder: "Cafe\u0301", Title: "Cafe\u0301",
			Authors: []string{"Second", "First"}, Narrators: []string{"Narrator"},
			ReviewState: ReviewUnreviewed,
		}},
		Files: []SourceFile{{
			ID: fileID, RootID: "root-a", RelativePath: "Cafe\u0301/book.m4b", Size: 1,
			ModifiedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
			Media:      map[string]any{"role": FileRoleAudio, "durationMs": int64(3_600_000)},
		}},
		Assignments: map[uuid.UUID][]uuid.UUID{bookID: {fileID}},
	}
	first, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Books[0].Authors = []string{"First", "Second"}
	second, err := BuildSearchProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("projection depends on author order:\nfirst=%#v\nsecond=%#v", first, second)
	}
	document := audiobookDocument(t, first, bookID)
	if want := uuid.MustParse("42f036f3-bf7e-5b7c-87dd-61c31a37e90f"); document.ID != want {
		t.Fatalf("audiobook document id=%s, want %s", document.ID, want)
	}
	if document.CompactName != "café" || document.CompactPath != "café" {
		t.Fatalf("unicode compact name=%q path=%q", document.CompactName, document.CompactPath)
	}
}

func TestBuildSearchProjectionGroupsSameNarrationAndCloseDuration(t *testing.T) {
	books := make([]Book, 0, 4)
	files := make([]SourceFile, 0, 4)
	assignments := make(map[uuid.UUID][]uuid.UUID, 4)
	bookIDs := []uuid.UUID{
		uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		uuid.MustParse("44444444-4444-4444-4444-444444444444"),
	}
	durations := []int64{36_000_000, 36_120_000, 36_000_000, 36_400_000}
	narrators := []string{"Narrator A", "Narrator A", "Narrator B", "Narrator A"}
	for index, bookID := range bookIDs {
		fileID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(bookID.String()))
		books = append(books, Book{
			ID: bookID, RootID: "audiobooks", SourceFolder: bookID.String(),
			Title: "Dune", Authors: []string{"Frank Herbert"},
			Narrators: []string{narrators[index]}, ReviewState: ReviewUnreviewed,
		})
		files = append(files, projectionFile(
			fileID, bookID.String()+"/book.m4b", FileRoleAudio, durations[index],
		))
		assignments[bookID] = []uuid.UUID{fileID}
	}

	projection, err := BuildSearchProjection(SearchProjectionInput{
		RootID: "audiobooks", ScanRunID: uuid.New(), CompletedAt: time.Now().UTC(),
		Books: books, Files: files, Assignments: assignments,
	})
	if err != nil {
		t.Fatal(err)
	}
	documents := make(map[uuid.UUID]SearchDocument)
	for _, document := range projection.Documents {
		if document.EntityKind == SearchEntityAudiobook {
			documents[document.BookID] = document
		}
	}
	first, second := documents[bookIDs[0]], documents[bookIDs[1]]
	if first.DuplicateGroupID == uuid.Nil || first.DuplicateGroupID != second.DuplicateGroupID ||
		first.DuplicateCount != 2 || second.DuplicateCount != 2 {
		t.Fatalf("close copies were not grouped: first=%#v second=%#v", first, second)
	}
	for _, bookID := range bookIDs[2:] {
		if document := documents[bookID]; document.DuplicateGroupID != uuid.Nil ||
			document.DuplicateCount != 1 {
			t.Fatalf("book %s unexpectedly grouped: %#v", bookID, document)
		}
	}
}

func projectionFile(id uuid.UUID, relativePath, role string, durationMS int64) SourceFile {
	return SourceFile{
		ID: id, RootID: "audiobooks", RelativePath: relativePath,
		Size: 1, ModifiedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Media: map[string]any{"role": role, "durationMs": durationMS},
	}
}

func assertSearchDocument(
	t *testing.T,
	projection SearchProjection,
	kind SearchEntityKind,
	relativePath string,
) {
	t.Helper()
	for _, document := range projection.Documents {
		if document.EntityKind == kind && document.RelativePath == relativePath {
			return
		}
	}
	t.Fatalf("missing %s document %q in %#v", kind, relativePath, projection.Documents)
}

func audiobookDocument(t *testing.T, projection SearchProjection, bookID uuid.UUID) SearchDocument {
	t.Helper()
	for _, document := range projection.Documents {
		if document.EntityKind == SearchEntityAudiobook && document.BookID == bookID {
			return document
		}
	}
	t.Fatalf("missing audiobook document for %s", bookID)
	return SearchDocument{}
}
