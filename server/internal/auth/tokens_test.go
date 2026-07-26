package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestTokenManagerIssuesAndValidatesExpectedClaims(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	manager, err := NewTokenManager(testSessionKey("a"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.ValidateAccess(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "owner" || claims.Kind != TokenKindAccess ||
		claims.Issuer != tokenIssuer || len(claims.Audience) != 1 ||
		claims.Audience[0] != tokenAudience ||
		claims.ID == "" || claims.IssuedAt == nil {
		t.Fatalf("claims = %#v", claims)
	}
	if want := now.Add(15 * time.Minute); !pair.AccessExpiry.Equal(want) {
		t.Fatalf("AccessExpiry = %v, want %v", pair.AccessExpiry, want)
	}
	if _, err := manager.Refresh(pair.AccessToken); !errors.Is(err, ErrWrongTokenKind) {
		t.Fatalf("Refresh(access) error = %v, want ErrWrongTokenKind", err)
	}

	refreshed, err := manager.Refresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ValidateAccess(refreshed.AccessToken); err != nil {
		t.Fatalf("ValidateAccess(refreshed) error = %v", err)
	}
	if refreshed.RefreshToken == pair.RefreshToken || refreshed.AccessToken == pair.AccessToken {
		t.Fatal("refresh reused a token")
	}
}

func TestTokenManagerStrictlyValidatesClaimsAndAlgorithm(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	key := testSessionKey("b")
	manager, err := NewTokenManager(key, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	valid := Claims{
		Kind: TokenKindAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   "owner",
			Audience:  jwt.ClaimStrings{tokenAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        "test-jti",
		},
	}

	tests := map[string]Claims{
		"wrong issuer":    withClaims(valid, func(c *Claims) { c.Issuer = "other" }),
		"wrong audience":  withClaims(valid, func(c *Claims) { c.Audience = jwt.ClaimStrings{"other"} }),
		"missing expiry":  withClaims(valid, func(c *Claims) { c.ExpiresAt = nil }),
		"expired":         withClaims(valid, func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Second)) }),
		"missing subject": withClaims(valid, func(c *Claims) { c.Subject = "" }),
		"missing jti":     withClaims(valid, func(c *Claims) { c.ID = "" }),
		"missing iat":     withClaims(valid, func(c *Claims) { c.IssuedAt = nil }),
		"future iat":      withClaims(valid, func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute)) }),
	}
	for name, claims := range tests {
		t.Run(name, func(t *testing.T) {
			token := signTestToken(t, jwt.SigningMethodHS256, key, claims)
			if _, err := manager.ValidateAccess(token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("ValidateAccess() error = %v, want ErrInvalidToken", err)
			}
		})
	}

	hs384 := signTestToken(t, jwt.SigningMethodHS384, key, valid)
	if _, err := manager.ValidateAccess(hs384); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ValidateAccess(HS384) error = %v, want ErrInvalidToken", err)
	}
	signed := signTestToken(t, jwt.SigningMethodHS256, key, valid)
	replacement := "A"
	if strings.HasSuffix(signed, replacement) {
		replacement = "B"
	}
	tampered := signed[:len(signed)-1] + replacement
	if _, err := manager.ValidateAccess(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ValidateAccess(tampered) error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenKindsExpiryAndRotation(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	manager, err := NewTokenManager(testSessionKey("c"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ValidateAccess(pair.RefreshToken); !errors.Is(err, ErrWrongTokenKind) {
		t.Fatalf("ValidateAccess(refresh) error = %v, want ErrWrongTokenKind", err)
	}

	now = now.Add(31 * 24 * time.Hour)
	if _, err := manager.Refresh(pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Refresh(expired) error = %v, want ErrInvalidToken", err)
	}

	now = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if err := manager.Rotate(testSessionKey("d")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ValidateAccess(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ValidateAccess(pre-rotation) error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenManagerRejectsInvalidKeysAndEmptySubject(t *testing.T) {
	if _, err := NewTokenManager([]byte("short"), time.Now); !errors.Is(err, ErrInvalidSessionKey) {
		t.Fatalf("NewTokenManager(short) error = %v, want ErrInvalidSessionKey", err)
	}
	manager, err := NewTokenManager(testSessionKey("e"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Issue(""); !errors.Is(err, ErrInvalidSubject) {
		t.Fatalf("Issue(empty) error = %v, want ErrInvalidSubject", err)
	}
	if err := manager.Rotate([]byte("short")); !errors.Is(err, ErrInvalidSessionKey) {
		t.Fatalf("Rotate(short) error = %v, want ErrInvalidSessionKey", err)
	}
}

func TestTokenManagerRotationIsConcurrencySafe(t *testing.T) {
	manager, err := NewTokenManager(testSessionKey("f"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(2)
		go func(index int) {
			defer wait.Done()
			_ = manager.Rotate(testSessionKey(string(rune('g' + index%10))))
		}(index)
		go func() {
			defer wait.Done()
			pair, issueErr := manager.Issue("owner")
			if issueErr == nil {
				_, _ = manager.ValidateAccess(pair.AccessToken)
			}
		}()
	}
	wait.Wait()
}

func withClaims(original Claims, change func(*Claims)) Claims {
	copy := original
	copy.Audience = append(jwt.ClaimStrings(nil), original.Audience...)
	change(&copy)
	return copy
}

func signTestToken(t *testing.T, method jwt.SigningMethod, key []byte, claims Claims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func testSessionKey(fill string) []byte {
	return []byte(strings.Repeat(fill, 32))
}
