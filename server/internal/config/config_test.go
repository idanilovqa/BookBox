package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresUniqueSafeRootIDs(t *testing.T) {
	path := writeConfig(t, `
listen: ":8787"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/a}
  - {id: audiobooks, name: Duplicate, container_path: /library/b}
`)

	_, err := Load(path)
	if !errors.Is(err, ErrDuplicateRootID) {
		t.Fatalf("Load() error = %v, want ErrDuplicateRootID", err)
	}
}

func TestLoadAcceptsAudiobookRoot(t *testing.T) {
	path := writeConfig(t, `
listen: ":8787"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	cfg, err := Load(path)
	if err != nil || cfg.Roots[0].ID != "audiobooks" {
		t.Fatalf("Load() = %#v, %v", cfg, err)
	}
}

func TestLoadAcceptsHTTPConfigurationWithoutTLS(t *testing.T) {
	path := writeConfigRaw(t, `
listen: ":8787"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8787" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
}

func TestLoadRejectsRemovedTLSSection(t *testing.T) {
	_, err := Load(writeConfigRaw(t, `
listen: ":8787"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
tls:
  cert_path: /tmp/server.crt
  key_path: /tmp/server.key
  hosts: [bookbox.local]
`))
	if err == nil || !strings.Contains(err.Error(), "field tls not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsUnsafeRootID(t *testing.T) {
	path := writeConfig(t, `
listen: ":8787"
roots:
  - {id: ../audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unsafe root ID")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
listen: ":8787"
unexpected: true
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unknown field")
	}
}

func TestLoadRequiresListenAndRoots(t *testing.T) {
	for name, contents := range map[string]string{
		"listen": `roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`,
		"roots": `listen: ":8787"
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, contents)); err == nil {
				t.Fatalf("Load() accepted configuration missing %s", name)
			}
		})
	}
}

func TestLoadRequiresAbsoluteContainerPath(t *testing.T) {
	path := writeConfig(t, `
listen: ":8787"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a relative container path")
	}
}

func TestLoadRequiresAuthenticationConfiguration(t *testing.T) {
	base := `
listen: ":8787"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`
	tests := map[string]string{
		"missing auth": base,
		"relative secret": base + `
auth:
  username: owner
  password_hash_file: secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfigRaw(t, contents)); err == nil {
				t.Fatal("Load() accepted incomplete security configuration")
			}
		})
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "..", "deploy", "config.example.yml")); err != nil {
		t.Fatalf("Load(config.example.yml) error = %v", err)
	}
}

func TestLoadSecretsReadsCRLFHashAndExactBinaryKey(t *testing.T) {
	directory := t.TempDir()
	hashPath := filepath.Join(directory, "password_hash")
	keyPath := filepath.Join(directory, "session_key")
	if err := os.WriteFile(hashPath, []byte("$argon2id$test\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte(" " + strings.Repeat("k", 30) + "\n")
	if len(key) != 32 {
		t.Fatalf("test key length = %d", len(key))
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}

	secrets, err := LoadSecrets(Auth{
		PasswordHashFile: hashPath,
		SessionKeyFile:   keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.PasswordHash != "$argon2id$test" {
		t.Fatalf("PasswordHash = %q", secrets.PasswordHash)
	}
	if string(secrets.SessionKey) != string(key) {
		t.Fatal("LoadSecrets trimmed binary session key")
	}
}

func TestLoadSecretsRejectsInsecureNonRegularAndWrongLengthFiles(t *testing.T) {
	validHash := []byte("$argon2id$test")
	validKey := []byte(strings.Repeat("k", 32))
	tests := []struct {
		name      string
		prepare   func(t *testing.T, hashPath, keyPath string)
		wantError error
	}{
		{
			name: "group readable hash",
			prepare: func(t *testing.T, hashPath, keyPath string) {
				writeSecret(t, hashPath, validHash, 0o640)
				writeSecret(t, keyPath, validKey, 0o600)
			},
			wantError: ErrInsecureSecretPermissions,
		},
		{
			name: "symlink hash",
			prepare: func(t *testing.T, hashPath, keyPath string) {
				target := hashPath + ".target"
				writeSecret(t, target, validHash, 0o600)
				if err := os.Symlink(target, hashPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				writeSecret(t, keyPath, validKey, 0o600)
			},
			wantError: ErrInvalidSecretFile,
		},
		{
			name: "directory key",
			prepare: func(t *testing.T, hashPath, keyPath string) {
				writeSecret(t, hashPath, validHash, 0o600)
				if err := os.Mkdir(keyPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantError: ErrInvalidSecretFile,
		},
		{
			name: "short key",
			prepare: func(t *testing.T, hashPath, keyPath string) {
				writeSecret(t, hashPath, validHash, 0o600)
				writeSecret(t, keyPath, []byte("short"), 0o600)
			},
			wantError: ErrInvalidSessionKey,
		},
		{
			name: "empty hash",
			prepare: func(t *testing.T, hashPath, keyPath string) {
				writeSecret(t, hashPath, nil, 0o600)
				writeSecret(t, keyPath, validKey, 0o600)
			},
			wantError: ErrEmptySecret,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			hashPath := filepath.Join(directory, "password_hash")
			keyPath := filepath.Join(directory, "session_key")
			test.prepare(t, hashPath, keyPath)
			_, err := LoadSecrets(Auth{
				PasswordHashFile: hashPath,
				SessionKeyFile:   keyPath,
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("LoadSecrets() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestLoadDatabaseDSNReadsPasswordOnlyFromProtectedFile(t *testing.T) {
	directory := t.TempDir()
	passwordPath := filepath.Join(directory, "database_password")
	writeSecret(t, passwordPath, []byte("p@ss word\n"), 0o600)

	dsn, err := LoadDatabaseDSN(Database{
		Host:         "postgres",
		Port:         5432,
		Name:         "bookbox",
		Username:     "bookbox",
		PasswordFile: passwordPath,
		SSLMode:      "disable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "bookbox:p%40ss%20word@postgres:5432/bookbox") {
		t.Fatalf("database DSN does not contain escaped file secret: %q", dsn)
	}

	secret := "must-not-appear"
	writeSecret(t, passwordPath, []byte(secret+"\nextra"), 0o600)
	if _, err := LoadDatabaseDSN(Database{
		Host:         "postgres",
		Port:         5432,
		Name:         "bookbox",
		Username:     "bookbox",
		PasswordFile: passwordPath,
		SSLMode:      "disable",
	}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("LoadDatabaseDSN() error = %v; want redacted invalid secret error", err)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	if !strings.Contains(contents, "\nauth:") {
		contents += `
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
`
	}
	return writeConfigRaw(t, contents)
}

func writeConfigRaw(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSecret(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}
