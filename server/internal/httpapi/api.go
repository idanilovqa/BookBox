package httpapi

import (
	"encoding/json"
	"net/http"

	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/library"
)

type Dependencies struct {
	Version string
	Roots   []config.Root
	Browser *library.Browser
}

type route struct {
	method  string
	path    string
	handler http.Handler
}

func New(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	for _, registered := range routes(deps) {
		mux.Handle(registered.method+" "+registered.path, registered.handler)
	}
	return mux
}

func routes(deps Dependencies) []route {
	return []route{
		{
			method: "GET",
			path:   "/v1/health",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{
					"status":  "ok",
					"version": deps.Version,
				})
			}),
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
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
