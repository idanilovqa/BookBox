//go:build windows

package main

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceFailureExitCode uint32 = 1

type serviceEvents interface {
	Info(string)
	Error(string)
}

type eventLogEvents struct{ log *eventlog.Log }

func (events eventLogEvents) Info(message string)  { _ = events.log.Info(1, message) }
func (events eventLogEvents) Error(message string) { _ = events.log.Error(1, message) }

type windowsService struct {
	program         func(context.Context) error
	events          serviceEvents
	stopBound       time.Duration
	checkpointEvery time.Duration
	newDeadline     func(time.Duration) (<-chan time.Time, func())
	newCheckpoint   func(time.Duration) (<-chan time.Time, func())
}

type programResult struct{ err error }

func runWindowsService(name string, program func(context.Context) error) error {
	eventSource, err := eventlog.Open(name)
	if err != nil {
		return err
	}
	defer eventSource.Close()
	return svc.Run(name, &windowsService{
		program: program,
		events:  eventLogEvents{log: eventSource},
	})
}

func (service windowsService) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	statuses chan<- svc.Status,
) (bool, uint32) {
	service.normalize()
	statuses <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan programResult, 1)
	go func() { completed <- programResult{err: service.program(ctx)} }()

	running := svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	statuses <- running
	service.events.Info("catalog worker service started")

	for {
		select {
		case <-completed:
			service.events.Error("catalog worker service stopped unexpectedly")
			return true, serviceFailureExitCode
		case request, ok := <-requests:
			if !ok {
				service.events.Error("catalog worker service control channel closed")
				return true, serviceFailureExitCode
			}
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				return service.stop(ctx, cancel, completed, statuses)
			case svc.Interrogate:
				statuses <- running
			}
		}
	}
}

func (service *windowsService) normalize() {
	if service.program == nil {
		service.program = func(context.Context) error { return errors.New("service program is required") }
	}
	if service.events == nil {
		service.events = discardServiceEvents{}
	}
	if service.stopBound <= 0 {
		service.stopBound = 30 * time.Second
	}
	if service.checkpointEvery <= 0 {
		service.checkpointEvery = 5 * time.Second
	}
	if service.newDeadline == nil {
		service.newDeadline = newServiceTimer
	}
	if service.newCheckpoint == nil {
		service.newCheckpoint = newServiceTicker
	}
}

func (service windowsService) stop(
	ctx context.Context,
	cancel context.CancelFunc,
	completed <-chan programResult,
	statuses chan<- svc.Status,
) (bool, uint32) {
	cancel()
	checkpoint := uint32(1)
	waitHint := durationMilliseconds(service.stopBound)
	statuses <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: waitHint}
	deadline, stopDeadline := service.newDeadline(service.stopBound)
	defer stopDeadline()
	ticks, stopTicks := service.newCheckpoint(service.checkpointEvery)
	defer stopTicks()

	for {
		select {
		case result := <-completed:
			if result.err == nil || (ctx.Err() != nil && errors.Is(result.err, context.Canceled)) {
				service.events.Info("catalog worker service stopped")
				return false, 0
			}
			service.events.Error("catalog worker service failed while stopping")
			return true, serviceFailureExitCode
		case <-ticks:
			checkpoint++
			statuses <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: waitHint}
		case <-deadline:
			service.events.Error("catalog worker service stop timed out")
			return true, serviceFailureExitCode
		}
	}
}

func durationMilliseconds(duration time.Duration) uint32 {
	milliseconds := duration / time.Millisecond
	if milliseconds <= 0 {
		return 1
	}
	if milliseconds > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(milliseconds)
}

func newServiceTimer(duration time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(duration)
	return timer.C, func() { timer.Stop() }
}

func newServiceTicker(duration time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(duration)
	return ticker.C, ticker.Stop
}

type discardServiceEvents struct{}

func (discardServiceEvents) Info(string)  {}
func (discardServiceEvents) Error(string) {}
