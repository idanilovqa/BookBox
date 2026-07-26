package tlsidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	creationMarkerMagic = "netstorage-tls-creation-v1"
	creationMarkerStale = 5 * time.Minute
)

var (
	ErrIncompleteIdentity     = errors.New("TLS identity is incomplete")
	ErrInvalidIdentity        = errors.New("TLS identity is invalid")
	ErrInsecureKeyPermissions = errors.New("TLS private key permissions are insecure")
	ErrInvalidHosts           = errors.New("at least one valid TLS host is required")
	ErrIdentityCreationLocked = errors.New("TLS identity creation is locked")
	ensureMu                  sync.Mutex
)

type Identity struct {
	SHA256Fingerprint string
}

type creationMarker struct {
	Magic      string    `json:"magic"`
	CertPath   string    `json:"certPath"`
	KeyPath    string    `json:"keyPath"`
	StartedAt  time.Time `json:"startedAt"`
	Phase      string    `json:"phase"`
	KeySHA256  string    `json:"keySHA256,omitempty"`
	CertSHA256 string    `json:"certSHA256,omitempty"`
}

func Ensure(certPath, keyPath string, hosts []string) (Identity, error) {
	same, err := samePath(certPath, keyPath)
	if err != nil {
		return Identity{}, err
	}
	if same {
		return Identity{}, ErrInvalidIdentity
	}
	dnsNames, ipAddresses, err := classifyHosts(hosts)
	if err != nil {
		return Identity{}, err
	}

	ensureMu.Lock()
	defer ensureMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return Identity{}, fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return Identity{}, fmt.Errorf("create key directory: %w", err)
	}
	lockPath := certPath + ".lock"
	if err := recoverCreation(lockPath, certPath, keyPath, time.Now().UTC()); err != nil {
		return Identity{}, err
	}

	certInfo, certExists, err := identityFileInfo(certPath)
	if err != nil {
		return Identity{}, err
	}
	keyInfo, keyExists, err := identityFileInfo(keyPath)
	if err != nil {
		return Identity{}, err
	}
	if certExists != keyExists {
		return Identity{}, ErrIncompleteIdentity
	}
	if certExists {
		return loadIdentity(certPath, keyPath, certInfo, keyInfo)
	}

	marker := creationMarker{
		Magic:     creationMarkerMagic,
		CertPath:  certPath,
		KeyPath:   keyPath,
		StartedAt: time.Now().UTC(),
		Phase:     "started",
	}
	if err := createCreationMarker(lockPath, marker); err != nil {
		return Identity{}, err
	}
	publishedKey := false
	defer func() {
		if !publishedKey {
			_ = os.Remove(lockPath)
		}
	}()

	certificatePEM, keyPEM, err := generateIdentity(dnsNames, ipAddresses, time.Now().UTC())
	if err != nil {
		return Identity{}, err
	}
	certTemporary, err := writeTemporary(certPath, certificatePEM, 0o644)
	if err != nil {
		return Identity{}, err
	}
	defer os.Remove(certTemporary)
	keyTemporary, err := writeTemporary(keyPath, keyPEM, 0o600)
	if err != nil {
		return Identity{}, err
	}
	defer os.Remove(keyTemporary)
	keyDigest := sha256.Sum256(keyPEM)
	certDigest := sha256.Sum256(certificatePEM)
	marker.Phase = "prepared"
	marker.KeySHA256 = hex.EncodeToString(keyDigest[:])
	marker.CertSHA256 = hex.EncodeToString(certDigest[:])
	if err := replaceCreationMarker(lockPath, marker); err != nil {
		return Identity{}, err
	}

	if _, exists, err := identityFileInfo(certPath); err != nil || exists {
		if err != nil {
			return Identity{}, err
		}
		return Identity{}, ErrIdentityCreationLocked
	}
	if _, exists, err := identityFileInfo(keyPath); err != nil || exists {
		if err != nil {
			return Identity{}, err
		}
		return Identity{}, ErrIdentityCreationLocked
	}
	if err := os.Rename(keyTemporary, keyPath); err != nil {
		return Identity{}, fmt.Errorf("publish TLS key: %w", err)
	}
	publishedKey = true
	if err := syncDirectory(filepath.Dir(keyPath)); err != nil {
		if removeErr := os.Remove(keyPath); removeErr == nil {
			publishedKey = false
		}
		return Identity{}, err
	}
	if err := os.Rename(certTemporary, certPath); err != nil {
		if removeErr := os.Remove(keyPath); removeErr == nil {
			publishedKey = false
		}
		return Identity{}, fmt.Errorf("publish TLS certificate: %w", err)
	}
	if err := syncDirectory(filepath.Dir(certPath)); err != nil {
		return Identity{}, err
	}
	if err := os.Remove(lockPath); err != nil {
		return Identity{}, fmt.Errorf("remove TLS creation marker: %w", err)
	}
	if err := syncDirectory(filepath.Dir(lockPath)); err != nil {
		return Identity{}, err
	}

	certInfo, _, err = identityFileInfo(certPath)
	if err != nil {
		return Identity{}, err
	}
	keyInfo, _, err = identityFileInfo(keyPath)
	if err != nil {
		return Identity{}, err
	}
	return loadIdentity(certPath, keyPath, certInfo, keyInfo)
}

