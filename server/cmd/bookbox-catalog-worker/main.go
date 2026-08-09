package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/covers"
	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
	"bookbox.local/server/internal/metadata"
	"bookbox.local/server/internal/monitor"
	"bookbox.local/server/internal/scanner"
	"bookbox.local/server/internal/worker"
	"bookbox.local/server/internal/workerlog"
)

const windowsServiceName = "BookBoxCatalogWorker"

type commandMode string

const (
	commandRun     commandMode = "run"
	commandService commandMode = "service"
	commandHelp    commandMode = "help"
)

const workerUsage = "usage: bookbox-catalog-worker [run|service] --config <path>"

type workerCommand struct {
	Mode       commandMode
	ConfigPath string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	command, err := parseCommand(args)
	if err != nil {
		return err
	}
	if command.Mode == commandHelp {
		_, err := fmt.Fprintln(os.Stdout, workerUsage)
		return err
	}
	if command.Mode == commandService {
		return runServiceCommand(command.ConfigPath)
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return runProgram(ctx, command.ConfigPath, log.Default())
}

func parseCommand(args []string) (workerCommand, error) {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		return workerCommand{Mode: commandHelp}, nil
	}
	mode := commandRun
	modeArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case string(commandRun):
			mode = commandRun
		case string(commandService):
			mode = commandService
		default:
			return workerCommand{}, errors.New("unknown catalog worker command")
		}
		modeArgs = args[1:]
	}
	flags := flag.NewFlagSet("bookbox-catalog-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String(
		"config",
		defaultWorkerConfigPath(),
		"path to the server configuration",
	)
	if err := flags.Parse(modeArgs); err != nil {
		return workerCommand{}, err
	}
	if flags.NArg() != 0 {
		return workerCommand{}, errors.New("unexpected catalog worker arguments")
	}
	return workerCommand{Mode: mode, ConfigPath: *configPath}, nil
}

func runProgram(ctx context.Context, configPath string, logger *log.Logger) error {
	if logger == nil {
		return errors.New("catalog worker logger is required")
	}
	cfg, err := config.LoadWorker(configPath)
	if err != nil {
		return err
	}
	return runConfiguredProgram(ctx, cfg, logger)
}

func runConfiguredProgram(ctx context.Context, cfg config.Config, logger *log.Logger) error {
	if logger == nil {
		return errors.New("catalog worker logger is required")
	}
	dsn, err := config.LoadDatabaseDSN(cfg.Database)
	if err != nil {
		return err
	}
	if dsn == "" {
		return errors.New("catalog worker requires PostgreSQL configuration")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("configure catalog database: %w", err)
	}
	defer pool.Close()
	if err := retryUntilReady(ctx, retryPolicy{
		Initial:        time.Second,
		Maximum:        time.Minute,
		AttemptTimeout: 10 * time.Second,
	}, pool.Ping, func(class string) {
		logger.Printf("warning: catalog database %s", class)
	}); err != nil {
		return err
	}
	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate catalog database: %w", err)
	}
	repository := catalog.NewPostgresRepository(pool)
	roots := make([]catalog.LibraryRoot, 0, len(cfg.Roots))
	rootByID := make(map[string]config.Root, len(cfg.Roots))
	for _, root := range cfg.Roots {
		roots = append(roots, catalog.LibraryRoot{
			ID: root.ID, DisplayName: root.Name,
		})
		rootByID[root.ID] = root
	}
	if err := repository.SyncRoots(ctx, roots); err != nil {
		return fmt.Errorf("sync catalog roots: %w", err)
	}

	resolver, err := library.NewResolver(cfg.Roots)
	if err != nil {
		return err
	}
	options, err := workerOptionsFromEnvironment()
	if err != nil {
		return err
	}
	options, err = applyWorkerSettings(options, cfg.Worker)
	if err != nil {
		return err
	}
	providers, err := metadataProviders(options)
	if err != nil {
		return err
	}
	scanHandler := &worker.ScanRootHandler{
		Repo: repository,
		Scanner: scanner.New(
			media.NewFFProbe(options.ffprobePath),
			scanner.Options{},
		),
		Roots: rootByID,
	}
	enrichHandler := &worker.EnrichBookHandler{
		Repo: repository, Matcher: metadata.NewMatcher(providers...),
	}
	publishHandler := &worker.PublishCoverHandler{
		Repo:       repository,
		Downloader: covers.NewDownloader(&http.Client{Timeout: 30 * time.Second}),
		Publisher:  covers.NewPublisher(resolver, repository, options.coverBackupRoot),
	}
	host, _ := os.Hostname()
	runtime := &worker.Worker{
		Repo:         repository,
		Roots:        cfg.Roots,
		WorkerID:     strings.TrimSpace(host) + ":" + uuid.NewString(),
		ScanInterval: options.scanInterval,
		Handlers: map[string]worker.Handler{
			catalog.JobScanRoot:     scanHandler,
			catalog.JobEnrichBook:   enrichHandler,
			catalog.JobPublishCover: publishHandler,
		},
	}
	supervisors := newRootSupervisors(cfg.Roots, func(root config.Root) monitor.Runner {
		return monitor.New(repository, []config.Root{root})
	}, func(err error) {
		logger.Printf("warning: root monitor unavailable: %v", err)
	})
	return runWorkerWithMonitors(ctx, runtime, supervisors, func(err error) {
		logger.Printf("warning: %v", err)
	})
}

