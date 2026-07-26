package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const defaultGoogleBooksBaseURL = "https://www.googleapis.com/books/v1"

type GoogleBooks struct {
	endpoint *url.URL
	apiKey   string
	client   *http.Client
}

func NewGoogleBooks(
	baseURL string,
	apiKey string,
	client *http.Client,
) (*GoogleBooks, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("Google Books API key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGoogleBooksBaseURL
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("invalid Google Books base URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("invalid Google Books base URL scheme")
	}
	if endpoint.User != nil {
		return nil, errors.New("Google Books base URL must not contain credentials")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/volumes"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	safeClient := cloneHTTPClient(client)
	safeClient.CheckRedirect = func(
		_ *http.Request,
		_ []*http.Request,
	) error {
		// The API key is a query parameter. Never allow net/http to replay that
		// credential to a redirect target, including a different port on the
		// same hostname.
		return errors.New("Google Books redirects are disabled")
	}
	return &GoogleBooks{
		endpoint: endpoint,
		apiKey:   apiKey,
		client:   safeClient,
	}, nil
}

func (provider *GoogleBooks) Search(
	ctx context.Context,
	query Query,
) ([]Candidate, error) {
	if provider == nil || provider.endpoint == nil ||
		provider.client == nil || provider.apiKey == "" {
		return nil, errors.New("Google Books provider is not configured")
	}

	searchQuery := googleBooksQuery(query)
	if searchQuery == "" {
		return []Candidate{}, nil
	}
	requestURL := *provider.endpoint
	parameters := requestURL.Query()
	parameters.Set("q", searchQuery)
	parameters.Set("printType", "books")
	parameters.Set("maxResults", "10")
	parameters.Set("key", provider.apiKey)
	requestURL.RawQuery = parameters.Encode()

	var response googleBooksResponse
	if err := getJSON(
		ctx,
		provider.client,
		ProviderGoogleBooks,
		requestURL.String(),
		&response,
	); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(response.Items))
	for _, item := range response.Items {
		candidate := mapGoogleBooksVolume(item)
		if candidate.ProviderID == "" || candidate.Title == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func googleBooksQuery(query Query) string {
	if isbn := normalizeISBN(query.ISBN); isbn != "" {
		return "isbn:" + isbn
	}
	parts := make([]string, 0, 2)
	if title := strings.TrimSpace(query.Title); title != "" {
		parts = append(parts, "intitle:"+title)
	}
	if author := strings.TrimSpace(query.Author); author != "" {
		parts = append(parts, "inauthor:"+author)
	}
	return strings.Join(parts, " ")
}

type googleBooksResponse struct {
	Items []googleBooksVolume `json:"items"`
}

type googleBooksVolume struct {
	ID         string                `json:"id"`
	VolumeInfo googleBooksVolumeInfo `json:"volumeInfo"`
}

type googleBooksVolumeInfo struct {
	Title               string                  `json:"title"`
	Subtitle            string                  `json:"subtitle"`
	Authors             []string                `json:"authors"`
	Publisher           string                  `json:"publisher"`
	PublishedDate       string                  `json:"publishedDate"`
	Categories          []string                `json:"categories"`
	IndustryIdentifiers []googleBooksIdentifier `json:"industryIdentifiers"`
	ImageLinks          googleBooksImageLinks   `json:"imageLinks"`
}

type googleBooksIdentifier struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

type googleBooksImageLinks struct {
	SmallThumbnail string `json:"smallThumbnail"`
	Thumbnail      string `json:"thumbnail"`
	Small          string `json:"small"`
	Medium         string `json:"medium"`
	Large          string `json:"large"`
	ExtraLarge     string `json:"extraLarge"`
}

func mapGoogleBooksVolume(volume googleBooksVolume) Candidate {
	info := volume.VolumeInfo
	return Candidate{
		Provider:      ProviderGoogleBooks,
		ProviderID:    strings.TrimSpace(volume.ID),
		Title:         strings.TrimSpace(info.Title),
		Subtitle:      strings.TrimSpace(info.Subtitle),
		Publisher:     strings.TrimSpace(info.Publisher),
		PublishedDate: strings.TrimSpace(info.PublishedDate),
		Authors:       cleanStrings(info.Authors),
		Subjects:      cleanStrings(info.Categories),
		ISBN:          preferredGoogleBooksISBN(info.IndustryIdentifiers),
		CoverURL:      googleBooksCoverURL(info.ImageLinks),
	}
}

func preferredGoogleBooksISBN(identifiers []googleBooksIdentifier) string {
	var fallback string
	for _, identifier := range identifiers {
		value := strings.TrimSpace(identifier.Identifier)
		if value == "" {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(identifier.Type)) {
		case "ISBN_13":
			return value
		case "ISBN_10":
			if fallback == "" {
				fallback = value
			}
		}
	}
	return fallback
}

func googleBooksCoverURL(links googleBooksImageLinks) string {
	for _, value := range []string{
		links.ExtraLarge,
		links.Large,
		links.Medium,
		links.Small,
		links.Thumbnail,
		links.SmallThumbnail,
	} {
		value = strings.TrimSpace(value)
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			continue
		}
		switch parsed.Scheme {
		case "https":
			return parsed.String()
		case "http":
			parsed.Scheme = "https"
			return parsed.String()
		}
	}
	return ""
}
