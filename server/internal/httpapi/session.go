package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"netstorage.local/server/internal/auth"
)

const maxSessionBodyBytes = 4096

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func loginHandler(deps Dependencies) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input loginRequest
		if !decodeSessionJSON(w, request, &input) {
			return
		}
		if deps.Limiter == nil || deps.Tokens == nil {
			writeInternalError(w)
			return
		}
		limitUsername := deps.Username
		if allowed, retry := deps.Limiter.Allow(limitUsername, request.RemoteAddr); !allowed {
			seconds := int64(math.Ceil(retry.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: errorDetail{
				Code:    "rate_limited",
				Message: "too many login attempts",
			}})
			return
		}

		passwordOK, err := auth.VerifyPassword(deps.PasswordHash, input.Password)
		if err != nil {
			writeInternalError(w)
			return
		}
		usernameOK := strings.EqualFold(
			strings.TrimSpace(input.Username),
			strings.TrimSpace(deps.Username),
		)
		if !passwordOK || !usernameOK {
			deps.Limiter.RecordFailure(limitUsername, request.RemoteAddr)
			writeInvalidCredentials(w)
			return
		}
		deps.Limiter.RecordSuccess(limitUsername, request.RemoteAddr)
		pair, err := deps.Tokens.Issue(deps.Username)
		if err != nil {
			writeInternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, pair)
	})
}

func refreshHandler(deps Dependencies) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input refreshRequest
		if !decodeSessionJSON(w, request, &input) {
			return
		}
		if deps.Tokens == nil {
			writeInternalError(w)
			return
		}
		pair, err := deps.Tokens.Refresh(input.RefreshToken)
		if err != nil {
			writeInvalidCredentials(w)
			return
		}
		writeJSON(w, http.StatusOK, pair)
	})
}

func decodeSessionJSON(w http.ResponseWriter, request *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: errorDetail{
			Code:    "unsupported_media_type",
			Message: "Content-Type must be application/json",
		}})
		return false
	}
	request.Body = http.MaxBytesReader(w, request.Body, maxSessionBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeInvalidRequest(w)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeInvalidRequest(w)
		return false
	}
	return true
}

func writeInvalidRequest(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, errorResponse{Error: errorDetail{
		Code:    "invalid_request",
		Message: "request body is invalid",
	}})
}

func writeInvalidCredentials(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, errorResponse{Error: errorDetail{
		Code:    "unauthorized",
		Message: "invalid credentials",
	}})
}

func writeInternalError(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorDetail{
		Code:    "internal_error",
		Message: "internal server error",
	}})
}
