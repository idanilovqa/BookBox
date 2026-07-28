package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/discovery"
)

type recordingRegistrar struct {
	cfg    discovery.Config
	cancel context.CancelFunc
}

func (r *recordingRegistrar) Register(cfg discovery.Config) (discovery.Registration, error) {
	r.cfg = cfg
	r.cancel()
	return inertRegistration{}, nil
}

type inertRegistration struct{}

func (inertRegistration) Shutdown() {}

type waitingRegistrar struct {
	registered chan struct{}
}

func (r waitingRegistrar) Register(discovery.Config) (discovery.Registration, error) {
	close(r.registered)
	return inertRegistration{}, nil
}

type countingRegistrar struct {
	calls int
}

func (r *countingRegistrar) Register(discovery.Config) (discovery.Registration, error) {
	r.calls++
	return inertRegistration{}, nil
}

type fixedAddressSource struct{ address net.IP }

func (s fixedAddressSource) Current(context.Context) (net.IP, error) {
	return s.address, nil
}

func TestRunNormalizesHostnameAndUsesDefaults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registrar := &recordingRegistrar{cancel: cancel}
	var output bytes.Buffer

	err := runWith(ctx, []string{"-address", "192.168.1.25", "-hostname", "bookbox.local"}, dependencies{
		registrar: registrar,
		addressSource: func(address net.IP) (discovery.AddressSource, error) {
			return fixedAddressSource{address: address}, nil
		},
		output: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registrar.cfg.Hostname != "bookbox.local." {
		t.Fatalf("hostname = %q", registrar.cfg.Hostname)
	}
	if registrar.cfg.Port != 8787 || registrar.cfg.PollInterval != 5*time.Second {
		t.Fatalf("config = %#v", registrar.cfg)
	}
	if !strings.Contains(output.String(), "bookbox.local.") ||
		!strings.Contains(output.String(), "192.168.1.25") {
		t.Fatalf("log output = %q", output.String())
	}
}

func TestRunRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing address", nil},
		{"public address", []string{"-address", "8.8.8.8"}},
		{"other hostname", []string{"-address", "192.168.1.25", "-hostname", "other.local"}},
		{"short poll", []string{"-address", "192.168.1.25", "-poll-interval", "999ms"}},
		{"empty stop file", []string{"-address", "192.168.1.25", "-stop-file", ""}},
		{"relative stop file", []string{"-address", "192.168.1.25", "-stop-file", "stop.signal"}},
		{"positional", []string{"-address", "192.168.1.25", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runWith(context.Background(), test.args, dependencies{}); err == nil {
				t.Fatal("runWith() error = nil")
			}
		})
	}
}

func TestRunStopsCleanlyWhenStopFileAppears(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "stop.signal")
	registered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runWith(
			context.Background(),
			[]string{"-address", "192.168.1.25", "-stop-file", stopFile},
			dependencies{
				registrar: waitingRegistrar{registered: registered},
				addressSource: func(address net.IP) (discovery.AddressSource, error) {
					return fixedAddressSource{address: address}, nil
				},
			},
		)
	}()
	select {
	case <-registered:
	case err := <-result:
		t.Fatalf("runWith returned before registration: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runWith did not register discovery")
	}
	if err := os.WriteFile(stopFile, []byte("stop"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runWith did not stop after stop file appeared")
	}
}

func TestRunRejectsPreexistingStopFileBeforeRegistration(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "stop.signal")
	if err := os.WriteFile(stopFile, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	registrar := &countingRegistrar{}

	err := runWith(
		context.Background(),
		[]string{"-address", "192.168.1.25", "-stop-file", stopFile},
		dependencies{registrar: registrar},
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("runWith() error = %v", err)
	}
	if registrar.calls != 0 {
		t.Fatalf("registrar calls = %d, want 0", registrar.calls)
	}
}

func TestRunRejectsStopFileInspectionErrorBeforeRegistration(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "stop.signal")
	registrar := &countingRegistrar{}

	err := runWith(
		context.Background(),
		[]string{"-address", "192.168.1.25", "-stop-file", stopFile},
		dependencies{
			registrar: registrar,
			stat: func(string) (os.FileInfo, error) {
				return nil, os.ErrPermission
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "inspect -stop-file") {
		t.Fatalf("runWith() error = %v", err)
	}
	if registrar.calls != 0 {
		t.Fatalf("registrar calls = %d, want 0", registrar.calls)
	}
}
