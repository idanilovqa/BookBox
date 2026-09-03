package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestMatcherRecognizesCombinedContributorsAndSplitSubtitle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		local     catalog.InferredBook
		candidate Candidate
	}{
		{"combined coauthors", catalog.InferredBook{Title: "Bastille vs. the Evil Librarians: Alcatraz Versus the Evil Librarians, Book 6", Authors: []string{"Brandon Sanderson, Janci Patterson"}}, Candidate{Title: "Bastille vs. the Evil Librarians", Authors: []string{"Brandon Sanderson", "Janci Patterson"}}},
		{"split subtitle", catalog.InferredBook{Title: "Behave: The Biology of Humans at Our Best and Worst", Authors: []string{"Robert M. Sapolsky"}}, Candidate{Title: "Behave", Subtitle: "The Biology of Humans at Our Best and Worst", Authors: []string{"Robert M. Sapolsky"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.candidate.Provider = ProviderOpenLibrary
			tc.candidate.ProviderID = "work"
			p := queryProvider(func(context.Context, Query) ([]Candidate, error) { return []Candidate{tc.candidate}, nil })
			d, err := NewMatcher(p).Match(t.Context(), tc.local)
			if err != nil || d.Selected == nil {
				t.Fatalf("correct stored candidate not selected: %+v %v", d, err)
			}
			if d.Selected.Title != tc.candidate.Title || d.Selected.Subtitle != tc.candidate.Subtitle {
				t.Fatal("comparison changed provider display metadata")
			}
		})
	}
}

func TestContributorComparisonRequiresAllNames(t *testing.T) {
	local := normalizedAuthors([]string{"Brandon Sanderson, Janci Patterson"})
	for _, authors := range [][]string{{"Brandon Sanderson"}, {"Brandon Sanderson", "Another Writer"}} {
		if got := authorSimilarity(local, normalizedAuthors(authors)); got >= 0.8 {
			t.Fatalf("conflicting contributors accepted: %v (%v)", authors, got)
		}
	}
}

func TestMatcherDoesNotIgnoreConflictingSubtitle(t *testing.T) {
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "wrong", Title: "Behave", Subtitle: "A Different Book", Authors: []string{"Robert M. Sapolsky"}}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "Behave: The Biology of Humans at Our Best and Worst", Authors: []string{"Robert M. Sapolsky"}})
	if err != nil || d.Selected != nil {
		t.Fatalf("wrong subtitle accepted: %+v %v", d, err)
	}
}

func TestContributorComparisonPreservesSeparateNameBoundaries(t *testing.T) {
	left := normalizedAuthors([]string{"John Smith", "Jane Doe"})
	right := normalizedAuthors([]string{"John Doe", "Jane Smith"})
	if got := authorSimilarity(left, right); got >= 0.8 {
		t.Fatalf("different people conflated: %v", got)
	}
}
