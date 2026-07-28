package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"bookbox.local/server/internal/auth"
)

func TestHashPasswordStdinModePrintsOnlyVerifiableHash(t *testing.T) {
	var output, prompts bytes.Buffer
	err := runAdmin(
		[]string{"hash-password", "--stdin"},
		strings.NewReader("correct horse\ncorrect horse\n"),
		&output,
		&prompts,
		false,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.TrimSuffix(output.String(), "\n")
	if strings.Contains(output.String(), "correct horse") ||
		!strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") ||
		prompts.Len() != 0 {
		t.Fatalf("stdout = %q, prompts = %q", output.String(), prompts.String())
	}
	ok, err := auth.VerifyPassword(encoded, "correct horse")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword() = %v, %v", ok, err)
	}
}

func TestHashPasswordTerminalModeReadsTwiceWithoutWritingSecret(t *testing.T) {
	var output, prompts bytes.Buffer
	values := [][]byte{[]byte("terminal secret"), []byte("terminal secret")}
	readIndex := 0
	err := runAdmin(
		[]string{"hash-password"},
		strings.NewReader("unused"),
		&output,
		&prompts,
		true,
		func() ([]byte, error) {
			value := values[readIndex]
			readIndex++
			return value, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if readIndex != 2 || strings.Contains(output.String(), "terminal secret") ||
		strings.Contains(prompts.String(), "terminal secret") {
		t.Fatalf("reads = %d, stdout = %q, prompts = %q", readIndex, output.String(), prompts.String())
	}
	if !strings.Contains(prompts.String(), "Password:") ||
		!strings.Contains(prompts.String(), "Confirm password:") {
		t.Fatalf("prompts = %q", prompts.String())
	}
}

func TestHashPasswordRejectsMismatchNonTerminalWithoutFlagAndPlaintextArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		input    string
		terminal bool
		want     error
	}{
		{"mismatch", []string{"hash-password", "--stdin"}, "one\ntwo\n", false, ErrPasswordMismatch},
		{"empty password", []string{"hash-password", "--stdin"}, "\n\n", false, ErrEmptyPassword},
		{"non-terminal needs flag", []string{"hash-password"}, "one\none\n", false, ErrExplicitStdinRequired},
		{"plaintext argument", []string{"hash-password", "secret"}, "", true, ErrUsage},
		{"unknown command", []string{"other"}, "", true, ErrUsage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runAdmin(
				test.args,
				strings.NewReader(test.input),
				&output,
				&bytes.Buffer{},
				test.terminal,
				func() ([]byte, error) { return []byte("unused"), nil },
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("runAdmin() error = %v, want %v", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("runAdmin() wrote output on error: %q", output.String())
			}
		})
	}
}
