package worker

import (
	"bookbox.local/server/internal/catalog"
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

type staleBookRepo struct {
	enrichRepositoryFake
	loadErr error
}

func (r *staleBookRepo) GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error) {
	return r.detail, r.loadErr
}
func TestEnrichmentRetiredJobsFinishWithoutRetry(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		id := uuid.New()
		now := time.Now()
		repo := &staleBookRepo{enrichRepositoryFake: enrichRepositoryFake{detail: catalog.BookDetail{Book: catalog.Book{ID: id, Title: "Empire in Black and Gold", MissingAt: &now}}}}
		if deleted {
			repo.loadErr = catalog.ErrNotFound
		}
		matcher := &recordingMatcherFake{}
		err := (&EnrichBookHandler{Repo: repo, Matcher: matcher}).Handle(t.Context(), catalog.WorkerJob{Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": id.String()}})
		if err != nil {
			t.Fatalf("obsolete job still retried: %v", err)
		}
		if matcher.calls != 0 || repo.saved || repo.confirmed || len(repo.jobs) > 0 {
			t.Fatal("obsolete job performed enrichment")
		}
	}
}
func TestEnrichmentStillRetriesDatabaseFailures(t *testing.T) {
	repo := &staleBookRepo{loadErr: errors.New("database unavailable")}
	err := (&EnrichBookHandler{Repo: repo, Matcher: &recordingMatcherFake{}}).Handle(t.Context(), catalog.WorkerJob{Kind: catalog.JobEnrichBook, Payload: map[string]any{"bookId": uuid.NewString()}})
	if err == nil {
		t.Fatal("database failure hidden")
	}
}
