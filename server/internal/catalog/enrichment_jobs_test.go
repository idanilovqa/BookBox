package catalog

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEnrichmentTerminalJobsRetryAfterCooldown(t *testing.T) {
	repo, pool := workerJobFixture(t)
	for _, status := range []string{JobDead, JobSucceeded} {
		key := "retry:" + status
		first, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobEnrichBook}, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(t.Context(), "update worker_jobs set status=$2,updated_at=now() where id=$1", first.ID, status); err != nil {
			t.Fatal(err)
		}
		recent, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobEnrichBook}, key)
		if err != nil || recent.ID != first.ID {
			t.Fatalf("recent=%+v err=%v", recent, err)
		}
		if _, err = pool.Exec(t.Context(), "update worker_jobs set updated_at=now()-interval '8 days' where id=$1", first.ID); err != nil {
			t.Fatal(err)
		}
		retried, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobEnrichBook}, key)
		if err != nil || retried.ID == first.ID || retried.Status != JobQueued {
			t.Fatalf("old %s should retry: %+v err=%v", status, retried, err)
		}
	}
}
func TestClaimJobKindsSeparatesMetadataFromScans(t *testing.T) {
	repo, _ := workerJobFixture(t)
	claimer, ok := any(repo).(interface {
		ClaimJobKinds(context.Context, string, time.Time, []string) (WorkerJob, error)
	})
	if !ok {
		t.Fatal("repository cannot isolate enrichment claims")
	}
	for _, kind := range []string{JobScanRoot, JobEnrichBook} {
		if err := repo.Enqueue(t.Context(), WorkerJob{ID: uuid.New(), Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := claimer.ClaimJobKinds(t.Context(), "metadata", time.Now().UTC(), []string{JobEnrichBook, JobPublishCover})
	if err != nil || got.Kind != JobEnrichBook {
		t.Fatalf("metadata claim=%+v err=%v", got, err)
	}
	got, err = claimer.ClaimJobKinds(t.Context(), "structural", time.Now().UTC(), []string{JobScanRoot})
	if err != nil || got.Kind != JobScanRoot {
		t.Fatalf("scan claim=%+v err=%v", got, err)
	}
}

func TestAutomaticConfirmationRejectsChangedBook(t *testing.T) {
	repo, pool := workerJobFixture(t)
	guard, ok := any(repo).(interface {
		ConfirmMatchIfUnchanged(context.Context, uuid.UUID, uuid.UUID, time.Time) error
	})
	if !ok {
		t.Fatal("automatic confirmation cannot validate the book version")
	}
	if err := repo.SyncRoots(t.Context(), []LibraryRoot{{ID: "audio", DisplayName: "Audio"}}); err != nil {
		t.Fatal(err)
	}
	id, matchID := uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `insert into books(id,root_id,source_folder,grouping_key,title,metadata_source,confidence,review_state) values($1,'audio','Book','folder','Old Title','inferred',0.8,'unreviewed')`, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMatches(t.Context(), id, []MetadataMatch{{ID: matchID, BookID: id, Provider: MetadataOpenLibrary, ProviderID: "work", Score: 1, Payload: map[string]any{"title": "Provider Title"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetBook(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "update books set title='New Identity',updated_at=updated_at+interval '1 second' where id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := guard.ConfirmMatchIfUnchanged(t.Context(), id, matchID, before.Book.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale confirmation=%v", err)
	}
	after, err := repo.GetBook(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Book.Title != "New Identity" || after.Book.ConfirmedMatchID != nil {
		t.Fatalf("stale metadata was applied: %+v", after.Book)
	}
	if err := guard.ConfirmMatchIfUnchanged(t.Context(), id, matchID, after.Book.UpdatedAt); err != nil {
		t.Fatalf("current confirmation=%v", err)
	}
}
func TestCoverFailuresRetryAfterCooldown(t *testing.T) {
	repo, pool := workerJobFixture(t)
	key := "cover:" + uuid.NewString()
	first, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobPublishCover}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "update worker_jobs set status='dead' where id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobPublishCover}, key)
	if err != nil || recent.ID != first.ID {
		t.Fatalf("cover retries must respect cooldown: %+v %v", recent, err)
	}
	if _, err := pool.Exec(t.Context(), "update worker_jobs set updated_at=now()-interval '2 days' where id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.EnqueueDeduplicated(t.Context(), WorkerJob{ID: uuid.New(), Kind: JobPublishCover}, key)
	if err != nil || retry.ID == first.ID {
		t.Fatalf("exhausted cover did not retry: %+v %v", retry, err)
	}
}

func TestSyncUnchangedRootsDoesNotWaitForScanLocks(t *testing.T) {
	repo, pool := workerJobFixture(t)
	roots := []LibraryRoot{{ID: "audio", DisplayName: "Audio"}}
	if err := repo.SyncRoots(t.Context(), roots); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(t.Context())
	if _, err := blocker.Exec(t.Context(), "select id from library_roots where id='audio' for update"); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(t.Context(), "select root_id from catalog_generations where root_id='audio' for update"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := repo.SyncRoots(ctx, roots); err != nil {
		t.Fatalf("unchanged startup configuration waited for scan: %v", err)
	}
}

func TestConfirmedCoverURLRejectsStaleIdentity(t *testing.T) {
	repo, pool := workerJobFixture(t)
	if err := repo.SyncRoots(t.Context(), []LibraryRoot{{ID: "audio", DisplayName: "Audio"}}); err != nil {
		t.Fatal(err)
	}
	id, mid := uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `insert into books(id,root_id,source_folder,grouping_key,title,metadata_source,confidence,review_state) values($1,'audio','Book','folder','Reaper Man','inferred',0.8,'unreviewed')`, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMatches(t.Context(), id, []MetadataMatch{{ID: mid, BookID: id, Provider: MetadataOpenLibrary, ProviderID: "work", Score: 1, Payload: map[string]any{"title": "Reaper Man"}}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmMatch(t.Context(), id, mid); err != nil {
		t.Fatal(err)
	}
	b, err := repo.GetBook(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FillConfirmedCoverURL(t.Context(), id, mid, b.Book.UpdatedAt.Add(-time.Second), "https://covers.openlibrary.org/b/id/1-L.jpg"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cover: %v", err)
	}
	if err := repo.FillConfirmedCoverURL(t.Context(), id, mid, b.Book.UpdatedAt, "https://covers.openlibrary.org/b/id/1-L.jpg"); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetBook(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Book.Title != b.Book.Title || *after.Book.ConfirmedMatchID != mid || after.Matches[0].CoverURL == "" {
		t.Fatalf("identity changed or no cover: %+v", after)
	}
}
