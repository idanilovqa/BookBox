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

func Ensure(certPath, keyPath string, hosts []string) (Identity, error) {
	dnsNames, ipAddresses, err := classifyHosts(hosts)
	if err != nil {
		return Identity{}, err
	}

	ensureMu.Lock()
	defer ensureMu.Unlock()

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

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return Identity{}, fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return Identity{}, fmt.Errorf("create key directory: %w", err)
	}
	lockPath := certPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return Identity{}, ErrIdentityCreationLocked
	}
	if err != nil {
		return Identity{}, fmt.Errorf("create identity lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return Identity{}, fmt.Errorf("close identity lock: %w", err)
	}
	defer os.Remove(lockPath)

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
	if err := syncDirectory(filepath.Dir(keyPath)); err != nil {
		_ = os.Remove(keyPath)
		return Identity{}, err
	}
	if err := os.Rename(certTemporary, certPath); err != nil {
		_ = os.Remove(keyPath)
		return Identity{}, fmt.Errorf("publish TLS certificate: %w", err)
	}
	if err := syncDirectory(filepath.Dir(certPath)); err != nil {
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
