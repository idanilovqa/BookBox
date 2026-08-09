package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
)

const testLeaseDuration = 45 * time.Millisecond

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
		select {
		case repository.called <- struct{}{}:
		default:
		}
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

func TestRunWithLeaseRenewsBeforeExpiry(t *testing.T) {
	job := leasedWorkerJob(testLeaseDuration)
	repository := &leaseRepositoryFake{called: make(chan struct{}, 1)}
	handlerFinished := make(chan struct{})

	err := runWithLease(
		t.Context(), repository, job, testLeaseDuration,
		func(context.Context) error {
			select {
			case <-repository.called:
				close(handlerFinished)
				return nil
			case <-time.After(time.Second):
				return errors.New("lease was not renewed")
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerFinished:
	default:
		t.Fatal("handler completed without observing a renewal")
	}
	renewals := repository.snapshot()
	if len(renewals) != 1 {
		t.Fatalf("renewals=%#v, want one", renewals)
	}
	if renewals[0].jobID != job.ID || renewals[0].leaseToken != job.LeaseToken ||
		!renewals[0].now.Before(*job.LeaseUntil) {
		t.Fatalf("renewal=%#v job=%#v", renewals[0], job)
	}
}

func TestRunWithLeaseCancelsHandlerAfterRenewalConflict(t *testing.T) {
	job := leasedWorkerJob(testLeaseDuration)
	repository := &leaseRepositoryFake{
		err: catalog.ErrConflict, called: make(chan struct{}, 1),
	}
	handlerCancelled := make(chan struct{})

	err := runWithLease(
		t.Context(), repository, job, testLeaseDuration,
		func(ctx context.Context) error {
			<-ctx.Done()
			close(handlerCancelled)
			return ctx.Err()
		},
	)
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("runWithLease()=%v, want renewal conflict", err)
	}
	select {
	case <-handlerCancelled:
	default:
		t.Fatal("handler context was not cancelled after renewal conflict")
	}
}

func TestRunWithLeaseStopsRenewalAfterHandlerCompletion(t *testing.T) {
	job := leasedWorkerJob(testLeaseDuration)
	repository := &leaseRepositoryFake{called: make(chan struct{}, 1)}

	if err := runWithLease(
		t.Context(), repository, job, testLeaseDuration,
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.called:
		t.Fatal("lease renewed after handler completion")
	case <-time.After(testLeaseDuration):
	}
	if renewals := repository.snapshot(); len(renewals) != 0 {
		t.Fatalf("renewals after handler completion=%#v", renewals)
	}
}

func TestRunWithLeasePreservesHandlerError(t *testing.T) {
	job := leasedWorkerJob(testLeaseDuration)
	repository := &leaseRepositoryFake{}
	handlerErr := errors.New("handler failed")

	err := runWithLease(
		t.Context(), repository, job, testLeaseDuration,
		func(context.Context) error { return handlerErr },
	)
	if err != handlerErr {
		t.Fatalf("runWithLease()=%v, want unchanged %v", err, handlerErr)
	}
}

func leasedWorkerJob(duration time.Duration) catalog.WorkerJob {
	now := time.Now().UTC()
	leaseUntil := now.Add(duration)
	return catalog.WorkerJob{
		ID: uuid.New(), Kind: catalog.JobScanRoot, Status: catalog.JobRunning,
		LeaseToken: uuid.New(), LeaseUntil: &leaseUntil,
	}
}
