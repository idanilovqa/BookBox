package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestChainedAuthorSeriesTitle(t *testing.T) {
	local := catalog.InferredBook{Title: "Brandon Sanderson - Mistborn 02 - Well of Ascension", Authors: []string{"Brandon Sanderson"}, SourceFolder: "Brandon Sanderson/Mistborn Series - Brandon Sanderson/M02 - Well of Ascension"}
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if (q.Title == "Well of Ascension" || q.Title == "The Well of Ascension") && q.Author == "Brandon Sanderson" {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "well", Title: "The Well of Ascension", Authors: local.Authors}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), local)
	if err != nil || d.Selected == nil {
		t.Fatalf("not identified: %+v %v", d, err)
	}
	if d.Selected.Series != "Mistborn" || d.Selected.SeriesPosition != "2" {
		t.Fatalf("series not retained: %+v", d.Selected)
	}
}
func TestCardinalBlackStoredEditionVariants(t *testing.T) {
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{
			{Provider: ProviderOpenLibrary, ProviderID: "one", Title: "Cardinal Black", Authors: []string{"Robert McCammon"}},
			{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "Cardinal Black", Authors: []string{"Robert R. McCammon"}},
		}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "Cardinal Black: Matthew Corbett, Book 7", Authors: []string{"Robert McCammon"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("not identified: %+v %v", d, err)
	}
}

func TestChainedTitleDoesNotStripDifferentAuthorOrDisc(t *testing.T) {
	for _, title := range []string{"Another Writer - Mistborn 02 - Well of Ascension", "Brandon Sanderson - Disc 02 - Well of Ascension"} {
		if got, _, _ := chainedTitleReference(catalog.InferredBook{Title: title, Authors: []string{"Brandon Sanderson"}}); got != "" {
			t.Fatalf("unsafe cleanup: %s", got)
		}
	}
}
func TestEditionAuthorsKeepConflictingInitialsSeparate(t *testing.T) {
	if equivalentEditionAuthors([]string{"Robert R. McCammon"}, []string{"Robert B. McCammon"}) {
		t.Fatal("conflicting initials merged")
	}
	if equivalentEditionAuthors([]string{"Robert McCammon"}, []string{"Robert Othername"}) {
		t.Fatal("different names merged")
	}
	candidates := []ScoredCandidate{
		{Candidate: Candidate{Provider: ProviderOpenLibrary, ProviderID: "a", Title: "Same Title", Authors: []string{"Robert McCammon"}}, Score: 1},
		{Candidate: Candidate{Provider: ProviderOpenLibrary, ProviderID: "b", Title: "Same Title", Authors: []string{"Robert R. McCammon"}}, Score: 0.98},
		{Candidate: Candidate{Provider: ProviderOpenLibrary, ProviderID: "c", Title: "Same Title", Authors: []string{"Robert B. McCammon"}}, Score: 0.97},
	}
	if d := searchDecision(catalog.InferredBook{Title: "Same Title", Authors: []string{"Robert McCammon"}}, candidates); d.Selected != nil {
		t.Fatal("missing initial bridged conflicting authors")
	}
}
