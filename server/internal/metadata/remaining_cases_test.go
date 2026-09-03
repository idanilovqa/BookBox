package metadata

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRemainingUserTitleCorpus(t *testing.T) {
	var cases []struct {
		Number           int
		Title            string
		Authors          []string
		Folder, Expected string
		ExpectedAuthors  []string
		Grouped          bool
	}
	raw, err := os.ReadFile("testdata/user_title_cases_209_276.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 68 {
		t.Fatalf("missing cases %d", len(cases))
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.Number), func(t *testing.T) {
			if c.Grouped {
				return
			} // Audited as an already-grouped chapter, not a book title.
			local := catalog.InferredBook{Title: c.Title, Authors: c.Authors, SourceFolder: c.Folder}
			p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
				if normalizeText(q.Title, false) != normalizeText(c.Expected, false) {
					return nil, nil
				}
				return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "work", Title: c.Expected, Authors: c.ExpectedAuthors}}, nil
			})
			d, err := NewMatcher(p).Match(t.Context(), local)
			if err != nil || d.Selected == nil {
				t.Fatalf("not identified: %q -> %q (%v)", c.Title, c.Expected, err)
			}
		})
	}
}
func TestMalformedSubjectsDoNotBlockIdentity(t *testing.T) {
	subjects := []string{"Evolution.\x1e 0", "Evolution", strings.Repeat("x", 501)}
	p := queryProvider(func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "work", Title: "The Blind Watchmaker", Authors: []string{"Richard Dawkins"}, Subjects: subjects}}, nil
	})
	d, err := NewMatcher(p).Match(t.Context(), catalog.InferredBook{Title: "The Blind Watchmaker", Authors: []string{"Richard Dawkins"}})
	if err != nil || d.Selected == nil {
		t.Fatalf("identity lost: %+v %v", d, err)
	}
	for _, subject := range d.Selected.Subjects {
		if strings.ContainsRune(subject, '\x1e') || len(subject) > 500 {
			t.Fatalf("invalid optional subject retained: %q", subject)
		}
	}
}

func TestMarketplaceIdentifierAndUnderscoreSubtitle(t *testing.T) {
	p := queryProvider(func(_ context.Context, q Query) ([]Candidate, error) {
		if q.Title != "Armageddon: What the Bible Really Says About the End" {
			return nil, nil
		}
		return []Candidate{{Provider: ProviderOpenLibrary, ProviderID: "work", Title: "Armageddon", Subtitle: "What the Bible Really Says About the End", Authors: []string{"Bart D. Ehrman"}}}, nil
	})
	b := catalog.InferredBook{Title: "Armageddon_ What the Bible Really Says About the End [B0B4V1MRGK]", SourceFolder: "Bart Ehrman/Armageddon [B0B4V1MRGK]"}
	d, err := NewMatcher(p).Match(t.Context(), b)
	if err != nil || d.Selected == nil {
		t.Fatalf("marketplace filename not found: %+v %v", d, err)
	}
}
