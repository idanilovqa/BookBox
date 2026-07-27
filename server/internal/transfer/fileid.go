package transfer

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	fileIdentityVersion  = 1
	maxEncodedFileIDSize = 16 * 1024
	maxIdentityPathSize  = 4096
)

var (
	fileSignerKeyDomain = []byte("netstorage:file-id:v1:key")
	fileSignerMACDomain = []byte("netstorage:file-id:v1:mac\x00")
	fileIdentityRootID  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

type FileSigner struct {
	key []byte
}

func NewFileSigner(sessionKey []byte) (*FileSigner, error) {
	if len(sessionKey) != 32 {
		return nil, ErrInvalidSignerKey
	}
	derivation := hmac.New(sha256.New, sessionKey)
	_, _ = derivation.Write(fileSignerKeyDomain)
	return &FileSigner{key: derivation.Sum(nil)}, nil
}

func (signer *FileSigner) Sign(identity FileIdentity) (string, error) {
	if identity.Version != 0 && identity.Version != fileIdentityVersion {
		return "", ErrInvalidFileID
	}
	identity.Version = fileIdentityVersion
	if !validFileIdentity(identity) {
		return "", ErrInvalidFileID
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write(fileSignerMACDomain)
	_, _ = mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (signer *FileSigner) Verify(encoded string) (FileIdentity, error) {
	if signer == nil || len(signer.key) != sha256.Size ||
		len(encoded) == 0 || len(encoded) > maxEncodedFileIDSize ||
		strings.Count(encoded, ".") != 1 {
		return FileIdentity{}, ErrInvalidFileID
	}
	encodedPayload, encodedSignature, _ := strings.Cut(encoded, ".")
	signature, err := base64.RawURLEncoding.Strict().DecodeString(encodedSignature)
	if err != nil || len(signature) != sha256.Size {
		return FileIdentity{}, ErrInvalidFileID
	}
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write(fileSignerMACDomain)
	_, _ = mac.Write([]byte(encodedPayload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return FileIdentity{}, ErrInvalidFileID
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encodedPayload)
	if err != nil || len(payload) == 0 || len(payload) > maxIdentityPathSize+1024 {
		return FileIdentity{}, ErrInvalidFileID
	}
	var wire struct {
		Version     *int    `json:"v"`
		RootID      *string `json:"rootId"`
		Path        *string `json:"path"`
		Size        *int64  `json:"size"`
		ModUnixNano *int64  `json:"mtimeUnixNano"`
		SHA256      *string `json:"sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return FileIdentity{}, ErrInvalidFileID
	}
	if err := requireJSONEOF(decoder); err != nil ||
		wire.Version == nil || wire.RootID == nil || wire.Path == nil ||
		wire.Size == nil || wire.ModUnixNano == nil || wire.SHA256 == nil {
		return FileIdentity{}, ErrInvalidFileID
	}
	identity := FileIdentity{
		Version:     *wire.Version,
		RootID:      *wire.RootID,
		Path:        *wire.Path,
		Size:        *wire.Size,
		ModUnixNano: *wire.ModUnixNano,
		SHA256:      *wire.SHA256,
	}
	if !validFileIdentity(identity) {
		return FileIdentity{}, ErrInvalidFileID
	}
	canonical, err := json.Marshal(identity)
	if err != nil || !bytes.Equal(payload, canonical) {
		return FileIdentity{}, ErrInvalidFileID
	}
	return identity, nil
}

func validFileIdentity(identity FileIdentity) bool {
	if identity.Version != fileIdentityVersion ||
		!fileIdentityRootID.MatchString(identity.RootID) ||
		identity.Path == "" || len(identity.Path) > maxIdentityPathSize ||
		!utf8.ValidString(identity.Path) || path.IsAbs(identity.Path) ||
		path.Clean(identity.Path) != identity.Path ||
		strings.Contains(identity.Path, `\`) ||
		identity.Size < 0 ||
		len(identity.SHA256) != sha256.Size*2 ||
		identity.SHA256 != strings.ToLower(identity.SHA256) {
		return false
	}
	for _, character := range identity.Path {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	decoded, err := hex.DecodeString(identity.SHA256)
	return err == nil && len(decoded) == sha256.Size
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ErrInvalidFileID
		}
		return err
	}
	return nil
}
