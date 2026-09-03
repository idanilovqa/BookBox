package metadata

import (
	"bookbox.local/server/internal/catalog"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Each interpretation adds a search alternative. It never overwrites source
// tags or turns a partial/contains match into a confirmed provider identity.
type TitleInterpretation struct {
	Title, Series, SeriesPosition, Rule string
	SearchOnly                          bool
}

var marketplaceIdentifier = regexp.MustCompile(`(?i)\s+\[(?:B[0-9A-Z]{9}|97[89][0-9]{10})\]\s*$`)
var trailingOrdinal = regexp.MustCompile(`\s+\(\s*#([0-9]{1,3}(?:\.[0-9]{1,3})?)\s*\)\s*$`)
var marketEdition = regexp.MustCompile(`(?i)\s+[\[(](?:USA|US|UK|unabridged|abridged)[\])]\s*$`)
var datedTrack = regexp.MustCompile(`^(.+?)\s+([0-9]{1,3})\s+\((?:18|19|20)[0-9]{2}\)$`)
var datedFolder = regexp.MustCompile(`^(?:18|19|20)[0-9]{2}\s+-\s+(.+)$`)
var transposedBookLabel = regexp.MustCompile(`(?i)^(.+?):\s+(.+?)\s+book\s*,\s*([0-9]{1,3})$`)
var explicitVolumeLabel = regexp.MustCompile(`(?i)^(.+?)\s*[:~]?\s+volume\s+(one|two|three|four|five|six|seven|eight|nine|ten|[0-9]{1,3})(?:\s+\[[0-9]{1,3}\])?$`)
var romanSeriesTitle = regexp.MustCompile(`^(.+?)\s+([IVX]+):\s+(.+)$`)

func identityAuthorNames(local catalog.InferredBook) []string {
	names := append([]string(nil), local.Authors...)
	names = append(names, strings.Split(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")...)
	var result []string
	for _, name := range names {
		if len(strings.Fields(name)) < 2 || strings.ContainsAny(name, "0123456789()[]/") {
			continue
		}
		result = append(result, name)
		words := strings.Fields(name)
		if !strings.Contains(name, ",") && len(words) >= 2 {
			result = append(result, words[len(words)-1]+", "+strings.Join(words[:len(words)-1], " "))
		}
	}
	return result
}

var authorTitleBoundary = regexp.MustCompile(`\s+[-–—]\s*|:\s+|\s{2,}`)

func withoutCorroboratedAuthor(title string, local catalog.InferredBook) string {
	for _, split := range authorTitleBoundary.FindAllStringIndex(title, -1) {
		prefix := normalizeText(title[:split[0]], true)
		for _, author := range identityAuthorNames(local) {
			if prefix != "" && prefix == normalizeText(author, true) {
				return strings.TrimSpace(title[split[1]:])
			}
		}
	}
	return ""
}

// TitleCleanupInterpretations explains additional evidence-backed title rules.
// Folder parsing deliberately avoids folderAuthorHints (which calls title
// parsing itself). Nested folder labels preserve only the innermost series.
func TitleCleanupInterpretations(local catalog.InferredBook) []TitleInterpretation {
	var result []TitleInterpretation
	add := func(title, series, position, rule string) {
		if title != "" {
			result = append(result, TitleInterpretation{Title: title, Series: series, SeriesPosition: position, Rule: rule})
		}
	}
	if clean := strings.TrimSpace(marketplaceIdentifier.ReplaceAllString(local.Title, "")); clean != local.Title {
		add(clean, "", "", "marketplace-identifier")
		if strings.Contains(clean, "_ ") {
			add(strings.ReplaceAll(clean, "_ ", ": "), "", "", "filename-subtitle-separator")
		}
	}
	if m := transposedBookLabel.FindStringSubmatch(local.Title); m != nil {
		add(m[1], m[2], normalizedPosition(m[3]), "transposed-book-label")
	}
	if m := explicitVolumeLabel.FindStringSubmatch(local.Title); m != nil {
		number := strings.ToLower(m[2])
		words := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
		for i, word := range words {
			if number == word {
				number = strconv.Itoa(i + 1)
			}
		}
		number = normalizedPosition(number)
		series := strings.TrimSpace(m[1])
		add(series+" Volume "+number, series, number, "explicit-volume-number")
	}
	if m := trailingOrdinal.FindStringSubmatch(local.Title); m != nil {
		add(strings.TrimSpace(trailingOrdinal.ReplaceAllString(local.Title, "")), "", normalizedPosition(m[1]), "trailing-series-number")
	}
	if clean := strings.TrimSpace(marketEdition.ReplaceAllString(local.Title, "")); clean != local.Title {
		series, position := "", ""
		if m := colonSeries.FindStringSubmatch(clean); m != nil {
			series, position = m[1], normalizedPosition(m[2])
		}
		if m := explicitSeries.FindStringSubmatch(clean); m != nil {
			series, position = m[1], normalizedPosition(m[2])
		}
		add(SearchTitle(clean), series, position, "edition-or-market-label")
	}
	if m := romanSeriesTitle.FindStringSubmatch(local.Title); m != nil {
		numerals := []string{"I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X", "XI", "XII", "XIII", "XIV", "XV", "XVI", "XVII", "XVIII", "XIX", "XX"}
		for n, r := range numerals {
			if m[2] == r && len(strings.Fields(m[1])) >= 2 {
				add(m[3], m[1], strconv.Itoa(n+1), "roman-series-number")
			}
		}
	}
	if title := withoutCorroboratedAuthor(local.Title, local); title != "" {
		add(title, "", "", "corroborated-author-prefix")
		if m := datedTrack.FindStringSubmatch(title); m != nil {
			leaf := path.Base(strings.ReplaceAll(local.SourceFolder, "\\", "/"))
			leaf = marketEdition.ReplaceAllString(leaf, "")
			if folder := datedFolder.FindStringSubmatch(leaf); folder != nil && normalizeText(folder[1], false) == normalizeText(m[1], false) {
				add(m[1], "", "", "track-title-agrees-with-book-folder")
			}
		}
	}
	leaf := path.Base(strings.ReplaceAll(local.SourceFolder, "\\", "/"))
	if rest := withoutCorroboratedAuthor(leaf, local); rest != "" {
		if series, number := shortSeriesReference(local.Title); series != "" {
			result = append(result, TitleInterpretation{Title: SearchTitle(rest), Series: series, SeriesPosition: number, Rule: "series-reference-with-author-title-folder", SearchOnly: true})
		}
		folderBook := local
		folderBook.Title = leaf
		if title, series, position := chainedTitleReference(folderBook); title != "" &&
			(normalizeText(local.Title, false) == normalizeText(series, false) || normalizeText(local.Title, false) == normalizeText(title, false)) {
			add(title, series, position, "explicit-book-folder-over-series-tag")
		}
		if normalizeText(local.Title, false) == normalizeText(leaf, false) {
			var series, number string
			for depth := 0; depth < 4; depth++ {
				m := chainedSeriesLabel.FindStringSubmatch(rest)
				if m == nil {
					break
				}
				series, number, rest = strings.TrimSpace(m[1]), normalizedPosition(m[2]), strings.TrimSpace(m[3])
			}
			if series != "" {
				add(rest, series, number, "nested-series-folder")
				add(series+": "+rest, series, number, "nested-series-provider-title")
			}
		}
	}
	return result
}
