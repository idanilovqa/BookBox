package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"testing"
)

func TestAdditionalDirtyTitleIdentities(t *testing.T) {
	for _, tc := range []struct {
		local     catalog.InferredBook
		candidate Candidate
	}{
		{catalog.InferredBook{Title: "Children of the Nameless", Authors: []string{"Voice of All"}, SourceFolder: "Brandon Sanderson/Children of the Nameless"}, Candidate{Title: "Children of the Nameless", Authors: []string{"Brandon Sanderson"}}},
		{catalog.InferredBook{Title: "Crouch, B: Recursion", Authors: []string{"Jon Lindstrom, Abby Craden"}, SourceFolder: "Blake Crouch/Crouch, Blake - Recursion (2019)"}, Candidate{Title: "Recursion", Authors: []string{"Blake Crouch"}}},
		{catalog.InferredBook{Title: "Cytonic: The Skyward Series: Book 3", Authors: []string{"Various Artists"}, SourceFolder: "Brandon Sanderson/Skyward/03 Brandon Sanderson - Cytonic - The Skyward Series, Book 3"}, Candidate{Title: "Cytonic", Authors: []string{"Brandon Sanderson"}}},
		{catalog.InferredBook{Title: "Dark One 01", Authors: []string{"Brandon Sanderson & Dan Wells"}, SourceFolder: "Brandon Sanderson/Brandon Sanderson - Dark One 01 - Forgotten"}, Candidate{Title: "Dark One--Forgotten", Authors: []string{"Brandon Sanderson", "Dan Wells"}}},
		{catalog.InferredBook{Title: "Dr. Sleep", Authors: []string{"Stephen King"}}, Candidate{Title: "Doctor Sleep", Authors: []string{"Stephen King"}}},
	} {
		t.Run(tc.local.Title, func(t *testing.T) {
			tc.candidate.Provider = ProviderOpenLibrary
			tc.candidate.ProviderID = "work"
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if normalizeText(q.Title, false) == normalizeText(tc.candidate.Title, false) {
					return []Candidate{tc.candidate}, nil
				}
				return nil, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), tc.local)
			if err != nil || d.Selected == nil {
				t.Fatalf("not identified: %+v %v", d, err)
			}
			if tc.local.Title == "Dark One 01" && (d.Selected.Series != "Dark One" || d.Selected.SeriesPosition != "1") {
				t.Fatalf("series lost: %+v", d.Selected)
			}
		})
	}
}

func TestAdditionalIdentityRejectsConflictingEvidence(t *testing.T) {
	for _, local := range []catalog.InferredBook{
		{Title: "Crouch, B: Recursion", SourceFolder: "Brian Other/Recursion"},
		{Title: "Crouch, B: Recursion", SourceFolder: "Adam Crouch/Recursion"},
	} {
		if got := abbreviatedAuthorTitle(local); got != "" {
			t.Fatalf("wrong author accepted: %s", got)
		}
	}
	for _, folder := range []string{"Other Author/Brandon Sanderson - Dark One 01 - Forgotten", "Brandon Sanderson/Brandon Sanderson - Dark One 02 - Forgotten", "Brandon Sanderson/Brandon Sanderson - Mistborn 01 - Forgotten"} {
		if title, _, _ := chainedFolderReference(catalog.InferredBook{Title: "Dark One 01", SourceFolder: folder}); title != "" {
			t.Fatalf("conflicting folder accepted: %s", folder)
		}
	}
}
