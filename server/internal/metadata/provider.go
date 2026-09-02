package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderOpenLibrary = "open_library"
	ProviderGoogleBooks = "google_books"

	defaultHTTPTimeout = 10 * time.Second
	maxResponseBytes   = 2 << 20
	maxRetries         = 2
	defaultRetryDelay  = 100 * time.Millisecond
	maxRetryDelay      = 2 * time.Second
	userAgent          = "BookBox/1.0 audiobook metadata catalog (contact: idanilovqa@gmail.com)"
)

var (
	ErrInvalidQuery     = errors.New("metadata provider query is empty")
	ErrResponseTooLarge = errors.New("metadata provider response exceeds 2 MiB")
)

type Query struct {
	Title  string
	Author string
	ISBN   string
}

type Candidate struct {
	Provider       string
	ProviderID     string
	Title          string
	Subtitle       string
	Publisher      string
	PublishedDate  string
	Authors        []string
	Narrators      []string
	Genres         []string
	Subjects       []string
	Series         string
	SeriesPosition string
	ISBN           string
	CoverURL       string
}

type Provider interface {
	Search(context.Context, Query) ([]Candidate, error)
}

type ProviderError struct {
	Provider string
	Status   int
}

func (err *ProviderError) Error() string {
	if err == nil {
		return "metadata provider request failed"
	}
	if err.Status == 0 {
		return fmt.Sprintf("%s request failed", err.Provider)
	}
	return fmt.Sprintf("%s returned HTTP status %d", err.Provider, err.Status)
}

func cloneHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: defaultHTTPTimeout}
	}
	cloned := *client
	if cloned.Timeout == 0 {
		cloned.Timeout = defaultHTTPTimeout
	}
	return &cloned
}

func getJSON(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestURL string,
	target any,
) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			requestURL,
			nil,
		)
		if err != nil {
			return fmt.Errorf("%s request could not be created", provider)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", userAgent)

		response, err := client.Do(request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// Do not wrap the transport error: it can contain the request URL,
			// including credentials stored in its query string.
			return &ProviderError{Provider: provider}
		}

		if response.StatusCode >= http.StatusOK &&
			response.StatusCode < http.StatusMultipleChoices {
			err = decodeBoundedJSON(response.Body, target)
			closeErr := response.Body.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return fmt.Errorf("%s response could not be closed", provider)
			}
			return nil
		}

		status := response.StatusCode
		retryAfter := response.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		_ = response.Body.Close()

		if !retryableStatus(status) || attempt == maxRetries {
			return &ProviderError{Provider: provider, Status: status}
		}
		if err := waitForRetry(ctx, retryAfter); err != nil {
			return err
		}
	}
	return &ProviderError{Provider: provider}
}

func decodeBoundedJSON(body io.Reader, target any) error {
	limited := io.LimitReader(body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return errors.New("metadata provider response could not be read")
	}
	if len(data) > maxResponseBytes {
		return ErrResponseTooLarge
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("metadata provider returned invalid JSON")
	}
	return nil
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError &&
			status <= 599
}

func waitForRetry(ctx context.Context, retryAfter string) error {
	delay := retryAfterDelay(retryAfter, time.Now())
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfterDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		maxSeconds := int64(maxRetryDelay / time.Second)
		if seconds > maxSeconds {
			return maxRetryDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		delay := retryAt.Sub(now)
		if delay <= 0 {
			return 0
		}
		if delay > maxRetryDelay {
			return maxRetryDelay
		}
		return delay
	}
	return defaultRetryDelay
}
