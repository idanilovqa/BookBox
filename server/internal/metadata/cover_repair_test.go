package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestCoverCandidateRequiresSameWorkAndAuthor(t *testing.T) {
	b := catalog.InferredBook{Title: "White Sand Volume 2", Authors: []string{"Brandon Sanderson"}}
	for _, c := range []Candidate{
		{Provider: ProviderOpenLibrary, Title: "White Sand Volume 1", Authors: b.Authors, CoverURL: "wrong"},
		{Provider: ProviderOpenLibrary, Title: b.Title, Authors: []string{"Another Author"}, CoverURL: "wrong"},
		{Provider: ProviderGoogleBooks, Title: b.Title, Authors: b.Authors, CoverURL: "wrong"},
	} {
		if SameWorkCover(b, ProviderOpenLibrary, []ScoredCandidate{{Candidate: c}}) != "" {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := Candidate{Provider: ProviderOpenLibrary, Title: b.Title, Authors: b.Authors, CoverURL: "right"}
	if SameWorkCover(b, ProviderOpenLibrary, []ScoredCandidate{{Candidate: c}}) != "right" {
		t.Fatal("same work rejected")
	}
}
func TestSeriesOnlyTagUsesExplicitBookFolder(t *testing.T) {
	b := catalog.InferredBook{Title: "The Fallen and the Faithful", Authors: []string{"John Gwynne"}, SourceFolder: "John Gwynne/John Gwynne - The Fallen and the Faithful 04 - Wrath"}
	found := false
	for _, i := range TitleCleanupInterpretations(b) {
		if i.Title == "Wrath" && i.SeriesPosition == "4" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit folder identity was ignored")
	}
}

func TestCoverRepairRejectsConflictingSubtitleVolumes(t *testing.T) {
	b := catalog.InferredBook{Title: "White Sand", Subtitle: "Volume 2", Authors: []string{"Brandon Sanderson"}}
	c := Candidate{Provider: ProviderOpenLibrary, Title: "White Sand", Subtitle: "Volume 1", Authors: b.Authors, CoverURL: "wrong"}
	if SameWorkCover(b, ProviderOpenLibrary, []ScoredCandidate{{Candidate: c}}) != "" {
		t.Fatal("different subtitle volume accepted")
	}
}
func TestCoverSearchContinuesPastMetadataOnlyHit(t *testing.T) {
	b := catalog.InferredBook{Title: "Reaper Man", Authors: []string{"Terry Pratchett"}}
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		c := Candidate{Provider: ProviderOpenLibrary, ProviderID: "work", Title: b.Title, Authors: b.Authors}
		if q.Title == "" {
			c.CoverURL = "cover"
		}
		return []Candidate{c}, nil
	})
	url, err := NewMatcher(p).MatchCover(t.Context(), b, ProviderOpenLibrary)
	if err != nil || url != "cover" {
		t.Fatalf("no author fallback cover: %q %v", url, err)
	}
}
