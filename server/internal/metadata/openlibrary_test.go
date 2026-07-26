package metadata

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenLibrarySearchEncodesRequestAndMapsCandidate(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search.json" {
			t.Errorf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("title") != "Dune & Messiah" {
			t.Errorf("title = %q", query.Get("title"))
		}
		if query.Get("author") != "Frank Herbert" {
			t.Errorf("author = %q", query.Get("author"))
		}
		if query.Get("isbn") != "9780441172719" {
			t.Errorf("isbn = %q", query.Get("isbn"))
		}
		if query.Get("limit") != "10" {
			t.Errorf("limit = %q", query.Get("limit"))
		}
		for _, field := range []string{
			"key", "title", "subtitle", "author_name", "publisher",
			"first_publish_year", "publish_date", "isbn", "subject", "cover_i",
		} {
			if !strings.Contains(","+query.Get("fields")+",", ","+field+",") {
				t.Errorf("fields %q does not contain %q", query.Get("fields"), field)
			}
		}
		if userAgent := request.Header.Get("User-Agent"); !strings.Contains(userAgent, "netStorage") {
			t.Errorf("User-Agent = %q", userAgent)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"docs":[{
				"key":"/works/OL893415W",
				"title":"Dune",
				"subtitle":"The Desert Planet",
				"author_name":["Frank Herbert"],
				"publisher":["Ace"],
				"first_publish_year":1965,
				"publish_date":["1965-08-01"],
				"isbn":["9780441172719"],
				"subject":["Science fiction","Dune (Imaginary place)"],
				"cover_i":8231856
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), Query{
		Title:  "Dune & Messiah",
		Author: "Frank Herbert",
		ISBN:   "9780441172719",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	got := candidates[0]
	if got.Provider != ProviderOpenLibrary || got.ProviderID != "/works/OL893415W" {
		t.Fatalf("identity = %#v", got)
	}
	if got.Title != "Dune" || got.Subtitle != "The Desert Planet" ||
		got.Publisher != "Ace" || got.PublishedDate != "1965-08-01" {
		t.Fatalf("mapped fields = %#v", got)
	}
	if len(got.Authors) != 1 || got.Authors[0] != "Frank Herbert" ||
		got.ISBN != "9780441172719" {
		t.Fatalf("authors/ISBN = %#v", got)
	}
	if len(got.Subjects) != 2 || got.Subjects[0] != "Science fiction" {
		t.Fatalf("subjects = %#v", got.Subjects)
	}
	if got.CoverURL != "https://covers.openlibrary.org/b/id/8231856-L.jpg" {
		t.Fatalf("cover URL = %q", got.CoverURL)
	}
}

func TestOpenLibrarySearchSkipsCandidatesWithoutStableIdentity(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"docs":[
				{"title":"No provider identifier"},
				{"key":"/works/OL-NO-TITLE"},
				{"key":"/works/OL-VALID","title":"Dune"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), Query{Title: "Dune"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 ||
		candidates[0].ProviderID != "/works/OL-VALID" ||
		candidates[0].Title != "Dune" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestOpenLibrarySearchRetriesAtMostTwiceForRetryableStatus(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := attempts.Add(1)
		if current < 3 {
			writer.Header().Set("Retry-After", "0")
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"docs":[]}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), Query{Title: "Dune"}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestOpenLibrarySearchDoesNotRetryOtherClientErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(writer, "bad request", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), Query{Title: "Dune"})
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Status != http.StatusBadRequest {
		t.Fatalf("error = %#v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error contains request URL: %q", err)
	}
}

func TestOpenLibrarySearchRetriesOnlyFiveHundredRangeStatuses(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(600)
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), Query{Title: "Dune"})
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Status != 600 {
		t.Fatalf("error = %#v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestOpenLibrarySearchRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", strconv.Itoa(maxResponseBytes+1))
		_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	t.Cleanup(server.Close)

	provider, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), Query{Title: "Dune"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewOpenLibraryAppliesDefaultTimeoutWithoutMutatingClient(t *testing.T) {
	t.Parallel()

	injected := &http.Client{}
	provider, err := NewOpenLibrary("https://openlibrary.example", injected)
	if err != nil {
		t.Fatal(err)
	}
	if provider.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %s", provider.client.Timeout)
	}
	if injected.Timeout != 0 {
		t.Fatalf("injected client mutated: %s", injected.Timeout)
	}
}

func TestRetryAfterDelayIsBounded(t *testing.T) {
	t.Parallel()

	if got := retryAfterDelay("999999", time.Now()); got != maxRetryDelay {
		t.Fatalf("numeric Retry-After delay = %s", got)
	}
	if got := retryAfterDelay(strconv.FormatInt(math.MaxInt64, 10), time.Now()); got != maxRetryDelay {
		t.Fatalf("overflowing numeric Retry-After delay = %s", got)
	}
	if got := retryAfterDelay(
		time.Now().Add(24*time.Hour).Format(http.TimeFormat),
		time.Now(),
	); got != maxRetryDelay {
		t.Fatalf("date Retry-After delay = %s", got)
	}
	if got := retryAfterDelay("invalid", time.Now()); got != defaultRetryDelay {
		t.Fatalf("invalid Retry-After delay = %s", got)
	}
}

func TestNewOpenLibraryRejectsBaseURLCredentials(t *testing.T) {
	t.Parallel()

	_, err := NewOpenLibrary(
		"https://username:password@openlibrary.example",
		http.DefaultClient,
	)
	if err == nil {
		t.Fatal("expected URL userinfo to be rejected")
	}
	if strings.Contains(err.Error(), "username") || strings.Contains(err.Error(), "password") {
		t.Fatalf("credential-bearing error = %q", err)
	}
}