func recoverCreation(lockPath, certPath, keyPath string, now time.Time) error {
	info, exists, infoErr := identityFileInfo(lockPath)
	if infoErr != nil {
		return ErrIdentityCreationLocked
	}
	if !exists {
		return nil
	}
	if info.Size() > 4096 {
		return ErrIdentityCreationLocked
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return ErrIdentityCreationLocked
	}
	var marker creationMarker
	if err := json.Unmarshal(raw, &marker); err != nil ||
		marker.Magic != creationMarkerMagic ||
		filepath.Clean(marker.CertPath) != filepath.Clean(certPath) ||
		filepath.Clean(marker.KeyPath) != filepath.Clean(keyPath) ||
		marker.StartedAt.IsZero() {
		return ErrIdentityCreationLocked
	}
	certInfo, certExists, certErr := identityFileInfo(certPath)
	if certErr != nil {
		return certErr
	}
	keyInfo, keyExists, keyErr := identityFileInfo(keyPath)
	if keyErr != nil {
		return keyErr
	}
	active := now.Sub(marker.StartedAt) < creationMarkerStale
	if active {
		return ErrIdentityCreationLocked
	}
	if certExists && keyExists {
		if marker.Phase != "prepared" ||
			!fileMatchesDigest(certPath, marker.CertSHA256) ||
			!fileMatchesDigest(keyPath, marker.KeySHA256) {
			return ErrIdentityCreationLocked
		}
		if _, err := loadIdentity(certPath, keyPath, certInfo, keyInfo); err != nil {
			return ErrIdentityCreationLocked
		}
		return removeCreationMarker(lockPath)
	}
	if !certExists && !keyExists {
		return removeCreationMarker(lockPath)
	}
	if !certExists && keyExists && marker.Phase == "prepared" && marker.KeySHA256 != "" {
		rawKey, readErr := os.ReadFile(keyPath)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(rawKey)
		if strings.EqualFold(marker.KeySHA256, hex.EncodeToString(digest[:])) {
			if err := os.Remove(keyPath); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(keyPath)); err != nil {
				return err
			}
			return removeCreationMarker(lockPath)
		}
	}
	return nil
}

func fileMatchesDigest(path, expected string) bool {
	if expected == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	return strings.EqualFold(expected, hex.EncodeToString(digest[:]))
}

func createCreationMarker(path string, marker creationMarker) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := writeTemporary(path, raw, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	err = os.Link(temporary, path)
	if errors.Is(err, os.ErrExist) {
		return ErrIdentityCreationLocked
	}
	if err != nil {
		return fmt.Errorf("create identity marker: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(path)
		}
	}()
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	success = true
	return nil
}

