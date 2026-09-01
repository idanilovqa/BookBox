package grouping

import (
	"crypto/sha256"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/scanner"
)

type Evidence struct {
	NormalizedTitle  string
	NormalizedAuthor string
	IsStandaloneM4B  bool
	FilenameTitle    string
	ChapterLike      bool
}

var (
	numberedUnit = regexp.MustCompile(
		`(?i)(^|[\s._-])(chapter|chap|part|pt|disc|disk|cd|track)[\s._-]*(\d+|one|two|three|four|five|six|seven|eight|nine|ten)([\s._-]|$)`,
	)
	leadingTrackNumber = regexp.MustCompile(`^\s*\d+[\s._-]+`)
	editionSuffix      = regexp.MustCompile(
		`\s+(unabridged(\s+edition)?|abridged(\s+edition)?|audiobook|audio\s+book|revised\s+edition|anniversary\s+edition|\d+(st|nd|rd|th)\s+edition)$`,
	)
	yearPrefix          = regexp.MustCompile(`^(\d{4})`)
	folderSeriesPattern = regexp.MustCompile(
		`^(.{2,}?)\s+(?:book\s+|#)?(\d+(?:\.\d+)?)\s*[-–—:]\s*(.{2,})$`,
	)
)

func Infer(folder scanner.Folder) []catalog.InferredBook {
	files := append([]scanner.File(nil), folder.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	audioFiles := filterKind(files, scanner.KindAudio)
	if len(audioFiles) == 0 {
		return nil
	}
	covers := filterKind(files, scanner.KindCover)
	evidence := make([]Evidence, len(audioFiles))
	for index, file := range audioFiles {
		evidence[index] = evidenceFor(file)
	}

	if canSplit(evidence) {
		books := make([]catalog.InferredBook, 0, len(audioFiles))
		for _, audio := range audioFiles {
			books = append(books, inferredBook(
				folder,
				[]scanner.File{audio},
				covers,
				catalog.ReviewUnreviewed,
				splitGroupingKey(folder, audio),
			))
		}
		sort.Slice(books, func(i, j int) bool {
			left := normalize(books[i].Title)
			right := normalize(books[j].Title)
			if left == right {
				return books[i].GroupingKey < books[j].GroupingKey
			}
			return left < right
		})
		return books
	}

	reviewState := catalog.ReviewUnreviewed
	if ambiguous(evidence) || hasProbeFailure(audioFiles) {
		reviewState = catalog.ReviewNeedsReview
	}
	return []catalog.InferredBook{inferredBook(
		folder,
		audioFiles,
		covers,
		reviewState,
		folderGroupingKey(folder),
	)}
}

func evidenceFor(file scanner.File) Evidence {
	filename := strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
	chapterLike := numberedUnit.MatchString(filename) ||
		leadingTrackNumber.MatchString(filename)
	standaloneM4B := strings.EqualFold(filepath.Ext(file.Name), ".m4b") &&
		!chapterLike
	embeddedTitle := file.Metadata.Title
	if !standaloneM4B && strings.TrimSpace(file.Metadata.Album) != "" {
		embeddedTitle = file.Metadata.Album
	}
	return Evidence{
		NormalizedTitle:  normalizeComparableTitle(embeddedTitle),
		NormalizedAuthor: normalize(file.Metadata.Author),
		IsStandaloneM4B:  standaloneM4B,
		FilenameTitle:    normalizeComparableTitle(filename),
		ChapterLike:      chapterLike,
	}
}

func canSplit(evidence []Evidence) bool {
	if len(evidence) < 2 {
		return false
	}
	if hasCrossConflict(evidence) {
		return false
	}
	identities := make(map[string]struct{}, len(evidence))
	titles := make(map[string]struct{}, len(evidence))
	filenameEvidence := true
	identityEvidence := true
	for _, item := range evidence {
		if !item.IsStandaloneM4B {
			return false
		}
		if item.NormalizedTitle == "" || item.FilenameTitle == "" ||
			!filenameSupports(item.FilenameTitle, item.NormalizedTitle, item.NormalizedAuthor) {
			filenameEvidence = false
		}
		if item.NormalizedTitle == "" || item.NormalizedAuthor == "" {
			identityEvidence = false
		}
		identity := item.NormalizedTitle + "\x00" + item.NormalizedAuthor
		if _, duplicate := identities[identity]; duplicate {
			identityEvidence = false
		}
		identities[identity] = struct{}{}
		titles[item.NormalizedTitle] = struct{}{}
	}
	filenameEvidence = filenameEvidence && len(titles) == len(evidence)
	identityEvidence = identityEvidence && len(identities) == len(evidence)
	return filenameEvidence || identityEvidence
}

func hasCrossConflict(evidence []Evidence) bool {
	for index, item := range evidence {
		if item.FilenameTitle == "" ||
			filenameSupports(item.FilenameTitle, item.NormalizedTitle, item.NormalizedAuthor) {
			continue
		}
		for otherIndex, other := range evidence {
			if index == otherIndex {
				continue
			}
			if filenameSupports(
				item.FilenameTitle,
				other.NormalizedTitle,
				other.NormalizedAuthor,
			) {
				return true
			}
		}
	}
	return false
}

func filenameSupports(filename, title, author string) bool {
	if filename == title {
		return true
	}
	if author == "" {
		return false
	}
	return filename == title+" "+author || filename == author+" "+title
}

func ambiguous(evidence []Evidence) bool {
	if len(evidence) < 2 {
		return false
	}
	filenameTitles := make(map[string]struct{})
	embeddedTitles := make(map[string]struct{})
	embeddedAuthors := make(map[string]struct{})
	standalone := 0
	for _, item := range evidence {
		if item.NormalizedTitle != "" {
			embeddedTitles[item.NormalizedTitle] = struct{}{}
		}
		if item.NormalizedAuthor != "" {
			embeddedAuthors[item.NormalizedAuthor] = struct{}{}
		}
		if !item.IsStandaloneM4B {
			continue
		}
		standalone++
		if item.FilenameTitle != "" {
			filenameTitles[item.FilenameTitle] = struct{}{}
		}
		if item.NormalizedTitle != "" && item.FilenameTitle != "" &&
			!filenameSupports(item.FilenameTitle, item.NormalizedTitle, item.NormalizedAuthor) &&
			item.IsStandaloneM4B {
			return true
		}
	}
	if len(embeddedTitles) > 1 || len(embeddedAuthors) > 1 {
		return true
	}
	if standalone < 2 {
		return false
	}
	return len(filenameTitles) > 1
}

func inferredBook(
	folder scanner.Folder,
	audioFiles []scanner.File,
	covers []scanner.File,
	reviewState string,
	groupingKey string,
) catalog.InferredBook {
	primary := audioFiles[0]
	folderSeries, folderPosition, folderTitle := parseFolderSeries(path.Base(folder.RelativePath))
	title := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Title
	})
	if title == "" {
		title = folderTitle
		if title == "" {
			title = path.Base(folder.RelativePath)
		}
		if title == "." || strings.TrimSpace(title) == "" {
			title = strings.TrimSuffix(primary.Name, filepath.Ext(primary.Name))
		}
	}
	author := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Author
	})
	narrator := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Narrator
	})
	genre := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Genre
	})
	series := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Series
	})
	seriesPosition := ""
	if series == "" && folderSeries != "" && normalizeComparableTitle(title) == normalizeComparableTitle(folderTitle) {
		series = folderSeries
		seriesPosition = folderPosition
	}
	date := consistentValue(audioFiles, func(file scanner.File) string {
		return file.Metadata.Date
	})

	sourceFileIDs := make([]uuid.UUID, 0, len(audioFiles)+len(covers))
	fileRoles := make(map[uuid.UUID]string, len(audioFiles)+len(covers))
	for _, file := range audioFiles {
		sourceFileIDs = append(sourceFileIDs, file.ID)
		fileRoles[file.ID] = catalog.FileRoleAudio
	}
	for _, file := range covers {
		sourceFileIDs = append(sourceFileIDs, file.ID)
		fileRoles[file.ID] = catalog.FileRoleCover
	}
	sort.Slice(sourceFileIDs, func(i, j int) bool {
		return sourceFileIDs[i].String() < sourceFileIDs[j].String()
	})

	metadataSource := catalog.MetadataInferred
	confidence := 0.4
	if primary.Metadata.Title != "" || primary.Metadata.Author != "" {
		metadataSource = catalog.MetadataLocal
		confidence = 0.75
	}
	book := catalog.InferredBook{
		RootID:           folder.RootID,
		SourceFolder:     folder.RelativePath,
		GroupingKey:      groupingKey,
		Title:            title,
		Authors:          stringSlice(author),
		Narrators:        stringSlice(narrator),
		Series:           series,
		SeriesPosition:   seriesPosition,
		Genres:           stringSlice(genre),
		ProviderSubjects: []string{},
		Identifiers:      map[string]any{},
		MetadataSource:   metadataSource,
		Confidence:       confidence,
		ReviewState:      reviewState,
		SourceFileIDs:    sourceFileIDs,
		FileRoles:        fileRoles,
		PublishedDate:    date,
	}
	if match := yearPrefix.FindStringSubmatch(strings.TrimSpace(date)); len(match) == 2 {
		if year, err := strconv.Atoi(match[1]); err == nil {
			book.PublicationYear = &year
		}
	}
	return book
}

