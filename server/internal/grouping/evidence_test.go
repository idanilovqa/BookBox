package grouping

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
)

func TestPlaceholderTrackTitleRejectsCountersButKeepsNumberedBooks(t *testing.T) {
	for _, value := range []string{"01/16", "- 01/16", "Track 01 of 16", "track 9/10"} {
		if !placeholderTrackTitle(value) {
			t.Errorf("placeholderTrackTitle(%q)=false", value)
		}
	}
	for _, value := range []string{"1984", "11/22/63", "Book 2", "Chapter 1 - Stormblessed"} {
		if placeholderTrackTitle(value) {
			t.Errorf("placeholderTrackTitle(%q)=true", value)
		}
	}
}

func TestInferTreeGroupsCounterTitledCollectionByFolder(t *testing.T) {
	names := []string{
		"Preface", "The Selish System", "The Hope of Elantris", "The Scadrian System",
		"The Eleventh Metal", "Allomancer Jak", "The Taldain System", "White Sand",
		"The Threnodite System", "Shadows for Silence", "The Drominad System", "Sixth of the Dusk",
		"The Rosharan System", "The Way of Kings Prime", "Edgedancer Prologue", "Edgedancer",
	}
	files := make([]catalog.SourceFile, 0, len(names))
	for index, name := range names {
		track := index + 1
		files = append(files, treeFile(
			fmt.Sprintf("Brandon Sanderson/Arcanum Unbounded/%d - %s.mp3", track, name),
			catalog.FileKindAudio,
			"",
			map[string]any{
				"title": fmt.Sprintf("- %02d/16", track),
				"track": strconv.Itoa(track),
			},
		))
	}
	tree := testTree("Brandon Sanderson/Arcanum Unbounded", files...)
	result := InferTree(tree)

	if len(result.Books) != 1 {
		t.Fatalf("books=%#v, want one collection", result.Books)
	}
	book := result.Books[0]
	if book.Title != "Arcanum Unbounded" || book.ReviewReason != "folder_default" {
		t.Fatalf("collection=%#v", book)
	}
	if got := audioPathsForBook(tree, book); len(got) != 16 {
		t.Fatalf("audio paths=%v, want 16", got)
	}
	if evidence := result.Evidence[book.GroupingKey]; evidence.ReasonCode != "folder_default" || len(evidence.Values[catalog.FileRoleAudio]) != 16 {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func TestInferTreeGroupsZeroBasedCourseLecturesByFolder(t *testing.T) {
	files := make([]catalog.SourceFile, 0, 25)
	for number := 0; number < 25; number++ {
		file := treeFile(
			fmt.Sprintf("The Great Courses/TTC - Art of Reading/%02d. Lecture.mp3", number),
			catalog.FileKindAudio, "", map[string]any{},
		)
		if number == 7 {
			file.ProbeStatus = catalog.ProbeFailed
			file.ProbeError = "damaged tag"
		}
		files = append(files, file)
	}
	result := InferTree(testTree("The Great Courses/TTC - Art of Reading", files...))
	if len(result.Books) != 1 || result.Books[0].Title != "TTC - Art of Reading" ||
		len(result.Books[0].SourceFileIDs) != 25 || result.Books[0].ReviewReason != "probe_failed" {
		t.Fatalf("course=%#v", result.Books)
	}
}

func TestInferTreeDoesNotMergeZeroBasedSequenceContainingStandaloneM4B(t *testing.T) {
	zero := treeFile("Shared/00. Introduction.mp3", catalog.FileKindAudio, "", map[string]any{})
	one := treeFile("Shared/01. Full Book.m4b", catalog.FileKindAudio, "", map[string]any{})
	result := InferTree(testTree("Shared", zero, one))
	if len(result.Books) == 1 && result.Books[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("unsafe mixed sequence merge: %#v", result.Books)
	}
}

func TestInferTreeDoesNotAutoConfirmCounterSequenceWithConflictingAuthors(t *testing.T) {
	first := treeFile("Collection/1 - First.mp3", catalog.FileKindAudio, "", map[string]any{
		"title": "01/02", "track": "1", "author": "First Author",
	})
	second := treeFile("Collection/2 - Second.mp3", catalog.FileKindAudio, "", map[string]any{
		"title": "02/02", "track": "2", "author": "Second Author",
	})
	result := InferTree(testTree("Collection", first, second))
	if len(result.Books) != 1 || result.Books[0].ReviewReason != "conflicting_author" ||
		result.Books[0].ReviewState != catalog.ReviewNeedsReview {
		t.Fatalf("books=%#v", result.Books)
	}
}

func TestInferTreeMarksCorruptedIdentityTagsForReview(t *testing.T) {
	first := treeFile("Book/01.mp3", catalog.FileKindAudio, "", map[string]any{
		"album": "N. A. Kun", "author": "Legends and Myths",
	})
	second := treeFile("Book/02.mp3", catalog.FileKindAudio, "", map[string]any{
		"album": "tr", "author": "tyh",
	})
	tree := testTree("Book", first, second)
	tree.Corrections = []catalog.GroupingCorrection{{
		ID: uuid.New(), RootID: tree.RootID, SourceFolder: tree.ScopePath,
		Groups: []catalog.GroupingGroup{{GroupingKey: "book", FileIDs: []uuid.UUID{first.ID, second.ID}}},
	}}
	result := InferTree(tree)
	if len(result.Books) != 1 || result.Books[0].ReviewState != catalog.ReviewConfirmed ||
		result.Books[0].ReviewReason != "corrupted_metadata" {
		t.Fatalf("books=%#v", result.Books)
	}
	if evidence := result.Evidence[result.Books[0].GroupingKey]; evidence.ReasonCode != "corrupted_metadata" {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func TestInferTreeKeepsCounterNamedStandaloneM4BsSeparate(t *testing.T) {
	first := treeFile("Shared/1 - First.m4b", catalog.FileKindAudio, "", map[string]any{
		"title": "01/02", "track": "1",
	})
	second := treeFile("Shared/2 - Second.m4b", catalog.FileKindAudio, "", map[string]any{
		"title": "02/02", "track": "2",
	})
	result := InferTree(testTree("Shared", first, second))
	if len(result.Books) != 2 {
		t.Fatalf("books=%#v, want two standalone M4Bs", result.Books)
	}
}

func TestInferTreeAssignsTitleNamedCoverOnlyToMatchingSharedBook(t *testing.T) {
	dune := treeAudio("Shared/Dune.m4b", "Dune", "Frank Herbert")
	foundation := treeAudio("Shared/Foundation.m4b", "Foundation", "Isaac Asimov")
	duneCover := treeFile("Shared/Dune.jpg", catalog.FileKindCover, "", nil)
	logo := treeFile("Shared/publisher-logo.png", catalog.FileKindCover, "", nil)
	duneCover.Media = map[string]any{"width": 600, "height": 900}
	logo.Media = map[string]any{"width": 600, "height": 900}
	result := InferTree(testTree("Shared", dune, foundation, duneCover, logo))

	duneBook := treeBookByTitle(t, result.Books, "Dune")
	if duneBook.FileRoles[duneCover.ID] != catalog.FileRoleCover ||
		duneBook.FileRoles[logo.ID] != "" {
		t.Fatalf("Dune cover roles=%#v", duneBook.FileRoles)
	}
	foundationBook := treeBookByTitle(t, result.Books, "Foundation")
	if foundationBook.FileRoles[duneCover.ID] != "" || foundationBook.FileRoles[logo.ID] != "" {
		t.Fatalf("Foundation cover roles=%#v", foundationBook.FileRoles)
	}
}

func TestInferTreeRejectsInvalidTinyAndLandscapeCoverCandidates(t *testing.T) {
	for _, test := range []struct {
		name  string
		media map[string]any
	}{
		{name: "invalid", media: nil},
		{name: "tiny", media: map[string]any{"width": 80, "height": 120}},
		{name: "landscape", media: map[string]any{"width": 1400, "height": 600}},
	} {
		t.Run(test.name, func(t *testing.T) {
			audio := treeAudio("Dune/Dune.m4b", "Dune", "Frank Herbert")
			candidate := treeFile("Dune/Dune.jpg", catalog.FileKindCover, "", test.media)
			result := InferTree(testTree("Dune", audio, candidate))
			book := treeBookByTitle(t, result.Books, "Dune")
			if book.FileRoles[candidate.ID] != "" {
				t.Fatalf("cover role=%q", book.FileRoles[candidate.ID])
			}
		})
	}
}

func TestInferTreeApprovedCases(t *testing.T) {
	tests := []struct {
		name string
		tree Tree
		want []expectedTreeBook
	}{
		{
			name: "dedicated chapter folder",
			tree: testTree("Author/Book",
				treeAudio("Author/Book/01 Book.mp3", "Book", "Author"),
				treeAudio("Author/Book/02 Book.mp3", "Book", "Author"),
			),
			want: []expectedTreeBook{{
				title: "Book", authors: []string{"Author"}, review: catalog.ReviewUnreviewed,
				reason: "folder_default", audioPaths: []string{
					"Author/Book/01 Book.mp3", "Author/Book/02 Book.mp3",
				},
			}},
		},
		{
			name: "common album keeps distinct chapter titles together",
			tree: testTree("Brandon Sanderson/Words of Radiance",
				treeChapterAudio("Brandon Sanderson/Words of Radiance/Chapter 1 - Stormblessed.mp3", "Words of Radiance", "Chapter 1", "Brandon Sanderson"),
				treeChapterAudio("Brandon Sanderson/Words of Radiance/Chapter 2 - Bridge Four.mp3", "Words of Radiance", "Chapter 2", "Brandon Sanderson"),
			),
			want: []expectedTreeBook{{
				title: "Words of Radiance", authors: []string{"Brandon Sanderson"}, review: catalog.ReviewUnreviewed,
				reason: "folder_default", audioPaths: []string{
					"Brandon Sanderson/Words of Radiance/Chapter 1 - Stormblessed.mp3", "Brandon Sanderson/Words of Radiance/Chapter 2 - Bridge Four.mp3",
				},
			}},
		},
		{
			name: "disc subfolders combine",
			tree: testTree("Author/Book",
				treeAudio("Author/Book/Disc 1/01.mp3", "Book", "Author"),
				treeAudio("Author/Book/Disk 2/01.mp3", "Book", "Author"),
			),
			want: []expectedTreeBook{{
				title: "Book", authors: []string{"Author"}, review: catalog.ReviewUnreviewed,
				reason: "consistent_subtree", audioPaths: []string{
					"Author/Book/Disc 1/01.mp3", "Author/Book/Disk 2/01.mp3",
				},
			}},
		},
		{
			name: "strong mixed folder splits all eight recognized audio formats",
			tree: testTree("Mixed",
				treeAudio("Mixed/Dune 01.m4b", "Dune", "Frank Herbert"),
				treeAudio("Mixed/Dune 02.m4a", "Dune", "Frank Herbert"),
				treeAudio("Mixed/Dune 03.mp3", "Dune", "Frank Herbert"),
				treeAudio("Mixed/Dune 04.aac", "Dune", "Frank Herbert"),
				treeAudio("Mixed/Foundation 01.flac", "Foundation", "Isaac Asimov"),
				treeAudio("Mixed/Foundation 02.ogg", "Foundation", "Isaac Asimov"),
				treeAudio("Mixed/Foundation 03.opus", "Foundation", "Isaac Asimov"),
				treeAudio("Mixed/Foundation 04.wav", "Foundation", "Isaac Asimov"),
			),
			want: []expectedTreeBook{
				{title: "Dune", authors: []string{"Frank Herbert"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Mixed/Dune 01.m4b", "Mixed/Dune 02.m4a", "Mixed/Dune 03.mp3", "Mixed/Dune 04.aac"}},
				{title: "Foundation", authors: []string{"Isaac Asimov"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Mixed/Foundation 01.flac", "Mixed/Foundation 02.ogg", "Mixed/Foundation 03.opus", "Mixed/Foundation 04.wav"}},
			},
		},
		{
			name: "failed probe remains isolated from strong subtree identities",
			tree: testTree("Incoming",
				treeAudio("Incoming/Dune/01.m4b", "Dune", "Frank Herbert"),
				treeAudio("Incoming/Dune/02.m4a", "Dune", "Frank Herbert"),
				treeAudio("Incoming/Foundation/01.flac", "Foundation", "Isaac Asimov"),
				treeAudio("Incoming/Foundation/02.ogg", "Foundation", "Isaac Asimov"),
				treeFailedAudio("Incoming/Unresolved/broken.opus"),
			),
			want: []expectedTreeBook{
				{title: "Dune", authors: []string{"Frank Herbert"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Incoming/Dune/01.m4b", "Incoming/Dune/02.m4a"}},
				{title: "Foundation", authors: []string{"Isaac Asimov"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Incoming/Foundation/01.flac", "Incoming/Foundation/02.ogg"}},
				{title: "Incoming", review: catalog.ReviewNeedsReview, reason: "probe_failed", audioPaths: []string{"Incoming/Unresolved/broken.opus"}},
			},
		},
		{
			name: "failed probes retain strong filename clusters",
			tree: testTree("Incoming",
				treeFailedAudio("Incoming/Dune - 01.mp3"),
				treeFailedAudio("Incoming/Dune - 02.m4b"),
				treeFailedAudio("Incoming/Foundation - 01.flac"),
				treeFailedAudio("Incoming/Foundation - 02.opus"),
			),
			want: []expectedTreeBook{
				{title: "Dune", review: catalog.ReviewNeedsReview, reason: "probe_failed", audioPaths: []string{"Incoming/Dune - 01.mp3", "Incoming/Dune - 02.m4b"}},
				{title: "Foundation", review: catalog.ReviewNeedsReview, reason: "probe_failed", audioPaths: []string{"Incoming/Foundation - 01.flac", "Incoming/Foundation - 02.opus"}},
			},
		},
		{
			name: "distinct m4b recordings split",
			tree: testTree("Omnibus",
				treeAudio("Omnibus/Dune.m4b", "Dune", "Frank Herbert"),
				treeAudio("Omnibus/Foundation.m4b", "Foundation", "Isaac Asimov"),
			),
			want: []expectedTreeBook{
				{title: "Dune", authors: []string{"Frank Herbert"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Omnibus/Dune.m4b"}},
				{title: "Foundation", authors: []string{"Isaac Asimov"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Omnibus/Foundation.m4b"}},
			},
		},
		{
			name: "weak loose root files remain visible",
			tree: testTree("",
				treeUnprobedAudio("Track 01.mp3"),
				treeUnprobedAudio("Track 02.mp3"),
			),
			want: []expectedTreeBook{{
				review: catalog.ReviewNeedsReview, reason: "weak_loose_files",
				audioPaths: []string{"Track 01.mp3", "Track 02.mp3"},
			}},
		},
		{
			name: "failed probe remains visible",
			tree: testTree("Book", treeFailedAudio("Book/01.mp3")),
			want: []expectedTreeBook{{
				title: "Book", review: catalog.ReviewNeedsReview, reason: "probe_failed",
				audioPaths: []string{"Book/01.mp3"},
			}},
		},
		{
			name: "conflicting title remains together for review",
			tree: testTree("Mixed",
				treeAudio("Mixed/Dune.mp3", "Foundation", "Shared Author"),
				treeAudio("Mixed/Foundation.mp3", "Dune", "Shared Author"),
			),
			want: []expectedTreeBook{{
				authors: []string{"Shared Author"}, review: catalog.ReviewNeedsReview, reason: "conflicting_title",
				audioPaths: []string{"Mixed/Dune.mp3", "Mixed/Foundation.mp3"},
			}},
		},
		{
			name: "conflicting author remains together for review",
			tree: testTree("Book",
				treeAudio("Book/01.mp3", "Book", "First Author"),
				treeAudio("Book/02.mp3", "Book", "Second Author"),
			),
			want: []expectedTreeBook{{
				title: "Book", review: catalog.ReviewNeedsReview, reason: "conflicting_author",
				audioPaths: []string{"Book/01.mp3", "Book/02.mp3"},
			}},
		},
		{
			name: "generic parent does not erase strong identities",
			tree: testTree("Incoming/Unknown",
				treeAudio("Incoming/Unknown/a.mp3", "Dune", "Frank Herbert"),
				treeAudio("Incoming/Unknown/b.wav", "Foundation", "Isaac Asimov"),
			),
			want: []expectedTreeBook{
				{title: "Dune", authors: []string{"Frank Herbert"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Incoming/Unknown/a.mp3"}},
				{title: "Foundation", authors: []string{"Isaac Asimov"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Incoming/Unknown/b.wav"}},
			},
		},
		{
			name: "common filename stems form strong chapter clusters",
			tree: testTree("Uploads",
				treeUnprobedAudio("Uploads/Dune - 01.mp3"),
				treeUnprobedAudio("Uploads/Dune - 02.mp3"),
				treeUnprobedAudio("Uploads/Foundation - 01.mp3"),
				treeUnprobedAudio("Uploads/Foundation - 02.mp3"),
			),
			want: []expectedTreeBook{
				{title: "Dune", review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Uploads/Dune - 01.mp3", "Uploads/Dune - 02.mp3"}},
				{title: "Foundation", review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Uploads/Foundation - 01.mp3", "Uploads/Foundation - 02.mp3"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertExpectedTreeBooks(t, test.tree, InferTree(test.tree), test.want)
		})
	}
}

func TestInferTreeUsesStoredCompanionEvidenceAndSharesCovers(t *testing.T) {
	cover := treeFile("Mixed/cover.jpg", catalog.FileKindCover, "", nil)
	cover.Media = map[string]any{"width": 600, "height": 900}
	ebook := treeFile("Mixed/Dune.epub", catalog.FileKindCompanion, catalog.CompanionEbook, nil)
	cue := treeFile("Mixed/books.cue", catalog.FileKindCompanion, catalog.CompanionCue, map[string]any{
		"references": []string{"dune-01.mp3"}, "title": "Dune", "author": "Frank Herbert",
	})
	nfo := treeFile("Mixed/foundation.nfo", catalog.FileKindCompanion, catalog.CompanionNFO, map[string]any{
		"title": "Foundation", "author": "Isaac Asimov",
	})
	opf := treeFile("Mixed/dune.opf", catalog.FileKindCompanion, catalog.CompanionOPF, map[string]any{
		"title": "Dune", "authors": []string{"Frank Herbert"},
	})
	playlist := treeFile("Mixed/foundation.m3u", catalog.FileKindCompanion, catalog.CompanionPlaylist, map[string]any{
		"references": []string{"foundation-01.mp3"},
	})
	tree := testTree("Mixed",
		treeUnprobedAudio("Mixed/dune-01.mp3"),
		treeUnprobedAudio("Mixed/foundation-01.mp3"),
		cover,
		cue,
		nfo,
		opf,
		playlist,
		ebook,
	)

	result := InferTree(tree)
	assertExpectedTreeBooks(t, tree, result, []expectedTreeBook{
		{title: "Dune", authors: []string{"Frank Herbert"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Mixed/dune-01.mp3"}, coverPaths: []string{"Mixed/cover.jpg"}, sidecarPaths: []string{"Mixed/books.cue", "Mixed/dune.opf", "Mixed/Dune.epub"}},
		{title: "Foundation", authors: []string{"Isaac Asimov"}, review: catalog.ReviewUnreviewed, reason: "distinct_identity", audioPaths: []string{"Mixed/foundation-01.mp3"}, coverPaths: []string{"Mixed/cover.jpg"}, sidecarPaths: []string{"Mixed/foundation.m3u", "Mixed/foundation.nfo"}},
	})

	for _, book := range result.Books {
		if book.FileRoles[cover.ID] != catalog.FileRoleCover {
			t.Errorf("book %q cover role = %q, want %q", book.Title, book.FileRoles[cover.ID], catalog.FileRoleCover)
		}
	}
	dune := treeBookByTitle(t, result.Books, "Dune")
	for _, companion := range []catalog.SourceFile{cue, opf, ebook} {
		if dune.FileRoles[companion.ID] != catalog.FileRoleSidecar {
			t.Errorf("Dune companion %q role = %q, want %q", companion.RelativePath, dune.FileRoles[companion.ID], catalog.FileRoleSidecar)
		}
	}
	foundation := treeBookByTitle(t, result.Books, "Foundation")
	for _, companion := range []catalog.SourceFile{nfo, playlist} {
		if foundation.FileRoles[companion.ID] != catalog.FileRoleSidecar {
			t.Errorf("Foundation companion %q role = %q, want %q", companion.RelativePath, foundation.FileRoles[companion.ID], catalog.FileRoleSidecar)
		}
	}
}

func TestInferTreeAppliesCorrectionsFirstAndLocksAssignments(t *testing.T) {
	lockedID := uuid.New()
	first := treeAudio("Shelf/Dune 01.mp3", "Dune", "Frank Herbert")
	second := treeAudio("Shelf/Dune 02.mp3", "Dune", "Frank Herbert")
	newFile := treeAudio("Shelf/Foundation.mp3", "Foundation", "Isaac Asimov")
	tree := testTree("Shelf", first, second, newFile)
	tree.Corrections = []catalog.GroupingCorrection{{
		ID:           uuid.New(),
		RootID:       tree.RootID,
		SourceFolder: tree.ScopePath,
		Groups: []catalog.GroupingGroup{{
			BookID: &lockedID, GroupingKey: "manual-dune", FileIDs: []uuid.UUID{first.ID, second.ID},
		}},
	}}

	result := InferTree(tree)
	if len(result.Books) != 2 {
		t.Fatalf("books = %#v, want locked correction plus provisional unassigned book", result.Books)
	}
	locked := treeBookByGroupingKey(t, result.Books, "manual-dune")
	if !locked.GroupingLocked || locked.ReviewState != catalog.ReviewConfirmed {
		t.Errorf("locked correction = %#v, want locked confirmed book", locked)
	}
	if got := audioPathsForBook(tree, locked); !equalStrings(got, []string{"Shelf/Dune 01.mp3", "Shelf/Dune 02.mp3"}) {
		t.Errorf("locked audio paths = %v", got)
	}
	if evidence := result.Evidence[locked.GroupingKey]; evidence.ReasonCode != "manual" || evidence.ReviewState != catalog.ReviewConfirmed {
		t.Errorf("locked correction evidence = %#v, want manual confirmed evidence", evidence)
	}

	provisional := treeBookContaining(t, tree, result.Books, newFile.ID)
	if provisional.GroupingLocked || provisional.ReviewState != catalog.ReviewNeedsReview || provisional.ReviewReason != "weak_loose_files" {
		t.Errorf("new-file provisional book = %#v", provisional)
	}
	assertEveryActiveAudioExactlyOnce(t, tree, result.Books)
	if treeBookContaining(t, tree, result.Books, newFile.ID).GroupingKey == "manual-dune" {
		t.Fatal("automatic inference expanded a user-locked assignment")
	}
}

type expectedTreeBook struct {
	title        string
	authors      []string
	review       string
	reason       string
	audioPaths   []string
	coverPaths   []string
	sidecarPaths []string
}

func testTree(scope string, files ...catalog.SourceFile) Tree {
	return Tree{RootID: "audiobooks", ScopePath: scope, Files: files}
}

func treeAudio(relativePath, title, author string) catalog.SourceFile {
	return treeFile(relativePath, catalog.FileKindAudio, "", map[string]any{
		"title": title, "album": title, "author": author,
	})
}

func treeChapterAudio(relativePath, album, title, author string) catalog.SourceFile {
	return treeFile(relativePath, catalog.FileKindAudio, "", map[string]any{
		"title": title, "album": album, "author": author,
	})
}

func treeUnprobedAudio(relativePath string) catalog.SourceFile {
	file := treeFile(relativePath, catalog.FileKindAudio, "", nil)
	file.ProbeStatus = catalog.ProbePending
	return file
}

func treeFailedAudio(relativePath string) catalog.SourceFile {
	file := treeUnprobedAudio(relativePath)
	file.ProbeStatus = catalog.ProbeFailed
	file.ProbeError = "ffprobe failed"
	return file
}

func treeFile(relativePath, fileKind, companionKind string, media map[string]any) catalog.SourceFile {
	probeStatus := catalog.ProbeSucceeded
	if fileKind != catalog.FileKindAudio && media == nil {
		probeStatus = catalog.ProbeNotApplicable
	}
	return catalog.SourceFile{
		ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte(relativePath)),
		RootID:        "audiobooks",
		RelativePath:  relativePath,
		FileKind:      fileKind,
		CompanionKind: companionKind,
		ProbeStatus:   probeStatus,
		Media:         media,
	}
}

func assertExpectedTreeBooks(t *testing.T, tree Tree, result Result, want []expectedTreeBook) {
	t.Helper()
	if len(result.Books) != len(want) {
		t.Fatalf("books = %#v, want %#v", result.Books, want)
	}
	for _, expected := range want {
		var book catalog.InferredBook
		if expected.title != "" {
			book = treeBookByTitle(t, result.Books, expected.title)
		} else {
			book = result.Books[0]
		}
		if !equalStrings(book.Authors, expected.authors) || book.ReviewState != expected.review || book.ReviewReason != expected.reason {
			t.Errorf("book %q = %#v, want authors=%v review=%q reason=%q", expected.title, book, expected.authors, expected.review, expected.reason)
		}
		if got := audioPathsForBook(tree, book); !equalStrings(got, expected.audioPaths) {
			t.Errorf("book %q audio paths = %v, want %v", expected.title, got, expected.audioPaths)
		}
		evidence, ok := result.Evidence[book.GroupingKey]
		if !ok || evidence.ReasonCode != expected.reason || evidence.ReviewState != expected.review {
			t.Errorf("book %q evidence = %#v, found=%v", expected.title, evidence, ok)
		}
		if !equalStrings(evidence.Values[catalog.FileRoleAudio], expected.audioPaths) {
			t.Errorf("book %q audio evidence = %v, want %v", expected.title, evidence.Values[catalog.FileRoleAudio], expected.audioPaths)
		}
		if expected.coverPaths != nil && !equalStrings(evidence.Values[catalog.FileRoleCover], expected.coverPaths) {
			t.Errorf("book %q cover evidence = %v, want %v", expected.title, evidence.Values[catalog.FileRoleCover], expected.coverPaths)
		}
		if expected.sidecarPaths != nil && !equalStrings(evidence.Values[catalog.FileRoleSidecar], expected.sidecarPaths) {
			t.Errorf("book %q sidecar evidence = %v, want %v", expected.title, evidence.Values[catalog.FileRoleSidecar], expected.sidecarPaths)
		}
	}
	assertEveryActiveAudioExactlyOnce(t, tree, result.Books)
}

func assertEveryActiveAudioExactlyOnce(t *testing.T, tree Tree, books []catalog.InferredBook) {
	t.Helper()
	counts := make(map[uuid.UUID]int)
	for _, book := range books {
		for _, fileID := range book.SourceFileIDs {
			if book.FileRoles[fileID] == catalog.FileRoleAudio {
				counts[fileID]++
			}
		}
	}
	for _, file := range tree.Files {
		if file.FileKind == catalog.FileKindAudio && file.MissingAt == nil && counts[file.ID] != 1 {
			t.Errorf("active audio %q assigned %d times, want exactly once", file.RelativePath, counts[file.ID])
		}
	}
}

func audioPathsForBook(tree Tree, book catalog.InferredBook) []string {
	pathsByID := make(map[uuid.UUID]string, len(tree.Files))
	for _, file := range tree.Files {
		pathsByID[file.ID] = file.RelativePath
	}
	result := make([]string, 0)
	for _, fileID := range book.SourceFileIDs {
		if book.FileRoles[fileID] == catalog.FileRoleAudio {
			result = append(result, pathsByID[fileID])
		}
	}
	sort.Strings(result)
	return result
}

func treeBookByTitle(t *testing.T, books []catalog.InferredBook, title string) catalog.InferredBook {
	t.Helper()
	for _, book := range books {
		if book.Title == title {
			return book
		}
	}
	t.Fatalf("no book with title %q in %#v", title, books)
	return catalog.InferredBook{}
}

func treeBookByGroupingKey(t *testing.T, books []catalog.InferredBook, groupingKey string) catalog.InferredBook {
	t.Helper()
	for _, book := range books {
		if book.GroupingKey == groupingKey {
			return book
		}
	}
	t.Fatalf("no book with grouping key %q in %#v", groupingKey, books)
	return catalog.InferredBook{}
}

func treeBookContaining(t *testing.T, tree Tree, books []catalog.InferredBook, fileID uuid.UUID) catalog.InferredBook {
	t.Helper()
	for _, book := range books {
		for _, assignedID := range book.SourceFileIDs {
			if assignedID == fileID && book.FileRoles[fileID] == catalog.FileRoleAudio {
				return book
			}
		}
	}
	t.Fatalf("no book contains active audio %q", path.Base(relativePathForID(tree, fileID)))
	return catalog.InferredBook{}
}

func relativePathForID(tree Tree, fileID uuid.UUID) string {
	for _, file := range tree.Files {
		if file.ID == fileID {
			return file.RelativePath
		}
	}
	return fileID.String()
}

func equalStrings(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
