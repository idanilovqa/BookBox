package metadata

import (
	"bookbox.local/server/internal/catalog"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var prefixSeries = regexp.MustCompile(`^\[([^\]]*\p{L}[^\]]*?)\s+([0-9]{1,3}(?:\.[0-9]{1,3})?)\]\s+`)
var colonSeries = regexp.MustCompile(`(?i):\s+([^:()]*\p{L}[^:()]*?)\s*,\s*(?:book|volume)\s+([0-9]{1,3}(?:\.[0-9]{1,3})?)\s*$`)
var explicitSeries = regexp.MustCompile(`(?i)\s+\(([^()]*[[:alpha:]][^()]*?)\s*,?\s+(?:book|volume)\s+([0-9]{1,3}(?:\.[0-9]{1,3})?)\)\s*$`)
var mediaPartPrefix = regexp.MustCompile(`(?i)^(?:cd|disc|disk|part|track|chapter)[\s._-]*[0-9]`)
var numberedSeriesTitle = regexp.MustCompile(`(?i)^(?:\(\s*#?\s*|\[\s*#?\s*|#\s*|[a-z]{1,4})?([0-9]{1,3}(?:\.[0-9]{1,3})?)(?:\)|\])?(?:\s*[-:._]\s*|\s+)(.+)$`)

// InferSeriesIdentity fills only missing series values from explicit title
// annotations or a numbered book within a distinct series folder.
func InferSeriesIdentity(book catalog.InferredBook) (string, string) {
	series, position := strings.TrimSpace(book.Series), strings.TrimSpace(book.SeriesPosition)
	if series != "" && position != "" {
		return series, position
	}
	apply := func(name, number string) (string, string) {
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), ","))
		if series != "" && !strings.EqualFold(series, name) {
			return series, position
		}
		if series == "" {
			series = name
		}
		if position == "" {
			if n, err := strconv.ParseFloat(number, 64); err == nil {
				position = strconv.FormatFloat(n, 'f', -1, 64)
			}
		}
		return series, position
	}
	if m := prefixSeries.FindStringSubmatch(book.Title); m != nil {
		return apply(m[1], m[2])
	}
	if m := colonSeries.FindStringSubmatch(book.Title); m != nil {
		return apply(m[1], m[2])
	}
	if m := explicitSeries.FindStringSubmatch(book.Title); m != nil {
		return apply(m[1], m[2])
	}
	if title, name, number := chainedTitleReference(book); title != "" {
		return apply(name, number)
	}
	if title, name, number := folderSeriesReference(book); title != "" {
		return apply(name, number)
	}
	folder := strings.Trim(strings.ReplaceAll(book.SourceFolder, "\\", "/"), "/")
	leaf := path.Base(folder)
	if mediaPartPrefix.MatchString(leaf) || mediaPartPrefix.MatchString(book.Title) {
		return series, position
	}
	m := numberedSeriesTitle.FindStringSubmatch(leaf)
	parent := path.Base(path.Dir(folder))
	if m != nil {
		title := normalizeText(SearchTitle(book.Title), false)
		folderTitle := normalizeText(SearchTitle(m[2]), false)
		if title == "" || folderTitle == "" || (!strings.Contains(title, folderTitle) && !strings.Contains(folderTitle, title)) {
			return series, position
		}
	}
	if m == nil {
		titles := SearchTitles(book.Title)
		if strings.Contains(normalizeText(leaf, false), normalizeText(titles[len(titles)-1], false)) {
			return series, position
		}
		cleanTitles := SearchTitles(book.Title)
		cleanTitle := cleanTitles[len(cleanTitles)-1]
		if mediaPartPrefix.MatchString(cleanTitle) || strings.EqualFold(cleanTitle, "Introduction") {
			return series, position
		}
		m = numberedSeriesTitle.FindStringSubmatch(book.Title)
		parent = leaf
	}
	if m == nil || parent == "." || parent == "" || len(book.Authors) == 0 {
		return series, position
	}
	for _, author := range book.Authors {
		if authorName := normalizeText(author, false); authorName != "" && strings.Contains(normalizeText(parent, false), authorName) {
			return series, position
		}
	}
	if numberedSeriesTitle.MatchString(parent) || strings.HasPrefix(strings.ToLower(parent), "narrated by ") {
		return series, position
	}
	switch strings.ToLower(parent) {
	case "audiobooks", "audio books", "books", "audio archive", "library", "novels", "fiction", "fantasy", "short stories", "nonfiction", "audiobook":
		return series, position
	}
	// A series folder needs an enclosing author/library folder; a root-level
	// numbered filename alone does not establish membership in a series.
	if !strings.Contains(folder, "/") {
		return series, position
	}
	return apply(parent, m[1])
}

func candidateWithSeries(candidate Candidate, local catalog.InferredBook) Candidate {
	// Provider labels take precedence over inferred folder names.
	evidence := catalog.InferredBook{Title: candidate.Title, Series: candidate.Series, SeriesPosition: candidate.SeriesPosition}
	candidate.Series, candidate.SeriesPosition = InferSeriesIdentity(evidence)
	if len(candidate.Authors) > 0 {
		local.Authors = candidate.Authors
	}
	local.Series, local.SeriesPosition = InferSeriesIdentity(local)
	if candidate.Series == "" {
		candidate.Series = local.Series
		if candidate.SeriesPosition == "" {
			candidate.SeriesPosition = local.SeriesPosition
		}
	} else if strings.EqualFold(candidate.Series, local.Series) && candidate.SeriesPosition == "" {
		candidate.SeriesPosition = local.SeriesPosition
	}
	return candidate
}

// InferSeriesFromFiles recovers the numbered book folder after grouping or a
// confirmed provider title has removed the original number. Conflicts abstain.
func InferSeriesFromFiles(book catalog.InferredBook, files []catalog.SourceFile) (string, string) {
	series, position := InferSeriesIdentity(book)
	if series != "" && position != "" {
		return series, position
	}
	var foundSeries, foundPosition string
	for _, file := range files {
		if file.MissingAt != nil || file.FileKind != catalog.FileKindAudio {
			continue
		}
		evidence := book
		evidence.SourceFolder = path.Dir(strings.ReplaceAll(file.RelativePath, "\\", "/"))
		s, p := InferSeriesIdentity(evidence)
		if s == "" || p == "" {
			continue
		}
		if foundSeries != "" && (!strings.EqualFold(s, foundSeries) || p != foundPosition) {
			return series, position
		}
		foundSeries, foundPosition = s, p
	}
	if foundSeries != "" {
		return foundSeries, foundPosition
	}
	return series, position
}
