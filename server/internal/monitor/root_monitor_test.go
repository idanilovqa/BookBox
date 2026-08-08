package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

type repositoryFake struct {
	mu   sync.Mutex
	base catalog.WorkerJob
	keys []string
	err  error
}

func (r *repositoryFake) EnqueueDeduplicated(_ context.Context, job catalog.WorkerJob, key string) (catalog.WorkerJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
	if r.err != nil {
		return catalog.WorkerJob{}, r.err
	}
	if strings.HasPrefix(key, "scan_root_followup:") {
		return job, nil
	}
	return r.base, nil
}
func (r *repositoryFake) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

type sourceFake struct {
	events chan event
	errs   chan error
	added  []string
	closed bool
}

func (s *sourceFake) Add(p string) error   { s.added = append(s.added, p); return nil }
func (s *sourceFake) Close() error         { s.closed = true; return nil }
func (s *sourceFake) Events() <-chan event { return s.events }
func (s *sourceFake) Errors() <-chan error { return s.errs }

func TestRootMonitorCoalescesPerRootAndSchedulesRunningFollowup(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	source := &sourceFake{events: make(chan event, 4), errs: make(chan error, 1)}
	var timers []chan time.Time
	var timerMu sync.Mutex
	repo := &repositoryFake{base: catalog.WorkerJob{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Status: catalog.JobRunning}}
	m := New(repo, []config.Root{{ID: "a", ContainerPath: a}, {ID: "b", ContainerPath: b}})
	m.newSource = func() (eventSource, error) { return source, nil }
	m.after = func(time.Duration) <-chan time.Time {
		timerMu.Lock()
		defer timerMu.Unlock()
		ch := make(chan time.Time, 1)
		timers = append(timers, ch)
		return ch
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	source.events <- event{name: filepath.Join(a, "one"), op: fsnotify.Write}
	source.events <- event{name: filepath.Join(a, "two"), op: fsnotify.Create}
	source.events <- event{name: filepath.Join(b, "three"), op: fsnotify.Remove}
	for {
		timerMu.Lock()
		n := len(timers)
		timerMu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	timerMu.Lock()
	timers[0] <- time.Now()
	timers[1] <- time.Now()
	timers[2] <- time.Now()
	timerMu.Unlock()
	wantA := "scan_root:a"
	wantB := "scan_root:b"
	deadline := time.After(time.Second)
	for {
		keys := repo.snapshot()
		if len(keys) >= 4 {
			if keys[0] != wantA && keys[0] != wantB {
				t.Fatalf("keys=%v", keys)
			}
			if !contains(keys, "scan_root_followup:a:11111111-1111-1111-1111-111111111111") || !contains(keys, "scan_root_followup:b:11111111-1111-1111-1111-111111111111") {
				t.Fatalf("keys=%v", keys)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("keys=%v", keys)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRootMonitorRegistersTreesFiltersEventsAndCloses(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "created")
	if err := os.Mkdir(created, 0o755); err != nil {
		t.Fatal(err)
	}
	source := &sourceFake{events: make(chan event, 8), errs: make(chan error, 1)}
	tick := make(chan time.Time, 8)
	repo := &repositoryFake{base: catalog.WorkerJob{ID: uuid.New(), Status: catalog.JobQueued}}
	m := New(repo, []config.Root{{ID: "a", ContainerPath: root}})
	m.newSource = func() (eventSource, error) { return source, nil }
	m.after = func(time.Duration) <-chan time.Time { return tick }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	for len(source.added) < 2 {
		time.Sleep(time.Millisecond)
	}
	source.events <- event{name: created, op: fsnotify.Create}
	source.events <- event{name: filepath.Join(root, "ignored"), op: fsnotify.Chmod}
	source.events <- event{name: filepath.Join(t.TempDir(), "outside"), op: fsnotify.Write}
	tick <- time.Now()
	time.Sleep(10 * time.Millisecond)
	if len(repo.snapshot()) != 1 {
		t.Fatalf("keys=%v", repo.snapshot())
	}
	if !contains(source.added, created) {
		t.Fatalf("added=%v", source.added)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !source.closed {
		t.Fatal("source not closed")
	}
}

func TestRootMonitorSanitizesWatcherFailure(t *testing.T) {
	root := t.TempDir()
	source := &sourceFake{events: make(chan event), errs: make(chan error, 1)}
	m := New(&repositoryFake{}, []config.Root{{ID: "a", ContainerPath: root}})
	m.newSource = func() (eventSource, error) { return source, nil }
	source.errs <- errors.New("secret " + root)
	err := m.Run(t.Context())
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("error=%v", err)
	}
}

func TestRootMonitorValidatesQueueResponsesAndConfiguration(t *testing.T) {
	root := config.Root{ID: "a", ContainerPath: t.TempDir()}
	for _, test := range []struct {
		name       string
		repository *repositoryFake
		wantError  bool
		wantKeys   int
	}{
		{name: "queued", repository: &repositoryFake{base: catalog.WorkerJob{ID: uuid.New(), Status: catalog.JobQueued}}, wantKeys: 1},
		{name: "running", repository: &repositoryFake{base: catalog.WorkerJob{ID: uuid.New(), Status: catalog.JobRunning}}, wantKeys: 2},
		{name: "malformed running", repository: &repositoryFake{base: catalog.WorkerJob{Status: catalog.JobRunning}}, wantError: true, wantKeys: 1},
		{name: "malformed status", repository: &repositoryFake{base: catalog.WorkerJob{ID: uuid.New()}}, wantError: true, wantKeys: 1},
		{name: "repository error", repository: &repositoryFake{err: errors.New("database secret")}, wantError: true, wantKeys: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(test.repository, []config.Root{root})
			err := m.enqueue(t.Context(), root.ID)
			if (err != nil) != test.wantError || len(test.repository.snapshot()) != test.wantKeys {
				t.Fatalf("err=%v keys=%v", err, test.repository.snapshot())
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsanitized error=%v", err)
			}
		})
	}
	source := &sourceFake{events: make(chan event), errs: make(chan error)}
	m := New(&repositoryFake{}, []config.Root{root, root})
	m.newSource = func() (eventSource, error) { return source, nil }
	if err := m.Run(t.Context()); err == nil {
		t.Fatal("duplicate roots accepted")
	}
	for name, roots := range map[string][]config.Root{
		"empty":    {},
		"relative": {{ID: "a", ContainerPath: "relative"}},
		"overlap":  {{ID: "a", ContainerPath: root.ContainerPath}, {ID: "b", ContainerPath: filepath.Join(root.ContainerPath, "nested")}},
	} {
		t.Run(name, func(t *testing.T) {
			m := New(&repositoryFake{}, roots)
			m.newSource = func() (eventSource, error) { return &sourceFake{events: make(chan event), errs: make(chan error)}, nil }
			if err := m.Run(t.Context()); err == nil {
				t.Fatal("invalid roots accepted")
			}
		})
	}
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
