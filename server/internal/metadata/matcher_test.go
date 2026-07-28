package metadata

import (
	"context"
	"testing"

	"bookbox.local/server/internal/catalog"
)

func TestScoreUsesNormalizedWeightedEvidence(t *testing.T) {
	t.Parallel()

	year := 1965
	local := catalog.InferredBook{
		Title:           "  DUNE: The Novel! ",
		Authors:         []string{"HERBERT, Frank"},
		PublicationYear: &year,
		Identifiers:     map[string]any{"isbn13": "978-0-441-17271-9"},
	}
	candidate := Candidate{
		Title:         "Dune The Novel",
		Authors:       []string{"Frank Herbert"},
		PublishedDate: "1965-08-01",
		ISBN:          "9780441172719",
	}
	if got := score(local, candidate); got != 1 {
		t.Fatalf("score = %f", got)
	}
}

func TestInferredISBNDeterministicallyPrefersISBN13(t *testing.T) {
	t.Parallel()

	local := catalog.InferredBook{Identifiers: map[string]any{
		"isbn10": "0441172717",
		"isbn13": "9780441172719",
	}}
	for range 100 {
		if got := inferredISBN(local); got != "9780441172719" {
			t.Fatalf("ISBN = %q", got)
		}
	}
}

func TestScoreRenormalizesWhenISBNAndYearAreUnavailable(t *testing.T) {
	t.Parallel()

	local := catalog.InferredBook{
		Title:   "Dune",
		Authors: []string{"Frank Herbert"},
	}
	candidate := Candidate{
		Title:         "Dune",
		Authors:       []string{"Frank Herbert"},
		PublishedDate: "1965",
	}
	if got := score(local, candidate); got != 1 {
		t.Fatalf("score = %f", got)
	}
}

func TestAuthorSimilarityPenalizesConflictingCoauthors(t *testing.T) {
	t.Parallel()

	identicalReordered := authorSimilarity(
		normalizedAuthors([]string{"Alice Writer", "Bob Writer"}),
		normalizedAuthors([]string{"Bob Writer", "Alice Writer"}),
	)
	if identicalReordered != 1 {
		t.Fatalf("identical reordered similarity = %f", identicalReordered)
	}

	partial := authorSimilarity(
		normalizedAuthors([]string{"Alice Writer", "Bob Writer"}),
		normalizedAuthors([]string{"Alice Writer", "Mallory Writer"}),
	)
	if partial >= 1 {
		t.Fatalf("partial/conflicting similarity = %f", partial)
	}
}

func TestDecideAutoAppliesOnlyWithThresholdAndLead(t *testing.T) {
	t.Parallel()

	high := decide([]ScoredCandidate{
		{Candidate: Candidate{ProviderID: "winner"}, Score: .92},
		{Candidate: Candidate{ProviderID: "runner-up"}, Score: .83},
	})
	if high.ReviewState != catalog.ReviewUnreviewed ||
		high.Selected == nil || high.Selected.ProviderID != "winner" {
		t.Fatalf("high-confidence decision = %#v", high)
	}

	tie := decide([]ScoredCandidate{
		{Candidate: Candidate{ProviderID: "first"}, Score: .92},
		{Candidate: Candidate{ProviderID: "second"}, Score: .90},
	})
	if tie.ReviewState != catalog.ReviewNeedsReview || tie.Selected != nil {
		t.Fatalf("tie decision = %#v", tie)
	}

	low := decide([]ScoredCandidate{
		{Candidate: Candidate{ProviderID: "low"}, Score: .89},
	})
	if low.ReviewState != catalog.ReviewNeedsReview || low.Selected != nil {
		t.Fatalf("low-confidence decision = %#v", low)
	}
}

func TestDecideAcceptsExactMinimumLead(t *testing.T) {
	t.Parallel()

	decision := decide([]ScoredCandidate{
		{Candidate: Candidate{ProviderID: "winner"}, Score: .98},
		{Candidate: Candidate{ProviderID: "runner-up"}, Score: .90},
	})
	if decision.Selected == nil || decision.Selected.ProviderID != "winner" {
		t.Fatalf("minimum lead decision = %#v", decision)
	}
}

func TestDecideSortsCandidatesDeterministically(t *testing.T) {
	t.Parallel()

	decision := decide([]ScoredCandidate{
		{Candidate: Candidate{Provider: "z", ProviderID: "2"}, Score: .8},
		{Candidate: Candidate{Provider: "a", ProviderID: "1"}, Score: .8},
	})
	if len(decision.Candidates) != 2 ||
		decision.Candidates[0].Provider != "a" ||
		decision.Candidates[1].Provider != "z" {
		t.Fatalf("candidates = %#v", decision.Candidates)
	}
}

