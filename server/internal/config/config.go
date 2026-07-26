package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

type Root struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	ContainerPath string `yaml:"container_path"`
}

type TLS struct {
	CertPath string   `yaml:"cert_path"`
	KeyPath  string   `yaml:"key_path"`
	Hosts    []string `yaml:"hosts"`
}

type Auth struct {
	Username         string `yaml:"username"`
	PasswordHashFile string `yaml:"password_hash_file"`
	SessionKeyFile   string `yaml:"session_key_file"`
}

type Config struct {
	Listen string `yaml:"listen"`
	Roots  []Root `yaml:"roots"`
	TLS    TLS    `yaml:"tls"`
	Auth   Auth   `yaml:"auth"`
}

type Secrets struct {
	PasswordHash string
	SessionKey   []byte
}

var (
	ErrDuplicateRootID           = errors.New("duplicate root id")
	ErrInsecureSecretPermissions = errors.New("secret file is group or world accessible")
	ErrInvalidSecretFile         = errors.New("secret path must be a regular file")
	ErrInvalidSessionKey         = errors.New("session key must be exactly 32 bytes")
	ErrEmptySecret               = errors.New("secret file is empty")
)

var safeRootID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func Load(path string) (Config, error) {
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
	if cfg.Listen == "" || len(cfg.Roots) == 0 {
		return Config{}, errors.New("listen and roots are required")
	}
	if cfg.TLS.CertPath == "" || cfg.TLS.KeyPath == "" || len(cfg.TLS.Hosts) == 0 ||
		cfg.Auth.Username == "" || cfg.Auth.PasswordHashFile == "" ||
		cfg.Auth.SessionKeyFile == "" {
		return Config{}, errors.New("tls and auth configuration are required")
	}
	for name, path := range map[string]string{
		"tls.cert_path":           cfg.TLS.CertPath,
		"tls.key_path":            cfg.TLS.KeyPath,
		"auth.password_hash_file": cfg.Auth.PasswordHashFile,
		"auth.session_key_file":   cfg.Auth.SessionKeyFile,
	} {
		if !filepath.IsAbs(path) {
			return Config{}, fmt.Errorf("%s must be absolute", name)
		}
	}
	for _, host := range cfg.TLS.Hosts {
		if strings.TrimSpace(host) == "" {
			return Config{}, errors.New("tls.hosts cannot contain empty values")
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

	return cfg, nil
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
