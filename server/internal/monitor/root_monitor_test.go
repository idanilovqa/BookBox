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
	jobs []catalog.WorkerJob
	err  error
}

func (r *repositoryFake) EnqueueDeduplicated(_ context.Context, job catalog.WorkerJob, key string) (catalog.WorkerJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
	r.jobs = append(r.jobs, job)
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

func (r *repositoryFake) snapshotJobs() []catalog.WorkerJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]catalog.WorkerJob(nil), r.jobs...)
}

type sourceFake struct {
	mu     sync.Mutex
	events chan event
	errs   chan error
	added  []string
	closed bool
}

func (s *sourceFake) Add(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, p)
	return nil
}
func (s *sourceFake) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *sourceFake) Events() <-chan event { return s.events }
func (s *sourceFake) Errors() <-chan error { return s.errs }
func (s *sourceFake) snapshot() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.added...), s.closed
}

func TestRootMonitorCoalescesSiblingChangesToParentScope(t *testing.T) {
	a := t.TempDir()
	source := &sourceFake{events: make(chan event, 2), errs: make(chan error, 1)}
	var timers []chan time.Time
	var timerMu sync.Mutex
	repo := &repositoryFake{base: catalog.WorkerJob{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Status: catalog.JobRunning}}
	m := New(repo, []config.Root{{ID: "audiobooks", ContainerPath: a}})
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
	source.events <- event{name: filepath.Join(a, "Author", "Book", "Disc 1", "01.mp3"), op: fsnotify.Write}
	source.events <- event{name: filepath.Join(a, "Author", "Book", "Disc 2", "01.mp3"), op: fsnotify.Remove}
	for {
		timerMu.Lock()
		n := len(timers)
		timerMu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	timerMu.Lock()
	timers[0] <- time.Now()
	timers[1] <- time.Now()
	timerMu.Unlock()
	deadline := time.After(time.Second)
	for {
		keys := repo.snapshot()
		if len(keys) >= 2 {
			wantScan := "scan_root:audiobooks:Author/Book"
			wantFollowup := "scan_root_followup:audiobooks:Author/Book:11111111-1111-1111-1111-111111111111"
			if !contains(keys, wantScan) || !contains(keys, wantFollowup) {
				t.Fatalf("keys=%v, want %q and %q", keys, wantScan, wantFollowup)
			}
			jobs := repo.snapshotJobs()
			assertScanPayload(t, jobs[0], "audiobooks", "Author/Book", false)
			assertScanPayload(t, jobs[1], "audiobooks", "Author/Book", false)
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

func TestRootMonitorUsesStableScopedFollowupDedupeKey(t *testing.T) {
	runningID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repository := &repositoryFake{base: catalog.WorkerJob{ID: runningID, Status: catalog.JobRunning}}
	monitor := New(repository, []config.Root{{ID: "audiobooks", ContainerPath: t.TempDir()}})

	for range 2 {
		if err := monitor.enqueue(t.Context(), "audiobooks", "Author/Book"); err != nil {
			t.Fatal(err)
		}
	}
	want := "scan_root_followup:audiobooks:Author/Book:" + runningID.String()
	keys := repository.snapshot()
	if len(keys) != 4 || keys[1] != want || keys[3] != want {
		t.Fatalf("keys=%v, want repeated stable follow-up key %q", keys, want)
	}
	for _, job := range repository.snapshotJobs() {
		assertScanPayload(t, job, "audiobooks", "Author/Book", false)
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
	for {
		added, _ := source.snapshot()
		if len(added) >= 2 {
			break
		}
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
	added, _ := source.snapshot()
	if !contains(added, created) {
		t.Fatalf("added=%v", added)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, closed := source.snapshot()
	if !closed {
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
			err := m.enqueue(t.Context(), root.ID, "Author/Book")
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

func assertScanPayload(t *testing.T, job catalog.WorkerJob, rootID, relativePath string, full bool) {
	t.Helper()
	if job.Kind != catalog.JobScanRoot || job.Payload["rootId"] != rootID ||
		job.Payload["relativePath"] != relativePath || job.Payload["full"] != full {
		t.Fatalf("scan payload=%#v, want rootId=%q relativePath=%q full=%v", job, rootID, relativePath, full)
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