func runServiceCommand(configPath string) error {
	return runWindowsService(windowsServiceName, func(ctx context.Context) error {
		cfg, err := config.LoadWorker(configPath)
		if err != nil {
			return err
		}
		logger, closer, err := openServiceLogger(cfg.Worker.LogDirectory)
		if err != nil {
			return err
		}
		defer closer.Close()
		err = runConfiguredProgram(ctx, cfg, logger)
		logServiceTerminal(logger, ctx, err)
		return err
	})
}

func logServiceTerminal(logger *log.Logger, ctx context.Context, err error) {
	if err == nil || logger == nil {
		return
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return
	}
	logger.Printf("error: catalog worker service stopped (class=%s)", readinessErrorClass(err))
}

func openServiceLogger(directory string) (*log.Logger, io.Closer, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, nil, errors.New("catalog worker service log directory is required")
	}
	writer, err := workerlog.Open(directory, 10<<20, 4)
	if err != nil {
		return nil, nil, err
	}
	return log.New(writer, "", log.Ldate|log.Ltime|log.LUTC), writer, nil
}

type contextRunner interface{ Run(context.Context) error }

type rootMonitorRunner struct {
	rootID string
	runner contextRunner
}

func newRootSupervisors(
	roots []config.Root,
	newRunner func(config.Root) monitor.Runner,
	warn func(error),
) []rootMonitorRunner {
	supervisors := make([]rootMonitorRunner, 0, len(roots))
	for _, configuredRoot := range roots {
		root := configuredRoot
		supervisors = append(supervisors, rootMonitorRunner{
			rootID: root.ID,
			runner: monitor.Supervisor{
				New: func() monitor.Runner { return newRunner(root) },
				Warn: func(err error) {
					if warn != nil {
						warn(fmt.Errorf("root %q: %w", root.ID, err))
					}
				},
			},
		})
	}
	return supervisors
}

func runWorkerWithMonitors(
	ctx context.Context,
	runtime contextRunner,
	rootMonitors []rootMonitorRunner,
	warn func(error),
) error {
	runCtx, cancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{}, len(rootMonitors))
	for _, configuredMonitor := range rootMonitors {
		rootMonitor := configuredMonitor
		go func() {
			defer func() { monitorDone <- struct{}{} }()
			if err := rootMonitor.runner.Run(runCtx); err != nil && runCtx.Err() == nil && warn != nil {
				warn(fmt.Errorf("root %q monitor supervisor stopped", rootMonitor.rootID))
			}
		}()
	}
	err := runtime.Run(runCtx)
	cancel()
	for range rootMonitors {
		<-monitorDone
	}
	return err
}

