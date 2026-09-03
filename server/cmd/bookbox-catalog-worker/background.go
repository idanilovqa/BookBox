package main

import (
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/worker"
	"context"
	"time"
)

type jobLaneRepository struct {
	*worker.PostgresStagedRepository
	kinds []string
}

func (r *jobLaneRepository) ClaimJob(ctx context.Context, id string, now time.Time) (catalog.WorkerJob, error) {
	return r.ClaimJobKinds(ctx, id, now, r.kinds)
}

// A failed lane stops its peer; Run waits for both to release owned resources.
type workerLanes []contextRunner

func (lanes workerLanes) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, len(lanes))
	for _, lane := range lanes {
		go func(r contextRunner) { done <- r.Run(runCtx) }(lane)
	}
	var result error
	for range lanes {
		if err := <-done; err != nil && result == nil {
			result = err
		}
		cancel()
	}
	return result
}
