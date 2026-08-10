package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
)

const testLeaseDuration = 15 * time.Minute

type leaseRenewal struct {
	jobID      uuid.UUID
	leaseToken uuid.UUID
	now        time.Time
}

type leaseRepositoryFake struct {
	mu       sync.Mutex
	renewals []leaseRenewal
	err      error
	called   chan struct{}
}

func (repository *leaseRepositoryFake) RenewJob(
	_ context.Context,
	jobID uuid.UUID,
	leaseToken uuid.UUID,
	now time.Time,
) (time.Time, error) {
	repository.mu.Lock()
	repository.renewals = append(repository.renewals, leaseRenewal{
		jobID: jobID, leaseToken: leaseToken, now: now,
	})
	err := repository.err
	repository.mu.Unlock()
	if repository.called != nil {
		repository.called <- struct{}{}
	}
	if err != nil {
		return time.Time{}, err
	}
	return now.Add(testLeaseDuration), nil
}

func (repository *leaseRepositoryFake) snapshot() []leaseRenewal {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]leaseRenewal(nil), repository.renewals...)
}

type fakeLeaseTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newFakeLeaseTicker() *fakeLeaseTicker {
	return &fakeLeaseTicker{ticks: make(chan time.Time, 1), stopped: make(chan struct{})}
}

func (ticker *fakeLeaseTicker) C() <-chan time.Time { return ticker.ticks }
func (ticker *fakeLeaseTicker) Stop() {
	ticker.once.Do(func() { close(ticker.stopped) })
}

type fakeLeaseClock struct {
	mu              sync.Mutex
	now             time.Time
	ticker          *fakeLeaseTicker
	requestedPeriod time.Duration
}

func newFakeLeaseClock(now time.Time) *fakeLeaseClock {
	return &fakeLeaseClock{now: now, ticker: newFakeLeaseTicker()}
}

func (clock *fakeLeaseClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeLeaseClock) NewTicker(period time.Duration) leaseTicker {
	clock.mu.Lock()
	clock.requestedPeriod = period
	clock.mu.Unlock()
	return clock.ticker
}

func (clock *fakeLeaseClock) advance(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
	// The ticker is only a wake-up signal. Renewal timestamps must come from
	// the separately injectable clock, not from an arbitrary channel value.
	clock.ticker.ticks <- time.Time{}
}

func (clock *fakeLeaseClock) period() time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.requestedPeriod
}

func TestRunWithLeaseRenewsBeforeExpiry(t *testing.T) {
	startedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	job := leasedWorkerJob(startedAt, testLeaseDuration)
	clock := newFakeLeaseClock(startedAt)
	repository := &leaseRepositoryFake{called: make(chan struct{}, 1)}
	renewedAt := startedAt.Add(testLeaseDuration / 3)

	err := runWithLeaseClock(
		t.Context(), repository, job, testLeaseDuration, clock,
		func(context.Context) error {
			clock.advance(renewedAt)
			<-repository.called
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if clock.period() != testLeaseDuration/3 {
		t.Fatalf("ticker period = %s, want %s", clock.period(), testLeaseDuration/3)
	}
	renewals := repository.snapshot()
	if len(renewals) != 1 {
		t.Fatalf("renewals=%#v, want one", renewals)
	}
	if renewals[0].jobID != job.ID || renewals[0].leaseToken != job.LeaseToken ||
		!renewals[0].now.Equal(renewedAt) || !renewals[0].now.Before(*job.LeaseUntil) {
		t.Fatalf("renewal=%#v job=%#v", renewals[0], job)
	}
}

func TestRunWithLeaseCancelsHandlerAfterRenewalConflict(t *testing.T) {
	startedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	job := leasedWorkerJob(startedAt, testLeaseDuration)
	clock := newFakeLeaseClock(startedAt)
	repository := &leaseRepositoryFake{err: catalog.ErrConflict, called: make(chan struct{}, 1)}
	handlerJoined := false

	err := runWithLeaseClock(
		t.Context(), repository, job, testLeaseDuration, clock,
		func(ctx context.Context) error {
			clock.advance(startedAt.Add(testLeaseDuration / 3))
			<-ctx.Done()
			handlerJoined = true
			return ctx.Err()
		},
	)
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("runWithLeaseClock()=%v, want renewal conflict", err)
	}
	if !handlerJoined {
		t.Fatal("runWithLeaseClock returned before the cancelled handler joined")
	}
}

func TestRunWithLeaseJoinsRenewalGoroutineAfterHandlerCompletion(t *testing.T) {
	startedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	job := leasedWorkerJob(startedAt, testLeaseDuration)
	clock := newFakeLeaseClock(startedAt)
	repository := &leaseRepositoryFake{}

	if err := runWithLeaseClock(
		t.Context(), repository, job, testLeaseDuration, clock,
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-clock.ticker.stopped:
	default:
		t.Fatal("runWithLeaseClock returned before the renewal ticker stopped")
	}
	if renewals := repository.snapshot(); len(renewals) != 0 {
		t.Fatalf("renewals after handler completion=%#v", renewals)
	}
}

func TestRunWithLeasePreservesHandlerError(t *testing.T) {
	startedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	job := leasedWorkerJob(startedAt, testLeaseDuration)
	clock := newFakeLeaseClock(startedAt)
	repository := &leaseRepositoryFake{}
	handlerErr := errors.New("handler failed")

	err := runWithLeaseClock(
		t.Context(), repository, job, testLeaseDuration, clock,
		func(context.Context) error { return handlerErr },
	)
	if err != handlerErr {
		t.Fatalf("runWithLeaseClock()=%v, want unchanged %v", err, handlerErr)
	}
}

type blockingLeaseRepository struct {
	started chan context.Context
}

func (repository *blockingLeaseRepository) RenewJob(
	ctx context.Context,
	_ uuid.UUID,
	_ uuid.UUID,
	_ time.Time,
) (time.Time, error) {
	repository.started <- ctx
	<-ctx.Done()
	return time.Time{}, ctx.Err()
}

func TestRunWithLeaseCancelsBlockingRenewalAfterHandlerCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		startedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
		job := leasedWorkerJob(startedAt, testLeaseDuration)
		clock := newFakeLeaseClock(startedAt)
		repository := &blockingLeaseRepository{started: make(chan context.Context)}
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()

		var renewalCtx context.Context
		handlerReturned := make(chan struct{})
		runDone := make(chan error, 1)
		go func() {
			runDone <- runWithLeaseClock(
				parent, repository, job, testLeaseDuration, clock,
				func(context.Context) error {
					clock.advance(startedAt.Add(testLeaseDuration / 3))
					renewalCtx = <-repository.started
					close(handlerReturned)
					return nil
				},
			)
		}()

		synctest.Wait()
		select {
		case <-handlerReturned:
		default:
			t.Fatal("handler did not return after the blocking renewal began")
		}
		if renewalCtx == nil || !errors.Is(renewalCtx.Err(), context.Canceled) {
			cancelParent()
			synctest.Wait()
			<-runDone
			t.Fatalf("renewal context after handler completion = %v, want canceled", renewalCtx)
		}
		if err := <-runDone; err != nil {
			t.Fatalf("runWithLeaseClock()=%v after handler success, want nil", err)
		}
	})
}

func leasedWorkerJob(now time.Time, duration time.Duration) catalog.WorkerJob {
	leaseUntil := now.Add(duration)
	return catalog.WorkerJob{
		ID: uuid.New(), Kind: catalog.JobScanRoot, Status: catalog.JobRunning,
		LeaseToken: uuid.New(), LeaseUntil: &leaseUntil,
	}
}
