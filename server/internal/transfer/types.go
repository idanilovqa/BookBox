package transfer

import (
	"errors"
	"time"
)

var (
	ErrInvalidSelection     = errors.New("invalid transfer selection")
	ErrDestinationCollision = errors.New("destination path collision")
	ErrUnsupportedFile      = errors.New("unsupported source file")
	ErrSourceChanged        = errors.New("source changed")
	ErrInvalidFileID        = errors.New("invalid file id")
	ErrInvalidSignerKey     = errors.New("file signer key must be exactly 32 bytes")
)

type Selection struct {
	RootID       string `json:"rootId"`
	RelativePath string `json:"relativePath"`
	Kind         string `json:"kind"`
	BookID       string `json:"bookId,omitempty"`
}

type Request struct {
	Selections []Selection `json:"selections"`
}

type ManifestFile struct {
	FileID          string    `json:"fileId"`
	SourcePath      string    `json:"sourcePath"`
	DestinationPath string    `json:"destinationPath"`
	Size            int64     `json:"size"`
	ModifiedAt      time.Time `json:"modifiedAt"`
	ETag            string    `json:"etag"`
	SHA256          string    `json:"sha256"`
}

type Manifest struct {
	ID         string         `json:"id"`
	Files      []ManifestFile `json:"files"`
	TotalBytes int64          `json:"totalBytes"`
}

type FileIdentity struct {
	Version     int    `json:"v"`
	RootID      string `json:"rootId"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ModUnixNano int64  `json:"mtimeUnixNano"`
	SHA256      string `json:"sha256"`
}
