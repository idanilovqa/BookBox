package metadata

import (
	"bookbox.local/server/internal/catalog"
	"regexp"
	"strings"
)

var chainedSeriesLabel = regexp.MustCompile(`(?i)^([^0-9]+?)\s+(?:(?:book|volume)\s+|#)?([0-9]{1,3}(?:\.[0-9]{1,3})?)(?:\s+[-–—]\s+|:\s+)(.+)$`)
var chainedSeparator = regexp.MustCompile(`\s+[-–—]\s+|:\s+`)
var trilogyDescriptor = regexp.MustCompile(`(?i)\s+trilogy$`)

// Accept Series NN - Title with an optional corroborated Author - prefix.
// Keep the original title among search queries; provider identity still decides
// the canonical title. A conflicting author prefix must not become a series.
func chainedTitleReference(local catalog.InferredBook) (title, series, position string) {
	rest := strings.TrimSpace(local.Title)
	if split := chainedSeparator.FindStringIndex(rest); split != nil {
		author := rest[:split[0]]
		knownAuthors := append(append([]string(nil), local.Authors...), strings.Split(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/")...)
		for _, known := range knownAuthors {
			if normalizeText(author, true) != "" && normalizeText(author, true) == normalizeText(known, true) {
				rest = rest[split[1]:]
				break
			}
		}
	}
	m := chainedSeriesLabel.FindStringSubmatch(rest)
	if m == nil || mediaPartPrefix.MatchString(rest) || chainedSeparator.MatchString(m[1]) {
		return
	}
	series = strings.TrimSpace(trilogyDescriptor.ReplaceAllString(strings.TrimSpace(m[1]), ""))
	if series == "" {
		return "", "", ""
	}
	return strings.TrimSpace(m[3]), series, normalizedPosition(m[2])
}

// A missing middle initial is a common edition variation. Conflicting initials
// or different full names remain separate candidates.
func equivalentEditionAuthors(left, right []string) bool {
	if strings.Join(normalizedAuthors(left), "\x00") == strings.Join(normalizedAuthors(right), "\x00") {
		return len(left) > 0
	}
	if len(left) != 1 || len(right) != 1 {
		return false
	}
	parts := func(name string) (string, string, int) {
		var full, initials []string
		for _, word := range strings.Fields(normalizeText(name, true)) {
			if len([]rune(word)) == 1 {
				initials = append(initials, word)
			} else {
				full = append(full, word)
			}
		}
		return strings.Join(full, " "), strings.Join(initials, " "), len(full)
	}
	a, ai, an := parts(left[0])
	b, bi, bn := parts(right[0])
	return an >= 2 && bn >= 2 && a == b && ((ai == "" && bi != "") || (bi == "" && ai != ""))
}
