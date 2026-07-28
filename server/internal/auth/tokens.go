package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	tokenIssuer       = "bookbox"
	tokenAudience     = "bookbox-android"
	TokenKindAccess   = "access"
	TokenKindRefresh  = "refresh"
	accessTokenTTL    = 15 * time.Minute
	refreshTokenTTL   = 30 * 24 * time.Hour
	sessionKeyBytes   = 32
	tokenIdentifierSz = 16
)

var (
	ErrInvalidSessionKey = errors.New("session key must be exactly 32 bytes")
	ErrInvalidSubject    = errors.New("token subject is required")
	ErrInvalidToken      = errors.New("invalid token")
	ErrWrongTokenKind    = errors.New("wrong token kind")
)

type Claims struct {
	Kind string `json:"kind"`
	jwt.RegisteredClaims
}

type Pair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	AccessExpiry time.Time `json:"accessExpiry"`
}

type TokenManager struct {
	mu     sync.RWMutex
	key    []byte
	now    func() time.Time
	random io.Reader
}

func NewTokenManager(key []byte, now func() time.Time) (*TokenManager, error) {
	return newTokenManagerWithRandom(key, now, rand.Reader)
}

func newTokenManagerWithRandom(
	key []byte,
	now func() time.Time,
	random io.Reader,
) (*TokenManager, error) {
	if len(key) != sessionKeyBytes {
		return nil, ErrInvalidSessionKey
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &TokenManager{
		key:    append([]byte(nil), key...),
		now:    now,
		random: random,
	}, nil
}

func (manager *TokenManager) Issue(subject string) (Pair, error) {
	if subject == "" {
		return Pair{}, ErrInvalidSubject
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.issueLocked(subject)
}

func (manager *TokenManager) issueLocked(subject string) (Pair, error) {
	now := manager.now().UTC()
	accessExpiry := now.Add(accessTokenTTL)
	access, err := manager.signLocked(subject, TokenKindAccess, now, accessExpiry)
	if err != nil {
		return Pair{}, err
	}
	refresh, err := manager.signLocked(subject, TokenKindRefresh, now, now.Add(refreshTokenTTL))
	if err != nil {
		return Pair{}, err
	}
	return Pair{
		AccessToken:  access,
		RefreshToken: refresh,
		AccessExpiry: accessExpiry,
	}, nil
}

func (manager *TokenManager) ValidateAccess(encoded string) (*Claims, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.validateLocked(encoded, TokenKindAccess)
}

func (manager *TokenManager) Refresh(encoded string) (Pair, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	claims, err := manager.validateLocked(encoded, TokenKindRefresh)
	if err != nil {
		return Pair{}, err
	}
	return manager.issueLocked(claims.Subject)
}

func (manager *TokenManager) Rotate(key []byte) error {
	if len(key) != sessionKeyBytes {
		return ErrInvalidSessionKey
	}
	manager.mu.Lock()
	manager.key = append(manager.key[:0], key...)
	manager.mu.Unlock()
	return nil
}

func (manager *TokenManager) signLocked(subject, kind string, now, expiry time.Time) (string, error) {
	identifier := make([]byte, tokenIdentifierSz)
	if _, err := io.ReadFull(manager.random, identifier); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	claims := Claims{
		Kind: kind,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{tokenAudience},
			ExpiresAt: jwt.NewNumericDate(expiry),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        base64.RawURLEncoding.EncodeToString(identifier),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(manager.key)
}

func (manager *TokenManager) validateLocked(encoded, expectedKind string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(
		encoded,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, ErrInvalidToken
			}
			return manager.key, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(manager.now),
	)
	if err != nil || token == nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	if claims.Subject == "" || claims.ID == "" || claims.IssuedAt == nil ||
		claims.IssuedAt.Time.After(manager.now()) {
		return nil, ErrInvalidToken
	}
	if claims.Kind != expectedKind {
		return nil, ErrWrongTokenKind
	}
	return claims, nil
}
