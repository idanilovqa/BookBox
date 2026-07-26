package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	rr := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	New(Dependencies{Version: "test"}).ServeHTTP(rr, request)

	if rr.Code != http.StatusOK || rr.Body.String() != "{\"status\":\"ok\",\"version\":\"test\"}\n" {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
}
