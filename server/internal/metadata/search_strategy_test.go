package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"errors"
	"fmt"
	"testing"
)

type queryProvider func(context.Context, Query) ([]Candidate, error)

func (p queryProvider) Search(ctx context.Context, q Query) ([]Candidate, error) { return p(ctx, q) }
func finalEmpireCandidate() Candidate {
	return Candidate{Provider: ProviderOpenLibrary, ProviderID: "/works/OL1W", Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}, CoverURL: "https://covers.openlibrary.org/b/id/1-L.jpg"}
}
func TestMatcherCleansSequencePrefixBeforeSearch(t *testing.T) {
	var queries []Query
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		if normalizeText(q.Title, false) == "the final empire" {
			return []Candidate{finalEmpireCandidate()}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "M01: tHe final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil || d.Selected.Title != "The Final Empire" {
		t.Fatalf("decision=%+v err=%v queries=%+v", d, err, queries)
	}
	if len(queries) != 1 {
		t.Fatalf("decisive cleaned query should stop fallback: %+v", queries)
	}
}
func TestMatcherFallsBackToAuthorAndDeduplicates(t *testing.T) {
	var authorCalls int
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title == "" && q.Author != "" {
			authorCalls++
			return []Candidate{finalEmpireCandidate(), finalEmpireCandidate(), {Provider: ProviderOpenLibrary, ProviderID: "other", Title: "The Final Empire and Other Novels Collection", Authors: []string{"Brandon Sanderson"}}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "M01: The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "/works/OL1W" || authorCalls != 1 {
		t.Fatalf("decision=%+v err=%v author calls=%d", d, err, authorCalls)
	}
	if len(d.Candidates) != 2 {
		t.Fatalf("duplicate candidates: %+v", d.Candidates)
	}
}
func TestMatcherKeepsHealthyProviderResult(t *testing.T) {
	broken := queryProvider(func(context.Context, Query) ([]Candidate, error) { return nil, errors.New("offline") })
	healthy := queryProvider(func(context.Context, Query) ([]Candidate, error) { return []Candidate{finalEmpireCandidate()}, nil })
	d, err := NewMatcher(broken, healthy).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestMatcherDoesNotAutoSelectSequelFromAuthorResults(t *testing.T) {
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title == "" {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "sequel", Title: "The Final Empire Returns", Authors: []string{"Brandon Sanderson"}}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected != nil {
		t.Fatalf("unsafe selection=%+v err=%v", d, err)
	}
}

func TestMatcherAuthorFallbackBoundsReviewResponse(t *testing.T) {
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title != "" {
			return nil, nil
		}
		candidates := []Candidate{finalEmpireCandidate()}
		for i := 0; i < 40; i++ {
			candidates = append(candidates, Candidate{Provider: ProviderOpenLibrary, ProviderID: fmt.Sprint(i), Title: fmt.Sprint("Unrelated book ", i), Authors: []string{"Brandon Sanderson"}})
		}
		return candidates, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil || len(d.Candidates) > 20 {
		t.Fatalf("selected=%v count=%d err=%v", d.Selected, len(d.Candidates), err)
	}
}
func TestMatcherEquivalentProviderEditionsDoNotBlockAutomaticMatch(t *testing.T) {
	first, second := finalEmpireCandidate(), finalEmpireCandidate()
	second.Provider = ProviderGoogleBooks
	second.ProviderID = "google-edition"
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) { return []Candidate{first, second}, nil })
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("same work should not be ambiguous: %+v err=%v", d, err)
	}
}
func TestMatcherRequiresAuthorEvidenceWhenKnown(t *testing.T) {
	c := finalEmpireCandidate()
	c.Authors = nil
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) { return []Candidate{c}, nil })
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected != nil {
		t.Fatalf("missing author should not auto apply: %+v err=%v", d, err)
	}
}

func TestMatcherRetriesPartialProviderOutageWithoutDecisiveMatch(t *testing.T) {
	broken := queryProvider(func(context.Context, Query) ([]Candidate, error) { return nil, errors.New("offline") })
	healthy := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderGoogleBooks, ProviderID: "unrelated", Title: "Another Book", Authors: []string{"Brandon Sanderson"}}}, nil
	})
	_, err := NewMatcher(broken, healthy).Match(t.Context(), catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}})
	if err == nil {
		t.Fatal("partial outage with no decisive match must retry")
	}
}

func TestSearchTitleCleansBracketedSequencePrefixes(t *testing.T) {
	for _, title := range []string{"(#11) Reaper Man", "(# 11) Reaper Man", "[#11] Reaper Man", "(11) Reaper Man", "[11] Reaper Man", "#11 Reaper Man"} {
		t.Run(title, func(t *testing.T) {
			if got := SearchTitle(title); got != "Reaper Man" {
				t.Fatalf("SearchTitle(%q) = %q", title, got)
			}
		})
	}
	for _, title := range []string{"1984", "2001: A Space Odyssey", "(1984) A Novel", "The #1 Ladies' Detective Agency"} {
		t.Run(title, func(t *testing.T) {
			if got := SearchTitle(title); got != title {
				t.Fatalf("meaningful title changed: %q -> %q", title, got)
			}
		})
	}
}

