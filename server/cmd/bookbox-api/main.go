package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/auth"
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/httpapi"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/metadata"
	"bookbox.local/server/internal/tlsidentity"
	"bookbox.local/server/internal/transfer"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return runWith(args, serveTLS, os.Stdout, logger)
}

type serveTLSFunc func(*http.Server, string, string) error

func runWith(args []string, serve serveTLSFunc, output io.Writer, logger *slog.Logger) error {
	flags := flag.NewFlagSet("bookbox-api", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "/etc/bookbox/config.yml", "path to the server configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	secrets, err := config.LoadSecrets(cfg.Auth)
	if err != nil {
		return err
	}
	if _, err := auth.VerifyPassword(secrets.PasswordHash, ""); err != nil {
		return err
	}
	databasePool, err := openCatalogDatabase(context.Background(), cfg)
	if err != nil {
		return err
	}
	if databasePool != nil {
		defer databasePool.Close()
	}
	identity, err := tlsidentity.Ensure(cfg.TLS.CertPath, cfg.TLS.KeyPath, cfg.TLS.Hosts)
	if err != nil {
		return err
	}
	handler, _, err := buildHandler(cfg, secrets, version, logger, databasePool)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	if _, err := fmt.Fprintf(
		output,
		"BookBox certificate SHA-256: %s\n",
		identity.SHA256Fingerprint,
	); err != nil {
		return err
	}
	return serve(server, cfg.TLS.CertPath, cfg.TLS.KeyPath)
}

func openCatalogDatabase(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	dsn, err := config.LoadDatabaseDSN(cfg.Database)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, nil
	}
	connectContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(connectContext, dsn)
	if err != nil {
		return nil, fmt.Errorf("configure catalog database: %w", err)
	}
	if err := pool.Ping(connectContext); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to catalog database: %w", err)
	}
	if err := database.Migrate(connectContext, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate catalog database: %w", err)
	}
	roots := make([]catalog.LibraryRoot, 0, len(cfg.Roots))
	for _, root := range cfg.Roots {
		roots = append(roots, catalog.LibraryRoot{
			ID:          root.ID,
			DisplayName: root.Name,
		})
	}
	if err := catalog.NewPostgresRepository(pool).SyncRoots(connectContext, roots); err != nil {
		pool.Close()
		return nil, fmt.Errorf("sync catalog roots: %w", err)
	}
	return pool, nil
}

func buildHandler(
	cfg config.Config,
	secrets config.Secrets,
	buildVersion string,
	logger *slog.Logger,
	databasePools ...*pgxpool.Pool,
) (http.Handler, *auth.TokenManager, error) {
	resolver, err := library.NewResolver(cfg.Roots)
	if err != nil {
		return nil, nil, err
	}
	tokens, err := auth.NewTokenManager(secrets.SessionKey, time.Now)
	if err != nil {
		return nil, nil, err
	}
	fileSigner, err := transfer.NewFileSigner(secrets.SessionKey)
	if err != nil {
		return nil, nil, err
	}
	var catalogRepository *catalog.PostgresRepository
	var matcher *metadata.Matcher
	var readiness httpapi.ReadinessProbe
	var transferBuilder httpapi.ManifestBuilder = transfer.NewBuilder(resolver, fileSigner)
	if len(databasePools) > 0 && databasePools[0] != nil {
		catalogRepository = catalog.NewPostgresRepository(databasePools[0])
		openLibrary, err := metadata.NewOpenLibrary("", nil)
		if err != nil {
			return nil, nil, err
		}
		matcher = metadata.NewMatcher(openLibrary)
		transferBuilder = transfer.NewCatalogBuilder(resolver, fileSigner, catalogRepository)
		rootPaths := make([]string, 0, len(cfg.Roots))
		for _, root := range cfg.Roots {
			rootPaths = append(rootPaths, root.ContainerPath)
		}
		readiness = httpapi.NewReadinessProbe(databasePools[0], true, rootPaths)
	}
	handler := httpapi.New(httpapi.Dependencies{
		Version:          buildVersion,
		Roots:            cfg.Roots,
		Browser:          library.NewBrowser(resolver),
		Username:         cfg.Auth.Username,
		PasswordHash:     secrets.PasswordHash,
		PasswordVerifier: auth.ProductionPasswordVerifier,
		Tokens:           tokens,
		Limiter:          auth.NewLoginLimiter(10000, time.Now),
		Logger:           logger,
		Resolver:         resolver,
		TransferBuilder:  transferBuilder,
		FileSigner:       fileSigner,
		Catalog:          catalogRepository,
		MetadataMatcher:  matcher,
		Readiness:        readiness,
	})
	return handler, tokens, nil
}

func serveTLS(server *http.Server, certPath, keyPath string) error {
	return server.ListenAndServeTLS(certPath, keyPath)
}
