package monitor

import (
	"context"
	"errors"
	"time"
)

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = time.Minute
)

var errMonitorRestarting = errors.New("root monitor failed; retrying")

type Runner interface {
	Run(context.Context) error
}

type Supervisor struct {
	New         func() Runner
	Warn        func(error)
	Wait        func(context.Context, time.Duration) error
	Now         func() time.Time
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	StableAfter time.Duration
}

func (supervisor Supervisor) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if supervisor.New == nil {
		return errors.New("root monitor supervisor requires a runner factory")
	}
	minimum := supervisor.MinBackoff
	if minimum <= 0 {
		minimum = defaultMinBackoff
	}
	maximum := supervisor.MaxBackoff
	if maximum < minimum {
		maximum = defaultMaxBackoff
		if maximum < minimum {
			maximum = minimum
		}
	}
	stableAfter := supervisor.StableAfter
	if stableAfter <= 0 {
		stableAfter = maximum
	}
	now := supervisor.Now
	if now == nil {
		now = time.Now
	}
	wait := supervisor.Wait
	if wait == nil {
		wait = waitForRetry
	}

	delay := minimum
	for {
		if ctx.Err() != nil {
			return nil
		}
		runner := supervisor.New()
		if runner == nil {
			return errors.New("root monitor supervisor factory returned nil")
		}
		started := now()
		_ = runner.Run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if now().Sub(started) >= stableAfter {
			delay = minimum
		}
		if supervisor.Warn != nil {
			supervisor.Warn(errMonitorRestarting)
		}
		if err := wait(ctx, delay); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("root monitor retry wait failed")
		}
		if delay >= maximum || delay > maximum/2 {
			delay = maximum
		} else {
			delay *= 2
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
