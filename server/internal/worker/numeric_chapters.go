package worker

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/grouping"
	"github.com/google/uuid"
	"path"
	"regexp"
	"strings"
)

var numericChapterFolder = regexp.MustCompile(`^[0-9]{1,3}$`)
var numericChapterFilename = regexp.MustCompile(`^[0-9_. -]+$`)

func numericChapterBookScope(directory string) (string, bool) {
	root := directory
	depth := 0
	for numericChapterFolder.MatchString(path.Base(root)) {
		root = path.Dir(root)
		depth++
	}
	if root == "." || depth < 2 {
		return "", false
	}
	parent := path.Base(path.Dir(root))
	repeated := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(path.Base(root)) + ` \([0-9]{4}\) by .+`)
	return root, repeated.MatchString(parent)
}

// Collapse only numeric chapter trees with counter filenames and no competing
// identity tags. Named files, numeric book titles and M4B stay separate.
func numericChapterScopes(tree grouping.Tree) map[uuid.UUID]string {
	candidates := map[string][]catalog.SourceFile{}
	for _, f := range tree.Files {
		if f.MissingAt != nil || f.FileKind != catalog.FileKindAudio {
			continue
		}
		root, repeatedBook := numericChapterBookScope(path.Dir(f.RelativePath))
		if repeatedBook {
			candidates[root] = append(candidates[root], f)
		}
	}
	result := map[uuid.UUID]string{}
	for root, files := range candidates {
		valid := len(files) > 1
		directories := map[string]bool{}
		authors := map[string]bool{}
		for _, f := range files {
			directories[path.Dir(f.RelativePath)] = true
			for _, key := range []string{"author", "albumArtist"} {
				if value, ok := f.Media[key].(string); ok && strings.TrimSpace(value) != "" {
					authors[strings.ToLower(strings.TrimSpace(value))] = true
				}
			}
			if strings.EqualFold(path.Ext(f.RelativePath), ".m4b") || !numericChapterFilename.MatchString(strings.TrimSuffix(path.Base(f.RelativePath), path.Ext(f.RelativePath))) {
				valid = false
			}
			for _, key := range []string{"album", "title"} {
				if value, ok := f.Media[key].(string); ok && strings.TrimSpace(value) != "" && !numericChapterFilename.MatchString(value) {
					valid = false
				}
			}
		}
		if valid && len(directories) > 1 && len(authors) <= 1 {
			for _, f := range files {
				result[f.ID] = root
			}
		}
	}
	return result
}
