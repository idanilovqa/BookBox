package catalog

import (
	"encoding/json"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

var searchNamespace = uuid.MustParse("9c0fe941-f519-5a13-a17a-581363a9b21d")

type projectionFileInfo struct {
	file       SourceFile
	role       string
	durationMS int64
	durationOK bool
}

func BuildSearchProjection(input SearchProjectionInput) (SearchProjection, error) {
	if !rootIDPattern.MatchString(input.RootID) || input.ScanRunID == uuid.Nil ||
		input.CompletedAt.IsZero() {
		return SearchProjection{}, ErrInvalidData
	}

	books := make(map[uuid.UUID]Book, len(input.Books))
	for _, book := range input.Books {
		if book.MissingAt != nil {
			continue
		}
		if book.ID == uuid.Nil || book.RootID != input.RootID ||
			ValidateSourceFolder(book.SourceFolder) != nil || strings.TrimSpace(book.Title) == "" ||
			!reviewStates[book.ReviewState] {
			return SearchProjection{}, ErrInvalidData
		}
		if _, exists := books[book.ID]; exists {
			return SearchProjection{}, ErrInvalidData
		}
		books[book.ID] = book
	}

	files := make(map[uuid.UUID]projectionFileInfo, len(input.Files))
	paths := make(map[string]struct{}, len(input.Files))
	for _, file := range input.Files {
		if file.MissingAt != nil {
			continue
		}
		if file.ID == uuid.Nil || file.RootID != input.RootID ||
			ValidateRelativePath(file.RelativePath) != nil || file.Size < 0 ||
			file.ModifiedAt.IsZero() {
			return SearchProjection{}, ErrInvalidData
		}
		if _, exists := files[file.ID]; exists {
			return SearchProjection{}, ErrInvalidData
		}
		if _, exists := paths[file.RelativePath]; exists {
			return SearchProjection{}, ErrInvalidData
		}
		role, duration, known, err := projectionMedia(file.Media)
		if err != nil {
			return SearchProjection{}, err
		}
		files[file.ID] = projectionFileInfo{
			file: file, role: role, durationMS: duration, durationOK: known,
		}
		paths[file.RelativePath] = struct{}{}
	}

	assignments := make(map[uuid.UUID][]uuid.UUID, len(input.Assignments))
	linkedBooks := make(map[uuid.UUID][]uuid.UUID)
	for bookID, fileIDs := range input.Assignments {
		if _, exists := books[bookID]; !exists {
			return SearchProjection{}, ErrInvalidData
		}
		seen := make(map[uuid.UUID]struct{}, len(fileIDs))
		for _, fileID := range fileIDs {
			if _, exists := files[fileID]; !exists {
				return SearchProjection{}, ErrInvalidData
			}
			if _, exists := seen[fileID]; exists {
				return SearchProjection{}, ErrInvalidData
			}
			seen[fileID] = struct{}{}
			assignments[bookID] = append(assignments[bookID], fileID)
			linkedBooks[fileID] = append(linkedBooks[fileID], bookID)
		}
		sort.Slice(assignments[bookID], func(i, j int) bool {
			return assignments[bookID][i].String() < assignments[bookID][j].String()
		})
	}

	documents := make([]SearchDocument, 0, len(books)+len(files)*2)
	bookDocument := make(map[uuid.UUID]int, len(books))
	bookIDs := sortedUUIDKeys(books)
	for _, bookID := range bookIDs {
		book := books[bookID]
		duration, known, filenames, _, err := audiobookMedia(assignments[bookID], files)
		if err != nil {
			return SearchProjection{}, err
		}
		recentlyScanned := timePointer(input.CompletedAt)
		document := SearchDocument{
			ID:     searchUUID(input.RootID, "audiobook", strings.ToLower(book.ID.String())),
			RootID: input.RootID, EntityKind: SearchEntityAudiobook, BookID: book.ID,
			RelativePath: book.SourceFolder, DisplayName: book.Title,
			Title: book.Title, Authors: cleanStrings(book.Authors), Series: strings.TrimSpace(book.Series),
			Narrators: cleanStrings(book.Narrators), Genres: cleanStrings(book.Genres),
			ReviewState: book.ReviewState, PublicationYear: copyInt(book.PublicationYear),
			CreatedAt: timePointer(book.CreatedAt), RecentlyScannedAt: recentlyScanned,
			Filenames: filenames, DuplicateCount: 1,
		}
		if known {
			document.DurationMS = &duration
		}
		prepareSearchDocument(&document)
		bookDocument[bookID] = len(documents)
		documents = append(documents, document)
	}

	assignDuplicateGroups(input.RootID, documents, bookDocument)

	folderFiles := make(map[string][]uuid.UUID)
	fileIDs := sortedUUIDKeys(files)
	for _, fileID := range fileIDs {
		info := files[fileID]
		if info.role != FileRoleAudio && info.role != FileRoleCover {
			continue
		}
		size := info.file.Size
		modified := info.file.ModifiedAt.UTC()
		document := SearchDocument{
			ID:     searchUUID(input.RootID, "file", strings.ToLower(fileID.String())),
			RootID: input.RootID, EntityKind: SearchEntityFile,
			RelativePath: info.file.RelativePath, DisplayName: path.Base(info.file.RelativePath),
			Size: &size, ModifiedAt: &modified, Filenames: []string{path.Base(info.file.RelativePath)},
			DuplicateCount: 1,
		}
		applyLinkedMetadata(&document, linkedBooks[fileID], books, documents, bookDocument)
		prepareSearchDocument(&document)
		documents = append(documents, document)
		for folder := path.Dir(info.file.RelativePath); folder != "." && folder != ""; folder = path.Dir(folder) {
			folderFiles[folder] = append(folderFiles[folder], fileID)
			if path.Dir(folder) == folder {
				break
			}
		}
	}
	for _, folder := range sortedStringKeys(folderFiles) {
		document := SearchDocument{
			ID:     searchUUID(input.RootID, "folder", norm.NFC.String(folder)),
			RootID: input.RootID, EntityKind: SearchEntityFolder,
			RelativePath: folder, DisplayName: path.Base(folder), DuplicateCount: 1,
		}
		var linked []uuid.UUID
		for _, fileID := range folderFiles[folder] {
			linked = append(linked, linkedBooks[fileID]...)
		}
		applyLinkedMetadata(&document, linked, books, documents, bookDocument)
		prepareSearchDocument(&document)
		documents = append(documents, document)
	}
	documentIDs := make(map[uuid.UUID]struct{}, len(documents))
	for _, document := range documents {
		if _, exists := documentIDs[document.ID]; exists {
			return SearchProjection{}, ErrInvalidData
		}
		documentIDs[document.ID] = struct{}{}
	}

	sort.Slice(documents, func(i, j int) bool {
		left, right := documents[i], documents[j]
		if left.EntityKind != right.EntityKind {
			return left.EntityKind < right.EntityKind
		}
		if left.CompactPath != right.CompactPath {
			return left.CompactPath < right.CompactPath
		}
		if left.RelativePath != right.RelativePath {
			return left.RelativePath < right.RelativePath
		}
		return left.ID.String() < right.ID.String()
	})
	return SearchProjection{
		Snapshot: SearchSnapshot{
			RootID: input.RootID, ScanRunID: input.ScanRunID,
			CompletedAt: input.CompletedAt.UTC(), DocumentCount: len(documents),
		},
		Documents: documents,
	}, nil
}

func projectionMedia(media map[string]any) (string, int64, bool, error) {
	roleValue, exists := media["role"]
	if !exists || roleValue == nil {
		return "", 0, false, nil
	}
	role, ok := roleValue.(string)
	if !ok || (role != FileRoleAudio && role != FileRoleCover && role != FileRoleSidecar) {
		return "", 0, false, ErrInvalidData
	}
	if role != FileRoleAudio {
		return role, 0, false, nil
	}
	value, exists := media["durationMs"]
	if !exists || value == nil {
		return role, 0, false, nil
	}
	duration, err := numericInt64(value)
	if err != nil || duration < 0 {
		return "", 0, false, ErrInvalidData
	}
	return role, duration, duration > 0, nil
}

func numericInt64(value any) (int64, error) {
	switch number := value.(type) {
	case int:
		return int64(number), nil
	case int8:
		return int64(number), nil
	case int16:
		return int64(number), nil
	case int32:
		return int64(number), nil
	case int64:
		return number, nil
	case uint:
		if uint64(number) > math.MaxInt64 {
			return 0, ErrInvalidData
		}
		return int64(number), nil
	case uint8:
		return int64(number), nil
	case uint16:
		return int64(number), nil
	case uint32:
		return int64(number), nil
	case uint64:
		if number > math.MaxInt64 {
			return 0, ErrInvalidData
		}
		return int64(number), nil
	case float32:
		return integralFloat(float64(number))
	case float64:
		return integralFloat(number)
	case json.Number:
		if result, err := strconv.ParseInt(string(number), 10, 64); err == nil {
			return result, nil
		}
		parsed, err := strconv.ParseFloat(string(number), 64)
		if err != nil {
			return 0, ErrInvalidData
		}
		return integralFloat(parsed)
	default:
		return 0, ErrInvalidData
	}
}

func integralFloat(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < math.MinInt64 ||
		value >= float64(math.MaxInt64) || math.Trunc(value) != value {
		return 0, ErrInvalidData
	}
	return int64(value), nil
}

func audiobookMedia(ids []uuid.UUID, files map[uuid.UUID]projectionFileInfo) (
	int64, bool, []string, *time.Time, error,
) {
	var total int64
	known := true
	audioCount := 0
	var filenames []string
	var latest time.Time
	for _, id := range ids {
		info := files[id]
		filenames = append(filenames, path.Base(info.file.RelativePath))
		if info.file.ModifiedAt.After(latest) {
			latest = info.file.ModifiedAt
		}
		if info.role != FileRoleAudio {
			continue
		}
		audioCount++
		if !info.durationOK {
			known = false
			continue
		}
		if info.durationMS > math.MaxInt64-total {
			return 0, false, nil, nil, ErrInvalidData
		}
		total += info.durationMS
	}
	sort.Strings(filenames)
	if audioCount == 0 {
		known = false
	}
	var scanned *time.Time
	if !latest.IsZero() {
		value := latest.UTC()
		scanned = &value
	}
	return total, known, filenames, scanned, nil
}

func assignDuplicateGroups(rootID string, documents []SearchDocument, indexes map[uuid.UUID]int) {
	partitions := make(map[string][]int)
	for bookID, index := range indexes {
		document := documents[index]
		if document.DurationMS == nil || *document.DurationMS <= 0 {
			continue
		}
		title := searchCompact(document.Title)
		authors := normalizedUnique(document.Authors)
		narrators := normalizedUnique(document.Narrators)
		if title == "" || len(authors) == 0 || len(narrators) == 0 {
			continue
		}
		key := encodeIdentity(title, encodeIdentity(authors...), encodeIdentity(narrators...))
		partitions[key] = append(partitions[key], indexes[bookID])
	}
	for _, members := range partitions {
		sort.Slice(members, func(i, j int) bool {
			left, right := documents[members[i]], documents[members[j]]
			if *left.DurationMS != *right.DurationMS {
				return *left.DurationMS < *right.DurationMS
			}
			return left.BookID.String() < right.BookID.String()
		})
		for start := 0; start < len(members); {
			end := start + 1
			anchor := *documents[members[start]].DurationMS
			for end < len(members) {
				candidate := *documents[members[end]].DurationMS
				if candidate-anchor > duplicateDurationTolerance(anchor, candidate) {
					break
				}
				end++
			}
			if end-start > 1 {
				groupID := searchUUID(rootID, "duplicate-group", strings.ToLower(documents[members[start]].BookID.String()))
				for _, index := range members[start:end] {
					documents[index].DuplicateGroupID = groupID
					documents[index].DuplicateCount = end - start
				}
			}
			start = end
		}
	}
}

func duplicateDurationTolerance(left, right int64) int64 {
	longer := max(left, right)
	return min(int64(5*time.Minute/time.Millisecond), max(int64(2*time.Minute/time.Millisecond), longer/200))
}

func applyLinkedMetadata(document *SearchDocument, ids []uuid.UUID, books map[uuid.UUID]Book, documents []SearchDocument, indexes map[uuid.UUID]int) {
	unique := make(map[uuid.UUID]struct{}, len(ids))
	groups := make(map[uuid.UUID]int)
	for _, id := range ids {
		if _, exists := books[id]; !exists {
			continue
		}
		unique[id] = struct{}{}
	}
	ordered := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].String() < ordered[j].String() })
	if len(ordered) == 1 {
		document.BookID = ordered[0]
	}
	for _, id := range ordered {
		book := books[id]
		document.Title = joinNonempty(document.Title, book.Title)
		document.Authors = append(document.Authors, book.Authors...)
		document.Narrators = append(document.Narrators, book.Narrators...)
		document.Genres = append(document.Genres, book.Genres...)
		document.Series = joinNonempty(document.Series, book.Series)
		bookDocument := documents[indexes[id]]
		if bookDocument.DuplicateGroupID != uuid.Nil {
			groups[bookDocument.DuplicateGroupID] = bookDocument.DuplicateCount
		}
	}
	document.Authors = cleanStrings(document.Authors)
	document.Narrators = cleanStrings(document.Narrators)
	document.Genres = cleanStrings(document.Genres)
	if len(groups) == 1 {
		for id, count := range groups {
			document.DuplicateGroupID, document.DuplicateCount = id, count
		}
	} else if len(groups) > 1 {
		document.DuplicateCount = 0
		for _, count := range groups {
			document.DuplicateCount += count
		}
	}
}

