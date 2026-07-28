package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"bookbox.local/server/internal/library"
)

const (
	maxSelections   = 100
	hashBufferBytes = 1024 * 1024
	manifestIDBytes = 16
)

type Builder struct {
	resolver *library.Resolver
	signer   *FileSigner
}

type normalizedSelection struct {
	rootID      string
	relative    string
	absolute    string
	destination string
}

func NewBuilder(resolver *library.Resolver, signer *FileSigner) *Builder {
	return &Builder{resolver: resolver, signer: signer}
}

func (builder *Builder) Build(ctx context.Context, request Request) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	selections, err := builder.normalizeSelections(request.Selections)
	if err != nil {
		return Manifest{}, err
	}
	manifestID := make([]byte, manifestIDBytes)
	if _, err := io.ReadFull(rand.Reader, manifestID); err != nil {
		return Manifest{}, fmt.Errorf("generate manifest id: %w", err)
	}
	manifest := Manifest{
		ID:    base64.RawURLEncoding.EncodeToString(manifestID),
		Files: make([]ManifestFile, 0),
	}
	sources := make(map[string]struct{})
	destinations := make(map[string]string)
	for _, selection := range selections {
		err := filepath.WalkDir(selection.absolute, func(absolute string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if absolute == selection.absolute {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrUnsupportedFile
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return ErrUnsupportedFile
			}
			relativeToSelection, err := filepath.Rel(selection.absolute, absolute)
			if err != nil {
				return err
			}
			sourceRelative := path.Join(
				selection.relative,
				filepath.ToSlash(relativeToSelection),
			)
			resolved, err := builder.resolver.Resolve(selection.rootID, sourceRelative)
			if err != nil {
				return err
			}
			if filepath.Clean(resolved.Absolute) != filepath.Clean(absolute) {
				return ErrUnsupportedFile
			}
			sourceKey := selection.rootID + "\x00" + resolved.Relative
			if _, exists := sources[sourceKey]; exists {
				return nil
			}
			destination := path.Join(selection.destination, filepath.ToSlash(relativeToSelection))
			destinationKey := strings.ToLower(destination)
			if existing, exists := destinations[destinationKey]; exists && existing != sourceKey {
				return ErrDestinationCollision
			}
			file, err := builder.hashFile(ctx, resolved)
			if err != nil {
				return err
			}
			file.DestinationPath = destination
			manifest.Files = append(manifest.Files, file)
			sources[sourceKey] = struct{}{}
			destinations[destinationKey] = sourceKey
			if file.Size > math.MaxInt64-manifest.TotalBytes {
				return ErrInvalidSelection
			}
			manifest.TotalBytes += file.Size
			return nil
		})
		if err != nil {
			return Manifest{}, err
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

func (builder *Builder) normalizeSelections(input []Selection) ([]normalizedSelection, error) {
	if len(input) == 0 || len(input) > maxSelections {
		return nil, ErrInvalidSelection
	}
	normalized := make([]normalizedSelection, 0, len(input))
	for _, selection := range input {
		if selection.Kind != "folder" {
			return nil, ErrInvalidSelection
		}
		resolved, err := builder.resolver.Resolve(selection.RootID, selection.RelativePath)
		if err != nil {
			return nil, err
		}
		requested := path.Clean(selection.RelativePath)
		if requested == "." {
			requested = ""
		}
		if requested != resolved.Relative {
			return nil, ErrUnsupportedFile
		}
		info, err := os.Lstat(resolved.Absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalidSelection
		}
		destination := path.Base(resolved.Relative)
		if resolved.Relative == "" {
			destination = selection.RootID
		}
		normalized = append(normalized, normalizedSelection{
			rootID:      selection.RootID,
			relative:    resolved.Relative,
			absolute:    resolved.Absolute,
			destination: destination,
		})
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].rootID != normalized[right].rootID {
			return normalized[left].rootID < normalized[right].rootID
		}
		leftDepth := strings.Count(normalized[left].relative, "/")
		rightDepth := strings.Count(normalized[right].relative, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return normalized[left].relative < normalized[right].relative
	})
	kept := make([]normalizedSelection, 0, len(normalized))
	for _, candidate := range normalized {
		covered := false
		for _, existing := range kept {
			if candidate.rootID == existing.rootID &&
				isSameOrDescendant(candidate.relative, existing.relative) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, candidate)
		}
	}
	destinationRoots := make(map[string]struct{}, len(kept))
	for _, selection := range kept {
		key := strings.ToLower(selection.destination)
		if _, exists := destinationRoots[key]; exists {
			return nil, ErrDestinationCollision
		}
		destinationRoots[key] = struct{}{}
	}
	return kept, nil
}

func isSameOrDescendant(candidate, ancestor string) bool {
	return candidate == ancestor || ancestor == "" ||
		strings.HasPrefix(candidate, ancestor+"/")
}

func (builder *Builder) hashFile(
	ctx context.Context,
	resolved library.ResolvedPath,
) (ManifestFile, error) {
	handle, opened, err := builder.resolver.OpenRegular(
		resolved.RootID,
		resolved.Relative,
	)
	if err != nil || opened.Relative != resolved.Relative {
		return ManifestFile{}, ErrSourceChanged
	}
	defer handle.Close()
	before, err := handle.Stat()
	if err != nil {
		return ManifestFile{}, err
	}
	if !before.Mode().IsRegular() {
		return ManifestFile{}, ErrUnsupportedFile
	}
	digest := sha256.New()
	buffer := make([]byte, hashBufferBytes)
	for {
		if err := ctx.Err(); err != nil {
			return ManifestFile{}, err
		}
		read, readErr := handle.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ManifestFile{}, readErr
		}
	}
	after, err := handle.Stat()
	if err != nil {
		return ManifestFile{}, err
	}
	verification, verified, err := builder.resolver.OpenRegular(
		resolved.RootID,
		resolved.Relative,
	)
	if err != nil || verified.Relative != resolved.Relative {
		return ManifestFile{}, ErrSourceChanged
	}
	defer verification.Close()
	verificationInfo, err := verification.Stat()
	if err != nil || !sameSourceInfo(before, after) ||
		!sameSourceInfo(before, verificationInfo) {
		return ManifestFile{}, ErrSourceChanged
	}
	encodedDigest := hex.EncodeToString(digest.Sum(nil))
	identity := FileIdentity{
		RootID:      resolved.RootID,
		Path:        resolved.Relative,
		Size:        before.Size(),
		ModUnixNano: before.ModTime().UnixNano(),
		SHA256:      encodedDigest,
	}
	fileID, err := builder.signer.Sign(identity)
	if err != nil {
		return ManifestFile{}, err
	}
	return ManifestFile{
		FileID:     fileID,
		SourcePath: resolved.Relative,
		Size:       before.Size(),
		ModifiedAt: before.ModTime().UTC(),
		ETag:       `"` + encodedDigest + `"`,
		SHA256:     encodedDigest,
	}, nil
}

func sameSourceInfo(left, right os.FileInfo) bool {
	return right != nil &&
		os.SameFile(left, right) &&
		left.Size() == right.Size() &&
		left.ModTime().UnixNano() == right.ModTime().UnixNano()
}
