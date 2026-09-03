package grouping

import (
	"bookbox.local/server/internal/catalog"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var namedTrackSuffix = regexp.MustCompile(`(?i)^(.+?)\s*[-_ ]+\s*([0-9]{1,3})\s*(?:\(enhanced\))?\s*$`)
var namedBookPrefix = regexp.MustCompile(`(?i)^(?:\(\s*#?[0-9]{1,3}\s*\)\s*[-_ ]*|[a-z]{1,4}[0-9]{1,3}\s*[-_]\s*)`)

var trackDateSuffix = regexp.MustCompile(`\s+\((?:18|19|20)[0-9]{2}\)$`)
var bookYearPrefix = regexp.MustCompile(`^(?:18|19|20)[0-9]{2}\s+-\s+`)
var bookEditionSuffix = regexp.MustCompile(`(?i)\s+\[(?:unabridged|abridged)\]$`)

// A numbered filename is a track only when every file repeats its enclosing
// book title and the sequence is complete. M4B files retain separate-book rules.
func namedTrackSequenceTitle(tree Tree, audio []catalog.SourceFile) string {
	if len(audio) < 2 || tree.ScopePath == "" {
		return ""
	}
	title := strings.TrimSpace(namedBookPrefix.ReplaceAllString(path.Base(tree.ScopePath), ""))
	title = bookEditionSuffix.ReplaceAllString(bookYearPrefix.ReplaceAllString(title, ""), "")
	if title == "" {
		return ""
	}
	seen := map[int]bool{}
	for _, file := range audio {
		if path.Dir(file.RelativePath) != tree.ScopePath || strings.EqualFold(path.Ext(file.RelativePath), ".m4b") {
			return ""
		}
		name := strings.TrimSuffix(path.Base(file.RelativePath), path.Ext(file.RelativePath))
		name = trackDateSuffix.ReplaceAllString(name, "")
		m := namedTrackSuffix.FindStringSubmatch(name)
		if m == nil {
			return ""
		}
		stem := strings.TrimSpace(namedBookPrefix.ReplaceAllString(m[1], ""))
		for _, author := range strings.Split(path.Dir(tree.ScopePath), "/") {
			if len(strings.Fields(author)) < 2 {
				continue
			}
			if len(stem) > len(author) && strings.EqualFold(stem[:len(author)], author) {
				suffix := stem[len(author):]
				if strings.HasPrefix(suffix, " ") || strings.HasPrefix(suffix, "-") {
					stem = strings.TrimLeft(suffix, " -_–—")
				}
			}
		}
		if normalizeComparableTitle(stem) != normalizeComparableTitle(title) {
			return ""
		}
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 1 || n > len(audio) || seen[n] {
			return ""
		}
		seen[n] = true
	}
	return title
}