func prepareSearchDocument(document *SearchDocument) {
	document.CompactName = searchCompact(document.DisplayName)
	document.CompactPath = searchCompact(document.RelativePath)
	document.CompactAuthors = normalizedUnique(document.Authors)
	document.CompactGenres = normalizedUnique(document.Genres)
	metadata := append([]string{document.Title, document.Series}, document.Authors...)
	metadata = append(metadata, document.Narrators...)
	metadata = append(metadata, document.Genres...)
	document.CompactMetadata = searchCompact(strings.Join(metadata, " "))
	document.SearchCompact = document.CompactName + document.CompactPath + document.CompactMetadata
	document.SearchTerms = searchTokens(strings.Join(append([]string{document.DisplayName, document.RelativePath}, metadata...), " "))
	document.SearchGrams = searchGrams(document.SearchCompact)
}

func searchUUID(rootID, kind, identity string) uuid.UUID {
	return uuid.NewSHA1(searchNamespace, []byte("bookbox-search\x00"+rootID+"\x00"+kind+"\x00"+identity))
}

func searchCompact(value string) string {
	value = strings.ToLower(norm.NFC.String(value))
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func searchTokens(value string) string {
	value = strings.ToLower(norm.NFC.String(value))
	return strings.Join(strings.FieldsFunc(value, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	}), " ")
}

