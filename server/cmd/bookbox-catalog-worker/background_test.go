package main

import (
	"context"
	"testing"
	"time"
)

func TestWorkerLanesProgressTogetherAndStopTogether(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan string, 2)
	stopped := make(chan string, 2)
	runner := func(name string) contextRunner {
		return runnerFunc(func(ctx context.Context) error {
			started <- name
			<-ctx.Done()
			stopped <- name
			return nil
		})
	}
	done := make(chan error, 1)
	go func() { done <- workerLanes{runner("scan"), runner("metadata")}.Run(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("one lane blocked the other")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lanes did not stop")
	}
	if len(stopped) != 2 {
		t.Fatal("returned before both lanes exited")
	}
}
