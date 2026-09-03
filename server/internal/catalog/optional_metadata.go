package catalog

import (
	"strings"
	"unicode"
)

// Optional provider classification tags must not invalidate a valid identity.
// Discard malformed/oversized tags, bound the list, and leave names strict.
func CleanOptionalMetadata(values []string) []string {
	result := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 500 || strings.IndexFunc(value, unicode.IsControl) >= 0 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == 100 {
			break
		}
	}
	return result
}