func replaceCreationMarker(path string, marker creationMarker) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := writeTemporary(path, raw, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace identity marker: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func removeCreationMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func samePath(left, right string) (bool, error) {
	leftAbsolute, err := filepath.Abs(left)
	if err != nil {
		return false, err
	}
	rightAbsolute, err := filepath.Abs(right)
	if err != nil {
		return false, err
	}
	leftAbsolute = filepath.Clean(leftAbsolute)
	rightAbsolute = filepath.Clean(rightAbsolute)
	if leftAbsolute == rightAbsolute {
		return true, nil
	}
	leftParent, leftErr := filepath.EvalSymlinks(filepath.Dir(leftAbsolute))
	rightParent, rightErr := filepath.EvalSymlinks(filepath.Dir(rightAbsolute))
	if leftErr == nil && rightErr == nil {
		leftResolved := filepath.Join(leftParent, filepath.Base(leftAbsolute))
		rightResolved := filepath.Join(rightParent, filepath.Base(rightAbsolute))
		return leftResolved == rightResolved, nil
	}
	return false, nil
}

func classifyHosts(hosts []string) ([]string, []net.IP, error) {
	if len(hosts) == 0 {
		return nil, nil, ErrInvalidHosts
	}
	dnsSeen := make(map[string]struct{})
	ipSeen := make(map[string]struct{})
	var dnsNames []string
	var ipAddresses []net.IP
	for _, raw := range hosts {
		host := strings.TrimSpace(raw)
		if host == "" {
			return nil, nil, ErrInvalidHosts
		}
		if address := net.ParseIP(host); address != nil {
			canonical := address.String()
			if _, exists := ipSeen[canonical]; !exists {
				ipSeen[canonical] = struct{}{}
				ipAddresses = append(ipAddresses, address)
			}
			continue
		}
		normalized := strings.ToLower(host)
		if _, exists := dnsSeen[normalized]; !exists {
			dnsSeen[normalized] = struct{}{}
			dnsNames = append(dnsNames, host)
		}
	}
	return dnsNames, ipAddresses, nil
}

func generateIdentity(dnsNames []string, ipAddresses []net.IP, now time.Time) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate TLS key: %w", err)
	}
	serialBytes := make([]byte, 16)
	if _, err := rand.Read(serialBytes); err != nil {
		return nil, nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	serial := new(big.Int).SetBytes(serialBytes)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "netStorage"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(5, 0, 0),
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal TLS key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		nil
}

func writeTemporary(finalPath string, contents []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create TLS temporary file: %w", err)
	}
	path := file.Name()
	success := false
	defer func() {
		if !success {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return "", fmt.Errorf("set TLS temporary permissions: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		return "", fmt.Errorf("write TLS temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync TLS temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close TLS temporary file: %w", err)
	}
	success = true
	return path, nil
}

func identityFileInfo(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, true, ErrInvalidIdentity
	}
	return info, true, nil
}

func loadIdentity(certPath, keyPath string, certInfo, keyInfo os.FileInfo) (Identity, error) {
	if certInfo == nil || keyInfo == nil {
		return Identity{}, ErrIncompleteIdentity
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return Identity{}, ErrInsecureKeyPermissions
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return Identity{}, fmt.Errorf("%w: certificate and key do not form a pair", ErrInvalidIdentity)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: parse certificate", ErrInvalidIdentity)
	}
	digest := sha256.Sum256(certificate.Raw)
	encoded := strings.ToUpper(hex.EncodeToString(digest[:]))
	bytes := make([]string, len(encoded)/2)
	for index := range bytes {
		bytes[index] = encoded[index*2 : index*2+2]
	}
	return Identity{SHA256Fingerprint: strings.Join(bytes, ":")}, nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open TLS directory for sync: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync TLS directory: %w", err)
	}
	return nil
}
