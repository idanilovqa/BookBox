package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleBooksSearchEncodesRequestAndMapsCandidate(t *testing.T) {
	t.Parallel()

	const apiKey = "very-secret-google-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/volumes" {
			t.Errorf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("q") != "intitle:Dune & Messiah inauthor:Frank Herbert" {
			t.Errorf("q = %q", query.Get("q"))
		}
		if query.Get("printType") != "books" {
			t.Errorf("printType = %q", query.Get("printType"))
		}
		if query.Get("maxResults") != "10" {
			t.Errorf("maxResults = %q", query.Get("maxResults"))
		}
		if query.Get("key") != apiKey {
			t.Errorf("key = %q", query.Get("key"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"items":[{
				"id":"google-volume-1",
				"volumeInfo":{
					"title":"Dune",
					"subtitle":"The Desert Planet",
					"authors":["Frank Herbert"],
					"publisher":"Ace",
					"publishedDate":"1965-08-01",
					"categories":["Fiction / Science Fiction / General"],
					"industryIdentifiers":[
						{"type":"ISBN_10","identifier":"0441172717"},
						{"type":"ISBN_13","identifier":"9780441172719"}
					],
					"imageLinks":{
						"thumbnail":"http://books.google.test/thumbnail.jpg",
						"large":"http://books.google.test/large.jpg"
					}
				}
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewGoogleBooks(server.URL, apiKey, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), Query{
		Title:  "Dune & Messiah",
		Author: "Frank Herbert",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	got := candidates[0]
	if got.Provider != ProviderGoogleBooks || got.ProviderID != "google-volume-1" {
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
	if len(got.Subjects) != 1 ||
		got.Subjects[0] != "Fiction / Science Fiction / General" {
		t.Fatalf("subjects = %#v", got.Subjects)
	}
	if got.CoverURL != "https://books.google.test/large.jpg" {
		t.Fatalf("cover URL = %q", got.CoverURL)
	}
}

func TestGoogleBooksSearchUsesISBNQueryWhenPresent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("q"); got != "isbn:9780441172719" {
			t.Errorf("q = %q", got)
		}
		_, _ = writer.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewGoogleBooks(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), Query{
		Title:  "ignored for precise ISBN search",
		Author: "ignored too",
		ISBN:   "978-0-441-17271-9",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGoogleBooksErrorsDoNotExposeAPIKeyOrRequestURL(t *testing.T) {
	t.Parallel()

	const apiKey = "top-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	provider, err := NewGoogleBooks(server.URL, apiKey, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), Query{Title: "Dune"})
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Status != http.StatusForbidden {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("credential-bearing error = %q", err)
	}
}

func TestGoogleBooksRejectsRedirectsWithoutLeakingAPIKey(t *testing.T) {
	t.Parallel()

	const apiKey = "redirect-secret-key"
	differentOrigin := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		t.Error("redirect target received API-key-bearing request")
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(differentOrigin.Close)

	var sameOrigin *httptest.Server
	sameOrigin = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/volumes":
			http.Redirect(writer, request, sameOrigin.URL+"/moved", http.StatusFound)
		case "/moved":
			t.Error("same-origin redirect was followed")
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path = %q", request.URL.Path)
		}
	}))
	t.Cleanup(sameOrigin.Close)

	for name, baseURL := range map[string]string{
		"same origin": sameOrigin.URL,
		"different port": newRedirectServer(
			t,
			differentOrigin.URL+"/capture",
		).URL,
		"cross origin": newRedirectServer(
			t,
			"http://example.invalid/capture",
		).URL,
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := NewGoogleBooks(baseURL, apiKey, http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Search(context.Background(), Query{Title: "Dune"})
			var providerError *ProviderError
			if !errors.As(err, &providerError) || providerError.Status != 0 {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), apiKey) ||
				strings.Contains(err.Error(), baseURL) {
				t.Fatalf("credential-bearing error = %q", err)
			}
		})
	}
}

func TestNewGoogleBooksRejectsMissingKeyAndCredentialedBaseURL(t *testing.T) {
	t.Parallel()

	if _, err := NewGoogleBooks("https://books.google.test", "", http.DefaultClient); err == nil {
		t.Fatal("expected missing key error")
	}
	_, err := NewGoogleBooks(
		"https://username:password@books.google.test",
		"key",
		http.DefaultClient,
	)
	if err == nil {
		t.Fatal("expected URL userinfo to be rejected")
	}
	if strings.Contains(err.Error(), "username") || strings.Contains(err.Error(), "password") {
		t.Fatalf("credential-bearing error = %q", err)
	}
}

func newRedirectServer(t *testing.T, destination string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, destination, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGoogleBooksSkipsIncompleteVolumes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"items":[
				{"volumeInfo":{"title":"No identifier"}},
				{"id":"no-title","volumeInfo":{}},
				{"id":"valid","volumeInfo":{"title":"Dune"}}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	provider, err := NewGoogleBooks(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), Query{Title: "Dune"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ProviderID != "valid" {
		t.Fatalf("candidates = %#v", candidates)
	}
}
