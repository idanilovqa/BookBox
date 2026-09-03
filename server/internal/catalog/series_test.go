package catalog

import (
	"strings"
	"testing"
)

func TestSeriesSortPaginatesNumerically(t *testing.T) {
	repo, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Ten", folder: "Ten"}, {title: "Two", folder: "Two"}, {title: "One", folder: "One"}, {title: "Side", folder: "Side"}, {title: "Unknown", folder: "Unknown"}, {title: "Standalone", folder: "Standalone"},
	})
	books, err := repo.ListBooks(ctx, BookQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]string{"Ten": "10", "Two": "2", "One": "1", "Side": "2.5", "Unknown": ""}
	for _, book := range books.Items {
		if pos, ok := positions[book.Title]; ok {
			if _, err := repo.FillMissingBookSeries(ctx, book.ID, book.UpdatedAt, "Example Series", pos); err != nil {
				t.Fatal(err)
			}
		}
	}
	projection, err := repo.LoadSearchProjection(ctx, "audiobooks", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.PublishSearchProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	var actual []string
	q := BookQuery{Sort: "series", Limit: 2}
	for {
		p, err := repo.ListBooks(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range p.Items {
			actual = append(actual, b.Title)
		}
		if p.NextCursor == "" {
			break
		}
		q.Cursor = p.NextCursor
	}
	if got := strings.Join(actual, ","); got != "One,Two,Side,Ten,Unknown,Standalone" {
		t.Fatal(got)
	}
	actual = nil
	sq := SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Sort: "series", Limit: 2}
	for {
		p, err := repo.SearchLibrary(ctx, sq)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range p.Groups {
			for _, d := range g.Documents {
				actual = append(actual, d.Title)
			}
		}
		if p.NextCursor == "" {
			break
		}
		sq.Cursor = p.NextCursor
	}
	if got := strings.Join(actual, ","); got != "One,Two,Side,Ten,Unknown,Standalone" {
		t.Fatal(got)
	}
}
func TestSeriesBackfillPreservesExistingValuesAndRejectsStaleBook(t *testing.T) {
	repo, ctx := searchPaginationFixture(t, []searchBookDefinition{{title: "Book", folder: "Book"}})
	page, err := repo.ListBooks(ctx, BookQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	book := page.Items[0]
	changed, err := repo.FillMissingBookSeries(ctx, book.ID, book.UpdatedAt, "Original", "2")
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	changed, err = repo.FillMissingBookSeries(ctx, book.ID, book.UpdatedAt, "Wrong", "9")
	if err != nil || changed {
		t.Fatalf("stale update %v %v", changed, err)
	}
	detail, err := repo.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = repo.FillMissingBookSeries(ctx, book.ID, detail.Book.UpdatedAt, "Wrong", "9")
	if err != nil || changed {
		t.Fatalf("overwrite %v %v", changed, err)
	}
}

func TestSeriesBackfillPublishesSearchAndInvalidatesCursor(t *testing.T) {
	repo, ctx := searchPaginationFixture(t, []searchBookDefinition{{title: "One", folder: "One"}, {title: "Two", folder: "Two"}})
	before, err := repo.SearchLibrary(ctx, SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Sort: "series", Limit: 1})
	if err != nil || before.NextCursor == "" {
		t.Fatalf("initial page: %v %+v", err, before)
	}
	oldProjection, err := repo.LoadSearchProjection(ctx, "audiobooks", 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListBooks(ctx, BookQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repo.FillMissingBookSeries(ctx, page.Items[0].ID, page.Items[0].UpdatedAt, "Unique Saga", "1"); err != nil || !changed {
		t.Fatalf("fill: %v %v", changed, err)
	}
	if err := repo.PublishSearchProjection(ctx, oldProjection); err != ErrConflict {
		t.Fatalf("old projection accepted: %v", err)
	}
	if err := repo.QueuePendingSeriesProjection(ctx, "audiobooks"); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := repo.pool.QueryRow(ctx, "select count(*) from worker_jobs where kind='publish_search' and status='queued'").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("publication jobs %d %v", jobs, err)
	}
	projection, err := repo.LoadSearchProjection(ctx, "audiobooks", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PublishSearchProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	found, err := repo.SearchLibrary(ctx, SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Text: "Unique Saga", Limit: 10})
	if err != nil || len(found.Groups) != 1 {
		t.Fatalf("series missing from search: %+v %v", found, err)
	}
	_, err = repo.SearchLibrary(ctx, SearchQuery{RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks, Sort: "series", Limit: 1, Cursor: before.NextCursor})
	if err != ErrInvalidPage {
		t.Fatalf("stale series cursor accepted: %v", err)
	}
}

func TestSeriesBackfillBookDetailIncludesFileKind(t *testing.T) {
	repo, ctx := searchPaginationFixture(t, []searchBookDefinition{{title: "Reaper Man", folder: "Discworld/11 Reaper Man"}})
	page, err := repo.ListBooks(ctx, BookQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := repo.GetBook(ctx, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Files) != 1 || detail.Files[0].FileKind != FileKindAudio {
		t.Fatalf("audio classification missing: %+v", detail.Files)
	}
}
