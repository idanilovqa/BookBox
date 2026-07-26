package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/httpapi"
)

const version = "dev"

var errTLSNotConfigured = errors.New("TLS is not configured")

func main() {
	if err := run(os.Args[1:], os.Getenv("NETSTORAGE_INSECURE_TEST_HTTP") == "1"); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, insecureTestHTTP bool) error {
	flags := flag.NewFlagSet("netstorage-api", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/netstorage/config.yml", "path to the server configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           httpapi.New(httpapi.Dependencies{Version: version}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return serve(server, insecureTestHTTP)
}

func serve(server *http.Server, insecureTestHTTP bool) error {
	if !insecureTestHTTP {
		return errTLSNotConfigured
	}
	return server.ListenAndServe()
}
