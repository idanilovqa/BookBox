package worker

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
)

type LeaseRepository interface {
	RenewJob(context.Context, uuid.UUID, uuid.UUID, time.Time) (time.Time, error)
}

type leaseTicker interface {
	C() <-chan time.Time
	Stop()
}

type leaseClock interface {
	Now() time.Time
	NewTicker(time.Duration) leaseTicker
}

type systemLeaseClock struct{}

func (systemLeaseClock) Now() time.Time { return time.Now() }
func (systemLeaseClock) NewTicker(period time.Duration) leaseTicker {
	return systemLeaseTicker{Ticker: time.NewTicker(period)}
}

type systemLeaseTicker struct {
	*time.Ticker
}

func (ticker systemLeaseTicker) C() <-chan time.Time { return ticker.Ticker.C }

func runWithLease(
	parent context.Context,
	repository LeaseRepository,
	job catalog.WorkerJob,
	leaseDuration time.Duration,
	handler func(context.Context) error,
) error {
	return runWithLeaseClock(
		parent, repository, job, leaseDuration, systemLeaseClock{}, handler,
	)
}

func runWithLeaseClock(
	parent context.Context,
	repository LeaseRepository,
	job catalog.WorkerJob,
	leaseDuration time.Duration,
	clock leaseClock,
	handler func(context.Context) error,
) error {
	if parent == nil || repository == nil || clock == nil || handler == nil ||
		job.ID == uuid.Nil || job.LeaseToken == uuid.Nil || leaseDuration <= 0 {
		return errors.New("valid worker lease is required")
	}
	renewalInterval := leaseDuration / 3
	if renewalInterval <= 0 {
		return errors.New("worker lease duration is too short")
	}

	handlerCtx, cancelHandler := context.WithCancel(parent)
	defer cancelHandler()
	renewalCtx, cancelRenewal := context.WithCancel(parent)
	defer cancelRenewal()
	finished := make(chan struct{})
	renewalDone := make(chan struct{})
	var renewalErr error
	go func() {
		defer close(renewalDone)
		ticker := clock.NewTicker(renewalInterval)
		if ticker == nil {
			renewalErr = errors.New("valid worker lease ticker is required")
			cancelHandler()
			return
		}
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-parent.Done():
				return
			case <-ticker.C():
				if _, err := repository.RenewJob(
					renewalCtx, job.ID, job.LeaseToken, clock.Now().UTC(),
				); err != nil {
					if errors.Is(err, context.Canceled) && renewalCtx.Err() != nil {
						return
					}
					cancelHandler()
					renewalErr = err
					return
				}
			}
		}
	}()

	handlerErr := handler(handlerCtx)
	cancelRenewal()
	close(finished)
	<-renewalDone
	if renewalErr != nil {
		return renewalErr
	}
	return handlerErr
}
