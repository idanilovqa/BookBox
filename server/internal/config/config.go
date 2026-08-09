package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Root struct {
	ID            string
	Name          string
	ContainerPath string
}

func (root *Root) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("root must be a mapping")
	}

	var path, legacyPath string
	var pathSet, legacyPathSet bool
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if _, exists := seen[key.Value]; exists {
			return fmt.Errorf("duplicate root field %q", key.Value)
		}
		seen[key.Value] = struct{}{}
		switch key.Value {
		case "id":
			if err := value.Decode(&root.ID); err != nil {
				return err
			}
		case "name":
			if err := value.Decode(&root.Name); err != nil {
				return err
			}
		case "path":
			pathSet = true
			if err := value.Decode(&path); err != nil {
				return err
			}
		case "container_path":
			legacyPathSet = true
			if err := value.Decode(&legacyPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("field %s not found in type config.Root", key.Value)
		}
	}
	if pathSet && legacyPathSet {
		return ErrAmbiguousRootPath
	}
	if pathSet {
		root.ContainerPath = path
	} else {
		root.ContainerPath = legacyPath
	}
	return nil
}

type Auth struct {
	Username         string `yaml:"username"`
	PasswordHashFile string `yaml:"password_hash_file"`
	SessionKeyFile   string `yaml:"session_key_file"`
}

type Database struct {
	Host         string `yaml:"host"`
	Port         uint16 `yaml:"port"`
	Name         string `yaml:"name"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`
	SSLMode      string `yaml:"ssl_mode"`
}

type WorkerSettings struct {
	FFProbePath           string `yaml:"ffprobe_path"`
	CoverBackupRoot       string `yaml:"cover_backup_root"`
	LogDirectory          string `yaml:"log_directory"`
	GoogleBooksAPIKeyFile string `yaml:"google_books_api_key_file"`
}

type Config struct {
	Listen   string         `yaml:"listen"`
	Roots    []Root         `yaml:"roots"`
	Auth     Auth           `yaml:"auth"`
	Database Database       `yaml:"database"`
	Worker   WorkerSettings `yaml:"worker"`
}

type Secrets struct {
	PasswordHash string
	SessionKey   []byte
}

var (
	ErrDuplicateRootID           = errors.New("duplicate root id")
	ErrAmbiguousRootPath         = errors.New("root path is ambiguous")
	ErrInsecureSecretPermissions = errors.New("secret file is group or world accessible")
	ErrInvalidSecretFile         = errors.New("secret path must be a regular file")
	ErrInvalidSessionKey         = errors.New("session key must be exactly 32 bytes")
	ErrEmptySecret               = errors.New("secret file is empty")
)

var safeRootID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var safeDatabaseName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func Load(path string) (Config, error) {
	return load(path, true)
}

func LoadWorker(path string) (Config, error) {
	return load(path, false)
}

func load(path string, requireAPIFields bool) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if len(cfg.Roots) == 0 || (requireAPIFields && cfg.Listen == "") {
		return Config{}, errors.New("required configuration is missing")
	}
	if requireAPIFields && (cfg.Auth.Username == "" || cfg.Auth.PasswordHashFile == "" ||
		cfg.Auth.SessionKeyFile == "") {
		return Config{}, errors.New("auth configuration is required")
	}
	if requireAPIFields {
		for name, path := range map[string]string{
			"auth.password_hash_file": cfg.Auth.PasswordHashFile,
			"auth.session_key_file":   cfg.Auth.SessionKeyFile,
		} {
			if !filepath.IsAbs(path) {
				return Config{}, fmt.Errorf("%s must be absolute", name)
			}
		}
	}
	seen := make(map[string]struct{}, len(cfg.Roots))
	for _, root := range cfg.Roots {
		if !safeRootID.MatchString(root.ID) {
			return Config{}, fmt.Errorf("unsafe root id %q", root.ID)
		}
		if _, exists := seen[root.ID]; exists {
			return Config{}, ErrDuplicateRootID
		}
		seen[root.ID] = struct{}{}
		if !filepath.IsAbs(root.ContainerPath) {
			return Config{}, fmt.Errorf("root %q path must be absolute", root.ID)
		}
	}
	if err := validateDatabase(cfg.Database); err != nil {
		return Config{}, err
	}
	for name, path := range map[string]string{
		"worker.ffprobe_path":              cfg.Worker.FFProbePath,
		"worker.cover_backup_root":         cfg.Worker.CoverBackupRoot,
		"worker.log_directory":             cfg.Worker.LogDirectory,
		"worker.google_books_api_key_file": cfg.Worker.GoogleBooksAPIKeyFile,
	} {
		if path != "" && !filepath.IsAbs(path) {
			return Config{}, fmt.Errorf("%s must be absolute", name)
		}
	}

	return cfg, nil
}

