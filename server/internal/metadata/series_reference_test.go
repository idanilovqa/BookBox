package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestShortSeriesNameUsesNumberedFolderTitle(t *testing.T) {
	local := catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}, SourceFolder: "Brandon Sanderson/Brandon Sanderson - Alcatraz vs The Evil Librarians/2 The Scrivener’s Bones"}
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title == "The Scrivener’s Bones" && q.Author == "Brandon Sanderson" {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "The Scrivener's Bones", Authors: []string{"Brandon Sanderson"}}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), local)
	if err != nil || d.Selected == nil {
		t.Fatalf("folder title not used: %+v %v", d, err)
	}
	if d.Selected.Series != "Alcatraz vs The Evil Librarians" || d.Selected.SeriesPosition != "2" {
		t.Fatalf("series lost: %+v", d.Selected)
	}
}

func TestShortSeriesReferenceRequiresProviderVolumeAndAuthor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []Candidate
		selected   bool
	}{
		{"correct", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "The Scrivener's Bones", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz Versus the Evil Librarians", SeriesPosition: "2"}}, true},
		{"wrong volume", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "three", Title: "The Knights of Crystallia", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz Versus the Evil Librarians", SeriesPosition: "3"}}, false},
		{"missing volume", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "The Scrivener's Bones", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz Versus the Evil Librarians"}}, false},
		{"wrong author", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "The Scrivener's Bones", Authors: []string{"Another Author"}, Series: "Alcatraz Versus the Evil Librarians", SeriesPosition: "2"}}, false},
		{"unrelated series", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "two", Title: "Other Book", Authors: []string{"Brandon Sanderson"}, Series: "Mistborn", SeriesPosition: "2"}}, false},
		{"ambiguous", []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "a", Title: "First Title", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz", SeriesPosition: "2"}, {Provider: ProviderOpenLibrary, ProviderID: "b", Title: "Different Title", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz", SeriesPosition: "2"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if q.Title == "Alcatraz" && q.Author == "Brandon Sanderson" {
					return tc.candidates, nil
				}
				return nil, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}})
			if err != nil || (d.Selected != nil) != tc.selected {
				t.Fatalf("unexpected selection: %+v %v", d, err)
			}
		})
	}
}

func TestSeriesReferenceDoesNotBorrowLocalPosition(t *testing.T) {
	local := catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}, Series: "Alcatraz", SeriesPosition: "2"}
	d := seriesReferenceDecision(local, []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "unknown", Title: "Unknown Book", Authors: local.Authors, Series: "Alcatraz"}})
	if d.Selected != nil {
		t.Fatal("local position mistaken for provider evidence")
	}
}
func TestFolderReferenceRejectsWrongVolumeAndPreservesManualTitle(t *testing.T) {
	b := catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}, SourceFolder: "Brandon Sanderson/Alcatraz/3 The Knights of Crystallia"}
	if got := ResolveFolderSeriesReference(b); got.Title != b.Title {
		t.Fatal("accepted conflicting folder number")
	}
	b.SourceFolder = "Brandon Sanderson/Alcatraz/2 The Scrivener’s Bones"
	b.GroupingLocked = true
	if got := ResolveFolderSeriesReference(b); got.Title != b.Title {
		t.Fatal("changed manual grouping title")
	}
}

func TestFolderReferenceRejectsMediaPartNumbers(t *testing.T) {
	for _, leaf := range []string{"CD2 Alcatraz Versus the Evil Librarians", "Part 2 Alcatraz Versus the Evil Librarians"} {
		t.Run(leaf, func(t *testing.T) {
			b := catalog.InferredBook{Title: "Alcatraz 2", Authors: []string{"Brandon Sanderson"}, SourceFolder: "Brandon Sanderson/Alcatraz/" + leaf}
			if got := ResolveFolderSeriesReference(b); got.Title != b.Title || got.Series != "" || got.SeriesPosition != "" {
				t.Fatalf("media part mistaken for book number: %+v", got)
			}
			if got := localSearchTitles(b); len(got) != 1 || got[0] != b.Title {
				t.Fatalf("media part used as title alias: %v", got)
			}
		})
	}
}
