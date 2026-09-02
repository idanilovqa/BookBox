package catalog

import (
	"strings"
	"testing"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestSearchSpellingMatchUsesBoundedWholeWordsAndPhrases(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := t.Context()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query, terms string
		want         bool
	}{
		{"sandyrsen", "brandon sanderson oathbringer", true},
		{"sandreson", "brandon sanderson oathbringer", true},
		{"dunemessaih", "frank herbert dune messiah", true},
		{"dune", "dunx", true},
		{"xyqtokem", "xyqtoken000001.m4b shelf/xyqtoken000001.m4b", true},
		{"sanderson", "jefferson mays", false},
		{"sanderson", "sandstorm", false},
		{"sanderson", "reasons and persons", false},
		{"sanderson", "", false},
		{"ab", "abc", false},
		{strings.Repeat("x", 256), strings.Repeat("x", 256), false},
		{"sanderson", strings.Repeat("x", 400) + " sanderson", true},
	} {
		var got bool
		if err := pool.QueryRow(ctx, `select catalog_search_spelling_match($1,$2)`, test.query, test.terms).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("spelling match(%q, %q)=%v, want %v", test.query, test.terms, got, test.want)
		}
	}
}

func TestSearchLibraryRejectsSharedSyllablesButKeepsSpellingErrors(t *testing.T) {
	repository, ctx := searchPaginationFixture(t, []searchBookDefinition{
		{title: "Oathbringer", folder: "Brandon Sanderson/Oathbringer", author: "Brandon Sanderson"},
		{title: "Caliban's War", folder: "James Corey/Caliban's War", author: "James Corey", narrator: "Jefferson Mays"},
		{title: "Sandstorm", folder: "James Rollins/Sandstorm", author: "James Rollins"},
		{title: "Best Served Cold", folder: "Joe Abercrombie/Standalones", author: "Joe Abercrombie"},
		{title: "Reasons and Persons", folder: "Non Fiction/Reasons and Persons", author: "Derek Parfit"},
	})
	for _, query := range []string{"sanderson", "sandyrsen", "sandreson", "oathbrnger"} {
		t.Run(query, func(t *testing.T) {
			page, err := repository.SearchLibrary(ctx, SearchQuery{
				RootIDs: []string{"audiobooks"}, Mode: SearchModeAudiobooks,
				Text: query, Sort: BookSortTitle, Limit: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Groups) != 1 || len(page.Groups[0].Documents) != 1 ||
				page.Groups[0].Documents[0].Title != "Oathbringer" || page.NextCursor != "" {
				t.Fatalf("query %q returned unrelated matches: %#v", query, page)
			}
		})
	}
}
