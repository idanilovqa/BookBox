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

func runWithLease(
	parent context.Context,
	repository LeaseRepository,
	job catalog.WorkerJob,
	leaseDuration time.Duration,
	handler func(context.Context) error,
) error {
	if parent == nil || repository == nil || handler == nil ||
		job.ID == uuid.Nil || job.LeaseToken == uuid.Nil || leaseDuration <= 0 {
		return errors.New("valid worker lease is required")
	}
	renewalInterval := leaseDuration / 3
	if renewalInterval <= 0 {
		return errors.New("worker lease duration is too short")
	}

	handlerCtx, cancelHandler := context.WithCancel(parent)
	defer cancelHandler()
	finished := make(chan struct{})
	renewalDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(renewalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				renewalDone <- nil
				return
			case <-parent.Done():
				renewalDone <- nil
				return
			case renewedAt := <-ticker.C:
				if _, err := repository.RenewJob(
					parent, job.ID, job.LeaseToken, renewedAt.UTC(),
				); err != nil {
					cancelHandler()
					renewalDone <- err
					return
				}
			}
		}
	}()

	handlerErr := handler(handlerCtx)
	close(finished)
	renewalErr := <-renewalDone
	if renewalErr != nil {
		return renewalErr
	}
	return handlerErr
}
