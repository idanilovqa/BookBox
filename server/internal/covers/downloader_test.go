package covers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloaderFetchesBoundedHTTPSImage(t *testing.T) {
	t.Parallel()

	imageBytes := testJPEG(t, 32, 48)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(imageBytes)
	}))
	defer server.Close()

	download := NewDownloader(server.Client())
	info, data, err := download.Fetch(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if info.Format != "jpeg" || !bytes.Equal(data, imageBytes) {
		t.Fatalf("Fetch() = %#v, %d bytes", info, len(data))
	}
}

func TestDownloaderRejectsUnsafeURLsRedirectsAndLargeResponses(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer httpServer.Close()

	if _, _, err := NewDownloader(httpServer.Client()).Fetch(t.Context(), httpServer.URL); err == nil {
		t.Fatal("plain HTTP URL accepted")
	}

	redirect := httptest.NewTLSServer(http.RedirectHandler(httpServer.URL, http.StatusFound))
	defer redirect.Close()
	if _, _, err := NewDownloader(redirect.Client()).Fetch(t.Context(), redirect.URL); err == nil {
		t.Fatal("HTTPS to HTTP redirect accepted")
	}

	large := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Length", "20971521")
		_, _ = response.Write(bytes.Repeat([]byte{0x41}, MaxInputBytes+1))
	}))
	defer large.Close()
	if _, _, err := NewDownloader(large.Client()).Fetch(t.Context(), large.URL); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestDownloaderDoesNotLeakURLSecretsInErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "no", http.StatusForbidden)
	}))
	defer server.Close()

	secret := "do-not-log-this"
	_, _, err := NewDownloader(server.Client()).Fetch(t.Context(), server.URL+"/cover?key="+secret)
	if err == nil {
		t.Fatal("Fetch() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Fetch() leaked URL secret: %v", err)
	}
}

func TestDownloaderRejectsMoreThanThreeRedirects(t *testing.T) {
	t.Parallel()

	imageBytes := testJPEG(t, 32, 48)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		step := len(strings.Trim(request.URL.Path, "/"))
		http.Redirect(response, request, server.URL+"/"+strings.Repeat("x", step+1), http.StatusFound)
	}))
	defer server.Close()

	if _, _, err := NewDownloader(server.Client()).Fetch(t.Context(), server.URL+"/"); err == nil {
		t.Fatal("more than three redirects accepted")
	}

	var bounded *httptest.Server
	bounded = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		step := len(strings.Trim(request.URL.Path, "/"))
		if step == 3 {
			_, _ = response.Write(imageBytes)
			return
		}
		http.Redirect(response, request, bounded.URL+"/"+strings.Repeat("x", step+1), http.StatusFound)
	}))
	defer bounded.Close()
	if _, _, err := NewDownloader(bounded.Client()).Fetch(t.Context(), bounded.URL+"/"); err != nil {
		t.Fatalf("exactly three redirects rejected: %v", err)
	}
}
