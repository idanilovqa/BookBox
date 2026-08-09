//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestDefaultWorkerConfigPathWindows(t *testing.T) {
	for _, programData := range []string{`C:\ProgramData`, ""} {
		t.Run(programData, func(t *testing.T) {
			t.Setenv("ProgramData", programData)
			if path := defaultWorkerConfigPath(); path != `C:\ProgramData\BookBox\catalog-worker.yml` {
				t.Fatalf("default config path = %q", path)
			}
		})
	}
}

type fakeServiceEvents struct {
	mu       sync.Mutex
	info     []string
	failures []string
}

func (events *fakeServiceEvents) Info(message string) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.info = append(events.info, message)
}

func (events *fakeServiceEvents) Error(message string) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.failures = append(events.failures, message)
}

func TestWindowsServiceStopAndShutdown(t *testing.T) {
	for _, command := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		t.Run(commandName(command), func(t *testing.T) {
			requests := make(chan svc.ChangeRequest, 1)
			statuses := make(chan svc.Status, 4)
			result := make(chan serviceResult, 1)
			programStopped := make(chan struct{})
			handler := windowsService{
				program: func(ctx context.Context) error {
					<-ctx.Done()
					close(programStopped)
					return ctx.Err()
				},
				events: &fakeServiceEvents{},
			}
			go func() {
				specific, code := handler.Execute(nil, requests, statuses)
				result <- serviceResult{specific: specific, code: code}
			}()

			assertServiceStatus(t, <-statuses, svc.StartPending, 0)
			assertServiceStatus(t, <-statuses, svc.Running, svc.AcceptStop|svc.AcceptShutdown)
			requests <- svc.ChangeRequest{Cmd: command}
			pending := <-statuses
			assertServiceStatus(t, pending, svc.StopPending, 0)
			if pending.CheckPoint != 1 || pending.WaitHint == 0 {
				t.Fatalf("StopPending checkpoint=%d waitHint=%d", pending.CheckPoint, pending.WaitHint)
			}
			<-programStopped
			if got := <-result; got.specific || got.code != 0 {
				t.Fatalf("Execute result = %#v", got)
			}
		})
	}
}

func TestWindowsServiceUnexpectedExitIsSanitizedAndNonzero(t *testing.T) {
	events := &fakeServiceEvents{}
	statuses := make(chan svc.Status, 3)
	result := make(chan serviceResult, 1)
	handler := windowsService{
		program: func(context.Context) error {
			return errors.New(`database C:\Private password=secret`)
		},
		events: events,
	}
	go func() {
		specific, code := handler.Execute(nil, make(chan svc.ChangeRequest), statuses)
		result <- serviceResult{specific: specific, code: code}
	}()
	assertServiceStatus(t, <-statuses, svc.StartPending, 0)
	assertServiceStatus(t, <-statuses, svc.Running, svc.AcceptStop|svc.AcceptShutdown)
	got := <-result
	if !got.specific || got.code == 0 {
		t.Fatalf("Execute result = %#v", got)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	joined := strings.Join(events.failures, " ")
	if joined == "" || strings.Contains(joined, "Private") || strings.Contains(joined, "secret") {
		t.Fatalf("failure events = %q", joined)
	}
}

func TestWindowsServiceStopIsBoundedAndReportsCheckpoints(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 5)
	result := make(chan serviceResult, 1)
	ticks := make(chan time.Time, 1)
	deadline := make(chan time.Time, 1)
	releaseProgram := make(chan struct{})
	events := &fakeServiceEvents{}
	handler := windowsService{
		program: func(context.Context) error {
			<-releaseProgram
			return nil
		},
		events:          events,
		stopBound:       30 * time.Second,
		checkpointEvery: 5 * time.Second,
		newDeadline: func(time.Duration) (<-chan time.Time, func()) {
			return deadline, func() {}
		},
		newCheckpoint: func(time.Duration) (<-chan time.Time, func()) {
			return ticks, func() {}
		},
	}
	go func() {
		specific, code := handler.Execute(nil, requests, statuses)
		result <- serviceResult{specific: specific, code: code}
	}()
	<-statuses
	<-statuses
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	first := <-statuses
	assertServiceStatus(t, first, svc.StopPending, 0)
	ticks <- time.Now()
	second := <-statuses
	if second.State != svc.StopPending || second.CheckPoint != first.CheckPoint+1 {
		t.Fatalf("checkpoint status = %#v after %#v", second, first)
	}
	deadline <- time.Now()
	got := <-result
	close(releaseProgram)
	if !got.specific || got.code == 0 {
		t.Fatalf("Execute result = %#v", got)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.failures) != 1 || events.failures[0] != "catalog worker service stop timed out" {
		t.Fatalf("failure events = %q", events.failures)
	}
}

func assertServiceStatus(t *testing.T, status svc.Status, state svc.State, accepts svc.Accepted) {
	t.Helper()
	if status.State != state || status.Accepts != accepts {
		t.Fatalf("status = %#v, want state=%v accepts=%v", status, state, accepts)
	}
}

func commandName(command svc.Cmd) string {
	if command == svc.Shutdown {
		return "shutdown"
	}
	return "stop"
}

type serviceResult struct {
	specific bool
	code     uint32
}
