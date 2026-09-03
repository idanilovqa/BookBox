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

// A numbered filename is a track only when every file repeats its enclosing
// book title and the sequence is complete. M4B files retain separate-book rules.
func namedTrackSequenceTitle(tree Tree, audio []catalog.SourceFile) string {
	if len(audio) < 2 || tree.ScopePath == "" {
		return ""
	}
	title := strings.TrimSpace(namedBookPrefix.ReplaceAllString(path.Base(tree.ScopePath), ""))
	if title == "" {
		return ""
	}
	seen := map[int]bool{}
	for _, file := range audio {
		if path.Dir(file.RelativePath) != tree.ScopePath || strings.EqualFold(path.Ext(file.RelativePath), ".m4b") {
			return ""
		}
		name := strings.TrimSuffix(path.Base(file.RelativePath), path.Ext(file.RelativePath))
		m := namedTrackSuffix.FindStringSubmatch(name)
		if m == nil {
			return ""
		}
		stem := strings.TrimSpace(namedBookPrefix.ReplaceAllString(m[1], ""))
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
