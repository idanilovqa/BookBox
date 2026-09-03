package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestAnniversaryEditionSearchVariants(t *testing.T) {
	for _, title := range []string{
		"Elantris: Tenth Anniversary Special Edition",
		"Elantris (10th Anniversary Edition)",
		"Elantris [XX Anniversary Collector's Edition]",
		"Elantris_XXX_ANNIVERSARY_EDITION",
		"Elantris — Twenty-Fifth Anniversary Revised and Expanded Edition",
		"Elantris: 10-year Anniversary Edition",
		"Elantris (10th Anniversary)",
		"Elantris: Tenth Anniversary Author’s Definitive Edition",
		"[XX Anniversary Edition] Elantris",
		"25th Anniversary Edition - Elantris",
		"Elantris 100th Anniversary Edition",
		"Elantris: XX Anniversary", "XX Anniversary: Elantris", "Elantris, Twenty-First Anniversary Edition",
	} {
		t.Run(title, func(t *testing.T) {
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if q.Title == "Elantris" && q.Author == "Brandon Sanderson" {
					return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "work", Title: "Elantris", Authors: []string{"Brandon Sanderson"}}}, nil
				}
				return nil, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: title, Authors: []string{"Brandon Sanderson"}})
			if err != nil || d.Selected == nil {
				t.Fatalf("not matched: %s (%v)", title, err)
			}
			if titles := SearchTitles(title); titles[0] != SearchTitle(title) {
				t.Fatal("original search variant lost")
			}
		})
	}
}
func TestAnniversaryWordsRemainWhenTheyAreTheTitle(t *testing.T) {
	for _, title := range []string{"Anniversary", "The Tenth Anniversary", "The Anniversary Edition", "Tenth Anniversary Edition", "Elantris and the Anniversary Mystery"} {
		if got := anniversaryTitleVariant(title); got != title {
			t.Fatalf("meaningful title stripped: %s -> %s", title, got)
		}
	}
}
func TestAnniversaryProviderEditionMatchesCanonicalTitle(t *testing.T) {
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "a", Title: "Elantris", Authors: []string{"Brandon Sanderson"}}, {Provider: ProviderOpenLibrary, ProviderID: "b", Title: "Elantris: Tenth Anniversary Special Edition", Authors: []string{"Brandon Sanderson"}}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "Elantris", Authors: []string{"Brandon Sanderson"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("edition ambiguity: %+v %v", d, err)
	}
}