func TestMatcherNoCandidatesKeepsLocalMetadata(t *testing.T) {
	t.Parallel()

	local := catalog.InferredBook{
		Title:          "Dune",
		Authors:        []string{"Frank Herbert"},
		MetadataSource: catalog.MetadataLocal,
		ReviewState:    catalog.ReviewUnreviewed,
	}
	matcher := NewMatcher(staticProvider{})
	decision, err := matcher.Match(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Book.Title != local.Title ||
		decision.Book.MetadataSource != catalog.MetadataLocal ||
		decision.Book.ReviewState != catalog.ReviewUnreviewed ||
		len(decision.Candidates) != 0 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestMatcherNeverOverwritesConfirmedMetadata(t *testing.T) {
	t.Parallel()

	provider := &countingProvider{candidates: []Candidate{
		{
			Provider:   ProviderGoogleBooks,
			ProviderID: "different-book",
			Title:      "Dune",
			Authors:    []string{"Frank Herbert"},
		},
	}}
	local := catalog.InferredBook{
		Title:          "User Confirmed Title",
		Authors:        []string{"User Confirmed Author"},
		MetadataSource: catalog.MetadataConfirmed,
		ReviewState:    catalog.ReviewConfirmed,
		Confidence:     1,
	}
	decision, err := NewMatcher(provider).Match(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if decision.Book.Title != local.Title ||
		decision.Book.Authors[0] != local.Authors[0] ||
		decision.Book.MetadataSource != catalog.MetadataConfirmed ||
		decision.Book.ReviewState != catalog.ReviewConfirmed ||
		decision.Selected != nil {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestMatcherStoresAlternativesAndAppliesHighConfidenceProviderFields(t *testing.T) {
	t.Parallel()

	local := catalog.InferredBook{
		Title:          "Dune",
		Authors:        []string{"Frank Herbert"},
		MetadataSource: catalog.MetadataLocal,
		ReviewState:    catalog.ReviewUnreviewed,
	}
	matcher := NewMatcher(staticProvider{candidates: []Candidate{
		{
			Provider:      ProviderGoogleBooks,
			ProviderID:    "winner",
			Title:         "Dune",
			Subtitle:      "The Desert Planet",
			Authors:       []string{"Frank Herbert"},
			Publisher:     "Ace",
			PublishedDate: "1965",
			Subjects:      []string{"Sci-Fi", "Space Opera"},
		},
	}})
	decision, err := matcher.Match(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Selected == nil || len(decision.Candidates) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.Book.Title != "Dune" ||
		decision.Book.Subtitle != "The Desert Planet" ||
		decision.Book.Publisher != "Ace" ||
		decision.Book.MetadataSource != catalog.MetadataMixed ||
		decision.Book.Confidence != 1 {
		t.Fatalf("book = %#v", decision.Book)
	}
	if len(decision.Book.ProviderSubjects) != 2 ||
		decision.Book.ProviderSubjects[0] != "Sci-Fi" {
		t.Fatalf("subjects = %#v", decision.Book.ProviderSubjects)
	}
	if len(decision.Book.Genres) != 2 ||
		decision.Book.Genres[0] != "Science Fiction" ||
		decision.Book.Genres[1] != "Space Opera" {
		t.Fatalf("genres = %#v", decision.Book.Genres)
	}
}

func TestMergeMetadataPrecedenceAndGenreNormalization(t *testing.T) {
	t.Parallel()

	inferred := Candidate{
		Title:   "Folder Title",
		Authors: []string{"Folder Author"},
		Genres:  []string{"Fantasy"},
	}
	provider := Candidate{
		Title:         "Provider Title",
		Subtitle:      "Provider Subtitle",
		Authors:       []string{"Provider Author"},
		Publisher:     "Provider Publisher",
		PublishedDate: "2001",
		Subjects:      []string{"Sci Fi", "Detective Fiction"},
	}
	local := Candidate{
		Title:   "Embedded Title",
		Authors: []string{"Embedded Author"},
	}
	confirmed := Candidate{
		Title: "Confirmed Title",
	}

	got := MergeMetadata(confirmed, local, provider, inferred)
	if got.Title != "Confirmed Title" ||
		len(got.Authors) != 1 || got.Authors[0] != "Embedded Author" ||
		got.Subtitle != "Provider Subtitle" ||
		got.Publisher != "Provider Publisher" {
		t.Fatalf("merged = %#v", got)
	}
	if len(got.Subjects) != 2 || got.Subjects[0] != "Sci Fi" {
		t.Fatalf("subjects = %#v", got.Subjects)
	}
	if len(got.Genres) != 3 ||
		got.Genres[0] != "Fantasy" ||
		got.Genres[1] != "Science Fiction" ||
		got.Genres[2] != "Mystery" {
		t.Fatalf("genres = %#v", got.Genres)
	}
}

func TestNormalizeGenresNormalizesPunctuatedAliasKeys(t *testing.T) {
	t.Parallel()

	got := normalizeGenres([]string{"science-fiction"})
	if len(got) != 1 || got[0] != "Science Fiction" {
		t.Fatalf("genres = %#v", got)
	}
}

type staticProvider struct {
	candidates []Candidate
	err        error
}

func (provider staticProvider) Search(context.Context, Query) ([]Candidate, error) {
	return provider.candidates, provider.err
}

type countingProvider struct {
	candidates []Candidate
	calls      int
}

func (provider *countingProvider) Search(context.Context, Query) ([]Candidate, error) {
	provider.calls++
	return provider.candidates, nil
}
