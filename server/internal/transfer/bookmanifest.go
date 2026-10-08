package transfer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/library"
	"github.com/google/uuid"
)

type BookRepository interface {
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
}

type CatalogBuilder struct {
	folders    *Builder
	resolver   *library.Resolver
	repository BookRepository
}

func NewCatalogBuilder(
	resolver *library.Resolver,
	signer *FileSigner,
	repository BookRepository,
) *CatalogBuilder {
	return &CatalogBuilder{
		folders: NewBuilder(resolver, signer), resolver: resolver, repository: repository,
	}
}

func (builder *CatalogBuilder) Build(ctx context.Context, request Request) (Manifest, error) {
	if builder == nil || builder.folders == nil || builder.resolver == nil ||
		builder.repository == nil || len(request.Selections) == 0 ||
		len(request.Selections) > maxSelections {
		return Manifest{}, ErrInvalidSelection
	}
	bookSelections := 0
	for _, selection := range request.Selections {
		if selection.Kind == "book" {
			bookSelections++
		}
	}
	if bookSelections == 0 {
		return builder.folders.Build(ctx, request)
	}
	if bookSelections != len(request.Selections) {
		return Manifest{}, ErrInvalidSelection
	}
	return builder.buildBooks(ctx, request.Selections)
}

func (builder *CatalogBuilder) buildBooks(
	ctx context.Context,
	selections []Selection,
) (Manifest, error) {
	manifestID := make([]byte, manifestIDBytes)
	if _, err := io.ReadFull(rand.Reader, manifestID); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		ID:    base64.RawURLEncoding.EncodeToString(manifestID),
		Files: make([]ManifestFile, 0),
	}
	sourceDestinations := make(map[string]struct{})
	destinations := make(map[string]string)
	for _, selection := range selections {
		if selection.RootID != "" || selection.RelativePath != "" {
			return Manifest{}, ErrInvalidSelection
		}
		bookID, err := uuid.Parse(selection.BookID)
		if err != nil || bookID == uuid.Nil {
			return Manifest{}, ErrInvalidSelection
		}
		detail, err := builder.repository.GetBook(ctx, bookID)
		if err != nil {
			return Manifest{}, err
		}
		if detail.Book.ID != bookID || detail.Book.MissingAt != nil ||
			catalog.ValidateSourceFolder(detail.Book.SourceFolder) != nil {
			return Manifest{}, ErrInvalidSelection
		}
		files, err := builder.bookFiles(ctx, detail)
		if err != nil {
			return Manifest{}, err
		}
		for _, file := range files {
			sourceKey := detail.Book.RootID + "\x00" + file.source
			destinationKey := strings.ToLower(file.destination)
			sourceDestinationKey := sourceKey + "\x00" + destinationKey
			if _, exists := sourceDestinations[sourceDestinationKey]; exists {
				continue
			}
			if previous, exists := destinations[destinationKey]; exists && previous != sourceKey {
				return Manifest{}, ErrDestinationCollision
			}
			resolved, err := builder.resolver.Resolve(detail.Book.RootID, file.source)
			if err != nil || resolved.Relative != file.source {
				return Manifest{}, ErrSourceChanged
			}
			manifestFile, err := builder.folders.hashFile(ctx, resolved)
			if err != nil {
				return Manifest{}, err
			}
			manifestFile.DestinationPath = file.destination
			if manifestFile.Size > math.MaxInt64-manifest.TotalBytes {
				return Manifest{}, ErrInvalidSelection
			}
			manifest.TotalBytes += manifestFile.Size
			manifest.Files = append(manifest.Files, manifestFile)
			sourceDestinations[sourceDestinationKey] = struct{}{}
			destinations[destinationKey] = sourceKey
		}
	}
	sort.Slice(manifest.Files, func(left, right int) bool {
		if manifest.Files[left].DestinationPath != manifest.Files[right].DestinationPath {
			return manifest.Files[left].DestinationPath < manifest.Files[right].DestinationPath
		}
		return manifest.Files[left].SourcePath < manifest.Files[right].SourcePath
	})
	return manifest, nil
}

type bookManifestPath struct {
	source      string
	destination string
}