func searchGrams(value string) []string {
	runes := []rune(value)
	set := make(map[string]struct{})
	for _, width := range []int{2, 3} {
		for start := 0; start+width <= len(runes); start++ {
			set[string(runes[start:start+width])] = struct{}{}
		}
	}
	return sortedStringKeys(set)
}

func normalizedUnique(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if compact := searchCompact(value); compact != "" {
			set[compact] = struct{}{}
		}
	}
	return sortedStringKeys(set)
}

func cleanStrings(values []string) []string {
	set := make(map[string]string, len(values))
	for _, value := range values {
		value = strings.TrimSpace(norm.NFC.String(value))
		if value == "" {
			continue
		}
		key := searchCompact(value)
		if key != "" {
			if existing, exists := set[key]; !exists || value < existing {
				set[key] = value
			}
		}
	}
	keys := sortedStringKeys(set)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, set[key])
	}
	return result
}

func sortedUUIDKeys[T any](values map[uuid.UUID]T) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func sortedStringKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func encodeIdentity(values ...string) string {
	var builder strings.Builder
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}
func joinNonempty(left, right string) string {
	if strings.TrimSpace(right) == "" {
		return left
	}
	if strings.TrimSpace(left) == "" {
		return strings.TrimSpace(right)
	}
	return left + " " + strings.TrimSpace(right)
}
