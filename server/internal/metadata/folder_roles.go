package metadata

import (
	"bookbox.local/server/internal/catalog"
	"strings"
)

func narratorFromFolder(folder string) string {
	for _, prefix := range []string{"narrated by ", "read by ", "narrator - "} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(folder)), prefix) {
			return strings.TrimSpace(strings.TrimSpace(folder)[len(prefix):])
		}
	}
	return ""
}

func candidateWithNarrators(candidate Candidate, local catalog.InferredBook) Candidate {
	var narrators []string
	seen := map[string]bool{}
	for _, part := range strings.Split(strings.ReplaceAll(local.SourceFolder, "\\", "/"), "/") {
		name := narratorFromFolder(part)
		if name != "" && !seen[strings.ToLower(name)] {
			narrators = append(narrators, name)
			seen[strings.ToLower(name)] = true
		}
	}
	if len(narrators) > 0 {
		candidate.Narrators = narrators
	} else if len(candidate.Narrators) == 0 {
		candidate.Narrators = append([]string(nil), local.Narrators...)
	}
	return candidate
}
