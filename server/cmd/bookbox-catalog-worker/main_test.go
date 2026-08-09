package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/monitor"
)

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }

func TestParseCommandModesLegacyAndDefaults(t *testing.T) {
	defaultPath := defaultWorkerConfigPath()
	tests := []struct {
		name string
		args []string
		mode commandMode
		path string
	}{
		{name: "default", mode: commandRun, path: defaultPath},
		{name: "explicit run", args: []string{"run", "--config", "run.yml"}, mode: commandRun, path: "run.yml"},
		{name: "service", args: []string{"service", "--config", "service.yml"}, mode: commandService, path: "service.yml"},
		{name: "legacy", args: []string{"--config", "legacy.yml"}, mode: commandRun, path: "legacy.yml"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := parseCommand(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if command.Mode != test.mode || command.ConfigPath != test.path {
				t.Fatalf("command = %#v, want mode=%q path=%q", command, test.mode, test.path)
			}
		})
	}
}

func TestParseCommandRejectsUnknownCommandsAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"},
		{"run", "extra"},
		{"service", "--config", "worker.yml", "extra"},
		{"run", "--unknown"},
	} {
		if command, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) = %#v, want error", args, command)
		}
	}
}

func TestOpenServiceLoggerWritesAndClosesOwnedLog(t *testing.T) {
	directory := t.TempDir()
	logger, closer, err := openServiceLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	logger.Print("catalog worker lifecycle")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "catalog-worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "catalog worker lifecycle") {
		t.Fatalf("service log = %q", contents)
	}
}

func TestServiceTerminalLogIsSanitizedAndSilentForExpectedCancellation(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&output, "", 0)
	logServiceTerminal(logger, context.Background(), errors.New(`database C:\Private password=secret`))
	if text := output.String(); !strings.Contains(text, "class=unavailable") ||
		strings.Contains(text, "Private") || strings.Contains(text, "secret") {
		t.Fatalf("terminal log = %q", text)
	}

	output.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logServiceTerminal(logger, ctx, context.Canceled)
	if output.Len() != 0 {
		t.Fatalf("expected cancellation log = %q", output.String())
	}
}

func TestWorkerShutdownCancelsAndJoinsMonitor(t *testing.T) {
	monitorStopped := false
	err := runWorkerWithMonitors(t.Context(), runnerFunc(func(context.Context) error { return nil }),
		[]rootMonitorRunner{{rootID: "audio", runner: runnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			monitorStopped = true
			return nil
		})}}, nil)
	if err != nil || !monitorStopped {
		t.Fatalf("err=%v monitorStopped=%v", err, monitorStopped)
	}
}

func TestRootSupervisorsIsolateFailuresAndJoinHealthyRoots(t *testing.T) {
	failureWarned := make(chan error, 1)
	healthyStarted := make(chan struct{})
	healthyStopped := make(chan struct{})
	created := make(chan string, 4)
	roots := []config.Root{{ID: "missing"}, {ID: "healthy"}}
	supervisors := newRootSupervisors(roots, func(root config.Root) monitor.Runner {
		created <- root.ID
		if root.ID == "missing" {
			return runnerFunc(func(context.Context) error { return errors.New("private path failed") })
		}
		return runnerFunc(func(ctx context.Context) error {
			select {
			case <-healthyStarted:
			default:
				close(healthyStarted)
			}
			<-ctx.Done()
			close(healthyStopped)
			return nil
		})
	}, func(err error) {
		select {
		case failureWarned <- err:
		default:
		}
	})
	worker := runnerFunc(func(context.Context) error {
		warning := <-failureWarned
		if !strings.Contains(warning.Error(), `root "missing"`) ||
			strings.Contains(warning.Error(), "private path") {
			return fmt.Errorf("unsafe root warning: %v", warning)
		}
		<-healthyStarted
		select {
		case <-healthyStopped:
			return errors.New("healthy root stopped after another root failed")
		default:
			return nil
		}
	})

	if err := runWorkerWithMonitors(t.Context(), worker, supervisors, func(error) {}); err != nil {
		t.Fatal(err)
	}
	<-healthyStopped
	seen := map[string]int{}
	for len(created) > 0 {
		seen[<-created]++
	}
	if seen["missing"] == 0 || seen["healthy"] != 1 {
		t.Fatalf("created runners = %v", seen)
	}
}

func TestTerminalRootSupervisorFailureWarnsWithoutLeakingCause(t *testing.T) {
	warned := make(chan error, 1)
	monitors := []rootMonitorRunner{{
		rootID: "audiobooks",
		runner: runnerFunc(func(context.Context) error {
			return errors.New(`watch failed at C:\Private\Library`)
		}),
	}}
	worker := runnerFunc(func(context.Context) error {
		<-warned
		return nil
	})
	var warning error
	if err := runWorkerWithMonitors(t.Context(), worker, monitors, func(err error) {
		warning = err
		warned <- err
	}); err != nil {
		t.Fatal(err)
	}
	if warning == nil || !strings.Contains(warning.Error(), "audiobooks") ||
		strings.Contains(warning.Error(), `C:\Private`) {
		t.Fatalf("warning = %v", warning)
	}
}

