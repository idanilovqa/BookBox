package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReadyRequiresDatabaseMigrationsAndReadableRoots(t *testing.T) {
	tests := []struct {
		name       string
		components ReadyComponents
		status     int
	}{
		{
			name: "database unavailable",
			components: ReadyComponents{
				Database: false, Migrations: true, Roots: true,
			},
			status: http.StatusServiceUnavailable,
		},
		{
			name: "migrations unavailable",
			components: ReadyComponents{
				Database: true, Migrations: false, Roots: true,
			},
			status: http.StatusServiceUnavailable,
		},
		{
			name: "root unavailable",
			components: ReadyComponents{
				Database: true, Migrations: true, Roots: false,
			},
			status: http.StatusServiceUnavailable,
		},
		{
			name: "all dependencies ready",
			components: ReadyComponents{
				Database: true, Migrations: true, Roots: true,
			},
			status: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := New(Dependencies{
				Version:   "test",
				Readiness: readyProbeFake{components: test.components},
			})
			response := httptest.NewRecorder()
			api.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/v1/ready",
				nil,
			))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			var body struct {
				Ready      bool            `json:"ready"`
				Components ReadyComponents `json:"components"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Ready != (test.status == http.StatusOK) ||
				body.Components != test.components {
				t.Fatalf("body = %#v", body)
			}
		})
	}
}

func TestReadyResponseNeverExposesUnderlyingDetails(t *testing.T) {
	api := New(Dependencies{
		Readiness: readyProbeFake{
			components: ReadyComponents{},
			secret:     `postgres://owner:password@db/H:/Audiobooks`,
		},
	})
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))

	body := response.Body.String()
	for _, forbidden := range []string{
		"password", "postgres://", "Audiobooks", "H:/", "owner",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("readiness response exposed %q: %s", forbidden, body)
		}
	}
}

func TestReadyFilesystemRequiresEveryConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	probe := NewReadinessProbe(
		databasePingerFake{},
		true,
		[]string{root, root + "/missing"},
	)
	components := probe.Check(t.Context())
	if !components.Database || !components.Migrations || components.Roots {
		t.Fatalf("components = %#v", components)
	}
}

func TestReadyFilesystemRequiresDirectoryEntriesToBeReadable(t *testing.T) {
	handle, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unreadable := &directoryHandleFake{
		File:    handle,
		readErr: errors.New("directory entries denied"),
	}
	if readableDirectory(unreadable) {
		t.Fatal("directory with unreadable entries reported ready")
	}
	if !unreadable.readCalled {
		t.Fatal("readiness did not attempt to read a directory entry")
	}
}

type readyProbeFake struct {
	components ReadyComponents
	secret     string
}

func (fake readyProbeFake) Check(context.Context) ReadyComponents {
	_ = fake.secret
	return fake.components
}

type databasePingerFake struct {
	err error
}

func (fake databasePingerFake) Ping(context.Context) error {
	return fake.err
}

type directoryHandleFake struct {
	*os.File
	readErr    error
	readCalled bool
}

func (handle *directoryHandleFake) Readdirnames(int) ([]string, error) {
	handle.readCalled = true
	return nil, handle.readErr
}
