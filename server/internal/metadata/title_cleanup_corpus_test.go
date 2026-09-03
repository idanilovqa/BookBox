package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestUserTitleCleanupCorpus(t *testing.T) {
	var cases []struct {
		Number          int
		Title           string
		Authors         []string
		Folder          string
		Expected        string
		ExpectedAuthors []string
	}
	raw, err := os.ReadFile("testdata/user_title_cases_144_207.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 64 {
		t.Fatalf("missing cases: %d", len(cases))
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.Number), func(t *testing.T) {
			local := catalog.InferredBook{Title: c.Title, Authors: c.Authors, SourceFolder: c.Folder}
			provider := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if c.Expected == "" {
					return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "one", Title: "Skyward", Authors: c.ExpectedAuthors}, {Provider: ProviderOpenLibrary, ProviderID: "two", Title: "Starsight", Authors: c.ExpectedAuthors}}, nil
				}
				if normalizeText(q.Title, false) != normalizeText(c.Expected, false) {
					return nil, nil
				}
				candidate := Candidate{Provider: ProviderOpenLibrary, ProviderID: "work", Title: c.Expected, Authors: c.ExpectedAuthors}
				if c.Number == 172 || c.Number == 173 {
					candidate.Series = "Skyward Flight"
					candidate.SeriesPosition = "1"
				}
				return []Candidate{candidate}, nil
			})
			d, err := NewMatcher(provider).Match(t.Context(), local)
			if err != nil {
				t.Fatal(err)
			}
			if c.Expected == "" {
				if d.Selected != nil {
					t.Fatal("series without number picked a book")
				}
				return
			}
			if d.Selected == nil {
				t.Fatalf("not identified: %q -> %q; variants=%v", c.Title, c.Expected, localSearchTitles(local))
			}
		})
	}
}

func TestTitleCleanupPreservesAmbiguityAndNumbers(t *testing.T) {
	for _, tc := range []struct{ title, folder string }{
		{"1984", "George Orwell"}, {"Catch-22", "Joseph Heller"}, {"The 39 Steps", "John Buchan"},
		{"Michael Crichton  Another Book 01 (1969)", "Michael Crichton/1969 - The Andromeda Strain [Unabridged]"},
		{"The Dark Tower IIV: Another Book", "Stephen King"},
	} {
		local := catalog.InferredBook{Title: tc.title, SourceFolder: tc.folder}
		for _, v := range TitleCleanupInterpretations(local) {
			if v.Rule == "track-title-agrees-with-book-folder" || v.Rule == "roman-series-number" {
				t.Fatalf("unsafe interpretation: %+v", v)
			}
		}
	}
	b := catalog.InferredBook{Title: "The Dark Tower VI: Song Of Susannah", Authors: []string{"Stephen King"}}
	s, p := InferSeriesIdentity(b)
	if s != "The Dark Tower" || p != "6" {
		t.Fatalf("lost Roman position: %s %s", s, p)
	}
	b = catalog.InferredBook{Title: "Mort (#4)", Authors: []string{"Terry Pratchett"}, SourceFolder: "Terry Pratchett/Discworld"}
	s, p = InferSeriesIdentity(b)
	if s != "Discworld" || p != "4" {
		t.Fatalf("lost suffix position: %s %s", s, p)
	}
}

func TestSeriesFolderAliasRequiresProviderOrdinal(t *testing.T) {
	local := catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}, SourceFolder: "Brandon Sanderson/Brandon Sanderson - The Knights of Crystallia"}
	for _, position := range []string{"", "3"} {
		provider := queryProvider(func(context.Context, Query) ([]Candidate, error) {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "third", Title: "The Knights of Crystallia", Authors: local.Authors, Series: "Alcatraz", SeriesPosition: position}}, nil
		})
		d, err := NewMatcher(provider).Match(t.Context(), local)
		if err != nil || d.Selected != nil {
			t.Fatalf("folder borrowed wrong ordinal: %+v %v", d, err)
		}
	}
	s, p := InferSeriesIdentity(catalog.InferredBook{Title: "Skyward: The Skyward Series, Book 1 (USA)"})
	if s != "The Skyward Series" || p != "1" {
		t.Fatalf("market label lost series: %s %s", s, p)
	}
}

func TestParenthesizedMarketSeries(t *testing.T) {
	s, p := InferSeriesIdentity(catalog.InferredBook{Title: "Skyward (The Skyward Series Book 1) (USA)"})
	if s != "The Skyward Series" || p != "1" {
		t.Fatalf("lost parenthesized series: %s %s", s, p)
	}
}
