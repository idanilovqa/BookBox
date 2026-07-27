package covers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"netstorage.local/server/internal/catalog"
	"netstorage.local/server/internal/library"
)

var (
	ErrUnsafeDestination = errors.New("unsafe cover destination")
	ErrNoImprovement     = errors.New("candidate cover is not a material improvement")
)

type CoverRepository interface {
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
	RecordCoverEvent(context.Context, catalog.CoverEvent) error
}

type Request struct {
	BookID      uuid.UUID
	Destination string
	Candidate   []byte
	Provider    string
}

type Publisher struct {
	resolver      *library.Resolver
	repository    CoverRepository
	backupRoot    string
	atomicReplace func(string, []byte, os.FileMode) error
}

func NewPublisher(resolver *library.Resolver, repository CoverRepository, backupRoot string) *Publisher {
	return &Publisher{
		resolver:      resolver,
		repository:    repository,
		backupRoot:    backupRoot,
		atomicReplace: atomicReplace,
	}
}

func (publisher *Publisher) Publish(ctx context.Context, request Request) (catalog.CoverEvent, error) {
	if publisher == nil || publisher.resolver == nil || publisher.repository == nil ||
		request.BookID == uuid.Nil || len(request.Candidate) == 0 ||
		strings.TrimSpace(request.Provider) == "" {
		return catalog.CoverEvent{}, ErrUnsafeDestination
	}
	detail, err := publisher.repository.GetBook(ctx, request.BookID)
	if err != nil {
		return catalog.CoverEvent{}, fmt.Errorf("get cover book: %w", err)
	}
	if detail.Book.ID != request.BookID || detail.Book.MissingAt != nil ||
		catalog.ValidateSourceFolder(detail.Book.SourceFolder) != nil {
		return catalog.CoverEvent{}, ErrUnsafeDestination
	}
	sourceRoot, err := publisher.resolver.Resolve(detail.Book.RootID, "")
	if err != nil {
		return catalog.CoverEvent{}, fmt.Errorf("resolve source root: %w", err)
	}
	bookFolder, err := publisher.resolver.Resolve(detail.Book.RootID, detail.Book.SourceFolder)
	if err != nil {
		return catalog.CoverEvent{}, fmt.Errorf("resolve book folder: %w", err)
	}
	folderInfo, err := os.Stat(bookFolder.Absolute)
	if err != nil || !folderInfo.IsDir() {
		return catalog.CoverEvent{}, ErrUnsafeDestination
	}
	if err := validateBackupRoot(publisher.backupRoot, sourceRoot.Absolute); err != nil {
		return catalog.CoverEvent{}, err
	}

	destination, existing, err := selectDestination(bookFolder.Absolute, request.Destination)
	if err != nil {
		return catalog.CoverEvent{}, err
	}
	target := filepath.Join(bookFolder.Absolute, destination)
	candidateInfo, candidateBytes, err := Inspect(bytes.NewReader(request.Candidate), int64(len(request.Candidate)))
	if err != nil {
		return catalog.CoverEvent{}, err
	}

	var oldBytes []byte
	var oldInfo ImageInfo
	var targetMode os.FileMode = 0o644
	if existing {
		targetInfo, err := os.Lstat(target)
		if err != nil || !targetInfo.Mode().IsRegular() {
			return catalog.CoverEvent{}, ErrUnsafeDestination
		}
		targetMode = targetInfo.Mode().Perm()
		oldBytes, err = os.ReadFile(target)
		if err != nil {
			return catalog.CoverEvent{}, fmt.Errorf("read existing cover: %w", err)
		}
		oldInfo, _, err = Inspect(bytes.NewReader(oldBytes), int64(len(oldBytes)))
		if err == nil && !ShouldReplace(oldInfo, candidateInfo) {
			return catalog.CoverEvent{}, ErrNoImprovement
		}
	}

	output, outputInfo, err := encodeForDestination(destination, candidateInfo, candidateBytes)
	if err != nil {
		return catalog.CoverEvent{}, err
	}
	var backupKey string
	if existing {
		backupKey, err = publisher.backup(oldBytes)
		if err != nil {
			return catalog.CoverEvent{}, fmt.Errorf("backup existing cover: %w", err)
		}
	}
	if err := publisher.atomicReplace(target, output, targetMode); err != nil {
		rollbackErr := rollbackPublication(target, existing, oldBytes, targetMode)
		if rollbackErr != nil {
			return catalog.CoverEvent{}, fmt.Errorf("publish cover: %w (rollback failed: %v)", err, rollbackErr)
		}
		return catalog.CoverEvent{}, fmt.Errorf("publish cover: %w", err)
	}

	event := catalog.CoverEvent{
		ID:            uuid.New(),
		BookID:        request.BookID,
		Action:        "created",
		Destination:   destination,
		NewSHA256:     hashBytes(output),
		NewDimensions: []int32{int32(outputInfo.Width), int32(outputInfo.Height)},
		Provider:      strings.TrimSpace(request.Provider),
		BackupKey:     backupKey,
		CreatedAt:     time.Now().UTC(),
	}
	if existing {
		event.Action = "replaced"
		event.OldSHA256 = hashBytes(oldBytes)
		if oldInfo.Width > 0 && oldInfo.Height > 0 {
			event.OldDimensions = []int32{int32(oldInfo.Width), int32(oldInfo.Height)}
		}
	}
	if err := publisher.repository.RecordCoverEvent(ctx, event); err != nil {
		rollbackErr := rollbackPublication(target, existing, oldBytes, targetMode)
		if rollbackErr != nil {
			return catalog.CoverEvent{}, fmt.Errorf("record cover event: %w (rollback failed: %v)", err, rollbackErr)
		}
		return catalog.CoverEvent{}, fmt.Errorf("record cover event: %w", err)
	}
	return event, nil
}

