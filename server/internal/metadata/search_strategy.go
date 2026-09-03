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
	title = editionSuffix.ReplaceAllString(title, "")
	return strings.TrimSpace(seriesLabelSuffix.ReplaceAllString(title, ""))
}

func searchQueries(local catalog.InferredBook) []Query {
	author := firstNonEmpty(local.Authors)
	titles := SearchTitles(local.Title)
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
	for _, title := range titles {
		add(Query{Title: title, Author: author})
		for _, hint := range folderAuthorHints(local) {
			add(Query{Title: title, Author: hint})
		}
	}
	add(Query{Title: strings.TrimSpace(local.Title), Author: author})
	if author != "" {
		for _, title := range titles {
			add(Query{Title: title})
		}
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
		d := searchTitleDecision(scoringBook, scored)
		if d.Selected != nil {
			return d, nil
		}
	}
	d := searchTitleDecision(scoringBook, scored)
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

var spacedSequencePrefix = regexp.MustCompile(`^[0-9]{1,3}\s+`)

// SearchTitles keeps the original identity first and adds a number-free search
// alternative. A leading number can be meaningful, as in "12 Years a Slave".
func SearchTitles(title string) []string {
	clean := SearchTitle(title)
	titles := []string{clean}
	if alternative := strings.TrimSpace(spacedSequencePrefix.ReplaceAllString(clean, "")); alternative != "" && alternative != clean {
		titles = append(titles, alternative)
	}
	return titles
}

func searchTitleDecision(local catalog.InferredBook, candidates []ScoredCandidate) Decision {
	requireFolderAuthor := len(local.Authors) == 0 && len(folderAuthorHints(local)) > 0
	local.Authors = corroboratedFolderAuthor(local, candidates)
	var best Decision
	for i, title := range SearchTitles(local.Title) {
		variant := local
		variant.Title = title
		rescored := make([]ScoredCandidate, len(candidates))
		for j, candidate := range candidates {
			comparison := candidate.Candidate
			comparison.Title = SearchTitle(comparison.Title)
			rescored[j] = ScoredCandidate{Candidate: candidate.Candidate, Score: score(variant, comparison)}
		}
		d := searchDecision(variant, rescored)
		isbnMatch := d.Selected != nil && inferredISBN(local) != "" && inferredISBN(local) == normalizeISBN(d.Selected.ISBN)
		if requireFolderAuthor && len(local.Authors) == 0 && d.Selected != nil && !isbnMatch {
			d.Selected = nil
			d.ReviewState = catalog.ReviewNeedsReview
		}
		if d.Selected != nil {
			return d
		}
		if i == 0 || (len(d.Candidates) > 0 && d.Candidates[0].Score > best.Candidates[0].Score) {
			best = d
		}
	}
	return best
}

var seriesLabelSuffix = regexp.MustCompile(`(?i)\s+\([^()]*[[:alpha:]][^()]*\s+(?:book|volume)\s+[0-9]{1,3}\)\s*$`)

// Folder names are search hints, never persisted as author metadata without a
// provider result that corroborates the hint and the book title.
func folderAuthorHints(local catalog.InferredBook) []string {
	if len(local.Authors) > 0 {
		return nil
	}
	parts := strings.Split(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")
	var hints []string
	seen := map[string]bool{}
	for i := len(parts) - 2; i >= 0 && len(hints) < 3; i-- {
		hint := strings.TrimSpace(parts[i])
		key := strings.ToLower(hint)
		if len(strings.Fields(hint)) < 2 || strings.ContainsAny(hint, "0123456789") || seen[key] {
			continue
		}
		seen[key] = true
		hints = append(hints, hint)
	}
	return hints
}

func corroboratedFolderAuthor(local catalog.InferredBook, candidates []ScoredCandidate) []string {
	for _, hint := range folderAuthorHints(local) {
		for _, c := range candidates {
			authorMatch := false
			for _, author := range c.Authors {
				if normalizeText(author, false) == normalizeText(hint, false) {
					authorMatch = true
				}
			}
			if !authorMatch {
				continue
			}
			for _, title := range SearchTitles(local.Title) {
				variant := local
				variant.Title = title
				if sameBookTitle(variant, c.Candidate) {
					return []string{hint}
				}
			}
		}
	}
	return local.Authors
}
