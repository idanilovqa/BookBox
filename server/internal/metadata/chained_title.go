package metadata

import (
	"bookbox.local/server/internal/catalog"
	"regexp"
	"strings"
)

var chainedSeriesLabel = regexp.MustCompile(`(?i)^([^0-9]+?)\s+(?:book\s+|volume\s+)?([0-9]{1,3}(?:\.[0-9]{1,3})?)\s+-\s+(.+)$`)

// Strip a leading author only when it agrees with the recorded attribution.
func chainedTitleReference(local catalog.InferredBook) (title, series, position string) {
	author, rest, ok := strings.Cut(local.Title, " - ")
	if !ok {
		return
	}
	matched := false
	for _, known := range local.Authors {
		if normalizeText(author, true) == normalizeText(known, true) {
			matched = true
			break
		}
	}
	if !matched {
		return
	}
	m := chainedSeriesLabel.FindStringSubmatch(rest)
	if m == nil || mediaPartPrefix.MatchString(rest) {
		return
	}
	return strings.TrimSpace(m[3]), strings.TrimSpace(m[1]), normalizedPosition(m[2])
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
