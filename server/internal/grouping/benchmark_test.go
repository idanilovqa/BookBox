package grouping

import (
	"testing"

	"bookbox.local/server/internal/catalog"
)

func TestCompareInferenceWithLabelsReportsFieldLevelDifferences(t *testing.T) {
	books := []catalog.InferredBook{{
		GroupingKey: "expanse-1", SourceFolder: "The Expanse 01 - Leviathan Wakes",
		Title: "Leviathan Wakes", Authors: []string{"James S. A. Corey"},
		Series: "The Expanse", SeriesPosition: "01",
	}}
	report := CompareInferenceWithLabels(books, []BenchmarkLabel{
		{GroupingKey: "expanse-1", Title: "Leviathan Wakes", Authors: []string{"James S. A. Corey"}, Series: "The Expanse", SeriesPosition: "1"},
		{GroupingKey: "missing", Title: "Missing"},
	})
	if report.Labels != 2 || report.Located != 1 || report.TitleMatches != 1 ||
		report.AuthorMatches != 1 || report.SeriesMatches != 0 || len(report.Disagreements) != 2 {
		t.Fatalf("report=%#v", report)
	}
}
