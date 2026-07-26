package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"netstorage.local/server/internal/auth"
	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/httpapi"
	"netstorage.local/server/internal/library"
	"netstorage.local/server/internal/tlsidentity"
)

const version = "dev"

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
	flags := flag.NewFlagSet("netstorage-api", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "/etc/netstorage/config.yml", "path to the server configuration")
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
	identity, err := tlsidentity.Ensure(cfg.TLS.CertPath, cfg.TLS.KeyPath, cfg.TLS.Hosts)
	if err != nil {
		return err
	}
	handler, _, err := buildHandler(cfg, secrets, version, logger)
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
		"netStorage certificate SHA-256: %s\n",
		identity.SHA256Fingerprint,
	); err != nil {
		return err
	}
	return serve(server, cfg.TLS.CertPath, cfg.TLS.KeyPath)
}

func buildHandler(
	cfg config.Config,
	secrets config.Secrets,
	buildVersion string,
	logger *slog.Logger,
) (http.Handler, *auth.TokenManager, error) {
	resolver, err := library.NewResolver(cfg.Roots)
	if err != nil {
		return nil, nil, err
	}
	tokens, err := auth.NewTokenManager(secrets.SessionKey, time.Now)
	if err != nil {
		return nil, nil, err
	}
	handler := httpapi.New(httpapi.Dependencies{
		Version:      buildVersion,
		Roots:        cfg.Roots,
		Browser:      library.NewBrowser(resolver),
		Username:     cfg.Auth.Username,
		PasswordHash: secrets.PasswordHash,
		Tokens:       tokens,
		Limiter:      auth.NewLoginLimiter(10000, time.Now),
		Logger:       logger,
	})
	return handler, tokens, nil
}

func serveTLS(server *http.Server, certPath, keyPath string) error {
	return server.ListenAndServeTLS(certPath, keyPath)
}
