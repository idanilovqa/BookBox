package companion

import (
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupportedKind = errors.New("unsupported companion kind")
	ErrInvalidLimit    = errors.New("invalid companion input limit")
	ErrInputTooLarge   = errors.New("companion input too large")
	ErrInvalidEncoding = errors.New("invalid companion encoding")
	ErrMalformedInput  = errors.New("malformed companion input")
)

// Evidence is the small, content-safe subset of companion metadata used by
// grouping. ReferencedFiles is sorted; PlaylistOrder preserves declared order.
type Evidence struct {
	Title           string
	Creators        []string
	ReferencedFiles []string
	PlaylistOrder   []string
}

// Parse extracts grouping evidence without ever buffering more than limit+1
// bytes. Returned errors are sentinels and never contain companion content.
func Parse(kind string, reader io.Reader, limit int64) (Evidence, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "cue", "nfo", "opf", "m3u", "m3u8":
	default:
		return Evidence{}, ErrUnsupportedKind
	}
	if limit <= 0 {
		return Evidence{}, ErrInvalidLimit
	}

	// A positive limit at MaxInt64 cannot be exceeded by an io.Reader in one
	// call, and adding one would overflow. Treat it as an uncapped practical
	// limit while retaining the same bounded-reader behavior for normal limits.
	readLimit := limit + 1
	if readLimit <= 0 {
		readLimit = limit
	}
	contents, err := io.ReadAll(io.LimitReader(reader, readLimit))
	if err != nil {
		return Evidence{}, ErrMalformedInput
	}
	if int64(len(contents)) > limit {
		return Evidence{}, ErrInputTooLarge
	}
	if !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
		return Evidence{}, ErrInvalidEncoding
	}
	body := strings.TrimPrefix(string(contents), "\ufeff")

	var evidence Evidence
	switch kind {
	case "cue":
		evidence, err = parseCUE(body)
	case "nfo":
		evidence = parseNFO(body)
	case "opf":
		evidence, err = parseOPF(body)
	case "m3u", "m3u8":
		evidence = parsePlaylist(body)
	}
	if err != nil {
		return Evidence{}, err
	}
	return normalizeEvidence(evidence), nil
}

func parseCUE(body string) (Evidence, error) {
	var evidence Evidence
	inTrack := false
	for _, rawLine := range splitLines(body) {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(strings.ToUpper(line), "REM ") {
			continue
		}
		directive, remainder, found := strings.Cut(line, " ")
		if !found {
			directive, remainder = line, ""
		}
		switch strings.ToUpper(strings.TrimSpace(directive)) {
		case "TRACK":
			inTrack = true
		case "TITLE":
			value, _, parseErr := cueArgument(remainder)
			if parseErr != nil {
				return Evidence{}, ErrMalformedInput
			}
			if !inTrack && evidence.Title == "" {
				evidence.Title = value
			}
		case "PERFORMER":
			value, _, parseErr := cueArgument(remainder)
			if parseErr != nil {
				return Evidence{}, ErrMalformedInput
			}
			if !inTrack {
				evidence.Creators = append(evidence.Creators, value)
			}
		case "FILE":
			value, _, parseErr := cueArgument(remainder)
			if parseErr != nil || value == "" {
				return Evidence{}, ErrMalformedInput
			}
			evidence.ReferencedFiles = append(evidence.ReferencedFiles, value)
		}
	}
	return evidence, nil
}

func cueArgument(input string) (value, remainder string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", nil
	}
	if input[0] != '"' {
		value, remainder, _ = strings.Cut(input, " ")
		return value, strings.TrimSpace(remainder), nil
	}
	closing := strings.IndexByte(input[1:], '"')
	if closing < 0 {
		return "", "", ErrMalformedInput
	}
	closing++
	return input[1:closing], strings.TrimSpace(input[closing+1:]), nil
}

