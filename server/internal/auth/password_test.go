package auth

import (
	"errors"
	"strings"
	"testing"
)

var testArgonParams = ArgonParams{
	MemoryKiB:   64,
	Iterations:  1,
	Parallelism: 1,
}

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse", testArgonParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoded hash: %q", encoded)
	}

	ok, err := VerifyPassword(encoded, "correct horse")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(encoded, "wrong")
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
}

func TestVerifyPasswordRejectsMalformedOrUnboundedHashes(t *testing.T) {
	valid, err := HashPassword("secret", testArgonParams)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(valid, "$")
	if len(parts) != 6 {
		t.Fatalf("test hash has %d fields", len(parts))
	}

	tests := map[string]string{
		"empty":                 "",
		"algorithm":             strings.Replace(valid, "argon2id", "argon2i", 1),
		"version":               strings.Replace(valid, "v=19", "v=18", 1),
		"missing parameter":     strings.Replace(valid, "m=64,t=1,p=1", "m=64,t=1", 1),
		"duplicate parameter":   strings.Replace(valid, "m=64,t=1,p=1", "m=64,t=1,p=1,p=1", 1),
		"unknown parameter":     strings.Replace(valid, "m=64,t=1,p=1", "m=64,t=1,p=1,x=1", 1),
		"zero memory":           strings.Replace(valid, "m=64", "m=0", 1),
		"excessive memory":      strings.Replace(valid, "m=64", "m=65537", 1),
		"excessive iterations":  strings.Replace(valid, "t=1", "t=4", 1),
		"excessive parallelism": strings.Replace(valid, "p=1", "p=3", 1),
		"numeric overflow":      strings.Replace(valid, "m=64", "m=999999999999999999999", 1),
		"bad salt base64":       strings.Join([]string{"", parts[1], parts[2], parts[3], "***", parts[5]}, "$"),
		"short salt":            strings.Join([]string{"", parts[1], parts[2], parts[3], "AA", parts[5]}, "$"),
		"bad hash base64":       strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], "***"}, "$"),
		"short hash":            strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], "AA"}, "$"),
		"extra field":           valid + "$extra",
	}

	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword(encoded, "secret")
			if ok || !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("VerifyPassword() = %v, %v; want false, ErrInvalidPasswordHash", ok, err)
			}
		})
	}
}

func TestHashPasswordRejectsUnsafeParameters(t *testing.T) {
	for name, params := range map[string]ArgonParams{
		"zero memory":           {MemoryKiB: 0, Iterations: 1, Parallelism: 1},
		"excessive memory":      {MemoryKiB: 65537, Iterations: 1, Parallelism: 1},
		"zero iterations":       {MemoryKiB: 64, Iterations: 0, Parallelism: 1},
		"excessive iterations":  {MemoryKiB: 64, Iterations: 4, Parallelism: 1},
		"zero parallelism":      {MemoryKiB: 64, Iterations: 1, Parallelism: 0},
		"excessive parallelism": {MemoryKiB: 64, Iterations: 1, Parallelism: 3},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := HashPassword("secret", params); !errors.Is(err, ErrInvalidArgonParams) {
				t.Fatalf("HashPassword() error = %v, want ErrInvalidArgonParams", err)
			}
		})
	}
}
