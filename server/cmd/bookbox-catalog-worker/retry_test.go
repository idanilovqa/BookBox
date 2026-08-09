package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRetryUntilReadySucceedsImmediately(t *testing.T) {
	attempts := 0
	waits := 0
	warnings := 0
	err := retryUntilReady(t.Context(), retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
		Wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}, func(context.Context) error {
		attempts++
		return nil
	}, func(string) {
		warnings++
	})
	if err != nil || attempts != 1 || waits != 0 || warnings != 0 {
		t.Fatalf("err=%v attempts=%d waits=%d warnings=%d", err, attempts, waits, warnings)
	}
}

func TestRetryUntilReadyCapsExponentialBackoff(t *testing.T) {
	var delays []time.Duration
	attempts := 0
	err := retryUntilReady(t.Context(), retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
		Wait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}, func(context.Context) error {
		attempts++
		if attempts == 8 {
			return nil
		}
		return errors.New("private database address")
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute,
	}
	if !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
}

func TestRetryUntilReadyBoundsEveryAttempt(t *testing.T) {
	attempts := 0
	err := retryUntilReady(t.Context(), retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 25 * time.Millisecond,
		Wait: func(context.Context, time.Duration) error {
			return nil
		},
	}, func(ctx context.Context) error {
		attempts++
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("attempt context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 25*time.Millisecond {
			t.Fatalf("attempt deadline remaining = %s", remaining)
		}
		if attempts == 2 {
			return nil
		}
		return errors.New("not ready")
	}, func(string) {})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRetryUntilReadyRejectsSuccessAfterAttemptDeadline(t *testing.T) {
	attempts := 0
	waits := 0
	var warnings []string
	err := retryUntilReady(t.Context(), retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Millisecond,
		Wait: func(_ context.Context, delay time.Duration) error {
			waits++
			if delay != time.Second {
				t.Fatalf("retry delay = %s, want 1s", delay)
			}
			return nil
		},
	}, func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			<-ctx.Done()
			return nil
		}
		return nil
	}, func(class string) {
		warnings = append(warnings, class)
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || waits != 1 || !reflect.DeepEqual(warnings, []string{"timeout"}) {
		t.Fatalf("attempts=%d waits=%d warnings=%q", attempts, waits, warnings)
	}
}

func TestRetryUntilReadyRateLimitsSanitizedWarnings(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	classes := []error{
		errors.New(`dial C:\\Private\\database: secret`),
		errors.New(`dial C:\\Another\\database: other secret`),
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		context.DeadlineExceeded,
		nil,
	}
	var warnings []string
	err := retryUntilReady(t.Context(), retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
		Now:            func() time.Time { return now },
		Wait: func(context.Context, time.Duration) error {
			now = now.Add(30 * time.Second)
			return nil
		},
	}, func(context.Context) error {
		err := classes[0]
		classes = classes[1:]
		return err
	}, func(class string) {
		warnings = append(warnings, class)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"unavailable", "timeout", "timeout"}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
	for _, warning := range warnings {
		if warning == `dial C:\\Private\\database: secret` || warning == `dial C:\\Another\\database: other secret` {
			t.Fatalf("warning leaked raw error: %q", warning)
		}
	}
}

func TestRetryUntilReadyCancellationDoesNotStartAnotherAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	warnings := 0
	err := retryUntilReady(ctx, retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
		Wait: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	}, func(context.Context) error {
		attempts++
		return errors.New("not ready")
	}, func(string) {
		warnings++
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 || warnings != 1 {
		t.Fatalf("err=%v attempts=%d warnings=%d", err, attempts, warnings)
	}
}

func TestRetryUntilReadyCallerCancellationIsSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	warnings := 0
	err := retryUntilReady(ctx, retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
	}, func(context.Context) error {
		attempts++
		return context.Canceled
	}, func(string) {
		warnings++
	})
	if !errors.Is(err, context.Canceled) || attempts != 0 || warnings != 0 {
		t.Fatalf("err=%v attempts=%d warnings=%d", err, attempts, warnings)
	}
}
