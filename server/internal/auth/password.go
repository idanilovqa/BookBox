package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonSaltBytes = 16
	argonKeyBytes  = 32
	maxMemoryKiB   = 65536
	maxIterations  = 3
	maxParallelism = 2
)

var (
	ErrInvalidArgonParams  = errors.New("invalid Argon2 parameters")
	ErrInvalidPasswordHash = errors.New("invalid password hash")
)

type ArgonParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

var ProductionArgonParams = ArgonParams{
	MemoryKiB:   65536,
	Iterations:  3,
	Parallelism: 2,
}

func HashPassword(password string, params ArgonParams) (string, error) {
	if err := validateArgonParams(params); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.MemoryKiB,
		params.Parallelism,
		argonKeyBytes,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.MemoryKiB,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(encoded, candidate string) (bool, error) {
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey(
		[]byte(candidate),
		salt,
		params.Iterations,
		params.MemoryKiB,
		params.Parallelism,
		uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

func parsePasswordHash(encoded string) (ArgonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}

	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 ||
		!strings.HasPrefix(parameters[0], "m=") ||
		!strings.HasPrefix(parameters[1], "t=") ||
		!strings.HasPrefix(parameters[2], "p=") {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	memory, err := strconv.ParseUint(strings.TrimPrefix(parameters[0], "m="), 10, 32)
	if err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	iterations, err := strconv.ParseUint(strings.TrimPrefix(parameters[1], "t="), 10, 32)
	if err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	parallelism, err := strconv.ParseUint(strings.TrimPrefix(parameters[2], "p="), 10, 8)
	if err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params := ArgonParams{
		MemoryKiB:   uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
	}
	if err := validateArgonParams(params); err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltBytes {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	hash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(hash) != argonKeyBytes {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, hash, nil
}

func validateArgonParams(params ArgonParams) error {
	if params.MemoryKiB == 0 || params.MemoryKiB > maxMemoryKiB ||
		params.Iterations == 0 || params.Iterations > maxIterations ||
		params.Parallelism == 0 || params.Parallelism > maxParallelism {
		return ErrInvalidArgonParams
	}
	return nil
}
