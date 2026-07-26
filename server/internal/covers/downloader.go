package covers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var ErrUnsafeURL = errors.New("cover URL must be credential-free HTTPS")

type Downloader struct {
	client *http.Client
}

func NewDownloader(client *http.Client) *Downloader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *client
	if copy.Timeout == 0 {
		copy.Timeout = 10 * time.Second
	}
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("cover download exceeded redirect limit")
		}
		if !safeHTTPSURL(request.URL) {
			return ErrUnsafeURL
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		return nil
	}
	return &Downloader{client: &copy}
}

func (downloader *Downloader) Fetch(ctx context.Context, rawURL string) (ImageInfo, []byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !safeHTTPSURL(parsed) {
		return ImageInfo{}, nil, ErrUnsafeURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ImageInfo{}, nil, errors.New("create cover download request")
	}
	response, err := downloader.client.Do(request)
	if err != nil {
		return ImageInfo{}, nil, errors.New("cover download failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ImageInfo{}, nil, fmt.Errorf("cover download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > MaxInputBytes {
		return ImageInfo{}, nil, ErrImageTooLarge
	}
	return Inspect(response.Body, response.ContentLength)
}

func safeHTTPSURL(value *url.URL) bool {
	return value != nil && value.Scheme == "https" && value.Host != "" && value.User == nil
}
