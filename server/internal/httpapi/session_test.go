package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bookbox.local/server/internal/auth"
	"bookbox.local/server/internal/config"
)

func TestSessionLoginRefreshAndProtectedAccess(t *testing.T) {
	api, manager := newSessionAPI(t, nil)
	login := postJSON(api, "/v1/session", `{"username":"owner","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var pair auth.Pair
	if err := json.Unmarshal(login.Body.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.AccessExpiry.IsZero() {
		t.Fatalf("pair = %#v", pair)
	}

	protected := httptest.NewRequest(http.MethodGet, "/v1/roots", nil)
	protected.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, protected)
	if response.Code != http.StatusOK {
		t.Fatalf("protected status = %d, body = %s", response.Code, response.Body.String())
	}

	refresh := postJSON(
		api,
		"/v1/session/refresh",
		fmt.Sprintf(`{"refreshToken":%q}`, pair.RefreshToken),
	)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	var refreshed auth.Pair
	if err := json.Unmarshal(refresh.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ValidateAccess(refreshed.AccessToken); err != nil {
		t.Fatalf("refreshed access token: %v", err)
	}
}

func TestLoginUsesUniformUnauthorizedResponseAndOneConfiguredAccountBucket(t *testing.T) {
	api, _ := newSessionAPI(t, nil)
	wantBody := ""
	for attempt := 0; attempt < 5; attempt++ {
		body := fmt.Sprintf(`{"username":"unknown-%d","password":"wrong-%d"}`, attempt, attempt)
		response := postJSON(api, "/v1/session", body)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, body = %s", attempt+1, response.Code, response.Body.String())
		}
		if attempt == 0 {
			wantBody = response.Body.String()
		} else if response.Body.String() != wantBody {
			t.Fatalf("unauthorized bodies differ: %q != %q", response.Body.String(), wantBody)
		}
	}

	blocked := postJSON(api, "/v1/session", `{"username":"owner","password":"secret"}`)
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked status = %d Retry-After = %q body = %s",
			blocked.Code, blocked.Header().Get("Retry-After"), blocked.Body.String())
	}
}

func TestSessionRejectsUnsafeJSON(t *testing.T) {
	api, _ := newSessionAPI(t, nil)
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
	}{
		{"unknown field", "application/json", `{"username":"owner","password":"secret","extra":true}`, http.StatusBadRequest},
		{"multiple values", "application/json", `{"username":"owner","password":"secret"} {}`, http.StatusBadRequest},
		{"oversized", "application/json", `{"username":"owner","password":"` + strings.Repeat("x", 5000) + `"}`, http.StatusBadRequest},
		{"wrong content type", "text/plain", `{"username":"owner","password":"secret"}`, http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/session", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestAuthenticationMiddlewareUsesExactRoutesAndBearerAccessTokens(t *testing.T) {
	api, manager := newSessionAPI(t, nil)
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	api.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	for name, authorization := range map[string][]string{
		"missing":       nil,
		"malformed":     {"Basic abc"},
		"refresh token": {"Bearer " + pair.RefreshToken},
		"multiple":      {"Bearer " + pair.AccessToken, "Bearer " + pair.AccessToken},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/roots", nil)
			for _, value := range authorization {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized ||
				response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("status = %d WWW-Authenticate = %q body = %s",
					response.Code, response.Header().Get("WWW-Authenticate"), response.Body.String())
			}
		})
	}

	wrongMethod := httptest.NewRecorder()
	api.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/v1/health", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health status = %d, want 405", wrongMethod.Code)
	}
	lookalike := httptest.NewRecorder()
	api.ServeHTTP(lookalike, httptest.NewRequest(http.MethodPost, "/v1/session/extra", nil))
	if lookalike.Code != http.StatusNotFound {
		t.Fatalf("session lookalike status = %d, want 404", lookalike.Code)
	}
}

func TestStructuredRequestLogsExcludeCredentialsHeadersAndQueries(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	api, _ := newSessionAPI(t, logger)

	_ = postJSON(api, "/v1/session", `{"username":"owner","password":"password-sentinel"}`)
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/browse?root=audiobooks&path=private-path-sentinel",
		nil,
	)
	request.Header.Set("Authorization", "Bearer authorization-sentinel")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)

	logged := output.String()
	for _, secret := range []string{
		"password-sentinel",
		"authorization-sentinel",
		"private-path-sentinel",
		"Authorization",
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("logs contain %q: %s", secret, logged)
		}
	}
	for _, field := range []string{`"method"`, `"route"`, `"status"`} {
		if !strings.Contains(logged, field) {
			t.Fatalf("logs missing allowlisted field %s: %s", field, logged)
		}
	}
}

func TestStructuredRequestLoggerPreservesResponseControllerFeatures(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	var flushError error
	handler := logRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flushError = http.NewResponseController(w).Flush()
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if flushError != nil {
		t.Fatalf("Flush() through logging middleware = %v", flushError)
	}
	if !response.Flushed {
		t.Fatal("logging middleware did not preserve flushing")
	}
}

func TestStatusResponseWriterForwardsOnlyFirstHeader(t *testing.T) {
	underlying := &countingHeaderWriter{header: make(http.Header)}
	writer := &statusResponseWriter{ResponseWriter: underlying}
	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusInternalServerError)
	if len(underlying.statuses) != 1 || underlying.statuses[0] != http.StatusCreated {
		t.Fatalf("forwarded statuses = %v, want [201]", underlying.statuses)
	}
	if writer.status != http.StatusCreated {
		t.Fatalf("recorded status = %d, want 201", writer.status)
	}
}

func TestLoginAtomicallyLimitsSameKeyBeforeArgonVerification(t *testing.T) {
	entered := make(chan struct{}, 6)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	api := newSessionAPIWithVerifier(t, auth.NewPasswordVerifier(10, func(_, _ string) (bool, error) {
		entered <- struct{}{}
		<-release
		return false, nil
	}))

	responses := make(chan int, 6)
	var ready sync.WaitGroup
	ready.Add(6)
	start := make(chan struct{})
	for index := 0; index < 6; index++ {
		go func() {
			ready.Done()
			<-start
			responses <- postLoginFrom(api, "192.0.2.1:1000").Code
		}()
	}
	ready.Wait()
	close(start)
	for index := 0; index < 5; index++ {
		<-entered
	}
	select {
	case <-entered:
		t.Fatal("more than five same-key requests entered password verification")
	case status := <-responses:
		if status != http.StatusTooManyRequests {
			t.Fatalf("sixth concurrent login status = %d, want 429", status)
		}
	case <-time.After(time.Second):
		t.Fatal("sixth same-key request did not fail closed")
	}
	close(release)
	for index := 0; index < 5; index++ {
		if status := <-responses; status != http.StatusUnauthorized {
			t.Fatalf("completed failed login status = %d, want 401", status)
		}
	}
}

func TestLoginCapsArgonVerificationAcrossDistinctIPs(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	api := newSessionAPIWithVerifier(t, auth.NewPasswordVerifier(2, func(_, _ string) (bool, error) {
		entered <- struct{}{}
		<-release
		return false, nil
	}))

	responses := make(chan int, 3)
	var ready sync.WaitGroup
	ready.Add(3)
	start := make(chan struct{})
	for index := 1; index <= 3; index++ {
		remote := fmt.Sprintf("192.0.2.%d:1000", index)
		go func() {
			ready.Done()
			<-start
			responses <- postLoginFrom(api, remote).Code
		}()
	}
	ready.Wait()
	close(start)
	<-entered
	<-entered
	select {
	case <-entered:
		t.Fatal("third distinct-IP request bypassed the verifier cap")
	case <-time.After(150 * time.Millisecond):
	}
	release <- struct{}{}
	<-entered
	close(release)
	for index := 0; index < 3; index++ {
		if status := <-responses; status != http.StatusUnauthorized {
			t.Fatalf("failed login status = %d, want 401", status)
		}
	}
}

type countingHeaderWriter struct {
	header   http.Header
	statuses []int
}

func (writer *countingHeaderWriter) Header() http.Header {
	return writer.header
}

func (writer *countingHeaderWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (writer *countingHeaderWriter) WriteHeader(status int) {
	writer.statuses = append(writer.statuses, status)
}

func newSessionAPI(t *testing.T, logger *slog.Logger) (http.Handler, *auth.TokenManager) {
	t.Helper()
	hash, err := auth.HashPassword("secret", auth.ArgonParams{
		MemoryKiB:   64,
		Iterations:  1,
		Parallelism: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewTokenManager([]byte(strings.Repeat("k", 32)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return New(Dependencies{
		Version:      "test",
		Roots:        []config.Root{},
		Username:     "owner",
		PasswordHash: hash,
		Tokens:       manager,
		Limiter:      auth.NewLoginLimiter(10000, time.Now),
		Logger:       logger,
	}), manager
}

func postJSON(handler http.Handler, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newSessionAPIWithVerifier(t *testing.T, verifier *auth.PasswordVerifier) http.Handler {
	t.Helper()
	hash, err := auth.HashPassword("secret", auth.ArgonParams{
		MemoryKiB: 64, Iterations: 1, Parallelism: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewTokenManager([]byte(strings.Repeat("v", 32)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return New(Dependencies{
		Username:         "owner",
		PasswordHash:     hash,
		PasswordVerifier: verifier,
		Tokens:           manager,
		Limiter:          auth.NewLoginLimiter(10000, time.Now),
	})
}

func postLoginFrom(handler http.Handler, remoteAddress string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/session",
		strings.NewReader(`{"username":"owner","password":"wrong"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddress
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
