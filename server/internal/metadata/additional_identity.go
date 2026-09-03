package metadata

import (
	"bookbox.local/server/internal/catalog"
	"path"
	"regexp"
	"strings"
)

var abbreviatedAuthorPrefix = regexp.MustCompile(`^([^,:]+),\s*([[:alpha:]])\.?\s*:\s*(.+)$`)

func abbreviatedAuthorTitle(local catalog.InferredBook) string {
	m := abbreviatedAuthorPrefix.FindStringSubmatch(local.Title)
	if m == nil {
		return ""
	}
	names := append(append([]string(nil), local.Authors...), strings.Split(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")...)
	for _, name := range names {
		words := strings.Fields(normalizeText(name, false))
		if len(words) < 2 || normalizeText(m[1], false) != strings.Join(words[1:], " ") {
			continue
		}
		if strings.EqualFold(m[2], string([]rune(words[0])[0])) {
			return strings.TrimSpace(m[3])
		}
	}
	return ""
}

// Short series tags can hide a fuller Author - Series NN - Title folder.
// Both the author folder and the series ordinal must corroborate that title.
func chainedFolderReference(local catalog.InferredBook) (title, series, position string) {
	name, number := shortSeriesReference(local.Title)
	if name == "" {
		return
	}
	folder := strings.Trim(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")
	leaf := path.Base(folder)
	author, _, ok := strings.Cut(leaf, " - ")
	if !ok {
		return
	}
	authorFound := false
	for _, part := range strings.Split(path.Dir(folder), "/") {
		if normalizeText(part, true) == normalizeText(author, true) {
			authorFound = true
			break
		}
	}
	if !authorFound {
		return
	}
	evidence := local
	evidence.Title = leaf
	evidence.Authors = []string{author}
	bookTitle, bookSeries, bookNumber := chainedTitleReference(evidence)
	if bookTitle == "" || !seriesNameMatches(name, bookSeries) || number != bookNumber {
		return
	}
	return bookSeries + ": " + bookTitle, bookSeries, bookNumber
}

// A fuller Author - Title folder can disambiguate an abbreviated album tag.
// Keep it as a search alternative and require the provider to match it exactly.
func fullerAuthorFolderTitle(local catalog.InferredBook) string {
	leaf := path.Base(strings.ReplaceAll(local.SourceFolder, "\\", "/"))
	author, rest, ok := strings.Cut(leaf, " - ")
	if !ok {
		return ""
	}
	matched := false
	for _, known := range local.Authors {
		if normalizeText(author, true) == normalizeText(known, true) {
			matched = true
			break
		}
	}
	if !matched {
		return ""
	}
	title := SearchTitle(rest)
	short := normalizeText(SearchTitle(local.Title), false)
	if len(strings.Fields(short)) < 2 || !strings.HasPrefix(normalizeText(title, false), short+" ") {
		return ""
	}
	return title
}
