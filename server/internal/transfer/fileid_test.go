package transfer

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestFileIDRoundTripAndRejectsTampering(t *testing.T) {
	signer, err := NewFileSigner(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	identity := FileIdentity{
		RootID:      "audiobooks",
		Path:        "Author/Book/chapter.m4b",
		Size:        123,
		ModUnixNano: 456,
		SHA256:      strings.Repeat("ab", 32),
	}
	encoded, err := signer.Sign(identity)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Verify(encoded)
	if err != nil {
		t.Fatal(err)
	}
	identity.Version = fileIdentityVersion
	if got != identity {
		t.Fatalf("Verify() = %#v, want %#v", got, identity)
	}

	tampered := []byte(encoded)
	tampered[len(tampered)-2] ^= 1
	if _, err := signer.Verify(string(tampered)); !errors.Is(err, ErrInvalidFileID) {
		t.Fatalf("Verify(tampered) error = %v, want ErrInvalidFileID", err)
	}
}

func TestFileIDSignAndVerifyRejectInvalidIdentities(t *testing.T) {
	signer, err := NewFileSigner(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	valid := FileIdentity{
		RootID:      "audiobooks",
		Path:        "Book/chapter.mp3",
		Size:        4,
		ModUnixNano: 10,
		SHA256:      strings.Repeat("01", 32),
	}
	tests := map[string]func(*FileIdentity){
		"unsafe root":        func(value *FileIdentity) { value.RootID = "../root" },
		"empty path":         func(value *FileIdentity) { value.Path = "" },
		"absolute path":      func(value *FileIdentity) { value.Path = "/Book/chapter.mp3" },
		"traversal path":     func(value *FileIdentity) { value.Path = "Book/../secret.mp3" },
		"backslash path":     func(value *FileIdentity) { value.Path = `Book\chapter.mp3` },
		"control path":       func(value *FileIdentity) { value.Path = "Book/\nchapter.mp3" },
		"negative size":      func(value *FileIdentity) { value.Size = -1 },
		"uppercase digest":   func(value *FileIdentity) { value.SHA256 = strings.Repeat("AB", 32) },
		"short digest":       func(value *FileIdentity) { value.SHA256 = "01" },
		"unexpected version": func(value *FileIdentity) { value.Version = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			identity := valid
			mutate(&identity)
			if _, err := signer.Sign(identity); !errors.Is(err, ErrInvalidFileID) {
				t.Fatalf("Sign() error = %v, want ErrInvalidFileID", err)
			}
		})
	}

	validID, err := signer.Sign(valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string]string{
		"empty":             "",
		"missing signature": strings.Split(validID, ".")[0],
		"extra segment":     validID + ".extra",
		"invalid base64":    "!.!",
		"oversized":         strings.Repeat("a", 20_000) + "." + strings.Repeat("b", 43),
		"missing field": signRawFileID(
			signer,
			`{"v":1,"rootId":"audiobooks","path":"Book/chapter.mp3","size":4,"sha256":"`+
				strings.Repeat("01", 32)+`"}`,
		),
		"unknown field": signRawFileID(
			signer,
			`{"v":1,"rootId":"audiobooks","path":"Book/chapter.mp3","size":4,"mtimeUnixNano":10,"sha256":"`+
				strings.Repeat("01", 32)+`","extra":true}`,
		),
	} {
		t.Run("verify "+name, func(t *testing.T) {
			if _, err := signer.Verify(encoded); !errors.Is(err, ErrInvalidFileID) {
				t.Fatalf("Verify() error = %v, want ErrInvalidFileID", err)
			}
		})
	}
}

func signRawFileID(signer *FileSigner, payload string) string {
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write(fileSignerMACDomain)
	_, _ = mac.Write([]byte(encodedPayload))
	return encodedPayload + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestFileSignerRejectsWrongKeyLength(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		if _, err := NewFileSigner(make([]byte, size)); !errors.Is(err, ErrInvalidSignerKey) {
			t.Fatalf("NewFileSigner(%d bytes) error = %v", size, err)
		}
	}
}