func parseFolderSeries(folderName string) (series, position, title string) {
	match := folderSeriesPattern.FindStringSubmatch(strings.TrimSpace(folderName))
	if len(match) != 4 {
		return "", "", ""
	}
	return strings.TrimSpace(match[1]), strings.TrimSpace(match[2]), strings.TrimSpace(match[3])
}

func consistentValue(files []scanner.File, value func(scanner.File) string) string {
	var selected string
	var normalized string
	for _, file := range files {
		current := strings.TrimSpace(value(file))
		if current == "" {
			continue
		}
		currentNormalized := normalize(current)
		if selected == "" {
			selected = current
			normalized = currentNormalized
			continue
		}
		if currentNormalized != normalized {
			return ""
		}
	}
	return selected
}

func filterKind(files []scanner.File, kind scanner.FileKind) []scanner.File {
	result := make([]scanner.File, 0, len(files))
	for _, file := range files {
		if file.Kind == kind {
			result = append(result, file)
		}
	}
	return result
}

func stringSlice(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	return []string{strings.TrimSpace(value)}
}

func folderGroupingKey(folder scanner.Folder) string {
	return stableGroupingKey("folder", folder.RootID, folder.RelativePath)
}

func splitGroupingKey(folder scanner.Folder, audio scanner.File) string {
	return stableGroupingKey(
		"m4b",
		folder.RootID,
		folder.RelativePath,
		audio.RelativePath,
	)
}

func stableGroupingKey(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%s-%x", parts[0], digest[:12])
}

func normalize(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	spacePending := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			if spacePending && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteRune(character)
			spacePending = false
			continue
		}
		spacePending = true
	}
	return strings.TrimSpace(editionSuffix.ReplaceAllString(builder.String(), ""))
}

func normalizeComparableTitle(value string) string {
	value = numberedUnit.ReplaceAllString(value, " ")
	value = leadingTrackNumber.ReplaceAllString(value, "")
	return normalize(value)
}

func hasProbeFailure(files []scanner.File) bool {
	for _, file := range files {
		if file.ProbeError != "" {
			return true
		}
	}
	return false
}
