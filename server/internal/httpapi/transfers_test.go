package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"netstorage.local/server/internal/auth"
	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/library"
	"netstorage.local/server/internal/transfer"
)

func TestTransferManifestEndpointIsStrictAuthenticatedAndPrivate(t *testing.T) {
	api, manager, _, root := newTransferAPI(t, []byte("0123456789"))
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	response := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodPost,
		"/v1/transfers/manifest",
		`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}]}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var manifest transfer.Manifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].DestinationPath != "Book/chapter.mp3" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if strings.Contains(response.Body.String(), filepath.ToSlash(root)) ||
		strings.Contains(response.Body.String(), root) {
		t.Fatalf("response leaked absolute root: %s", response.Body.String())
	}

	for name, test := range map[string]struct {
		contentType string
		body        string
		status      int
	}{
		"unknown field": {
			"application/json",
			`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}],"extra":true}`,
			http.StatusBadRequest,
		},
		"trailing value": {
			"application/json",
			`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}]} {}`,
			http.StatusBadRequest,
		},
		"empty selection": {"application/json", `{"selections":[]}`, http.StatusBadRequest},
		"non-folder": {
			"application/json",
			`{"selections":[{"rootId":"audiobooks","relativePath":"Book/chapter.mp3","kind":"file"}]}`,
			http.StatusBadRequest,
		},
		"path escape": {
			"application/json",
			`{"selections":[{"rootId":"audiobooks","relativePath":"../secret","kind":"folder"}]}`,
			http.StatusBadRequest,
		},
		"wrong content type": {
			"text/plain",
			`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}]}`,
			http.StatusUnsupportedMediaType,
		},
		"oversized": {
			"application/json",
			`{"selections":[{"rootId":"audiobooks","relativePath":"` +
				strings.Repeat("x", (1<<20)+1) + `","kind":"folder"}]}`,
			http.StatusRequestEntityTooLarge,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := serveAuthorized(
				t,
				api,
				pair.AccessToken,
				http.MethodPost,
				"/v1/transfers/manifest",
				test.body,
				map[string]string{"Content-Type": test.contentType},
			)
			if got.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", got.Code, test.status, got.Body.String())
			}
			if strings.Contains(got.Body.String(), root) {
				t.Fatalf("error leaked absolute root: %s", got.Body.String())
			}
		})
	}
	tooManySelections := transfer.Request{
		Selections: make([]transfer.Selection, 101),
	}
	for index := range tooManySelections.Selections {
		tooManySelections.Selections[index] = transfer.Selection{
			RootID:       "audiobooks",
			RelativePath: "Book",
			Kind:         "folder",
		}
	}
	tooManyBody, err := json.Marshal(tooManySelections)
	if err != nil {
		t.Fatal(err)
	}
	tooMany := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodPost,
		"/v1/transfers/manifest",
		string(tooManyBody),
		map[string]string{"Content-Type": "application/json"},
	)
	if tooMany.Code != http.StatusBadRequest {
		t.Fatalf("101 selections status = %d body = %s", tooMany.Code, tooMany.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	api.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodPost, "/v1/transfers/manifest", strings.NewReader(`{"selections":[]}`)),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
}

