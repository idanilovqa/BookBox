package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bookbox.local/server/internal/discovery"
)

type dependencies struct {
	registrar     discovery.Registrar
	addressSource func(net.IP) (discovery.AddressSource, error)
	output        io.Writer
	stat          func(string) (os.FileInfo, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWith(ctx, os.Args[1:], dependencies{output: os.Stderr}); err != nil {
		log.Fatal(err)
	}
}

func runWith(ctx context.Context, args []string, deps dependencies) error {
	flags := flag.NewFlagSet("bookbox-discovery", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	addressRaw := flags.String("address", "", "private IPv4 address to advertise")
	port := flags.Int("port", 8787, "BookBox HTTP port")
	hostname := flags.String("hostname", "bookbox.local", "BookBox mDNS hostname")
	pollInterval := flags.Duration("poll-interval", 5*time.Second, "interface address polling interval")
	stopFile := flags.String("stop-file", "", "optional absolute path whose creation requests shutdown")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	stopFileSet := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == "stop-file" {
			stopFileSet = true
		}
	})
	stopPath := strings.TrimSpace(*stopFile)
	if stopFileSet && stopPath == "" {
		return errors.New("-stop-file must not be empty when provided")
	}
	if stopPath != "" && !filepath.IsAbs(stopPath) {
		return errors.New("-stop-file must be an absolute path")
	}

	rawHostname := strings.TrimSpace(*hostname)
	if rawHostname != "" && !strings.HasSuffix(rawHostname, ".") {
		rawHostname += "."
	}
	address := net.ParseIP(strings.TrimSpace(*addressRaw))
	if address == nil {
		return errors.New("-address must be a valid private IPv4 address")
	}
	cfg := discovery.Config{
		Hostname:     rawHostname,
		Service:      discovery.Service,
		Domain:       discovery.Domain,
		Address:      address,
		Port:         *port,
		PollInterval: *pollInterval,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if stopPath != "" {
		stat := deps.stat
		if stat == nil {
			stat = os.Stat
		}
		_, err := stat(stopPath)
		switch {
		case err == nil:
			return fmt.Errorf("-stop-file already exists: %s", stopPath)
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return fmt.Errorf("inspect -stop-file: %w", err)
		}
	}

	registrar := deps.registrar
	if registrar == nil {
		registrar = discovery.NewZeroconfRegistrar()
	}
	newAddressSource := deps.addressSource
	if newAddressSource == nil {
		newAddressSource = func(address net.IP) (discovery.AddressSource, error) {
			return discovery.NewInterfaceAddressSource(address)
		}
	}
	source, err := newAddressSource(cfg.Address)
	if err != nil {
		return fmt.Errorf("select discovery interface: %w", err)
	}

	output := deps.output
	if output == nil {
		output = io.Discard
	}
	logger := log.New(output, "", log.LstdFlags)
	logger.Printf(
		"starting hostname=%s address=%s port=%d",
		cfg.Hostname,
		cfg.Address,
		cfg.Port,
	)
	runCtx := ctx
	if stopPath != "" {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		go watchStopFile(runCtx, stopPath, cancel)
	}
	if err := discovery.Run(runCtx, cfg, registrar, source); err != nil {
		return err
	}
	logger.Printf(
		"stopped hostname=%s address=%s port=%d",
		cfg.Hostname,
		cfg.Address,
		cfg.Port,
	)
	return nil
}

func watchStopFile(ctx context.Context, path string, cancel context.CancelFunc) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				cancel()
				return
			}
		}
	}
}
