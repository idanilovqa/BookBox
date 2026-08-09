package monitor

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
)

const DefaultDebounce = 5 * time.Second

type Repository interface {
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type event struct {
	name string
	op   fsnotify.Op
}

type eventSource interface {
	Add(string) error
	Close() error
	Events() <-chan event
	Errors() <-chan error
}

type RootMonitor struct {
	Repo      Repository
	Roots     []config.Root
	Debounce  time.Duration
	newSource func() (eventSource, error)
	after     func(time.Duration) <-chan time.Time
}

func New(repo Repository, roots []config.Root) *RootMonitor {
	return &RootMonitor{Repo: repo, Roots: append([]config.Root(nil), roots...), Debounce: DefaultDebounce,
		newSource: newFSNotifySource, after: time.After}
}

func (monitor *RootMonitor) Run(ctx context.Context) error {
	if monitor == nil || monitor.Repo == nil {
		return errors.New("root monitor repository is required")
	}
	source, err := monitor.newSource()
	if err != nil {
		return errors.New("root watcher could not start")
	}
	defer source.Close()
	if len(monitor.Roots) == 0 {
		return errors.New("root monitor configuration is invalid")
	}
	roots := make(map[string]config.Root, len(monitor.Roots))
	for _, root := range monitor.Roots {
		rootPath := filepath.Clean(root.ContainerPath)
		if strings.TrimSpace(root.ID) == "" || !filepath.IsAbs(rootPath) {
			return errors.New("root monitor configuration is invalid")
		}
		if _, exists := roots[root.ID]; exists {
			return errors.New("root monitor configuration is invalid")
		}
		for _, existing := range roots {
			existingPath := filepath.Clean(existing.ContainerPath)
			if rootPath == existingPath || strings.HasPrefix(rootPath, existingPath+string(filepath.Separator)) || strings.HasPrefix(existingPath, rootPath+string(filepath.Separator)) {
				return errors.New("root monitor configuration is invalid")
			}
		}
		root.ContainerPath = rootPath
		roots[root.ID] = root
		if err := addTree(source, root.ContainerPath); err != nil {
			return errors.New("root watcher could not start")
		}
	}
	debounce := monitor.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	after := monitor.after
	if after == nil {
		after = time.After
	}
	type firing struct {
		id         string
		generation int
	}
	fires := make(chan firing)
	generations := map[string]int{}
	pendingScopes := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-source.Errors():
			if !ok {
				return errors.New("root watcher stopped")
			}
			return errors.New("root watcher failed")
		case change, ok := <-source.Events():
			if !ok {
				return errors.New("root watcher stopped")
			}
			rootID := rootForPath(roots, change.name)
			if rootID == "" || change.op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			scope, ok := eventParentScope(roots[rootID], change.name)
			if !ok {
				continue
			}
			if change.op&fsnotify.Create != 0 {
				if err := addTree(source, change.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return errors.New("root watcher failed")
				}
			}
			if previous, exists := pendingScopes[rootID]; exists {
				pendingScopes[rootID] = nearestCommonScope(previous, scope)
			} else {
				pendingScopes[rootID] = scope
			}
			generations[rootID]++
			generation := generations[rootID]
			timer := after(debounce)
			go func(id string, generation int, timer <-chan time.Time) {
				select {
				case <-ctx.Done():
				case <-timer:
					select {
					case fires <- firing{id, generation}:
					case <-ctx.Done():
					}
				}
			}(rootID, generation, timer)
		case fire := <-fires:
			if generations[fire.id] != fire.generation {
				continue
			}
			scope := pendingScopes[fire.id]
			delete(pendingScopes, fire.id)
			if err := monitor.enqueue(ctx, fire.id, scope); err != nil {
				return err
			}
		}
	}
}

func (monitor *RootMonitor) enqueue(ctx context.Context, rootID, relativePath string) error {
	if catalog.ValidateSourceFolder(relativePath) != nil {
		return errors.New("root change scan scope was invalid")
	}
	job := catalog.WorkerJob{ID: uuid.New(), Kind: catalog.JobScanRoot, Payload: map[string]any{
		"rootId": rootID, "relativePath": relativePath, "full": false,
	}}
	key := "scan_root:" + rootID + ":" + relativePath
	base, err := monitor.Repo.EnqueueDeduplicated(ctx, job, key)
	if err != nil {
		return errors.New("root change scan could not be queued")
	}
	if base.Status == catalog.JobQueued {
		return nil
	}
	if base.Status != catalog.JobRunning {
		return errors.New("root change scan response was invalid")
	}
	if base.ID == uuid.Nil {
		return errors.New("root change scan response was invalid")
	}
	_, err = monitor.Repo.EnqueueDeduplicated(ctx, job,
		"scan_root_followup:"+rootID+":"+relativePath+":"+base.ID.String())
	if err != nil {
		return errors.New("root change follow-up could not be queued")
	}
	return nil
}

func eventParentScope(root config.Root, name string) (string, bool) {
	relative, err := filepath.Rel(root.ContainerPath, filepath.Clean(name))
	if err != nil || relative == "." {
		return "", err == nil
	}
	relative = filepath.ToSlash(filepath.Dir(relative))
	if relative == "." {
		relative = ""
	}
	if catalog.ValidateSourceFolder(relative) != nil {
		return "", false
	}
	return relative, true
}

func nearestCommonScope(left, right string) string {
	if left == "" || right == "" {
		return ""
	}
	leftParts := strings.Split(left, "/")
	rightParts := strings.Split(right, "/")
	limit := len(leftParts)
	if len(rightParts) < limit {
		limit = len(rightParts)
	}
	common := 0
	for common < limit && leftParts[common] == rightParts[common] {
		common++
	}
	return strings.Join(leftParts[:common], "/")
}

func rootForPath(roots map[string]config.Root, name string) string {
	clean := filepath.Clean(name)
	match, matchLength := "", -1
	for id, root := range roots {
		base := filepath.Clean(root.ContainerPath)
		if (clean == base || strings.HasPrefix(clean, base+string(filepath.Separator))) && len(base) > matchLength {
			match, matchLength = id, len(base)
		}
	}
	return match
}

func addTree(source eventSource, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return source.Add(path)
		}
		return nil
	})
}

type fsnotifySource struct {
	watcher *fsnotify.Watcher
	events  chan event
	done    chan struct{}
}

func newFSNotifySource() (eventSource, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	s := &fsnotifySource{watcher: w, events: make(chan event, 256), done: make(chan struct{})}
	go func() {
		defer close(s.events)
		for e := range w.Events {
			select {
			case s.events <- event{name: e.Name, op: e.Op}:
			case <-s.done:
				return
			}
		}
	}()
	return s, nil
}
func (s *fsnotifySource) Add(path string) error { return s.watcher.Add(path) }
func (s *fsnotifySource) Close() error          { close(s.done); return s.watcher.Close() }
func (s *fsnotifySource) Events() <-chan event  { return s.events }
func (s *fsnotifySource) Errors() <-chan error  { return s.watcher.Errors }
