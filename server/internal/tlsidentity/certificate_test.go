package tlsidentity

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnsureCreatesReusablePrivateIdentityWithExpectedSANs(t *testing.T) {
	certPath, keyPath := identityPaths(t.TempDir())
	first, err := Ensure(certPath, keyPath, []string{"localhost", "192.0.2.10", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(certPath, keyPath, []string{"ignored-after-creation.example"})
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256Fingerprint != second.SHA256Fingerprint {
		t.Fatalf("fingerprints differ: %q != %q", first.SHA256Fingerprint, second.SHA256Fingerprint)
	}
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(first.SHA256Fingerprint) {
		t.Fatalf("fingerprint = %q", first.SHA256Fingerprint)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("key permissions = %#o, want 0600", permissions)
	}

	certificate := readCertificate(t, certPath)
	if err := certificate.VerifyHostname("localhost"); err != nil {
		t.Fatalf("DNS SAN: %v", err)
	}
	for _, address := range []string{"192.0.2.10", "::1"} {
		if !containsIP(certificate.IPAddresses, net.ParseIP(address)) {
			t.Fatalf("certificate IP SANs %v do not contain %s", certificate.IPAddresses, address)
		}
	}
}

func TestEnsureRejectsPartialMismatchedAndInsecureExistingIdentity(t *testing.T) {
	sourceCert, sourceKey := identityPaths(filepath.Join(t.TempDir(), "source"))
	if _, err := Ensure(sourceCert, sourceKey, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	otherCert, otherKey := identityPaths(filepath.Join(t.TempDir(), "other"))
	if _, err := Ensure(otherCert, otherKey, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}

	t.Run("certificate only", func(t *testing.T) {
		cert, key := identityPaths(t.TempDir())
		copyFile(t, sourceCert, cert, 0o644)
		if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrIncompleteIdentity) {
			t.Fatalf("Ensure() error = %v, want ErrIncompleteIdentity", err)
		}
		if _, err := os.Stat(key); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Ensure created missing key: %v", err)
		}
	})
	t.Run("key only", func(t *testing.T) {
		cert, key := identityPaths(t.TempDir())
		copyFile(t, sourceKey, key, 0o600)
		if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrIncompleteIdentity) {
			t.Fatalf("Ensure() error = %v, want ErrIncompleteIdentity", err)
		}
	})
	t.Run("mismatched pair", func(t *testing.T) {
		cert, key := identityPaths(t.TempDir())
		copyFile(t, sourceCert, cert, 0o644)
		copyFile(t, otherKey, key, 0o600)
		if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("Ensure() error = %v, want ErrInvalidIdentity", err)
		}
	})
	t.Run("readable private key", func(t *testing.T) {
		cert, key := identityPaths(t.TempDir())
		copyFile(t, sourceCert, cert, 0o644)
		copyFile(t, sourceKey, key, 0o644)
		if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrInsecureKeyPermissions) {
			t.Fatalf("Ensure() error = %v, want ErrInsecureKeyPermissions", err)
		}
	})
}

func TestEnsureRejectsSymlinksAndEmptyHosts(t *testing.T) {
	if _, err := Ensure(
		filepath.Join(t.TempDir(), "cert.pem"),
		filepath.Join(t.TempDir(), "key.pem"),
		nil,
	); !errors.Is(err, ErrInvalidHosts) {
		t.Fatalf("Ensure(empty hosts) error = %v, want ErrInvalidHosts", err)
	}

	sourceCert, sourceKey := identityPaths(filepath.Join(t.TempDir(), "source"))
	if _, err := Ensure(sourceCert, sourceKey, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	cert, key := identityPaths(t.TempDir())
	if err := os.Symlink(sourceCert, cert); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	copyFile(t, sourceKey, key, 0o600)
	if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Ensure(symlink) error = %v, want ErrInvalidIdentity", err)
	}
}

func TestEnsureConcurrentFirstCreationReturnsOneIdentity(t *testing.T) {
	cert, key := identityPaths(t.TempDir())
	const callers = 16
	results := make(chan Identity, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			identity, err := Ensure(cert, key, []string{"localhost"})
			results <- identity
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Ensure() error = %v", err)
		}
	}
	fingerprint := ""
	for identity := range results {
		if fingerprint == "" {
			fingerprint = identity.SHA256Fingerprint
		}
		if identity.SHA256Fingerprint != fingerprint {
			t.Fatalf("concurrent fingerprints differ: %q != %q", identity.SHA256Fingerprint, fingerprint)
		}
	}
}

