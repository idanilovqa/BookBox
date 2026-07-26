package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/library"
	"netstorage.local/server/internal/media"
)

type FileKind string

const (
	KindAudio FileKind = "audio"
	KindCover FileKind = "cover"
)

type File struct {
	ID           uuid.UUID
	RootID       string
	RelativePath string
	Name         string
	Folder       string
	Kind         FileKind
	Size         int64
	ModifiedAt   time.Time
	Metadata     media.Metadata
	ProbeError   string
}

type Folder struct {
	RootID       string
	RelativePath string
	Files        []File
}

type Warning struct {
	RelativePath string
	Operation    string
}

type Result struct {
	RootID   string
	Files    []File
	Folders  []Folder
	Warnings []Warning
}

type Options struct {
	Exclusions []string
}

type Scanner struct {
	probe      media.Probe
	exclusions []string
}

func New(probe media.Probe, options Options) *Scanner {
	exclusions := make([]string, 0, len(options.Exclusions))
	for _, exclusion := range options.Exclusions {
		cleaned := strings.Trim(strings.ReplaceAll(exclusion, `\`, "/"), "/")
		if cleaned != "" {
			exclusions = append(exclusions, cleaned)
		}
	}
	sort.Strings(exclusions)
	return &Scanner{probe: probe, exclusions: exclusions}
}

func (s *Scanner) Scan(ctx context.Context, root config.Root) (Result, error) {
	result := Result{RootID: root.ID}
	if s == nil || s.probe == nil {
		return result, errors.New("media probe is required")
	}
	resolver, err := library.NewResolver([]config.Root{root})
	if err != nil {
		return result, errors.New("configured root is unavailable")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.ContainerPath)
	if err != nil {
		return result, errors.New("configured root is unavailable")
	}

	err = filepath.WalkDir(canonicalRoot, func(
		absolute string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			if isRaceError(walkErr) {
				return nil
			}
			return walkErr
		}
		relativeOS, err := filepath.Rel(canonicalRoot, absolute)
		if err != nil {
			return err
		}
		if relativeOS == "." {
			return nil
		}
		relative := filepath.ToSlash(relativeOS)
		if isHidden(relative) || s.isExcluded(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		kind, recognized := classify(entry.Name())
		if !recognized {
			return nil
		}

		handle, _, err := resolver.OpenRegular(root.ID, relative)
		if err != nil {
			if isRaceError(err) || errors.Is(err, library.ErrNotRegular) ||
				errors.Is(err, library.ErrPathEscape) {
				return nil
			}
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "open",
			})
			return nil
		}
		info, err := handle.Stat()
		if err != nil {
			_ = handle.Close()
			if isRaceError(err) {
				return nil
			}
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "stat",
			})
			return nil
		}

		file := File{
			ID:           stableFileID(root.ID, relative),
			RootID:       root.ID,
			RelativePath: relative,
			Name:         path.Base(relative),
			Folder:       path.Dir(relative),
			Kind:         kind,
			Size:         info.Size(),
			ModifiedAt:   info.ModTime().UTC(),
		}
		if file.Folder == "." {
			file.Folder = ""
		}
		if kind == KindAudio {
			metadata, probeErr := probeOpenedFile(ctx, s.probe, handle)
			if errors.Is(probeErr, media.ErrUnsupportedMedia) || isRaceError(probeErr) {
				_ = handle.Close()
				return nil
			}
			if probeErr != nil {
				file.ProbeError = "probe_failed"
			} else {
				file.Metadata = metadata
			}
		}
		if err := handle.Close(); err != nil {
			result.Warnings = append(result.Warnings, Warning{
				RelativePath: relative,
				Operation:    "close",
			})
		}
		result.Files = append(result.Files, file)
		return nil
	})
	if err != nil {
		return Result{RootID: root.ID}, err
	}
	sortFiles(result.Files)
	sort.Slice(result.Warnings, func(i, j int) bool {
		if result.Warnings[i].RelativePath == result.Warnings[j].RelativePath {
			return result.Warnings[i].Operation < result.Warnings[j].Operation
		}
		return result.Warnings[i].RelativePath < result.Warnings[j].RelativePath
	})
	result.Folders = makeFolders(result.Files)
	return result, nil
}

func probeOpenedFile(
	ctx context.Context,
	probe media.Probe,
	handle *os.File,
) (media.Metadata, error) {
	if descriptorProbe, ok := probe.(media.OpenFileProbe); ok {
		return descriptorProbe.ProbeOpenFile(ctx, handle)
	}
	if runtime.GOOS != "linux" {
		return media.Metadata{}, media.ErrProbeFailed
	}
	descriptorPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), handle.Fd())
	return probe.Probe(ctx, descriptorPath)
}

func classify(name string) (FileKind, bool) {
	lower := strings.ToLower(name)
	extension := strings.ToLower(filepath.Ext(lower))
	if extension == ".part" || extension == ".tmp" {
		return "", false
	}
	switch extension {
	case ".m4b", ".m4a", ".mp3", ".aac", ".flac", ".ogg", ".opus", ".wav":
		return KindAudio, true
	case ".jpg", ".jpeg", ".png", ".webp":
		stem := strings.TrimSuffix(lower, extension)
		if stem == "cover" || stem == "folder" || stem == "front" {
			return KindCover, true
		}
	}
	return "", false
}

func (s *Scanner) isExcluded(relative string) bool {
	for _, exclusion := range s.exclusions {
		if relative == exclusion || strings.HasPrefix(relative, exclusion+"/") {
			return true
		}
		if strings.HasSuffix(exclusion, "/**") {
			prefix := strings.TrimSuffix(exclusion, "/**")
			if relative == prefix || strings.HasPrefix(relative, prefix+"/") {
				return true
			}
		}
		if matched, err := path.Match(exclusion, relative); err == nil && matched {
			return true
		}
	}
	return false
}

func isHidden(relative string) bool {
	for _, component := range strings.Split(relative, "/") {
		if strings.HasPrefix(component, ".") {
			return true
		}
	}
	return false
}

func stableFileID(rootID, relative string) uuid.UUID {
	return uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("netstorage-source:"+rootID+"\x00"+relative),
	)
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool {
		left := strings.ToLower(files[i].RelativePath)
		right := strings.ToLower(files[j].RelativePath)
		if left == right {
			return files[i].RelativePath < files[j].RelativePath
		}
		return left < right
	})
}

func makeFolders(files []File) []Folder {
	byPath := make(map[string][]File)
	hasAudio := make(map[string]bool)
	for _, file := range files {
		byPath[file.Folder] = append(byPath[file.Folder], file)
		if file.Kind == KindAudio {
			hasAudio[file.Folder] = true
		}
	}
	paths := make([]string, 0, len(hasAudio))
	for folder := range hasAudio {
		paths = append(paths, folder)
	}
	sort.Strings(paths)
	folders := make([]Folder, 0, len(paths))
	for _, folder := range paths {
		folderFiles := append([]File(nil), byPath[folder]...)
		sortFiles(folderFiles)
		folders = append(folders, Folder{
			RootID:       folderFiles[0].RootID,
			RelativePath: folder,
			Files:        folderFiles,
		})
	}
	return folders
}

func isRaceError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ELOOP)
}
