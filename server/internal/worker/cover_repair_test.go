package worker

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/metadata"
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

type coverRepairRepo struct {
	enrichRepositoryFake
	url string
	err error
}

func (r *coverRepairRepo) FillConfirmedCoverURL(_ context.Context, _, _ uuid.UUID, _ time.Time, url string) error {
	r.url = url
	return r.err
}
func TestConfirmedCoverRepairPreservesIdentity(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		id, mid := uuid.New(), uuid.New()
		r := &coverRepairRepo{enrichRepositoryFake: enrichRepositoryFake{detail: catalog.BookDetail{Book: catalog.Book{ID: id, Title: "White Sand Volume 2", Authors: []string{"Brandon Sanderson"}, ReviewState: catalog.ReviewConfirmed, ConfirmedMatchID: &mid}, Matches: []catalog.MetadataMatch{{ID: mid, BookID: id, Provider: metadata.ProviderOpenLibrary, Confirmed: true}}}}}
		if conflict {
			r.err = catalog.ErrConflict
		}
		candidates := []metadata.ScoredCandidate{{Candidate: metadata.Candidate{Provider: metadata.ProviderOpenLibrary, Title: "White Sand Volume 1", Authors: []string{"Brandon Sanderson"}, CoverURL: "wrong"}}, {Candidate: metadata.Candidate{Provider: metadata.ProviderOpenLibrary, Title: "White Sand Volume 2", Authors: []string{"Brandon Sanderson"}, CoverURL: "https://covers.openlibrary.org/b/id/2-L.jpg"}}}
		h := EnrichBookHandler{Repo: r, Matcher: matcherFake{decision: metadata.Decision{Candidates: candidates}}}
		err := h.Handle(t.Context(), catalog.WorkerJob{Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": id.String()}})
		if conflict {
			if !errors.Is(err, catalog.ErrConflict) || len(r.jobs) != 0 {
				t.Fatalf("stale update: %v %+v", err, r.jobs)
			}
			continue
		}
		if err != nil || r.url != "https://covers.openlibrary.org/b/id/2-L.jpg" || r.saved || r.confirmed || len(r.jobs) != 1 {
			t.Fatalf("repair: %+v %v", r, err)
		}
	}
}
