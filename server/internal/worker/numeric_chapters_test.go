package worker

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/grouping"
	"testing"
	"time"
)

func TestNumericChapterSubtreesGroupAtNamedBook(t *testing.T) {
	a := stagedSourceFile("Wuthering Heights (2008) By Emily Bronte/Wuthering Heights/01/01/01_01_01.mp3", catalog.FileKindAudio, time.Now())
	b := stagedSourceFile("Wuthering Heights (2008) By Emily Bronte/Wuthering Heights/02/01/01_02_01.mp3", catalog.FileKindAudio, time.Now())
	result := inferFullInventoryTree(grouping.Tree{RootID: "audio", Files: []catalog.SourceFile{a, b}}, nil)
	if len(result.Books) != 1 || result.Books[0].Title != "Wuthering Heights" {
		t.Fatalf("chapters became books: %+v", result.Books)
	}
	a.Media = map[string]any{"album": "A Different Book"}
	b.Media = map[string]any{"album": "Another Book"}
	result = inferFullInventoryTree(grouping.Tree{RootID: "audio", Files: []catalog.SourceFile{a, b}}, nil)
	if len(result.Books) != 2 {
		t.Fatal("distinct books merged")
	}
	a.Media = map[string]any{"author": "One Author"}
	b.Media = map[string]any{"author": "Another Author"}
	result = inferFullInventoryTree(grouping.Tree{RootID: "audio", Files: []catalog.SourceFile{a, b}}, nil)
	if len(result.Books) != 2 {
		t.Fatal("conflicting authors merged")
	}
	a.RelativePath = "Library/01/01.mp3"
	b.RelativePath = "Library/02/01.mp3"
	a.Media = nil
	b.Media = nil
	result = inferFullInventoryTree(grouping.Tree{RootID: "audio", Files: []catalog.SourceFile{a, b}}, nil)
	if len(result.Books) != 2 {
		t.Fatal("numbered books merged")
	}
	a.RelativePath = "Author/1984/01.mp3"
	b.RelativePath = "Author/2001/01.mp3"
	a.Media = nil
	b.Media = nil
	result = inferFullInventoryTree(grouping.Tree{RootID: "audio", Files: []catalog.SourceFile{a, b}}, nil)
	if len(result.Books) != 2 {
		t.Fatal("meaningful numeric titles merged")
	}
}
