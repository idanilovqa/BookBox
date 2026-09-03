package worker

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/grouping"
	"bookbox.local/server/internal/scanner"
	"context"
	"github.com/google/uuid"
	"strconv"
	"testing"
)

func TestAuthorScopePreservesArcanumTrackGrouping(t *testing.T) {
	for _, mode := range []string{"scan", "regroup"} {
		t.Run(mode, func(t *testing.T) {
			files := []catalog.SourceFile{}
			for n := 1; n <= 16; n++ {
				name := strconv.Itoa(n) + " - Story.mp3"
				if n == 1 {
					name = "1 - Preface.mp3"
				}
				f := inventoryGroupingAudio("Brandon Sanderson/Arcanum Unbounded/" + name)
				f.Media = map[string]any{"title": "- " + strconv.Itoa(n) + "/16", "track": strconv.Itoa(n)}
				files = append(files, f)
			}
			other := inventoryGroupingAudio("Brandon Sanderson/Elantris/1 - Opening.mp3")
			other.Media = map[string]any{"album": "Elantris", "track": "1"}
			files = append(files, other)
			repo := &stagedRepositoryFake{run: catalog.ScanRun{ID: uuid.New(), RootID: "audiobooks"}, generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 7}, tree: grouping.Tree{RootID: "audiobooks", ScopePath: "Brandon Sanderson", Files: files}}
			var err error
			if mode == "regroup" {
				err = (RegroupScopeHandler{Repo: repo}).Handle(context.Background(), catalog.WorkerJob{Kind: catalog.JobRegroupScope, Payload: map[string]any{"rootId": "audiobooks", "scopePath": "Brandon Sanderson", "catalogGeneration": 7}})
			} else {
				scope, _ := scanner.PathScope("Brandon Sanderson")
				result := scanner.InventoryResult{RootID: "audiobooks", Scope: scope}
				for _, f := range files {
					result.Files = append(result.Files, stagedInventoryFile(f))
				}
				handler := stagedScanHandler(repo, result)
				handler.InferTree = grouping.InferTree
				err = handler.Handle(context.Background(), stagedScanJob("audiobooks", "Brandon Sanderson", false))
			}
			if err != nil {
				t.Fatal(err)
			}
			books := repo.lastRefinement.Books
			if len(books) != 2 {
				t.Fatalf("author scope produced %d books, want Arcanum and Elantris", len(books))
			}
			found := false
			for _, b := range books {
				if b.SourceFolder == "Brandon Sanderson/Arcanum Unbounded" {
					found = true
					if b.Title != "Arcanum Unbounded" || len(b.SourceFileIDs) != 16 {
						t.Fatalf("bad collection: %+v", b)
					}
				}
			}
			if !found {
				t.Fatalf("Arcanum folder lost: %+v", books)
			}
		})
	}
}

func TestInventoryPartitionStaysInsideDiscScope(t *testing.T) {
	scope := "Author/Book/CD1"
	tree := grouping.Tree{RootID: "audiobooks", ScopePath: scope, Files: []catalog.SourceFile{inventoryGroupingAudio(scope + "/1 - Preface.mp3"), inventoryGroupingAudio(scope + "/2 - Chapter.mp3")}}
	for _, b := range inferInventoryTree(tree, grouping.InferTree).Books {
		if b.SourceFolder != scope {
			t.Fatalf("escaped scope: %s", b.SourceFolder)
		}
	}
}

func TestAuthorPartitionPreservesDescendantCorrection(t *testing.T) {
	a := inventoryGroupingAudio("Author/Collection/1 - Preface.mp3")
	b := inventoryGroupingAudio("Author/Collection/2 - Story.mp3")
	c := inventoryGroupingAudio("Author/Other/book.m4b")
	tree := grouping.Tree{RootID: "audiobooks", ScopePath: "Author", Files: []catalog.SourceFile{a, b, c}, Corrections: []catalog.GroupingCorrection{{SourceFolder: "Author/Other", Groups: []catalog.GroupingGroup{{GroupingKey: "manual", FileIDs: []uuid.UUID{c.ID}}}}}}
	result := inferInventoryTree(tree, grouping.InferTree)
	if len(result.Books) != 2 {
		t.Fatalf("got %d books", len(result.Books))
	}
	for _, book := range result.Books {
		if book.SourceFolder == "Author" {
			t.Fatal("sibling correction collapsed author scope")
		}
	}
}

func TestAuthorPartitionPreservesCrossFolderManualGroup(t *testing.T) {
	a := inventoryGroupingAudio("Author/Series/Book1/a.mp3")
	b := inventoryGroupingAudio("Author/Series/Book2/b.mp3")
	c := inventoryGroupingAudio("Author/Other/book.m4b")
	tree := grouping.Tree{RootID: "audiobooks", ScopePath: "Author", Files: []catalog.SourceFile{a, b, c}, Corrections: []catalog.GroupingCorrection{{SourceFolder: "Author/Series", Groups: []catalog.GroupingGroup{{GroupingKey: "manual-cross-folder", FileIDs: []uuid.UUID{a.ID, b.ID}}}}}}
	result := inferInventoryTree(tree, grouping.InferTree)
	if len(result.Books) != 2 {
		t.Fatalf("manual group split into %d books", len(result.Books))
	}
	found := false
	for _, book := range result.Books {
		if book.GroupingKey == "manual-cross-folder" {
			found = true
			if book.SourceFolder != "Author/Series" || len(book.SourceFileIDs) != 2 {
				t.Fatalf("manual group changed: %+v", book)
			}
		}
	}
	if !found {
		t.Fatal("manual assignment dropped")
	}
}

func TestRootBooksIgnoreUnrelatedDescendantCorrection(t *testing.T) {
	a := inventoryGroupingAudio("A.m4b")
	b := inventoryGroupingAudio("B.m4b")
	c := inventoryGroupingAudio("Author/Series/book.m4b")
	tree := grouping.Tree{RootID: "audiobooks", Files: []catalog.SourceFile{a, b, c}, Corrections: []catalog.GroupingCorrection{{SourceFolder: "Author/Series", Groups: []catalog.GroupingGroup{{GroupingKey: "manual", FileIDs: []uuid.UUID{c.ID}}}}}}
	result := inferInventoryTree(tree, grouping.InferTree)
	if len(result.Books) != 3 {
		t.Fatalf("unrelated correction merged root books: %+v", result.Books)
	}
}