func TestMetadataProvidersAllowEmptyOptionalGoogleKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", keyPath)

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := metadataProviders(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want Open Library only", len(providers))
	}
}

func TestWorkerEnvironmentOverridesStayBoundedAndUseFixtureProviders(t *testing.T) {
	t.Setenv("BOOKBOX_SCAN_INTERVAL", "5s")
	t.Setenv("BOOKBOX_OPENLIBRARY_BASE_URL", "http://provider-fixtures:8080/openlibrary")
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_BASE_URL", "http://provider-fixtures:8080/googlebooks")

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if options.scanInterval != 5*time.Second {
		t.Fatalf("scan interval = %s, want 5s", options.scanInterval)
	}
	if options.openLibraryBaseURL != "http://provider-fixtures:8080/openlibrary" {
		t.Fatalf("Open Library base URL = %q", options.openLibraryBaseURL)
	}
	if options.googleBooksBaseURL != "http://provider-fixtures:8080/googlebooks" {
		t.Fatalf("Google Books base URL = %q", options.googleBooksBaseURL)
	}

	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte("fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", keyPath)
	providers, err := metadataProviders(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want deterministic Open Library and Google Books", len(providers))
	}
}

func TestWorkerConfigurationOverridesEnvironmentPathFallbacks(t *testing.T) {
	t.Setenv("BOOKBOX_COVER_BACKUP_ROOT", "/environment/backups")
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", "/environment/google-books-key")

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	options, err = applyWorkerSettings(options, config.WorkerSettings{
		CoverBackupRoot:       "/configured/backups",
		GoogleBooksAPIKeyFile: "/configured/google-books-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	if options.coverBackupRoot != "/configured/backups" {
		t.Fatalf("cover backup root = %q", options.coverBackupRoot)
	}
	if options.googleBooksAPIKeyFile != "/configured/google-books-key" {
		t.Fatalf("Google Books API key file = %q", options.googleBooksAPIKeyFile)
	}
}

func TestWorkerFFProbePathUsesEnvironmentFallbackAndConfigurationPrecedence(t *testing.T) {
	t.Setenv("BOOKBOX_FFPROBE_PATH", `  C:\Tools\ffmpeg\ffprobe.exe  `)

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if options.ffprobePath != `C:\Tools\ffmpeg\ffprobe.exe` {
		t.Fatalf("environment ffprobe path = %q", options.ffprobePath)
	}
	options, err = applyWorkerSettings(options, config.WorkerSettings{
		FFProbePath: ` D:\BookBox\ffprobe.exe `,
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.ffprobePath != `D:\BookBox\ffprobe.exe` {
		t.Fatalf("configured ffprobe path = %q", options.ffprobePath)
	}
}

func TestWorkerFFProbePathDefaultsAndRejectsLineBreaks(t *testing.T) {
	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if options.ffprobePath != "ffprobe" {
		t.Fatalf("default ffprobe path = %q", options.ffprobePath)
	}

	for _, value := range []string{
		"ffprobe\n--help", "ffprobe\r--help", "ffprobe\n", "ffprobe\r",
	} {
		t.Run(strings.ReplaceAll(value, "\n", "newline"), func(t *testing.T) {
			t.Setenv("BOOKBOX_FFPROBE_PATH", value)
			if _, err := workerOptionsFromEnvironment(); err == nil {
				t.Fatalf("BOOKBOX_FFPROBE_PATH=%q was accepted", value)
			}

			base := workerOptions{ffprobePath: "ffprobe"}
			if _, err := applyWorkerSettings(base, config.WorkerSettings{FFProbePath: value}); err == nil {
				t.Fatalf("configured ffprobe path %q was accepted", value)
			}
		})
	}
}

func TestWorkerEnvironmentRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "zero interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "0s"},
		{name: "negative interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "-1s"},
		{name: "excessive interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "25h"},
		{name: "Open Library credentials", variable: "BOOKBOX_OPENLIBRARY_BASE_URL", value: "http://owner:secret@fixtures/openlibrary"},
		{name: "Google Books file URL", variable: "BOOKBOX_GOOGLE_BOOKS_BASE_URL", value: "file:///fixtures/googlebooks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.variable, test.value)
			if _, err := workerOptionsFromEnvironment(); err == nil {
				t.Fatalf("%s=%q was accepted", test.variable, test.value)
			}
		})
	}
}