func TestManifestBuildGateAllowsOnlyOneBuildPerSubject(t *testing.T) {
	manager, err := auth.NewTokenManager(bytes.Repeat([]byte{3}, 32), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	blocker := &blockingManifestBuilder{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	api := New(Dependencies{Tokens: manager, TransferBuilder: blocker})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- serveAuthorized(
			t,
			api,
			pair.AccessToken,
			http.MethodPost,
			"/v1/transfers/manifest",
			`{"selections":[{"rootId":"audiobooks","kind":"folder"}]}`,
			map[string]string{"Content-Type": "application/json"},
		)
	}()
	select {
	case <-blocker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first manifest build did not enter")
	}
	second := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodPost,
		"/v1/transfers/manifest",
		`{"selections":[{"rootId":"audiobooks","kind":"folder"}]}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second status = %d Retry-After = %q body = %s",
			second.Code, second.Header().Get("Retry-After"), second.Body.String())
	}
	close(blocker.release)
	select {
	case first := <-firstDone:
		if first.Code != http.StatusOK {
			t.Fatalf("first status = %d body = %s", first.Code, first.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first manifest build did not finish")
	}
	third := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodPost,
		"/v1/transfers/manifest",
		`{"selections":[{"rootId":"audiobooks","kind":"folder"}]}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if third.Code != http.StatusOK {
		t.Fatalf("third status = %d body = %s", third.Code, third.Body.String())
	}
}

func TestFileEndpointSupportsRangesConditionalsAndChangedSource(t *testing.T) {
	content := []byte("0123456789")
	api, manager, signer, root := newTransferAPI(t, content)
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Book", "chapter.mp3")
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	shaHex := hex.EncodeToString(sum[:])
	fileID, err := signer.Sign(transfer.FileIdentity{
		RootID:      "audiobooks",
		Path:        "Book/chapter.mp3",
		Size:        int64(len(content)),
		ModUnixNano: info.ModTime().UnixNano(),
		SHA256:      shaHex,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/files/" + url.PathEscape(fileID)
	wantETag := `"` + shaHex + `"`
	wantDigest := "sha-256=" + base64.StdEncoding.EncodeToString(sum[:])

	partial := serveAuthorized(t, api, pair.AccessToken, http.MethodGet, target, "", map[string]string{
		"Range": "bytes=3-5",
	})
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "345" {
		t.Fatalf("range response = %d %q", partial.Code, partial.Body.String())
	}
	if partial.Header().Get("ETag") != wantETag ||
		partial.Header().Get("Accept-Ranges") != "bytes" ||
		partial.Header().Get("Digest") != wantDigest {
		t.Fatalf("range headers = %#v", partial.Header())
	}

	invalidRange := serveAuthorized(t, api, pair.AccessToken, http.MethodGet, target, "", map[string]string{
		"Range": "bytes=99-100",
	})
	if invalidRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status = %d body = %s", invalidRange.Code, invalidRange.Body.String())
	}
	ifRangeMatch := serveAuthorized(t, api, pair.AccessToken, http.MethodGet, target, "", map[string]string{
		"Range":    "bytes=3-5",
		"If-Range": wantETag,
	})
	if ifRangeMatch.Code != http.StatusPartialContent || ifRangeMatch.Body.String() != "345" {
		t.Fatalf("matching If-Range = %d %q", ifRangeMatch.Code, ifRangeMatch.Body.String())
	}
	ifRangeMiss := serveAuthorized(t, api, pair.AccessToken, http.MethodGet, target, "", map[string]string{
		"Range":    "bytes=3-5",
		"If-Range": `"different"`,
	})
	if ifRangeMiss.Code != http.StatusOK || !bytes.Equal(ifRangeMiss.Body.Bytes(), content) {
		t.Fatalf("mismatching If-Range = %d %q", ifRangeMiss.Code, ifRangeMiss.Body.String())
	}

	if err := os.WriteFile(source, []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	sameMetadataChange := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodGet,
		target,
		"",
		nil,
	)
	if sameMetadataChange.Code != http.StatusConflict {
		t.Fatalf(
			"same-size restored-mtime change status = %d body = %s",
			sameMetadataChange.Code,
			sameMetadataChange.Body.String(),
		)
	}
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	replacement := byte('A')
	if fileID[len(fileID)-1] == replacement {
		replacement = 'B'
	}
	tampered := fileID[:len(fileID)-1] + string(replacement)
	badID := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodGet,
		"/v1/files/"+url.PathEscape(tampered),
		"",
		nil,
	)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("tampered ID status = %d body = %s", badID.Code, badID.Body.String())
	}

	if err := os.WriteFile(source, []byte("changed source"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := serveAuthorized(t, api, pair.AccessToken, http.MethodGet, target, "", nil)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed source status = %d body = %s", changed.Code, changed.Body.String())
	}
}

func TestTransferEndpointsDoNotMutateSourceTreeOrExposeMutationRoutes(t *testing.T) {
	api, manager, _, root := newTransferAPI(t, []byte("0123456789"))
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotSourceTree(t, root)
	manifestResponse := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodPost,
		"/v1/transfers/manifest",
		`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}]}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest status = %d body = %s", manifestResponse.Code, manifestResponse.Body.String())
	}
	var manifest transfer.Manifest
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	download := serveAuthorized(
		t,
		api,
		pair.AccessToken,
		http.MethodGet,
		"/v1/files/"+url.PathEscape(manifest.Files[0].FileID),
		"",
		nil,
	)
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d body = %s", download.Code, download.Body.String())
	}
	after := snapshotSourceTree(t, root)
	if !sourceSnapshotsEqual(before, after) {
		t.Fatalf("source tree changed:\nbefore=%#v\nafter=%#v", before, after)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		response := serveAuthorized(
			t,
			api,
			pair.AccessToken,
			method,
			"/v1/files/"+url.PathEscape(manifest.Files[0].FileID),
			"",
			nil,
		)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s file status = %d, want 405", method, response.Code)
		}
	}
}

type blockingManifestBuilder struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (builder *blockingManifestBuilder) Build(
	ctx context.Context,
	_ transfer.Request,
) (transfer.Manifest, error) {
	builder.once.Do(func() { close(builder.entered) })
	select {
	case <-builder.release:
		return transfer.Manifest{ID: "manifest", Files: []transfer.ManifestFile{}}, nil
	case <-ctx.Done():
		return transfer.Manifest{}, ctx.Err()
	}
}

func newTransferAPI(
	t *testing.T,
	content []byte,
) (http.Handler, *auth.TokenManager, *transfer.FileSigner, string) {
	t.Helper()
	root := t.TempDir()
	book := filepath.Join(root, "Book")
	if err := os.MkdirAll(book, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "chapter.mp3"), content, 0o640); err != nil {
		t.Fatal(err)
	}
	resolver, err := library.NewResolver([]config.Root{{
		ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := transfer.NewFileSigner(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewTokenManager(bytes.Repeat([]byte{6}, 32), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return New(Dependencies{
		Tokens:          manager,
		Resolver:        resolver,
		TransferBuilder: transfer.NewBuilder(resolver, signer),
		FileSigner:      signer,
	}), manager, signer, root
}

func serveAuthorized(
	t *testing.T,
	api http.Handler,
	accessToken string,
	method string,
	target string,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+accessToken)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

type sourceSnapshot struct {
	Relative string
	Mode     os.FileMode
	Modified int64
	Content  string
}

func snapshotSourceTree(t *testing.T, root string) []sourceSnapshot {
	t.Helper()
	var snapshot []sourceSnapshot
	err := filepath.Walk(root, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		content := ""
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			content = hex.EncodeToString(raw)
		}
		snapshot = append(snapshot, sourceSnapshot{
			Relative: filepath.ToSlash(relative),
			Mode:     info.Mode(),
			Modified: info.ModTime().UnixNano(),
			Content:  content,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func sourceSnapshotsEqual(left, right []sourceSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
