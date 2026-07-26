package httpapi

import (
	"encoding/json"
	"net/http"
)

type Dependencies struct {
	Version string
}

func New(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": deps.Version,
		})
	})
	return mux
}
