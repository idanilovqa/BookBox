package grouping

import (
	"sort"
	"strings"

	"bookbox.local/server/internal/catalog"
)

// BenchmarkLabel is a dev-only reference identity, typically produced by a
// reviewed sample or an LLM pass over folder/file names. Runtime inference
// never reads these labels.
type BenchmarkLabel struct {
	GroupingKey    string
	SourceFolder   string
	Title          string
	Authors        []string
	Series         string
	SeriesPosition string
}

type BenchmarkDisagreement struct {
	Identity string
	Fields   []string
}

type BenchmarkReport struct {
	Labels        int
	Located       int
	TitleMatches  int
	AuthorMatches int
	SeriesMatches int
	Disagreements []BenchmarkDisagreement
}

// CompareInferenceWithLabels measures deterministic inference against an
// external reference set without introducing an LLM or network dependency in
// the catalog runtime.
func CompareInferenceWithLabels(books []catalog.InferredBook, labels []BenchmarkLabel) BenchmarkReport {
	byKey := make(map[string]catalog.InferredBook, len(books))
	for _, book := range books {
		byKey[benchmarkIdentity(book.GroupingKey, book.SourceFolder)] = book
	}
	report := BenchmarkReport{Labels: len(labels)}
	for _, label := range labels {
		identity := benchmarkIdentity(label.GroupingKey, label.SourceFolder)
		book, ok := byKey[identity]
		if !ok {
			report.Disagreements = append(report.Disagreements, BenchmarkDisagreement{
				Identity: identity, Fields: []string{"missing"},
			})
			continue
		}
		report.Located++
		fields := make([]string, 0, 3)
		if normalize(book.Title) == normalize(label.Title) {
			report.TitleMatches++
		} else {
			fields = append(fields, "title")
		}
		if normalizedBenchmarkStrings(book.Authors) == normalizedBenchmarkStrings(label.Authors) {
			report.AuthorMatches++
		} else {
			fields = append(fields, "authors")
		}
		if normalize(book.Series) == normalize(label.Series) &&
			normalize(book.SeriesPosition) == normalize(label.SeriesPosition) {
			report.SeriesMatches++
		} else {
			fields = append(fields, "series")
		}
		if len(fields) > 0 {
			report.Disagreements = append(report.Disagreements, BenchmarkDisagreement{
				Identity: identity, Fields: fields,
			})
		}
	}
	sort.Slice(report.Disagreements, func(i, j int) bool {
		return report.Disagreements[i].Identity < report.Disagreements[j].Identity
	})
	return report
}

func benchmarkIdentity(groupingKey, sourceFolder string) string {
	if groupingKey = strings.TrimSpace(groupingKey); groupingKey != "" {
		return "key:" + groupingKey
	}
	return "folder:" + strings.TrimSpace(sourceFolder)
}

func normalizedBenchmarkStrings(values []string) string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = normalize(value); value != "" {
			normalized = append(normalized, value)
		}
	}
	sort.Strings(normalized)
	return strings.Join(normalized, "\x00")
}
