package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"errors"
)

// SameWorkCover preserves confirmed identity and provider provenance. In
// particular volume numbers, conflicting authors and subtitles stay distinct.
func SameWorkCover(book catalog.InferredBook, provider string, candidates []ScoredCandidate) string {
	for _, c := range candidates {
		if c.Provider != provider || c.CoverURL == "" || !sameBookTitle(book, c.Candidate) || !equivalentEditionAuthors(book.Authors, c.Authors) {
			continue
		}
		if normalizeText(book.Subtitle, false) != normalizeText(c.Subtitle, false) {
			continue
		}
		if book.SeriesPosition != "" && c.SeriesPosition != "" && normalizedPosition(book.SeriesPosition) != normalizedPosition(c.SeriesPosition) {
			continue
		}
		return c.CoverURL
	}
	return ""
}

// MatchCover stops on a compatible image, rather than on a metadata-only hit.
// It examines all returned editions, without the review UI's 20-item limit.
func (m *Matcher) MatchCover(ctx context.Context, book catalog.InferredBook, provider string) (string, error) {
	if m == nil {
		return "", nil
	}
	var failures []error
	failed := map[int]bool{}
	for _, q := range searchQueries(book) {
		for i, p := range m.providers {
			if failed[i] {
				continue
			}
			candidates, err := p.Search(ctx, q)
			if err != nil {
				failed[i] = true
				failures = append(failures, err)
				continue
			}
			for _, c := range candidates {
				if url := SameWorkCover(book, provider, []ScoredCandidate{{Candidate: c}}); url != "" {
					return url, nil
				}
			}
		}
	}
	return "", errors.Join(failures...)
}