func (database Database) Enabled() bool {
	return database.Host != "" || database.Port != 0 || database.Name != "" ||
		database.Username != "" || database.PasswordFile != "" || database.SSLMode != ""
}

func LoadDatabaseDSN(database Database) (string, error) {
	if err := validateDatabase(database); err != nil {
		return "", err
	}
	if !database.Enabled() {
		return "", nil
	}
	passwordBytes, err := readSecretFile(database.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read database password: %w", err)
	}
	password := string(passwordBytes)
	if strings.HasSuffix(password, "\r\n") {
		password = strings.TrimSuffix(password, "\r\n")
	} else if strings.HasSuffix(password, "\n") {
		password = strings.TrimSuffix(password, "\n")
	}
	if password == "" {
		return "", ErrEmptySecret
	}
	if strings.ContainsAny(password, "\r\n") {
		return "", fmt.Errorf("%w: database password contains a line break", ErrInvalidSecretFile)
	}
	connection := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(database.Username, password),
		Host:   net.JoinHostPort(database.Host, strconv.Itoa(int(database.Port))),
		Path:   "/" + database.Name,
	}
	query := connection.Query()
	query.Set("sslmode", database.SSLMode)
	connection.RawQuery = query.Encode()
	return connection.String(), nil
}

func validateDatabase(database Database) error {
	if !database.Enabled() {
		return nil
	}
	if strings.TrimSpace(database.Host) == "" || database.Port == 0 ||
		!safeDatabaseName.MatchString(database.Name) ||
		!safeDatabaseName.MatchString(database.Username) ||
		!filepath.IsAbs(database.PasswordFile) {
		return errors.New("database host, port, name, username, and absolute password_file are required")
	}
	switch database.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return errors.New("database ssl_mode is invalid")
	}
	for _, character := range database.Host {
		if character == '/' || character == '\\' || character <= 0x20 || character == 0x7f {
			return errors.New("database host is invalid")
		}
	}
	return nil
}

func LoadSecrets(authConfig Auth) (Secrets, error) {
	hashBytes, err := readSecretFile(authConfig.PasswordHashFile)
	if err != nil {
		return Secrets{}, fmt.Errorf("read password hash: %w", err)
	}
	key, err := readSecretFile(authConfig.SessionKeyFile)
	if err != nil {
		return Secrets{}, fmt.Errorf("read session key: %w", err)
	}
	hash := string(hashBytes)
	if strings.HasSuffix(hash, "\r\n") {
		hash = strings.TrimSuffix(hash, "\r\n")
	} else if strings.HasSuffix(hash, "\n") {
		hash = strings.TrimSuffix(hash, "\n")
	}
	if hash == "" {
		return Secrets{}, ErrEmptySecret
	}
	if strings.TrimSpace(hash) != hash || strings.ContainsAny(hash, "\r\n") {
		return Secrets{}, fmt.Errorf("%w: password hash contains whitespace", ErrInvalidSecretFile)
	}
	if len(key) != 32 {
		return Secrets{}, ErrInvalidSessionKey
	}
	return Secrets{PasswordHash: hash, SessionKey: key}, nil
}

func readSecretFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalidSecretFile
	}
	if runtime.GOOS == "linux" && info.Mode().Perm()&0o077 != 0 {
		return nil, ErrInsecureSecretPermissions
	}
	if info.Size() == 0 {
		return nil, ErrEmptySecret
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("%w: secret file is too large", ErrInvalidSecretFile)
	}
	return os.ReadFile(path)
}
