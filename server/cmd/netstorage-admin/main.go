package main

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"netstorage.local/server/internal/auth"
)

var (
	ErrUsage                 = errors.New("usage: netstorage-admin hash-password [--stdin]")
	ErrPasswordMismatch      = errors.New("passwords do not match")
	ErrEmptyPassword         = errors.New("password cannot be empty")
	ErrExplicitStdinRequired = errors.New("non-terminal input requires --stdin")
)

func main() {
	fd := int(os.Stdin.Fd())
	err := runAdmin(
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		term.IsTerminal(fd),
		func() ([]byte, error) {
			return term.ReadPassword(fd)
		},
	)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAdmin(
	args []string,
	input io.Reader,
	output io.Writer,
	prompts io.Writer,
	terminal bool,
	readTerminal func() ([]byte, error),
) error {
	if len(args) == 0 || args[0] != "hash-password" {
		return ErrUsage
	}
	stdinMode := len(args) == 2 && args[1] == "--stdin"
	if len(args) != 1 && !stdinMode {
		return ErrUsage
	}
	if !terminal && !stdinMode {
		return ErrExplicitStdinRequired
	}

	var first, second []byte
	var err error
	if stdinMode {
		first, second, err = readPasswordLines(input)
	} else {
		if readTerminal == nil {
			return errors.New("terminal password reader is unavailable")
		}
		_, _ = fmt.Fprint(prompts, "Password: ")
		first, err = readTerminal()
		_, _ = fmt.Fprintln(prompts)
		if err == nil {
			_, _ = fmt.Fprint(prompts, "Confirm password: ")
			second, err = readTerminal()
			_, _ = fmt.Fprintln(prompts)
		}
	}
	if err != nil {
		return err
	}
	if len(first) == 0 {
		return ErrEmptyPassword
	}
	if subtle.ConstantTimeCompare(first, second) != 1 {
		return ErrPasswordMismatch
	}
	encoded, err := auth.HashPassword(string(first), auth.ProductionArgonParams)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, encoded)
	return err
}

func readPasswordLines(input io.Reader) ([]byte, []byte, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4096)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, io.ErrUnexpectedEOF
	}
	first := append([]byte(nil), scanner.Bytes()...)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, io.ErrUnexpectedEOF
	}
	second := append([]byte(nil), scanner.Bytes()...)
	return first, second, nil
}