func TestEnsureRecoversStaleOwnedCreationMarkerWithoutPublishedPair(t *testing.T) {
	cert, key := identityPaths(t.TempDir())
	writeCreationMarkerForTest(t, cert, key, time.Now().Add(-time.Hour), "started", "")

	if _, err := Ensure(cert, key, []string{"localhost"}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := os.Stat(cert + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale marker remains: %v", err)
	}
}

func TestEnsureTreatsFreshOwnedCreationMarkerAsActive(t *testing.T) {
	cert, key := identityPaths(t.TempDir())
	writeCreationMarkerForTest(t, cert, key, time.Now(), "started", "")

	if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrIdentityCreationLocked) {
		t.Fatalf("Ensure() error = %v, want ErrIdentityCreationLocked", err)
	}
	if _, err := os.Stat(cert); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Ensure published certificate behind active marker: %v", err)
	}
}

func TestEnsureRecoversOnlyOwnedKeyFromInterruptedPublication(t *testing.T) {
	sourceCert, sourceKey := identityPaths(filepath.Join(t.TempDir(), "source"))
	if _, err := Ensure(sourceCert, sourceKey, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	originalKey, err := os.ReadFile(sourceKey)
	if err != nil {
		t.Fatal(err)
	}

	cert, key := identityPaths(t.TempDir())
	copyFile(t, sourceKey, key, 0o600)
	keyDigest := sha256.Sum256(originalKey)
	writeCreationMarkerForTest(
		t,
		cert,
		key,
		time.Now().Add(-time.Hour),
		"prepared",
		hex.EncodeToString(keyDigest[:]),
	)
	if _, err := Ensure(cert, key, []string{"localhost"}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	recoveredKey, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(recoveredKey) == string(originalKey) {
		t.Fatal("interrupted owned key was reused instead of replaced")
	}
}

func TestEnsurePreservesUnexplainedPartialIdentity(t *testing.T) {
	sourceCert, sourceKey := identityPaths(filepath.Join(t.TempDir(), "source"))
	if _, err := Ensure(sourceCert, sourceKey, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	cert, key := identityPaths(t.TempDir())
	copyFile(t, sourceKey, key, 0o600)
	writeCreationMarkerForTest(
		t,
		cert,
		key,
		time.Now().Add(-time.Hour),
		"prepared",
		strings.Repeat("0", sha256.Size*2),
	)

	if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrIncompleteIdentity) {
		t.Fatalf("Ensure() error = %v, want ErrIncompleteIdentity", err)
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("unexplained key was removed: %v", err)
	}
}

func TestEnsureRejectsIdenticalCertificateAndKeyPathsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.pem")
	if _, err := Ensure(path, path, []string{"localhost"}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Ensure() error = %v, want ErrInvalidIdentity", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Ensure wrote identical cert/key path: %v", err)
	}
}

func TestEnsureRejectsSymlinkAliasedIdenticalPaths(t *testing.T) {
	realDirectory := t.TempDir()
	aliasDirectory := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDirectory, aliasDirectory); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cert := filepath.Join(realDirectory, "identity.pem")
	key := filepath.Join(aliasDirectory, "identity.pem")
	if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Ensure() error = %v, want ErrInvalidIdentity", err)
	}
	if _, err := os.Stat(cert); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Ensure wrote through symlink-aliased path: %v", err)
	}
}

func TestEnsureFailsClosedForUnrecognizedMarker(t *testing.T) {
	cert, key := identityPaths(t.TempDir())
	if err := os.WriteFile(cert+".lock", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(cert, key, []string{"localhost"}); !errors.Is(err, ErrIdentityCreationLocked) {
		t.Fatalf("Ensure() error = %v, want ErrIdentityCreationLocked", err)
	}
	if raw, err := os.ReadFile(cert + ".lock"); err != nil || string(raw) != "{" {
		t.Fatalf("unrecognized marker changed: %q, %v", raw, err)
	}
}

func identityPaths(directory string) (string, string) {
	return filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key")
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func copyFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func containsIP(addresses []net.IP, target net.IP) bool {
	for _, address := range addresses {
		if address.Equal(target) {
			return true
		}
	}
	return false
}

func writeCreationMarkerForTest(
	t *testing.T,
	certPath string,
	keyPath string,
	startedAt time.Time,
	phase string,
	keySHA256 string,
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"magic":     "netstorage-tls-creation-v1",
		"certPath":  certPath,
		"keyPath":   keyPath,
		"startedAt": startedAt.UTC(),
		"phase":     phase,
		"keySHA256": keySHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath+".lock", raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
