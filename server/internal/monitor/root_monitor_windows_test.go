//go:build windows

package monitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"github.com/google/uuid"
)

func TestRootMonitorReceivesNativeWindowsEvents(t *testing.T) {
	const (
		rootID       = "native-windows-events"
		debounce     = 100 * time.Millisecond
		eventTimeout = 5 * time.Second
	)

	root := t.TempDir()
	outside := t.TempDir()
	repository := &repositoryFake{base: catalog.WorkerJob{ID: uuid.New(), Status: catalog.JobQueued}}
	warnings := make(chan error, 1)
	supervisor := Supervisor{
		New: func() Runner {
			rootMonitor := New(repository, []config.Root{{ID: rootID, ContainerPath: root}})
			rootMonitor.Debounce = debounce
			return rootMonitor
		},
		Warn: func(err error) {
			select {
			case warnings <- err:
			default:
			}
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Supervisor.Run() cleanup error = %v", err)
			}
		case <-time.After(eventTimeout):
			t.Error("Supervisor.Run() did not stop after cancellation")
		}
	})

	waitForNativeWatcher(t, repository, root, rootID, debounce, eventTimeout)
	waitForNativeEventQuiet(t, repository, 4*debounce, eventTimeout)

	file := filepath.Join(root, "watched.mp3")
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.WriteFile(file, []byte("created"), 0o600)
	})
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.WriteFile(file, []byte("updated"), 0o600)
	})

	nested := filepath.Join(root, "nested")
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.Mkdir(nested, 0o700)
	})
	nestedFile := filepath.Join(nested, "registered.mp3")
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.WriteFile(nestedFile, []byte("nested"), 0o600)
	})

	renamed := filepath.Join(nested, "renamed.mp3")
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.Rename(file, renamed)
	})
	expectNativeRootJob(t, repository, rootID, debounce, eventTimeout, func() error {
		return os.Remove(renamed)
	})

	waitForNativeEventQuiet(t, repository, 4*debounce, eventTimeout)
	baseline := len(repository.snapshot())
	if err := os.WriteFile(filepath.Join(outside, "ignored.mp3"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * debounce)
	if keys := repository.snapshot(); len(keys) != baseline {
		t.Fatalf("outside-root change enqueued jobs: before=%d after=%d keys=%v", baseline, len(keys), keys)
	}

	select {
	case err := <-warnings:
		t.Fatalf("native watcher unexpectedly restarted: %v", err)
	default:
	}
}

func waitForNativeWatcher(t *testing.T, repository *repositoryFake, root, rootID string, debounce, timeout time.Duration) {
	t.Helper()
	probe := filepath.Join(root, ".watcher-ready")
	baseline := len(repository.snapshot())
	deadline := time.Now().Add(timeout)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if err := os.WriteFile(probe, []byte{byte(attempt)}, 0o600); err != nil {
			t.Fatal(err)
		}
		if waitForNativeRootJob(repository, rootID, baseline, 2*debounce) >= 0 {
			if err := os.Remove(probe); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("native watcher did not become ready within %s", timeout)
}

func expectNativeRootJob(t *testing.T, repository *repositoryFake, rootID string, debounce, timeout time.Duration, operation func() error) {
	t.Helper()
	waitForNativeEventQuiet(t, repository, 3*debounce, timeout)
	baseline := len(repository.snapshot())
	if err := operation(); err != nil {
		t.Fatal(err)
	}
	jobIndex := waitForNativeRootJob(repository, rootID, baseline, timeout)
	if jobIndex < 0 {
		t.Fatalf("filesystem operation did not enqueue scan_root:%s within %s; keys=%v", rootID, timeout, repository.snapshot())
	}
	jobs := repository.snapshotJobs()
	if len(jobs) <= jobIndex {
		t.Fatalf("repository did not record job for key index %d: jobs=%d", jobIndex, len(jobs))
	}
	job := jobs[jobIndex]
	if job.Kind != catalog.JobScanRoot || job.Payload["rootId"] != rootID {
		t.Fatalf("enqueued job = %#v, want kind=%q payload rootId=%q", job, catalog.JobScanRoot, rootID)
	}
}

func waitForNativeRootJob(repository *repositoryFake, rootID string, baseline int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		keys := repository.snapshot()
		for offset, key := range keys[baseline:] {
			if key == "scan_root:"+rootID {
				return baseline + offset
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return -1
}

func waitForNativeEventQuiet(t *testing.T, repository *repositoryFake, quiet, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	lastCount := len(repository.snapshot())
	quietSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		count := len(repository.snapshot())
		if count != lastCount {
			lastCount = count
			quietSince = time.Now()
		}
		if time.Since(quietSince) >= quiet {
			return
		}
	}
	t.Fatalf("native watcher did not become quiet within %s; keys=%v", timeout, repository.snapshot())
}
