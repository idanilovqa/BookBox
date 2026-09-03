package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestSearchesAdditionalCatalogueTitleFormats(t *testing.T) {
	for _, tc := range []struct{ dirty, clean, author string }{
		{"A4 Alcatraz Versus the Shattered Lens", "Alcatraz Versus the Shattered Lens", "Brandon Sanderson"},
		{"A Hat Full of Sky: Discworld, Book 32", "A Hat Full of Sky", "Terry Pratchett"},
		{"A Brief History Of Time", "A Brief History Of Time", "Stephen Hawking"},
		{"Abandon", "Abandon", "Blake Crouch"},
	} {
		t.Run(tc.dirty, func(t *testing.T) {
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if q.Title == tc.clean && q.Author == tc.author {
					return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "work", Title: tc.clean, Authors: []string{tc.author}}}, nil
				}
				return nil, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: tc.dirty, Authors: []string{tc.author}})
			if err != nil || d.Selected == nil {
				t.Fatalf("not identified: %+v %v", d, err)
			}
			if tc.author == "Terry Pratchett" && (d.Selected.Series != "Discworld" || d.Selected.SeriesPosition != "32") {
				t.Fatalf("series lost: %+v", d.Selected)
			}
		})
	}
}
func TestAlphaNumericTitleKeepsOriginalVariant(t *testing.T) {
	titles := SearchTitles("A4 Paper")
	if len(titles) == 0 || titles[0] != "A4 Paper" {
		t.Fatalf("meaningful prefix discarded: %v", titles)
	}
	if SearchTitle("Dune: Book 1") != "Dune: Book 1" {
		t.Fatal("bare book label discarded")
	}
}
