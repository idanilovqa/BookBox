package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"errors"
	"regexp"
	"strings"
)

var sequencePrefix = regexp.MustCompile(`(?i)^(?:[a-z]{1,4}[0-9]{1,3}|(?:book|vol(?:ume)?|part)\s*[0-9]{1,3}|[0-9]{1,3})\s*[:._)\-]+\s*`)
var bracketedSequencePrefix = regexp.MustCompile(`^(?:\(\s*#?\s*[0-9]{1,3}\s*\)|\[\s*#?\s*[0-9]{1,3}\s*\]|#\s*[0-9]{1,3})\s+`)
var editionSuffix = regexp.MustCompile(`(?i)\s*\((?:unabridged|abridged|audiobook)\)\s*$`)

// SearchTitle removes explicit catalog sequence labels, preserving meaningful
// numbers and the source name. The provider supplies canonical display casing.
func SearchTitle(title string) string {
	title = strings.TrimSpace(title)
	title = bracketedSequencePrefix.ReplaceAllString(title, "")
	title = sequencePrefix.ReplaceAllString(title, "")
	return strings.TrimSpace(editionSuffix.ReplaceAllString(title, ""))
}

func searchQueries(local catalog.InferredBook) []Query {
	author := firstNonEmpty(local.Authors)
	clean := SearchTitle(local.Title)
	var queries []Query
	seen := map[Query]bool{}
	add := func(q Query) {
		key := Query{Title: strings.ToLower(q.Title), Author: strings.ToLower(q.Author), ISBN: q.ISBN}
		if !seen[key] && (key.Title != "" || key.Author != "" || key.ISBN != "") {
			seen[key] = true
			queries = append(queries, q)
		}
	}
	if isbn := inferredISBN(local); isbn != "" {
		add(Query{ISBN: isbn})
	}
	add(Query{Title: clean, Author: author})
	add(Query{Title: strings.TrimSpace(local.Title), Author: author})
	if author != "" {
		add(Query{Title: clean})
		add(Query{Author: author})
	}
	return queries
}

func sameBookTitle(local catalog.InferredBook, candidate Candidate) bool {
	title := normalizeText(SearchTitle(local.Title), false)
	other := normalizeText(SearchTitle(candidate.Title), false)
	// A known series can appear as a title prefix in a provider's edition.
	series := normalizeText(local.Series, false)
	if series != "" {
		title = strings.TrimPrefix(title, series+" ")
		other = strings.TrimPrefix(other, series+" ")
	}
	return title != "" && title == other
}

func searchDecision(local catalog.InferredBook, candidates []ScoredCandidate) Decision {
	all := decide(candidates).Candidates
	var works []ScoredCandidate
	identities := map[string]int{}
	for _, c := range all {
		key := normalizeText(SearchTitle(c.Title), false) + "\x00" + strings.Join(normalizedAuthors(c.Authors), "\x00")
		if len(c.Authors) == 0 {
			key += "\x00" + c.Provider + "\x00" + c.ProviderID
		}
		if index, ok := identities[key]; ok {
			if c.Score == works[index].Score && works[index].CoverURL == "" && c.CoverURL != "" {
				works[index] = c
			}
			continue
		}
		identities[key] = len(works)
		works = append(works, c)
	}
	d := decide(works)
	if d.Selected != nil {
		isbn := inferredISBN(local)
		isbnMatch := isbn != "" && isbn == normalizeISBN(d.Selected.ISBN)
		authorsMatch := len(local.Authors) == 0 || (len(d.Selected.Authors) > 0 && authorSimilarity(normalizedAuthors(local.Authors), normalizedAuthors(d.Selected.Authors)) >= 0.8)
		if !isbnMatch && (!sameBookTitle(local, *d.Selected) || !authorsMatch) {
			d.Selected = nil
			d.ReviewState = catalog.ReviewNeedsReview
		}
	}
	// Preserve provider editions for review, with the selected edition first.
	if d.Selected != nil {
		for i, c := range all {
			if c.Provider == d.Selected.Provider && c.ProviderID == d.Selected.ProviderID {
				copy(all[1:i+1], all[:i])
				all[0] = c
				break
			}
		}
	}
	if len(all) > 20 {
		all = all[:20]
	}
	d.Candidates = all
	return d
}

func (matcher *Matcher) search(ctx context.Context, local catalog.InferredBook) (Decision, error) {
	if matcher == nil {
		return decide(nil), nil
	}
	var scored []ScoredCandidate
	seen := map[string]bool{}
	failed := map[int]bool{}
	var failures []error
	scoringBook := local
	scoringBook.Title = SearchTitle(local.Title)
	for _, query := range searchQueries(local) {
		for index, provider := range matcher.providers {
			if failed[index] {
				continue
			}
			candidates, err := provider.Search(ctx, query)
			if ctx.Err() != nil {
				return Decision{}, ctx.Err()
			}
			if err != nil {
				failed[index] = true
				failures = append(failures, err)
				continue
			}
			for _, candidate := range candidates {
				key := candidate.Provider + "\x00" + candidate.ProviderID
				if seen[key] || candidate.ProviderID == "" || strings.TrimSpace(candidate.Title) == "" {
					continue
				}
				seen[key] = true
				scored = append(scored, ScoredCandidate{Candidate: candidate, Score: score(scoringBook, candidate)})
			}
		}
		d := searchDecision(scoringBook, scored)
		if d.Selected != nil {
			return d, nil
		}
	}
	d := searchDecision(scoringBook, scored)
	if d.Selected == nil && len(failures) > 0 {
		return Decision{}, errors.Join(failures...)
	}
	// Android accepts at most 20 review choices, even when author fallback
	// examines hundreds of works locally.
	if len(d.Candidates) > 20 {
		d.Candidates = d.Candidates[:20]
	}
	return d, nil
}