func (builder *CatalogBuilder) bookFiles(
	ctx context.Context,
	detail catalog.BookDetail,
) ([]bookManifestPath, error) {
	folder, err := builder.resolver.Resolve(detail.Book.RootID, detail.Book.SourceFolder)
	if err != nil {
		return nil, err
	}
	folderInfo, err := os.Stat(folder.Absolute)
	if err != nil || !folderInfo.IsDir() {
		return nil, ErrInvalidSelection
	}
	entries, err := os.ReadDir(folder.Absolute)
	if err != nil {
		return nil, err
	}
	totalM4B := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".m4b") {
			totalM4B++
		}
	}

	var assignedAudio []catalog.SourceFile
	for _, file := range detail.Files {
		fileFolder := path.Dir(file.RelativePath)
		if fileFolder == "." {
			fileFolder = ""
		}
		if file.MissingAt != nil || file.RootID != detail.Book.RootID ||
			fileFolder != detail.Book.SourceFolder ||
			!audioExtension(path.Ext(file.RelativePath)) {
			continue
		}
		assignedAudio = append(assignedAudio, file)
	}
	if len(assignedAudio) == 0 {
		return nil, ErrInvalidSelection
	}
	sort.Slice(assignedAudio, func(left, right int) bool {
		return assignedAudio[left].RelativePath < assignedAudio[right].RelativePath
	})
	assignedM4B := 0
	for _, file := range assignedAudio {
		if strings.EqualFold(path.Ext(file.RelativePath), ".m4b") {
			assignedM4B++
		}
	}
	split := detail.Book.SourceFolder == "" ||
		strings.HasPrefix(detail.Book.GroupingKey, "m4b-") ||
		totalM4B > 1 && assignedM4B > 0 && assignedM4B < totalM4B
	if !split {
		folderManifest, err := builder.folders.Build(ctx, Request{Selections: []Selection{{
			RootID: detail.Book.RootID, RelativePath: detail.Book.SourceFolder, Kind: "folder",
		}}})
		if err != nil {
			return nil, err
		}
		result := make([]bookManifestPath, len(folderManifest.Files))
		for index, file := range folderManifest.Files {
			result[index] = bookManifestPath{
				source: file.SourcePath, destination: sanitizeDestinationPath(file.DestinationPath, detail.Book.Title),
			}
		}
		return result, nil
	}

	destinationName := bookDestinationName(detail.Book, assignedAudio)
	result := make([]bookManifestPath, 0, len(assignedAudio)+2)
	stems := make(map[string]struct{}, len(assignedAudio))
	for _, file := range assignedAudio {
		name := path.Base(file.RelativePath)
		extension := path.Ext(name)
		stem := strings.TrimSuffix(name, extension)
		stems[strings.ToLower(stem)] = struct{}{}
		safeName := sanitizeFileName(name, detail.Book.Title)
		result = append(result, bookManifestPath{
			source: file.RelativePath, destination: path.Join(destinationName, safeName),
		})
	}
	cover := recognizedSharedCover(entries)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.Name() == cover {
				return nil, ErrUnsupportedFile
			}
			if _, exists := stems[strings.ToLower(strings.TrimSuffix(
				entry.Name(), filepath.Ext(entry.Name()),
			))]; exists {
				return nil, ErrUnsupportedFile
			}
			continue
		}
		name := entry.Name()
		extension := filepath.Ext(name)
		stem := strings.ToLower(strings.TrimSuffix(name, extension))
		if audioExtension(extension) {
			continue
		}
		if name != cover {
			if _, exists := stems[stem]; !exists {
				continue
			}
		}
		source := path.Join(detail.Book.SourceFolder, name)
		result = append(result, bookManifestPath{
			source: source, destination: path.Join(destinationName, sanitizeFileName(name, detail.Book.Title)),
		})
	}
	return result, nil
}

func bookDestinationName(book catalog.Book, assignedAudio []catalog.SourceFile) string {
	fallback := ""
	for _, file := range assignedAudio {
		if strings.EqualFold(path.Ext(file.RelativePath), ".m4b") {
			fallback = strings.TrimSuffix(
				path.Base(file.RelativePath),
				path.Ext(file.RelativePath),
			)
			break
		}
	}
	if fallback == "" && len(assignedAudio) > 0 {
		name := path.Base(assignedAudio[0].RelativePath)
		fallback = strings.TrimSuffix(name, path.Ext(name))
	}
	if book.SourceFolder == "" && book.ConfirmedMatchID != nil {
		title := strings.TrimSpace(book.Title)
		if title != "" {
			name := title
			if len(book.Authors) > 0 && strings.TrimSpace(book.Authors[0]) != "" {
				name = strings.TrimSpace(book.Authors[0]) + " - " + name
			}
			if book.PublicationYear != nil && *book.PublicationYear > 0 {
				name += " (" + strconv.Itoa(*book.PublicationYear) + ")"
			}
			return sanitizeWithFallback(name, fallback)
		}
	}
	return sanitizeWithFallback(fallback, book.Title)
}

func recognizedSharedCover(entries []os.DirEntry) string {
	for _, base := range []string{"cover", "folder", "front"} {
		for _, extension := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			for _, entry := range entries {
				if !entry.IsDir() &&
					strings.EqualFold(entry.Name(), base+extension) {
					return entry.Name()
				}
			}
		}
	}
	return ""
}

func audioExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".m4b", ".m4a", ".mp3", ".aac", ".flac", ".ogg", ".opus", ".wav":
		return true
	default:
		return false
	}
}

func SanitizeAndroidName(value string) string {
	return sanitizeWithFallback(value, "Audiobook")
}

func sanitizeWithFallback(value, fallback string) string {
	var builder strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f ||
			strings.ContainsRune(`\/:*?"<>|`, character) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(character)
	}
	safe := strings.TrimRightFunc(builder.String(), func(character rune) bool {
		return character == ' ' || character == '.'
	})
	if strings.TrimSpace(safe) != "" {
		return safe
	}
	if fallback != value {
		return sanitizeWithFallback(fallback, "Audiobook")
	}
	return "Audiobook"
}

func sanitizeFileName(name, fallback string) string {
	extension := path.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	safeStem := sanitizeWithFallback(stem, fallback)
	safeExtension := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) ||
			strings.ContainsRune(`\/:*?"<>|`, character) {
			return '_'
		}
		return character
	}, extension)
	return strings.TrimRight(safeStem+safeExtension, " .")
}

func sanitizeDestinationPath(destination, fallback string) string {
	components := strings.Split(destination, "/")
	for index := range components {
		if index == len(components)-1 {
			components[index] = sanitizeFileName(components[index], fallback)
		} else {
			components[index] = sanitizeWithFallback(components[index], fallback)
		}
	}
	return path.Join(components...)
}