func validateBackupRoot(backupRoot, sourceRoot string) error {
	if !filepath.IsAbs(backupRoot) {
		return errors.New("cover backup root must be absolute")
	}
	canonicalBackup, err := canonicalFuturePath(backupRoot)
	if err != nil {
		return fmt.Errorf("resolve cover backup root: %w", err)
	}
	relative, err := filepath.Rel(sourceRoot, canonicalBackup)
	if err != nil {
		return fmt.Errorf("compare cover backup root: %w", err)
	}
	if relative == "." || relative == "" ||
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("cover backup root must be outside the source root")
	}
	return nil
}

func canonicalFuturePath(path string) (string, error) {
	current := filepath.Clean(path)
	var missing []string
	for {
		canonical, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			return filepath.Clean(canonical), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func selectDestination(bookFolder, requested string) (string, bool, error) {
	if requested == "" {
		for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png", "cover.webp", "folder.jpg", "folder.jpeg", "folder.png", "folder.webp", "front.jpg", "front.jpeg", "front.png", "front.webp"} {
			if info, err := os.Lstat(filepath.Join(bookFolder, name)); err == nil {
				if !info.Mode().IsRegular() {
					return "", false, ErrUnsafeDestination
				}
				return name, true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", false, err
			}
		}
		return "cover.jpg", false, nil
	}
	if filepath.Base(requested) != requested || strings.ContainsAny(requested, `/\`) ||
		!recognizedCoverName(requested) {
		return "", false, ErrUnsafeDestination
	}
	info, err := os.Lstat(filepath.Join(bookFolder, requested))
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", false, ErrUnsafeDestination
		}
		return requested, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if requested != "cover.jpg" {
		return "", false, ErrUnsafeDestination
	}
	return requested, false, nil
}

func recognizedCoverName(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	return (base == "cover" || base == "folder" || base == "front") &&
		(extension == ".jpg" || extension == ".jpeg" || extension == ".png" || extension == ".webp")
}

func encodeForDestination(destination string, candidate ImageInfo, source []byte) ([]byte, ImageInfo, error) {
	extension := strings.ToLower(filepath.Ext(destination))
	if extension == ".webp" {
		if candidate.Format != "webp" {
			return nil, ImageInfo{}, ErrUnsupportedImage
		}
		return source, candidate, nil
	}
	if (extension == ".jpg" || extension == ".jpeg") && candidate.Format == "jpeg" {
		return source, candidate, nil
	}
	if extension == ".png" && candidate.Format == "png" {
		return source, candidate, nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, ImageInfo{}, fmt.Errorf("%w: %v", ErrInvalidImage, err)
	}
	var output bytes.Buffer
	switch extension {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(&output, decoded, &jpeg.Options{Quality: 92})
	case ".png":
		err = png.Encode(&output, decoded)
	default:
		return nil, ImageInfo{}, ErrUnsafeDestination
	}
	if err != nil {
		return nil, ImageInfo{}, fmt.Errorf("encode cover image: %w", err)
	}
	info, data, err := Inspect(bytes.NewReader(output.Bytes()), int64(output.Len()))
	return data, info, err
}

func (publisher *Publisher) backup(source []byte) (string, error) {
	if publisher.backupRoot == "" {
		return "", errors.New("cover backup root is required")
	}
	if err := os.MkdirAll(publisher.backupRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(publisher.backupRoot, 0o700); err != nil {
		return "", err
	}
	key := hashBytes(source)
	path := filepath.Join(publisher.backupRoot, key)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, source) {
			return "", errors.New("content-addressed backup collision")
		}
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return "", chmodErr
		}
		return key, nil
	}
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(source); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	if err := syncDirectory(publisher.backupRoot); err != nil {
		return "", err
	}
	return key, nil
}

func atomicReplace(target string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(target)
	temp, err := os.CreateTemp(directory, ".netstorage-cover-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(tempName, target); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func rollbackPublication(target string, existed bool, oldBytes []byte, mode os.FileMode) error {
	if existed {
		return atomicReplace(target, oldBytes, mode)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func hashBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
