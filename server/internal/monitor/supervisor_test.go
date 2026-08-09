package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }

func TestSupervisorRetriesFreshRunnersWithCappedBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var waits []time.Duration
	newCalls := 0
	var warnings []error
	supervisor := Supervisor{
		New: func() Runner {
			newCalls++
			return runnerFunc(func(context.Context) error {
				return errors.New(`watch failed at C:\Private\Audiobooks`)
			})
		},
		Warn: func(err error) { warnings = append(warnings, err) },
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			if len(waits) == 4 {
				cancel()
				return context.Canceled
			}
			return nil
		},
		MinBackoff: time.Second,
		MaxBackoff: 4 * time.Second,
	}

	if err := supervisor.Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for index := range want {
		if waits[index] != want[index] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
	if newCalls != 4 || len(warnings) != 4 {
		t.Fatalf("new calls = %d warnings = %d", newCalls, len(warnings))
	}
	for _, warning := range warnings {
		if warning == nil || strings.Contains(warning.Error(), `C:\Private`) {
			t.Fatalf("unsanitized warning = %v", warning)
		}
	}
}

func TestSupervisorResetsBackoffAfterStableRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Unix(100, 0)
	runs := 0
	var waits []time.Duration
	supervisor := Supervisor{
		Now: func() time.Time { return now },
		New: func() Runner {
			runs++
			current := runs
			return runnerFunc(func(context.Context) error {
				if current == 2 {
					now = now.Add(10 * time.Second)
				}
				return errors.New("watch failed")
			})
		},
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			if len(waits) == 3 {
				cancel()
				return context.Canceled
			}
			return nil
		},
		MinBackoff:  time.Second,
		MaxBackoff:  8 * time.Second,
		StableAfter: 5 * time.Second,
	}

	if err := supervisor.Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, time.Second, 2 * time.Second}
	for index := range want {
		if waits[index] != want[index] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
}

func TestSupervisorReplacesFailureWithHealthyRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	newCalls := 0
	warnings := 0
	recovered := make(chan struct{})
	done := make(chan error, 1)
	supervisor := Supervisor{
		New: func() Runner {
			newCalls++
			if newCalls == 1 {
				return runnerFunc(func(context.Context) error { return errors.New("watch failed") })
			}
			return runnerFunc(func(ctx context.Context) error {
				close(recovered)
				<-ctx.Done()
				return ctx.Err()
			})
		},
		Warn: func(error) { warnings++ },
		Wait: func(context.Context, time.Duration) error { return nil },
	}
	go func() { done <- supervisor.Run(ctx) }()
	<-recovered
	if newCalls != 2 || warnings != 1 {
		t.Fatalf("new calls = %d warnings = %d", newCalls, warnings)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if newCalls != 2 || warnings != 1 {
		t.Fatalf("post-recovery new calls = %d warnings = %d", newCalls, warnings)
	}
}

func TestSupervisorCancellationDuringWaitStopsWithoutAnotherRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	newCalls := 0
	waiting := make(chan struct{})
	done := make(chan error, 1)
	supervisor := Supervisor{
		New: func() Runner {
			newCalls++
			return runnerFunc(func(context.Context) error { return errors.New("watch failed") })
		},
		Wait: func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	go func() { done <- supervisor.Run(ctx) }()
	<-waiting
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if newCalls != 1 {
		t.Fatalf("new calls = %d, want 1", newCalls)
	}
}

func TestSupervisorCancellationOfHealthyRunnerReturnsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	warned := false
	waited := false
	done := make(chan error, 1)
	supervisor := Supervisor{
		New: func() Runner {
			return runnerFunc(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})
		},
		Warn: func(error) { warned = true },
		Wait: func(context.Context, time.Duration) error {
			waited = true
			return nil
		},
	}
	go func() { done <- supervisor.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if warned || waited {
		t.Fatalf("warned = %v waited = %v", warned, waited)
	}
}