func TestMatcherAutomaticallySelectsReaperManWithSequencePrefix(t *testing.T) {
	var queries []Query
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		return []Candidate{
			{Provider: ProviderOpenLibrary, ProviderID: "/works/OL453654W", Title: "Reaper Man", Authors: []string{"Terry Pratchett"}, CoverURL: "https://covers.openlibrary.org/b/id/14648856-L.jpg"},
			{Provider: ProviderOpenLibrary, ProviderID: "/works/OL453731W", Title: "Novels. Reaper Man / Guards! Guards!", Authors: []string{"Terry Pratchett"}},
		}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "(#11) Reaper Man", Authors: []string{"Terry Pratchett"}})
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "/works/OL453654W" || d.Selected.CoverURL == "" {
		t.Fatalf("correct book with cover was not selected: %+v err=%v", d, err)
	}
	if len(queries) != 1 || queries[0].Title != "Reaper Man" || queries[0].Author != "Terry Pratchett" {
		t.Fatalf("unexpected provider queries: %+v", queries)
	}
}

func TestMatcherIdentifiesSpaceNumberedBooks(t *testing.T) {
	for _, tc := range []struct{ dirty, title, author string }{
		{"01 The Eye of the World", "The Eye of the World", "Robert Jordan"},
		{"01 The Great Train Robbery", "The Great Train Robbery", ""},
		{"02 The Great Hunt", "The Great Hunt", "Robert Jordan"},
		{"03 The Dragon Reborn", "The Dragon Reborn", "Robert Jordan"},
		{"11 Knife of Dreams", "Knife of Dreams", "Robert Jordan"},
	} {
		t.Run(tc.dirty, func(t *testing.T) {
			var queries []Query
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				queries = append(queries, q)
				if q.Title != tc.title {
					return nil, nil
				}
				author := tc.author
				if author == "" {
					author = "Michael Crichton"
				}
				return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "correct", Title: tc.title, Authors: []string{author}, CoverURL: "https://covers.openlibrary.org/b/id/1-L.jpg"}}, nil
			})
			var authors []string
			if tc.author != "" {
				authors = []string{tc.author}
			}
			d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: tc.dirty, Authors: authors})
			if err != nil || d.Selected == nil || d.Selected.Title != tc.title {
				t.Fatalf("book not identified: decision=%+v queries=%+v err=%v", d, queries, err)
			}
		})
	}
}

func TestMatcherPreservesMeaningfulLeadingNumber(t *testing.T) {
	var queries []Query
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "correct", Title: "12 Years a Slave", Authors: []string{"Solomon Northup"}}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "12 Years a Slave", Authors: []string{"Solomon Northup"}})
	if err != nil || d.Selected == nil || d.Selected.Title != "12 Years a Slave" || len(queries) != 1 || queries[0].Title != "12 Years a Slave" {
		t.Fatalf("meaningful number was lost: decision=%+v queries=%+v err=%v", d, queries, err)
	}
}

func TestMatcherRecognizesSeriesLabelButNotPartialVolume(t *testing.T) {
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{
			{Provider: ProviderOpenLibrary, ProviderID: "full", Title: "The Great Hunt (The Wheel of Time Book 2)", Authors: []string{"Robert Jordan"}},
			{Provider: ProviderOpenLibrary, ProviderID: "part", Title: "The Great Hunt (part 1/2)", Authors: []string{"Robert Jordan"}},
		}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "02 The Great Hunt", Authors: []string{"Robert Jordan"}})
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "full" {
		t.Fatalf("full novel not identified: %+v %v", d, err)
	}
}

func TestMatcherUsesFolderAuthorToDisambiguateTitle(t *testing.T) {
	var queries []Query
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		return []Candidate{
			{Provider: ProviderOpenLibrary, ProviderID: "wrong", Title: "The Great Train Robbery", Authors: []string{"John Gosling"}},
			{Provider: ProviderOpenLibrary, ProviderID: "correct", Title: "The Great Train Robbery", Authors: []string{"Michael Crichton"}},
		}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "01 The Great Train Robbery", SourceFolder: "Michael Crichton/Michael Crichton/1975 - The Great Train Robbery [Unabridged]"})
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "correct" {
		t.Fatalf("folder author not used: %+v %v queries=%+v", d, err, queries)
	}
}

func TestMatcherWaitsForFolderAuthorBeforeAcceptingSameTitle(t *testing.T) {
	var queries []Query
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		author, id := "John Gosling", "wrong"
		if q.Author == "Michael Crichton" {
			author, id = "Michael Crichton", "correct"
		}
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: id, Title: "The Great Train Robbery", Authors: []string{author}}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "01 The Great Train Robbery", SourceFolder: "Michael Crichton/1975 - The Great Train Robbery"})
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "correct" || len(queries) < 2 {
		t.Fatalf("accepted wrong author before hint search: %+v queries=%+v err=%v", d, queries, err)
	}
}

func TestMatcherPreservesBareVolumeAndBookLabels(t *testing.T) {
	for _, label := range []string{"Volume 1", "Volume 2", "Book 1"} {
		t.Run(label, func(t *testing.T) {
			p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
				return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "partial", Title: "The Great Hunt (" + label + ")", Authors: []string{"Robert Jordan"}}}, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "02 The Great Hunt", Authors: []string{"Robert Jordan"}})
			if err != nil || d.Selected != nil {
				t.Fatalf("partial volume accepted as full book: %+v %v", d, err)
			}
		})
	}
}

func TestMatcherISBNOverridesUncorroboratedFolderHint(t *testing.T) {
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "isbn", Title: "Some Book", Authors: []string{"An Author"}, ISBN: "9781234567890"}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "Some Book", SourceFolder: "Audio Books/Some Book", Identifiers: map[string]any{"isbn": "9781234567890"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("exact ISBN blocked by folder hint: %+v %v", d, err)
	}
}
