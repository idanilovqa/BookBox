package metadata

import (
	"bookbox.local/server/internal/catalog"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var shortSeriesPattern = regexp.MustCompile(`(?i)^(.+?)\s+(?:book\s+|volume\s+|#)?([0-9]{1,3}(?:\.[0-9]{1,3})?)$`)

func shortSeriesReference(title string) (string, string) {
	m := shortSeriesPattern.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", ""
	}
	name := strings.TrimSpace(m[1])
	normalized := normalizeText(name, false)
	switch normalized {
	case "book", "volume", "part", "chapter", "track", "disc", "cd", "the", "a":
		return "", ""
	}
	if len([]rune(normalized)) < 4 || strings.ContainsAny(normalized, "0123456789") {
		return "", ""
	}
	return name, normalizedPosition(m[2])
}
func normalizedPosition(value string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || n < 0 {
		return ""
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}
func seriesNameMatches(short, full string) bool {
	a, b := normalizeText(short, false), normalizeText(full, false)
	return a != "" && (a == b || strings.HasPrefix(b, a+" "))
}
func folderSeriesReference(local catalog.InferredBook) (title, series, position string) {
	if title, series, position = chainedFolderReference(local); title != "" {
		return
	}
	name, number := shortSeriesReference(local.Title)
	if name == "" {
		return
	}
	folder := strings.Trim(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")
	leafName := path.Base(folder)
	if mediaPartPrefix.MatchString(leafName) {
		return
	}
	leaf := numberedSeriesTitle.FindStringSubmatch(leafName)
	if leaf == nil || normalizedPosition(leaf[1]) != number {
		return
	}
	parent := path.Base(path.Dir(folder))
	if author, rest, ok := strings.Cut(parent, " - "); ok {
		for _, known := range local.Authors {
			if normalizeText(author, true) == normalizeText(known, true) {
				parent = rest
				break
			}
		}
	}
	if !seriesNameMatches(name, parent) {
		return
	}
	title = SearchTitle(leaf[2])
	if title == "" || mediaPartPrefix.MatchString(title) {
		return "", "", ""
	}
	return title, parent, number
}
func localSearchTitles(local catalog.InferredBook) []string {
	titles := SearchTitles(local.Title)
	if title := abbreviatedAuthorTitle(local); title != "" {
		titles = append(titles, title)
	}
	if title, _, _ := chainedTitleReference(local); title != "" {
		titles = append(titles, SearchTitles(title)...)
		if !strings.HasPrefix(strings.ToLower(title), "the ") {
			titles = append(titles, "The "+title)
		}
	}
	if title, _, _ := folderSeriesReference(local); title != "" {
		for _, existing := range titles {
			if normalizeText(existing, false) == normalizeText(title, false) {
				return titles
			}
		}
		titles = append(titles, title)
	}
	return titles
}

// ResolveFolderSeriesReference replaces an abbreviated local tag only when the
// enclosing series and the book-folder number agree with it.
func ResolveFolderSeriesReference(local catalog.InferredBook) catalog.InferredBook {
	if local.GroupingLocked || local.MetadataSource == catalog.MetadataConfirmed || local.ReviewState == catalog.ReviewConfirmed {
		return local
	}
	title, series, position := folderSeriesReference(local)
	if title == "" {
		return local
	}
	local.Title = title
	if local.Series == "" {
		local.Series = series
	}
	if local.SeriesPosition == "" && seriesNameMatches(local.Series, series) {
		local.SeriesPosition = position
	}
	return local
}

// Only provider-owned series evidence is accepted here, before local series
// hints are attached to candidates. The ordinal is never guessed from titles.
func seriesReferenceDecision(local catalog.InferredBook, raw []Candidate) Decision {
	name, position := shortSeriesReference(local.Title)
	if name == "" {
		return Decision{}
	}
	authors := append(append([]string(nil), local.Authors...), folderAuthorHints(local)...)
	var matched []ScoredCandidate
	for _, candidate := range raw {
		series, number := InferSeriesIdentity(catalog.InferredBook{Title: candidate.Title, Series: candidate.Series, SeriesPosition: candidate.SeriesPosition})
		if !seriesNameMatches(name, series) || normalizedPosition(number) != position {
			continue
		}
		authorMatch := false
		for _, a := range authors {
			for _, b := range candidate.Authors {
				if normalizeText(a, true) != "" && normalizeText(a, true) == normalizeText(b, true) {
					authorMatch = true
				}
			}
		}
		if !authorMatch {
			continue
		}
		candidate.Series, candidate.SeriesPosition = series, position
		candidate = candidateWithNarrators(candidate, local)
		matched = append(matched, ScoredCandidate{Candidate: candidate, Score: 0.99})
	}
	if len(matched) == 0 {
		return Decision{}
	}
	comparison := local
	comparison.Title = matched[0].Title
	comparison.Authors = matched[0].Authors
	return searchDecision(comparison, matched)
}
