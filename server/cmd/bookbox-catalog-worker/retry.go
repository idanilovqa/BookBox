package main

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const retryWarningInterval = time.Minute

type retryPolicy struct {
	Initial        time.Duration
	Maximum        time.Duration
	AttemptTimeout time.Duration
	Wait           func(context.Context, time.Duration) error
	Now            func() time.Time
}

func retryUntilReady(
	ctx context.Context,
	policy retryPolicy,
	attempt func(context.Context) error,
	warn func(errorClass string),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt == nil {
		return errors.New("readiness attempt is required")
	}

	policy = normalizedRetryPolicy(policy)
	delay := policy.Initial
	lastClass := ""
	var lastWarning time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		attemptCtx, cancel := context.WithTimeout(ctx, policy.AttemptTimeout)
		err := attempt(attemptCtx)
		attemptContextErr := attemptCtx.Err()
		cancel()
		if attemptContextErr != nil {
			err = attemptContextErr
		}
		if err == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		class := readinessErrorClass(err)
		now := policy.Now()
		if lastClass == "" || class != lastClass || now.Sub(lastWarning) >= retryWarningInterval {
			if warn != nil {
				warn(class)
			}
			lastClass = class
			lastWarning = now
		}

		if err := policy.Wait(ctx, delay); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return err
		}
		if delay >= policy.Maximum/2 {
			delay = policy.Maximum
		} else {
			delay *= 2
		}
	}
}

func normalizedRetryPolicy(policy retryPolicy) retryPolicy {
	if policy.Initial <= 0 {
		policy.Initial = time.Second
	}
	if policy.Maximum < policy.Initial {
		policy.Maximum = policy.Initial
	}
	if policy.AttemptTimeout <= 0 {
		policy.AttemptTimeout = 10 * time.Second
	}
	if policy.Wait == nil {
		policy.Wait = waitForRetry
	}
	if policy.Now == nil {
		policy.Now = time.Now
	}
	return policy
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

func readinessErrorClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return "database"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return "network"
	}
	return "unavailable"
}
