package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
)

type ReadyComponents struct {
	Database   bool `json:"database"`
	Migrations bool `json:"migrations"`
	Roots      bool `json:"roots"`
}

func (components ReadyComponents) Ready() bool {
	return components.Database && components.Migrations && components.Roots
}

type ReadinessProbe interface {
	Check(context.Context) ReadyComponents
}

type DatabasePinger interface {
	Ping(context.Context) error
}

type readinessProbe struct {
	database        DatabasePinger
	migrationsReady bool
	roots           []string
}

func readinessHandler(probe ReadinessProbe) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		components := ReadyComponents{}
		if probe != nil {
			components = probe.Check(request.Context())
		}
		status := http.StatusServiceUnavailable
		if components.Ready() {
			status = http.StatusOK
		}
		writeJSON(response, status, struct {
			Ready      bool            `json:"ready"`
			Components ReadyComponents `json:"components"`
		}{
			Ready:      components.Ready(),
			Components: components,
		})
	})
}

func NewReadinessProbe(
	database DatabasePinger,
	migrationsReady bool,
	roots []string,
) ReadinessProbe {
	return &readinessProbe{
		database:        database,
		migrationsReady: migrationsReady,
		roots:           append([]string(nil), roots...),
	}
}

func (probe *readinessProbe) Check(ctx context.Context) ReadyComponents {
	components := ReadyComponents{
		Migrations: probe != nil && probe.migrationsReady,
		Roots:      probe != nil && len(probe.roots) > 0,
	}
	if probe == nil {
		return components
	}
	components.Database = probe.database != nil && probe.database.Ping(ctx) == nil
	for _, root := range probe.roots {
		if !rootIsReadable(root) {
			components.Roots = false
			break
		}
	}
	return components
}

type directoryHandle interface {
	Stat() (os.FileInfo, error)
	Readdirnames(int) ([]string, error)
	Close() error
}

func rootIsReadable(path string) bool {
	handle, err := os.Open(path)
	if err != nil {
		return false
	}
	return readableDirectory(handle)
}

func readableDirectory(handle directoryHandle) bool {
	if handle == nil {
		return false
	}
	info, statErr := handle.Stat()
	if statErr != nil || !info.IsDir() {
		_ = handle.Close()
		return false
	}
	_, readErr := handle.Readdirnames(1)
	closeErr := handle.Close()
	return (readErr == nil || errors.Is(readErr, io.EOF)) && closeErr == nil
}
