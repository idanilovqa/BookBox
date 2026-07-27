package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultOpenLibraryBaseURL = "https://openlibrary.org"

var openLibraryFields = []string{
	"key",
	"title",
	"subtitle",
	"author_name",
	"publisher",
	"first_publish_year",
	"publish_date",
	"isbn",
	"subject",
	"cover_i",
}

type OpenLibrary struct {
	endpoint *url.URL
	client   *http.Client
}

func NewOpenLibrary(baseURL string, client *http.Client) (*OpenLibrary, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenLibraryBaseURL
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("invalid Open Library base URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("invalid Open Library base URL scheme")
	}
	if endpoint.User != nil {
		return nil, errors.New("Open Library base URL must not contain credentials")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/search.json"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return &OpenLibrary{
		endpoint: endpoint,
		client:   cloneHTTPClient(client),
	}, nil
}

func (provider *OpenLibrary) Search(
	ctx context.Context,
	query Query,
) ([]Candidate, error) {
	if provider == nil || provider.endpoint == nil || provider.client == nil {
		return nil, errors.New("Open Library provider is not configured")
	}
	requestURL := *provider.endpoint
	parameters := requestURL.Query()
	parameters.Set("title", strings.TrimSpace(query.Title))
	parameters.Set("author", strings.TrimSpace(query.Author))
	if isbn := strings.TrimSpace(query.ISBN); isbn != "" {
		parameters.Set("isbn", isbn)
	}
	parameters.Set("limit", "10")
	parameters.Set("fields", strings.Join(openLibraryFields, ","))
	requestURL.RawQuery = parameters.Encode()

	var response openLibraryResponse
	if err := getJSON(
		ctx,
		provider.client,
		ProviderOpenLibrary,
		requestURL.String(),
		&response,
	); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(response.Docs))
	for _, document := range response.Docs {
		candidate := mapOpenLibraryDocument(document)
		if candidate.ProviderID == "" || candidate.Title == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

type openLibraryResponse struct {
	Docs []openLibraryDocument `json:"docs"`
}

type openLibraryDocument struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	AuthorNames      []string `json:"author_name"`
	Publishers       []string `json:"publisher"`
	FirstPublishYear int      `json:"first_publish_year"`
	PublishDates     []string `json:"publish_date"`
	ISBNs            []string `json:"isbn"`
	Subjects         []string `json:"subject"`
	CoverID          int64    `json:"cover_i"`
}

func mapOpenLibraryDocument(document openLibraryDocument) Candidate {
	publishedDate := firstNonEmpty(document.PublishDates)
	if publishedDate == "" && document.FirstPublishYear > 0 {
		publishedDate = strconv.Itoa(document.FirstPublishYear)
	}
	coverURL := ""
	if document.CoverID > 0 {
		coverURL = fmt.Sprintf(
			"https://covers.openlibrary.org/b/id/%d-L.jpg",
			document.CoverID,
		)
	}
	return Candidate{
		Provider:      ProviderOpenLibrary,
		ProviderID:    strings.TrimSpace(document.Key),
		Title:         strings.TrimSpace(document.Title),
		Subtitle:      strings.TrimSpace(document.Subtitle),
		Publisher:     firstNonEmpty(document.Publishers),
		PublishedDate: publishedDate,
		Authors:       cleanStrings(document.AuthorNames),
		Subjects:      cleanStrings(document.Subjects),
		ISBN:          preferredISBN(document.ISBNs),
		CoverURL:      coverURL,
	}
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func preferredISBN(values []string) string {
	var fallback string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		normalized := strings.NewReplacer("-", "", " ", "").Replace(value)
		if len(normalized) == 13 {
			return value
		}
		if fallback == "" {
			fallback = value
		}
	}
	return fallback
}
