package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type manifestEntry struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Narrator string `json:"narrator"`
	Duration string `json:"duration"`
}

type document struct {
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []map[string]any `json:"streams"`
}

func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}

func main() {
	if len(os.Args) != 8 || os.Args[1] != "-v" || os.Args[2] != "error" ||
		os.Args[3] != "-show_entries" ||
		os.Args[4] != "format=duration:format_tags:stream=codec_type,codec_name,width,height" ||
		os.Args[5] != "-of" || os.Args[6] != "json" {
		fail("ffprobe arguments are invalid")
	}
	root, err := filepath.Abs(os.Getenv("BOOKBOX_FFPROBE_FIXTURE_ROOT"))
	if err != nil || strings.TrimSpace(os.Getenv("BOOKBOX_FFPROBE_FIXTURE_ROOT")) == "" {
		fail("fixture root is invalid")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil || !samePath(root, canonicalRoot) {
		fail("fixture root is not canonical")
	}
	manifestCandidate := os.Getenv("BOOKBOX_FFPROBE_FIXTURE_MANIFEST")
	manifestAbsolute, absoluteErr := filepath.Abs(manifestCandidate)
	manifestPath, err := confinedPath(root, manifestCandidate)
	if absoluteErr != nil || err != nil || !samePath(manifestAbsolute, manifestPath) {
		fail("fixture manifest is invalid")
	}
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		fail("fixture manifest is unavailable")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fail("fixture manifest is unavailable")
	}
	var entries []manifestEntry
	if json.Unmarshal(raw, &entries) != nil || len(entries) == 0 || len(entries) > 256 {
		fail("fixture manifest is invalid")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if clean != entry.Path || filepath.IsAbs(entry.Path) || clean == ".." ||
			strings.HasPrefix(clean, "../") || seen[clean] ||
			invalidField(entry.Title) || invalidField(entry.Author) ||
			invalidField(entry.Narrator) || invalidField(entry.Duration) {
			fail("fixture manifest entry is invalid")
		}
		seen[clean] = true
	}
	inputAbsolute, absoluteErr := filepath.Abs(os.Args[7])
	input, err := confinedPath(root, os.Args[7])
	if absoluteErr != nil || err != nil || !samePath(inputAbsolute, input) {
		fail("ffprobe input is invalid")
	}
	relative, _ := filepath.Rel(root, input)
	relative = filepath.ToSlash(relative)
	var selected *manifestEntry
	for index := range entries {
		if filepath.ToSlash(entries[index].Path) == relative {
			selected = &entries[index]
			break
		}
	}
	if selected == nil {
		fail("ffprobe input is absent from the task manifest")
	}
	if pidDirectory := os.Getenv("BOOKBOX_FFPROBE_FIXTURE_PID_DIRECTORY"); pidDirectory != "" {
		pidAbsolute, absoluteErr := filepath.Abs(pidDirectory)
		pidRoot, pathErr := confinedPath(root, pidDirectory)
		pidInfo, infoErr := os.Lstat(pidRoot)
		if absoluteErr != nil || pathErr != nil || !samePath(pidAbsolute, pidRoot) ||
			infoErr != nil || !pidInfo.IsDir() || pidInfo.Mode()&os.ModeSymlink != 0 {
			fail("fixture PID directory is invalid")
		}
		pidPath := filepath.Join(pidRoot, fmt.Sprintf("%d.pid", os.Getpid()))
		pidFile, openErr := os.OpenFile(pidPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr != nil {
			fail("fixture PID marker is unavailable")
		}
		_, writeErr := fmt.Fprintln(pidFile, os.Getpid())
		closeErr := pidFile.Close()
		if writeErr != nil || closeErr != nil {
			fail("fixture PID marker write failed")
		}
	}
	result := document{}
	result.Format.Duration = selected.Duration
	result.Format.Tags = map[string]string{
		"title": selected.Title, "artist": selected.Author,
		"author": selected.Author, "narrator": selected.Narrator,
	}
	result.Streams = []map[string]any{{"codec_type": "audio", "codec_name": "aac"}}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		fail("fixture output failed")
	}
}

func confinedPath(root, candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil || strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("invalid path")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path escapes root")
	}
	return canonical, nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func invalidField(value string) bool {
	return len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00")
}
