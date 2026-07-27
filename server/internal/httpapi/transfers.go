package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"sync"

	"netstorage.local/server/internal/library"
	"netstorage.local/server/internal/transfer"
)

const maxManifestBodyBytes = 1 << 20

type ManifestBuilder interface {
	Build(ctx context.Context, request transfer.Request) (transfer.Manifest, error)
}

type manifestGate struct {
	mu         sync.Mutex
	active     map[string]struct{}
	maxEntries int
}

func newManifestGate(maxEntries int) *manifestGate {
	return &manifestGate{
		active:     make(map[string]struct{}),
		maxEntries: maxEntries,
	}
}

func (gate *manifestGate) acquire(subject string) (func(), bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if subject == "" || len(gate.active) >= gate.maxEntries {
		return nil, false
	}
	if _, exists := gate.active[subject]; exists {
		return nil, false
	}
	gate.active[subject] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			delete(gate.active, subject)
			gate.mu.Unlock()
		})
	}, true
}

func manifestHandler(builder ManifestBuilder, gate *manifestGate) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if builder == nil || gate == nil {
			writeInternalError(w)
			return
		}
		var input transfer.Request
		if !decodeManifestJSON(w, request, &input) {
			return
		}
		subject, ok := accessSubject(request.Context())
		if !ok {
			writeBearerUnauthorized(w)
			return
		}
		release, allowed := gate.acquire(subject)
		if !allowed {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: errorDetail{
				Code:    "transfer_busy",
				Message: "a transfer manifest is already being prepared",
			}})
			return
		}
		defer release()
		manifest, err := builder.Build(request.Context(), input)
		if err != nil {
			writeTransferError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, manifest)
	})
}

func decodeManifestJSON(
	w http.ResponseWriter,
	request *http.Request,
	destination *transfer.Request,
) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: errorDetail{
			Code:    "unsupported_media_type",
			Message: "Content-Type must be application/json",
		}})
		return false
	}
	request.Body = http.MaxBytesReader(w, request.Body, maxManifestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: errorDetail{
				Code:    "request_too_large",
				Message: "request body is too large",
			}})
		} else {
			writeInvalidRequest(w)
		}
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: errorDetail{
				Code:    "request_too_large",
				Message: "request body is too large",
			}})
		} else {
			writeInvalidRequest(w)
		}
		return false
	}
	return true
}

func fileHandler(
	resolver *library.Resolver,
	signer *transfer.FileSigner,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if resolver == nil || signer == nil {
			writeInternalError(w)
			return
		}
		identity, err := signer.Verify(request.PathValue("fileId"))
		if err != nil {
			writeTransferError(w, err)
			return
		}
		digest, err := hex.DecodeString(identity.SHA256)
		if err != nil || len(digest) != sha256.Size {
			writeTransferError(w, transfer.ErrInvalidFileID)
			return
		}
		handle, resolved, err := resolver.OpenRegular(identity.RootID, identity.Path)
		if err != nil || resolved.Relative != identity.Path {
			writeTransferError(w, transfer.ErrSourceChanged)
			return
		}
		defer handle.Close()
		before, err := handle.Stat()
		if err != nil ||
			!before.Mode().IsRegular() ||
			before.Size() != identity.Size ||
			before.ModTime().UnixNano() != identity.ModUnixNano {
			writeTransferError(w, transfer.ErrSourceChanged)
			return
		}
		actualDigest, err := hashOpenFile(request.Context(), handle)
		if err != nil {
			writeTransferError(w, transfer.ErrSourceChanged)
			return
		}
		after, err := handle.Stat()
		if err != nil || !os.SameFile(before, after) ||
			before.Size() != after.Size() ||
			before.ModTime().UnixNano() != after.ModTime().UnixNano() ||
			subtle.ConstantTimeCompare(actualDigest, digest) != 1 {
			writeTransferError(w, transfer.ErrSourceChanged)
			return
		}
		if _, err := handle.Seek(0, io.SeekStart); err != nil {
			writeInternalError(w)
			return
		}
		if after.Size() != identity.Size ||
			after.ModTime().UnixNano() != identity.ModUnixNano {
			writeTransferError(w, transfer.ErrSourceChanged)
			return
		}
		w.Header().Set("ETag", strconv.Quote(identity.SHA256))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(digest))
		http.ServeContent(
			w,
			request,
			path.Base(identity.Path),
			after.ModTime(),
			handle,
		)
	})
}

func hashOpenFile(ctx context.Context, handle *os.File) ([]byte, error) {
	digest := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, readErr := handle.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			return digest.Sum(nil), nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func writeTransferError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transfer.ErrInvalidFileID):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: errorDetail{
			Code:    "invalid_file_id",
			Message: "file identifier is invalid",
		}})
	case errors.Is(err, transfer.ErrSourceChanged), errors.Is(err, os.ErrNotExist):
		writeJSON(w, http.StatusConflict, errorResponse{Error: errorDetail{
			Code:    "source_changed",
			Message: "source file changed; prepare a new transfer",
		}})
	case errors.Is(err, transfer.ErrInvalidSelection),
		errors.Is(err, transfer.ErrDestinationCollision),
		errors.Is(err, transfer.ErrUnsupportedFile),
		errors.Is(err, library.ErrUnknownRoot),
		errors.Is(err, library.ErrPathEscape):
		writeInvalidRequest(w)
	default:
		writeInternalError(w)
	}
}