func parseNFO(body string) Evidence {
	var evidence Evidence
	for _, rawLine := range splitLines(body) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		equals := strings.IndexByte(line, '=')
		separator := colon
		if separator < 0 || (equals >= 0 && equals < separator) {
			separator = equals
		}
		if separator < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:separator]))
		value := strings.TrimSpace(line[separator+1:])
		switch key {
		case "title":
			if evidence.Title == "" {
				evidence.Title = value
			}
		case "author", "creator":
			evidence.Creators = append(evidence.Creators, value)
		case "file", "filename":
			evidence.ReferencedFiles = append(evidence.ReferencedFiles, value)
		}
	}
	return evidence
}

type manifestItem struct {
	id   string
	href string
}

func parseOPF(body string) (Evidence, error) {
	decoder := xml.NewDecoder(strings.NewReader(body))
	decoder.Strict = true

	var evidence Evidence
	var stack []string
	var items []manifestItem
	manifestByID := make(map[string]string)
	var spineIDs []string

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Evidence{}, ErrMalformedInput
		}
		switch typed := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(typed.Name.Local)
			stack = append(stack, name)
			if name == "item" && containsElement(stack, "manifest") {
				id, href := xmlAttribute(typed.Attr, "id"), xmlAttribute(typed.Attr, "href")
				if href != "" {
					decoded, decodeErr := url.PathUnescape(href)
					if decodeErr != nil {
						return Evidence{}, ErrMalformedInput
					}
					items = append(items, manifestItem{id: id, href: decoded})
					if id != "" {
						if _, exists := manifestByID[id]; !exists {
							manifestByID[id] = decoded
						}
					}
				}
			}
			if name == "itemref" && containsElement(stack, "spine") {
				if idref := xmlAttribute(typed.Attr, "idref"); idref != "" {
					spineIDs = append(spineIDs, idref)
				}
			}
			if (name == "title" || name == "creator") && containsElement(stack, "metadata") {
				var value string
				if decodeErr := decoder.DecodeElement(&value, &typed); decodeErr != nil {
					return Evidence{}, ErrMalformedInput
				}
				stack = stack[:len(stack)-1]
				if name == "title" && evidence.Title == "" {
					evidence.Title = value
				} else if name == "creator" {
					evidence.Creators = append(evidence.Creators, value)
				}
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != strings.ToLower(typed.Name.Local) {
				return Evidence{}, ErrMalformedInput
			}
			stack = stack[:len(stack)-1]
		}
	}
	for _, item := range items {
		evidence.ReferencedFiles = append(evidence.ReferencedFiles, item.href)
	}
	for _, id := range spineIDs {
		if href := manifestByID[id]; href != "" {
			evidence.PlaylistOrder = append(evidence.PlaylistOrder, href)
		}
	}
	return evidence, nil
}

func parsePlaylist(body string) Evidence {
	var evidence Evidence
	for _, rawLine := range splitLines(body) {
		line := strings.TrimSpace(strings.TrimPrefix(rawLine, "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if key, value, found := strings.Cut(line[1:], ":"); found && strings.EqualFold(strings.TrimSpace(key), "PLAYLIST") && evidence.Title == "" {
				evidence.Title = value
			}
			continue
		}
		evidence.ReferencedFiles = append(evidence.ReferencedFiles, line)
		evidence.PlaylistOrder = append(evidence.PlaylistOrder, line)
	}
	return evidence
}

func normalizeEvidence(evidence Evidence) Evidence {
	evidence.Title = normalizeText(evidence.Title)
	evidence.Creators = sortedUnique(evidence.Creators, normalizeText)
	evidence.ReferencedFiles = sortedUnique(evidence.ReferencedFiles, normalizePath)
	evidence.PlaylistOrder = orderedUnique(evidence.PlaylistOrder, normalizePath)
	return evidence
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(strings.TrimPrefix(value, "\ufeff")), " ")
}

func normalizePath(value string) string {
	value = normalizeText(value)
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func sortedUnique(values []string, normalize func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalize(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

func orderedUnique(values []string, normalize func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalize(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func splitLines(body string) []string {
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}

func containsElement(stack []string, wanted string) bool {
	for _, element := range stack {
		if element == wanted {
			return true
		}
	}
	return false
}

func xmlAttribute(attributes []xml.Attr, name string) string {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute.Name.Local, name) {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}