type workerOptions struct {
	scanInterval          time.Duration
	ffprobePath           string
	openLibraryBaseURL    string
	googleBooksBaseURL    string
	coverBackupRoot       string
	googleBooksAPIKeyFile string
}

func workerOptionsFromEnvironment() (workerOptions, error) {
	rawFFProbePath := os.Getenv("BOOKBOX_FFPROBE_PATH")
	if hasLineBreak(rawFFProbePath) {
		return workerOptions{}, errors.New("BOOKBOX_FFPROBE_PATH is invalid")
	}
	options := workerOptions{
		scanInterval:          worker.ScanInterval,
		ffprobePath:           strings.TrimSpace(rawFFProbePath),
		openLibraryBaseURL:    strings.TrimSpace(os.Getenv("BOOKBOX_OPENLIBRARY_BASE_URL")),
		googleBooksBaseURL:    strings.TrimSpace(os.Getenv("BOOKBOX_GOOGLE_BOOKS_BASE_URL")),
		coverBackupRoot:       strings.TrimSpace(os.Getenv("BOOKBOX_COVER_BACKUP_ROOT")),
		googleBooksAPIKeyFile: strings.TrimSpace(os.Getenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE")),
	}
	if options.ffprobePath == "" {
		options.ffprobePath = "ffprobe"
	}
	if options.coverBackupRoot == "" {
		options.coverBackupRoot = "/var/lib/bookbox/backups"
	}
	if raw := strings.TrimSpace(os.Getenv("BOOKBOX_SCAN_INTERVAL")); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 || interval > 24*time.Hour {
			return workerOptions{}, errors.New("BOOKBOX_SCAN_INTERVAL is invalid")
		}
		options.scanInterval = interval
	}
	for variable, baseURL := range map[string]string{
		"BOOKBOX_OPENLIBRARY_BASE_URL":  options.openLibraryBaseURL,
		"BOOKBOX_GOOGLE_BOOKS_BASE_URL": options.googleBooksBaseURL,
	} {
		if err := validateProviderBaseURL(baseURL); err != nil {
			return workerOptions{}, fmt.Errorf("%s is invalid", variable)
		}
	}
	return options, nil
}

func applyWorkerSettings(options workerOptions, settings config.WorkerSettings) (workerOptions, error) {
	if hasLineBreak(settings.FFProbePath) {
		return workerOptions{}, errors.New("worker.ffprobe_path is invalid")
	}
	if configured := strings.TrimSpace(settings.FFProbePath); configured != "" {
		options.ffprobePath = configured
	}
	if settings.CoverBackupRoot != "" {
		options.coverBackupRoot = settings.CoverBackupRoot
	}
	if settings.GoogleBooksAPIKeyFile != "" {
		options.googleBooksAPIKeyFile = settings.GoogleBooksAPIKeyFile
	}
	return options, nil
}

func hasLineBreak(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func validateProviderBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("provider base URL is unsafe")
	}
	return nil
}

func metadataProviders(options workerOptions) ([]metadata.Provider, error) {
	openLibrary, err := metadata.NewOpenLibrary(options.openLibraryBaseURL, nil)
	if err != nil {
		return nil, err
	}
	providers := []metadata.Provider{openLibrary}
	keyPath := options.googleBooksAPIKeyFile
	if keyPath == "" {
		keyPath = strings.TrimSpace(os.Getenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE"))
	}
	if keyPath == "" {
		return providers, nil
	}
	info, err := os.Lstat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("Google Books API key file is invalid")
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, errors.New("Google Books API key could not be read")
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return providers, nil
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Google Books API key file is invalid")
	}
	googleBooks, err := metadata.NewGoogleBooks(options.googleBooksBaseURL, key, nil)
	if err != nil {
		return nil, err
	}
	return append(providers, googleBooks), nil
}
