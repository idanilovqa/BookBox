package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func expanseLocal() catalog.InferredBook {
	return catalog.InferredBook{Title: "[The Expanse 0.1] Drive (novella)", Authors: []string{"Jefferson Mays"}, SourceFolder: "James Corey/James S. A. Corey - The Expanse (complete series)/narrated by Jefferson Mays/2.7 - Drive (novella)"}
}
func TestMatcherUsesAuthorFolderDespiteWrongExistingAuthor(t *testing.T) {
	var queries []Query
	provider := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		queries = append(queries, q)
		if q.Title == "Drive" && q.Author == "James S. A. Corey" {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "drive", Title: "Drive", Authors: []string{"James S. A. Corey"}}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(provider).Match(t.Context(), expanseLocal())
	if err != nil || d.Selected == nil {
		t.Fatalf("no match: %+v %v queries=%+v", d, err, queries)
	}
	if d.Selected.Series != "The Expanse" || d.Selected.SeriesPosition != "0.1" {
		t.Fatalf("series lost: %+v", d.Selected)
	}
	if len(d.Selected.Narrators) != 1 || d.Selected.Narrators[0] != "Jefferson Mays" {
		t.Fatalf("narrator lost: %+v", d.Selected)
	}
	for _, q := range queries {
		if q.Author == "narrated by Jefferson Mays" {
			t.Fatalf("narrator folder searched as author: %+v", q)
		}
	}
}
func TestMatcherFallsBackToFolderAuthorCatalogue(t *testing.T) {
	provider := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title == "" && q.Author == "James S. A. Corey" {
			return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "drive", Title: "Drive", Authors: []string{"James S. A. Corey"}}, {Provider: ProviderOpenLibrary, ProviderID: "churn", Title: "The Churn", Authors: []string{"James S. A. Corey"}}}, nil
		}
		return nil, nil
	})
	d, err := NewMatcher(provider).Match(t.Context(), expanseLocal())
	if err != nil || d.Selected == nil || d.Selected.ProviderID != "drive" {
		t.Fatalf("author fallback failed: %+v %v", d, err)
	}
}
func TestFolderAuthorDoesNotOverrideCorroboratedExistingAuthor(t *testing.T) {
	local := catalog.InferredBook{Title: "Shared Title", Authors: []string{"Real Author"}, SourceFolder: "Wrong Author/Shared Title"}
	candidates := []ScoredCandidate{{Candidate: Candidate{Provider: ProviderOpenLibrary, ProviderID: "real", Title: "Shared Title", Authors: []string{"Real Author"}}}, {Candidate: Candidate{Provider: ProviderOpenLibrary, ProviderID: "wrong", Title: "Shared Title", Authors: []string{"Wrong Author"}}}}
	d := searchTitleDecision(local, candidates)
	if d.Selected == nil || d.Selected.ProviderID != "real" {
		t.Fatalf("overrode existing corroborated author: %+v", d)
	}
}

func TestNumberedBookFolderIsNotAnAuthorHint(t *testing.T) {
	local := catalog.InferredBook{Title: "01 The Great Train Robbery", SourceFolder: "The Great Train Robbery"}
	if hints := folderAuthorHints(local); len(hints) != 0 {
		t.Fatalf("title became author hint: %v", hints)
	}
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "train", Title: "The Great Train Robbery", Authors: []string{"Michael Crichton"}}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), local)
	if err != nil || d.Selected == nil {
		t.Fatalf("valid title match blocked: %+v %v", d, err)
	}
}
