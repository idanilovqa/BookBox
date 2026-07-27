package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

	"netstorage.local/server/internal/catalog"
	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/covers"
	"netstorage.local/server/internal/database"
	"netstorage.local/server/internal/library"
	"netstorage.local/server/internal/media"
	"netstorage.local/server/internal/metadata"
	"netstorage.local/server/internal/scanner"
	"netstorage.local/server/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("netstorage-catalog-worker", flag.ContinueOnError)
	configPath := flags.String(
		"config",
		"/etc/netstorage/config.yml",
		"path to the server configuration",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	dsn, err := config.LoadDatabaseDSN(cfg.Database)
	if err != nil {
		return err
	}
	if dsn == "" {
		return errors.New("catalog worker requires PostgreSQL configuration")
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.New(startupCtx, dsn)
	if err != nil {
		return fmt.Errorf("configure catalog database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(startupCtx); err != nil {
		return fmt.Errorf("connect to catalog database: %w", err)
	}
	if err := database.Migrate(startupCtx, pool); err != nil {
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
	if err := repository.SyncRoots(startupCtx, roots); err != nil {
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
	providers, err := metadataProviders(options)
	if err != nil {
		return err
	}
	backupRoot := strings.TrimSpace(os.Getenv("NETSTORAGE_COVER_BACKUP_ROOT"))
	if backupRoot == "" {
		backupRoot = "/var/lib/netstorage/backups"
	}
	scanHandler := &worker.ScanRootHandler{
		Repo: repository,
		Scanner: scanner.New(
			media.NewFFProbe("ffprobe"),
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
		Publisher:  covers.NewPublisher(resolver, repository, backupRoot),
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
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return runtime.Run(ctx)
}

type workerOptions struct {
	scanInterval       time.Duration
	openLibraryBaseURL string
	googleBooksBaseURL string
}

func workerOptionsFromEnvironment() (workerOptions, error) {
	options := workerOptions{
		scanInterval:       worker.ScanInterval,
		openLibraryBaseURL: strings.TrimSpace(os.Getenv("NETSTORAGE_OPENLIBRARY_BASE_URL")),
		googleBooksBaseURL: strings.TrimSpace(os.Getenv("NETSTORAGE_GOOGLE_BOOKS_BASE_URL")),
	}
	if raw := strings.TrimSpace(os.Getenv("NETSTORAGE_SCAN_INTERVAL")); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 || interval > 24*time.Hour {
			return workerOptions{}, errors.New("NETSTORAGE_SCAN_INTERVAL is invalid")
		}
		options.scanInterval = interval
	}
	for variable, baseURL := range map[string]string{
		"NETSTORAGE_OPENLIBRARY_BASE_URL":  options.openLibraryBaseURL,
		"NETSTORAGE_GOOGLE_BOOKS_BASE_URL": options.googleBooksBaseURL,
	} {
		if err := validateProviderBaseURL(baseURL); err != nil {
			return workerOptions{}, fmt.Errorf("%s is invalid", variable)
		}
	}
	return options, nil
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
	keyPath := strings.TrimSpace(os.Getenv("NETSTORAGE_GOOGLE_BOOKS_API_KEY_FILE"))
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
