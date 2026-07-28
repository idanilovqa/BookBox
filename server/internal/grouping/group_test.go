package grouping

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/media"
	"bookbox.local/server/internal/scanner"
)

func TestInferKeepsChaptersAndSplitsDistinctM4BBooks(t *testing.T) {
	chapters := folder("Series",
		audio("01 Part One.mp3", "Book", "Author"),
		audio("02 Part Two.mp3", "Book", "Author"),
	)
	if got := Infer(chapters); len(got) != 1 ||
		got[0].ReviewState != catalog.ReviewUnreviewed {
		t.Fatalf("chapter inference = %#v, want one unreviewed group", got)
	}

	distinct := folder("Omnibus",
		audio("Dune.m4b", "Dune", "Frank Herbert"),
		audio("Dune Messiah.m4b", "Dune Messiah", "Frank Herbert"),
	)
	got := Infer(distinct)
	if len(got) != 2 {
		t.Fatalf("book groups = %d, want 2: %#v", len(got), got)
	}
	if got[0].Title != "Dune" || got[1].Title != "Dune Messiah" {
		t.Fatalf("titles = %q, %q", got[0].Title, got[1].Title)
	}
}

func TestInferKeepsFilenameOnlyDifferencesForReview(t *testing.T) {
	got := Infer(folder("Unknown",
		audio("Dune.m4b", "", ""),
		audio("Foundation.m4b", "", ""),
	))
	if len(got) != 1 || got[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("inference = %#v, want one needs-review group", got)
	}
}

func TestInferKeepsConflictingTagsForReview(t *testing.T) {
	got := Infer(folder("Mixed",
		audio("Dune.m4b", "Foundation", "Isaac Asimov"),
		audio("Foundation.m4b", "Dune", "Frank Herbert"),
	))
	if len(got) != 1 || got[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("inference = %#v, want one needs-review group", got)
	}
}

func TestInferFlagsConflictingMP3TagsForReview(t *testing.T) {
	got := Infer(folder("Mixed",
		audio("01.mp3", "Dune", "Frank Herbert"),
		audio("02.mp3", "Foundation", "Isaac Asimov"),
	))
	if len(got) != 1 || got[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("inference = %#v, want one needs-review group", got)
	}
}

func TestInferKeepsLeadingOrdinalM4BChaptersTogether(t *testing.T) {
	first := audio("01 - Prologue.m4b", "Prologue", "Author")
	first.Metadata.Album = "Book"
	second := audio("02 - Chapter.m4b", "Chapter", "Author")
	second.Metadata.Album = "Book"
	got := Infer(folder("Book", first, second))
	if len(got) != 1 || got[0].ReviewState != catalog.ReviewUnreviewed {
		t.Fatalf("inference = %#v, want one unreviewed chapter group", got)
	}
}

func TestInferSplitsDistinctEmbeddedTitleAuthorIdentitiesWithGenericNames(t *testing.T) {
	got := Infer(folder("Import",
		audio("file-alpha.m4b", "Dune", "Frank Herbert"),
		audio("file-beta.m4b", "Foundation", "Isaac Asimov"),
	))
	if len(got) != 2 {
		t.Fatalf("inference = %#v, want two embedded-identity groups", got)
	}
}

func TestInferNeverSplitsPartDiscOrChapterM4B(t *testing.T) {
	for _, names := range [][]string{
		{"Book Part 1.m4b", "Book Part 2.m4b"},
		{"Book Disc 1.m4b", "Book Disc 2.m4b"},
		{"Book Chapter 01.m4b", "Book Chapter 02.m4b"},
	} {
		files := make([]scanner.File, 0, len(names))
		for _, name := range names {
			files = append(files, audio(name, "Book", "Author"))
		}
		if got := Infer(folder("Book", files...)); len(got) != 1 ||
			got[0].ReviewState != catalog.ReviewUnreviewed {
			t.Fatalf("%v produced inference %#v", names, got)
		}
	}
}

func TestEvidenceNormalizationStripsComparisonOnlyNoise(t *testing.T) {
	got := evidenceFor(audio(
		"01 - Dune Part 1 (Unabridged).m4b",
		"DUNE: Part 1 (Unabridged Edition)",
		"Frank Herbert",
	))
	if got.NormalizedTitle != "dune" || got.FilenameTitle != "dune" {
		t.Fatalf("evidence = %#v, want normalized title and filename dune", got)
	}
}

func TestInferUsesAudioStemWhenConfiguredRootContainsAudioDirectly(t *testing.T) {
	file := audio("Dune.m4b", "", "")
	file.RelativePath = "Dune.m4b"
	got := Infer(scanner.Folder{
		RootID:       "audiobooks",
		RelativePath: "",
		Files:        []scanner.File{file},
	})
	if len(got) != 1 || got[0].Title != "Dune" {
		t.Fatalf("inference = %#v, want Dune fallback title", got)
	}
}

func TestInferFlagsProbeFailuresForReview(t *testing.T) {
	file := audio("Dune.m4b", "", "")
	file.ProbeError = "probe_failed"
	got := Infer(folder("Dune", file))
	if len(got) != 1 || got[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("inference = %#v, want needs-review probe failure", got)
	}
}

func TestInferIsStableAndAssignsEveryAudioExactlyOnce(t *testing.T) {
	cover := scanner.File{
		ID:           stableID("cover.jpg"),
		RelativePath: "Omnibus/cover.jpg",
		Name:         "cover.jpg",
		Kind:         scanner.KindCover,
	}
	firstFolder := folder("Omnibus",
		audio("Dune Messiah.m4b", "Dune Messiah", "Frank Herbert"),
		cover,
		audio("Dune.m4b", "Dune", "Frank Herbert"),
	)
	secondFolder := firstFolder
	secondFolder.Files = []scanner.File{
		firstFolder.Files[2], firstFolder.Files[0], firstFolder.Files[1],
	}

	first := Infer(firstFolder)
	second := Infer(secondFolder)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inference is order-dependent:\nfirst=%#v\nsecond=%#v", first, second)
	}

	counts := make(map[uuid.UUID]int)
	for _, book := range first {
		if book.GroupingKey == "" {
			t.Fatal("empty grouping key")
		}
		for _, id := range book.SourceFileIDs {
			if book.FileRoles[id] == catalog.FileRoleAudio {
				counts[id]++
			}
		}
		if book.FileRoles[cover.ID] != catalog.FileRoleCover {
			t.Fatalf("book %q does not include recognized shared cover", book.Title)
		}
	}
	for _, file := range firstFolder.Files {
		if file.Kind == scanner.KindAudio && counts[file.ID] != 1 {
			t.Fatalf("audio %s assigned %d times", file.Name, counts[file.ID])
		}
	}
}

func folder(name string, files ...scanner.File) scanner.Folder {
	return scanner.Folder{
		RootID:       "audiobooks",
		RelativePath: name,
		Files:        append([]scanner.File(nil), files...),
	}
}

func audio(name, title, author string) scanner.File {
	return scanner.File{
		ID:           stableID(name),
		RelativePath: filepath.ToSlash(filepath.Join("Omnibus", name)),
		Name:         name,
		Kind:         scanner.KindAudio,
		Metadata: media.Metadata{
			Title:  title,
			Author: author,
		},
	}
}

func stableID(value string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value))
}
