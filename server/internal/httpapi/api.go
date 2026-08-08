package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"bookbox.local/server/internal/auth"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/transfer"
)

type Dependencies struct {
	Version          string
	Roots            []config.Root
	Browser          *library.Browser
	Username         string
	PasswordHash     string
	PasswordVerifier *auth.PasswordVerifier
	Tokens           *auth.TokenManager
	Limiter          *auth.LoginLimiter
	Logger           *slog.Logger
	Resolver         *library.Resolver
	TransferBuilder  ManifestBuilder
	FileSigner       *transfer.FileSigner
	Catalog          CatalogRepository
	MetadataMatcher  MetadataMatcher
	Readiness        ReadinessProbe
	Now              func() time.Time
}

type route struct {
	method  string
	path    string
	public  bool
	handler http.Handler
}

func New(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	for _, registered := range routes(deps) {
		handler := registered.handler
		if !registered.public {
			handler = requireAccessToken(deps.Tokens, handler)
		}
		mux.Handle(registered.method+" "+registered.path, handler)
	}
	if deps.Logger == nil {
		return mux
	}
	return logRequests(deps.Logger, mux)
}

func routes(deps Dependencies) []route {
	gate := newManifestGate(128)
	return []route{
		{
			method: "GET",
			path:   "/v1/health",
			public: true,
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{
					"status":  "ok",
					"version": deps.Version,
				})
			}),
		},
		{
			method:  "GET",
			path:    "/v1/ready",
			public:  true,
			handler: readinessHandler(deps.Readiness),
		},
		{
			method:  "POST",
			path:    "/v1/session",
			public:  true,
			handler: loginHandler(deps),
		},
		{
			method:  "POST",
			path:    "/v1/session/refresh",
			public:  true,
			handler: refreshHandler(deps),
		},
		{
			method:  "GET",
			path:    "/v1/roots",
			handler: rootsHandler(deps.Roots),
		},
		{
			method:  "GET",
			path:    "/v1/browse",
			handler: browseHandler(deps.Browser),
		},
		{
			method:  "GET",
			path:    "/v1/browse/search",
			handler: folderSearchHandler(deps.Catalog, deps.Roots),
		},
		{
			method:  "GET",
			path:    "/v1/books",
			handler: booksHandler(deps.Catalog, deps.Roots),
		},
		{
			method:  "GET",
			path:    "/v1/books/{bookId}",
			handler: bookDetailHandler(deps.Catalog),
		},
		{
			method:  "GET",
			path:    "/v1/books/{bookId}/files",
			handler: bookFilesHandler(deps.Catalog),
		},
		{
			method:  "GET",
			path:    "/v1/books/{bookId}/cover",
			handler: bookCoverHandler(deps.Catalog, deps.Resolver),
		},
		{
			method:  "GET",
			path:    "/v1/catalog/filters",
			handler: catalogFiltersHandler(deps.Catalog),
		},
		{
			method:  "POST",
			path:    "/v1/catalog/matches/search",
			handler: matchSearchHandler(deps.Catalog, deps.MetadataMatcher),
		},
		{
			method:  "PUT",
			path:    "/v1/books/{bookId}/match",
			handler: confirmMatchHandler(deps.Catalog),
		},
		{
			method:  "GET",
			path:    "/v1/books/{bookId}/grouping",
			handler: groupingContextHandler(deps.Catalog),
		},
		{
			method:  "PUT",
			path:    "/v1/books/{bookId}/grouping",
			handler: groupingHandler(deps.Catalog),
		},
		{
			method:  "POST",
			path:    "/v1/catalog/rescan",
			handler: rescanHandler(deps.Catalog, deps.Roots, deps.Now),
		},
		{
			method:  "POST",
			path:    "/v1/transfers/manifest",
			handler: manifestHandler(deps.TransferBuilder, gate),
		},
		{
			method:  "GET",
			path:    "/v1/files/{fileId}",
			handler: fileHandler(deps.Resolver, deps.FileSigner),
		},
	}
}

type accessSubjectContextKey struct{}

func requireAccessToken(tokens *auth.TokenManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		values := request.Header.Values("Authorization")
		if len(values) != 1 {
			writeBearerUnauthorized(w)
			return
		}
		fields := strings.Fields(values[0])
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") ||
			strings.Contains(fields[1], ",") || tokens == nil {
			writeBearerUnauthorized(w)
			return
		}
		claims, err := tokens.ValidateAccess(fields[1])
		if err != nil {
			writeBearerUnauthorized(w)
			return
		}
		ctx := context.WithValue(
			request.Context(),
			accessSubjectContextKey{},
			claims.Subject,
		)
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}

func accessSubject(ctx context.Context) (string, bool) {
	subject, ok := ctx.Value(accessSubjectContextKey{}).(string)
	return subject, ok && subject != ""
}

func writeBearerUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, errorResponse{Error: errorDetail{
		Code:    "unauthorized",
		Message: "authentication required",
	}})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusResponseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(writer, request)
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		routePattern := request.Pattern
		if routePattern == "" {
			routePattern = "<unmatched>"
		}
		logger.Info("http request",
			"method", request.Method,
			"route", routePattern,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
