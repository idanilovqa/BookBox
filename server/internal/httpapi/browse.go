package httpapi

import (
	"errors"
	"net/http"
	"os"

	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/library"
)

type publicRoot struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type rootsResponse struct {
	Roots []publicRoot `json:"roots"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func rootsHandler(roots []config.Root) http.Handler {
	public := make([]publicRoot, len(roots))
	for index, root := range roots {
		public[index] = publicRoot{ID: root.ID, Name: root.Name}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, rootsResponse{Roots: public})
	})
}

func browseHandler(browser *library.Browser) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if browser == nil {
			writeError(w, errors.New("browser is not configured"))
			return
		}
		directory, err := browser.List(
			request.Context(),
			request.URL.Query().Get("root"),
			request.URL.Query().Get("path"),
		)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, directory)
	})
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	detail := errorDetail{Code: "internal_error", Message: "internal server error"}
	switch {
	case errors.Is(err, library.ErrUnknownRoot):
		status = http.StatusNotFound
		detail = errorDetail{Code: "root_not_found", Message: "root not found"}
	case errors.Is(err, library.ErrPathEscape):
		status = http.StatusBadRequest
		detail = errorDetail{
			Code:    "invalid_path",
			Message: "path must stay within the selected root",
		}
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
		detail = errorDetail{Code: "path_not_found", Message: "path not found"}
	}
	writeJSON(w, status, errorResponse{Error: detail})
}
