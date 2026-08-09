package grouping

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
)

// Tree is the persisted, read-only input to subtree grouping. Companion data
// must already have been parsed into SourceFile.Media before inference begins.
type Tree struct {
	RootID      string
	ScopePath   string
	Files       []catalog.SourceFile
	Corrections []catalog.GroupingCorrection
}

type Result struct {
	Books    []catalog.InferredBook
	Evidence map[string]catalog.GroupingEvidence
}

type treeIdentity struct {
	title  string
	author string
}

type treeCluster struct {
	identity treeIdentity
	audio    []catalog.SourceFile
	reason   string
	review   string
	locked   bool
	key      string
}

// InferTree conservatively groups every active audio file exactly once. User
// corrections take precedence; automatic inference never expands a locked
// assignment.
func InferTree(tree Tree) Result {
	result := Result{Evidence: make(map[string]catalog.GroupingEvidence)}
	files := activeTreeFiles(tree.Files)
	audio := filesOfKind(files, catalog.FileKindAudio)
	if len(audio) == 0 {
		return result
	}

	byID := make(map[uuid.UUID]catalog.SourceFile, len(files))
	assigned := make(map[uuid.UUID]bool, len(audio))
	for _, file := range files {
		byID[file.ID] = file
	}

	clusters := make([]treeCluster, 0)
	corrections := append([]catalog.GroupingCorrection(nil), tree.Corrections...)
	sort.Slice(corrections, func(i, j int) bool {
		if corrections[i].UpdatedAt.Equal(corrections[j].UpdatedAt) {
			return corrections[i].ID.String() < corrections[j].ID.String()
		}
		return corrections[i].UpdatedAt.Before(corrections[j].UpdatedAt)
	})
	for _, correction := range corrections {
		groups := append([]catalog.GroupingGroup(nil), correction.Groups...)
		sort.Slice(groups, func(i, j int) bool { return groups[i].GroupingKey < groups[j].GroupingKey })
		for _, group := range groups {
			cluster := treeCluster{reason: "manual", review: catalog.ReviewConfirmed, locked: true, key: group.GroupingKey}
			for _, fileID := range group.FileIDs {
				file, ok := byID[fileID]
				if !ok || file.FileKind != catalog.FileKindAudio || assigned[fileID] {
					continue
				}
				assigned[fileID] = true
				cluster.audio = append(cluster.audio, file)
			}
			if len(cluster.audio) > 0 {
				cluster.identity = consistentTreeIdentity(cluster.audio)
				clusters = append(clusters, cluster)
			}
		}
	}

	remaining := make([]catalog.SourceFile, 0, len(audio))
	for _, file := range audio {
		if !assigned[file.ID] {
			remaining = append(remaining, file)
		}
	}
	if len(remaining) > 0 {
		if len(corrections) > 0 {
			clusters = append(clusters, treeCluster{
				audio: remaining, reason: "weak_loose_files", review: catalog.ReviewNeedsReview,
			})
		} else {
			clusters = append(clusters, inferAutomaticTreeClusters(tree, remaining, files)...)
		}
	}

	sharedCovers := filesOfKind(files, catalog.FileKindCover)
	for index := range clusters {
		cluster := &clusters[index]
		sort.Slice(cluster.audio, func(i, j int) bool { return cluster.audio[i].RelativePath < cluster.audio[j].RelativePath })
		if cluster.key == "" {
			cluster.key = treeGroupingKey(tree, *cluster)
		}
		companions := companionsForCluster(files, cluster.identity, cluster.audio)
		book := inferredTreeBook(tree, *cluster, sharedCovers, companions)
		result.Books = append(result.Books, book)
		result.Evidence[book.GroupingKey] = treeGroupingEvidence(book, cluster.reason, byID)
	}
	sort.Slice(result.Books, func(i, j int) bool {
		left, right := result.Books[i], result.Books[j]
		if normalize(left.Title) != normalize(right.Title) {
			return normalize(left.Title) < normalize(right.Title)
		}
		return left.GroupingKey < right.GroupingKey
	})
	return result
}

func inferAutomaticTreeClusters(tree Tree, audio, allFiles []catalog.SourceFile) []treeCluster {
	for _, file := range audio {
		if file.ProbeStatus == catalog.ProbeFailed || strings.TrimSpace(file.ProbeError) != "" {
			return []treeCluster{{audio: audio, reason: "probe_failed", review: catalog.ReviewNeedsReview}}
		}
	}

	identities := make([]treeIdentity, len(audio))
	for index, file := range audio {
		identities[index] = identityForTreeFile(file)
		if identities[index].title == "" {
			identities[index] = companionIdentityForAudio(file, allFiles)
		}
	}
	if crossTitleConflict(audio, identities) {
		return []treeCluster{{audio: audio, reason: "conflicting_title", review: catalog.ReviewNeedsReview}}
	}
	if authorConflict(identities) {
		return []treeCluster{{audio: audio, identity: consistentTreeIdentity(audio), reason: "conflicting_author", review: catalog.ReviewNeedsReview}}
	}

	groups := make(map[string][]catalog.SourceFile)
	identityByKey := make(map[string]treeIdentity)
	for index, file := range audio {
		identity := identities[index]
		if identity.title == "" {
			continue
		}
		key := identity.title + "\x00" + identity.author
		groups[key] = append(groups[key], file)
		identityByKey[key] = identity
	}
	if len(groups) > 1 && groupedFileCount(groups) == len(audio) {
		return clustersFromIdentityGroups(groups, identityByKey)
	}

	filenameGroups := make(map[string][]catalog.SourceFile)
	for _, file := range audio {
		stem := normalizedTreeFilename(file.RelativePath)
		if stem != "" {
			filenameGroups[stem] = append(filenameGroups[stem], file)
		}
	}
	if len(filenameGroups) > 1 && groupedFileCount(filenameGroups) == len(audio) {
		clusters := make([]treeCluster, 0, len(filenameGroups))
		for stem, grouped := range filenameGroups {
			clusters = append(clusters, treeCluster{
				identity: treeIdentity{title: stem},
				audio:    grouped, reason: "distinct_identity", review: catalog.ReviewUnreviewed,
			})
		}
		sortTreeClusters(clusters)
		return clusters
	}

	identity := consistentTreeIdentity(audio)
	reason := "folder_default"
	review := catalog.ReviewUnreviewed
	if tree.ScopePath == "" || identity.title == "" {
		reason, review = "weak_loose_files", catalog.ReviewNeedsReview
	} else if hasDiscSubtree(tree.ScopePath, audio) {
		reason = "consistent_subtree"
	}
	return []treeCluster{{identity: identity, audio: audio, reason: reason, review: review}}
}

func clustersFromIdentityGroups(groups map[string][]catalog.SourceFile, identities map[string]treeIdentity) []treeCluster {
	clusters := make([]treeCluster, 0, len(groups))
	for key, audio := range groups {
		clusters = append(clusters, treeCluster{
			identity: identities[key], audio: audio,
			reason: "distinct_identity", review: catalog.ReviewUnreviewed,
		})
	}
	sortTreeClusters(clusters)
	return clusters
}

func inferredTreeBook(tree Tree, cluster treeCluster, covers, companions []catalog.SourceFile) catalog.InferredBook {
	identity := cluster.identity
	if identity.title == "" {
		identity = consistentTreeIdentity(cluster.audio)
	}
	title := treeDisplayValue(cluster.audio, "title")
	if title == "" {
		title = companionDisplayValue(companions, "title", "album")
	}
	if title == "" && identity.title != "" {
		title = displayTreeStem(cluster.audio[0].RelativePath, identity.title)
	}
	if title == "" {
		title = path.Base(strings.TrimSuffix(tree.ScopePath, "/"))
		if title == "." || title == "" {
			title = displayTreeStem(cluster.audio[0].RelativePath, normalizedTreeFilename(cluster.audio[0].RelativePath))
		}
	}
	author := treeDisplayValue(cluster.audio, "author")
	if author == "" {
		author = companionDisplayValue(companions, "author", "authors")
	}

	book := catalog.InferredBook{
		RootID: tree.RootID, SourceFolder: tree.ScopePath, GroupingKey: cluster.key,
		GroupingLocked: cluster.locked, Title: title, Authors: stringSlice(author),
		Narrators:        stringSlice(treeDisplayValue(cluster.audio, "narrator")),
		Genres:           stringSlice(treeDisplayValue(cluster.audio, "genre")),
		ProviderSubjects: []string{}, Identifiers: map[string]any{},
		MetadataSource: catalog.MetadataInferred, Confidence: treeConfidence(cluster.reason),
		ReviewState: cluster.review, ReviewReason: cluster.reason,
		FileRoles: make(map[uuid.UUID]string),
	}
	if treeDisplayValue(cluster.audio, "title") != "" || author != "" {
		book.MetadataSource = catalog.MetadataLocal
	}
	for _, file := range cluster.audio {
		book.SourceFileIDs = append(book.SourceFileIDs, file.ID)
		book.FileRoles[file.ID] = catalog.FileRoleAudio
	}
	for _, file := range covers {
		book.SourceFileIDs = append(book.SourceFileIDs, file.ID)
		book.FileRoles[file.ID] = catalog.FileRoleCover
	}
	for _, file := range companions {
		book.SourceFileIDs = append(book.SourceFileIDs, file.ID)
		book.FileRoles[file.ID] = catalog.FileRoleSidecar
	}
	sort.Slice(book.SourceFileIDs, func(i, j int) bool { return book.SourceFileIDs[i].String() < book.SourceFileIDs[j].String() })
	return book
}

func treeGroupingEvidence(book catalog.InferredBook, reason string, byID map[uuid.UUID]catalog.SourceFile) catalog.GroupingEvidence {
	values := map[string][]string{
		catalog.FileRoleAudio: {}, catalog.FileRoleCover: {}, catalog.FileRoleSidecar: {},
	}
	for _, fileID := range book.SourceFileIDs {
		file, ok := byID[fileID]
		if ok {
			values[book.FileRoles[fileID]] = append(values[book.FileRoles[fileID]], file.RelativePath)
		}
	}
	for role := range values {
		sort.Strings(values[role])
	}
	return catalog.GroupingEvidence{ReasonCode: reason, Confidence: treeConfidence(reason), ReviewState: book.ReviewState, Values: values}
}

func companionsForCluster(files []catalog.SourceFile, identity treeIdentity, audio []catalog.SourceFile) []catalog.SourceFile {
	result := make([]catalog.SourceFile, 0)
	for _, file := range files {
		if file.FileKind != catalog.FileKindCompanion {
			continue
		}
		companionIdentity := identityForCompanion(file)
		if referencesAudio(file, audio) ||
			(companionIdentity.title != "" && companionIdentity.title == identity.title &&
				(companionIdentity.author == "" || identity.author == "" || companionIdentity.author == identity.author)) ||
			(file.CompanionKind == catalog.CompanionEbook && normalizedTreeFilename(file.RelativePath) == identity.title) {
			result = append(result, file)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result
}

func companionIdentityForAudio(audio catalog.SourceFile, files []catalog.SourceFile) treeIdentity {
	filename := normalizedTreeFilename(audio.RelativePath)
	for _, file := range files {
		if file.FileKind != catalog.FileKindCompanion {
			continue
		}
		identity := identityForCompanion(file)
		if referencesAudio(file, []catalog.SourceFile{audio}) {
			if identity.title != "" {
				return identity
			}
		}
		if identity.title != "" && identity.title == filename {
			return identity
		}
		if file.CompanionKind == catalog.CompanionEbook && normalizedTreeFilename(file.RelativePath) == filename {
			return treeIdentity{title: filename}
		}
	}
	return treeIdentity{title: filename}
}

func identityForTreeFile(file catalog.SourceFile) treeIdentity {
	return treeIdentity{title: normalizeComparableTitle(mediaString(file.Media, "album", "title")), author: normalize(mediaString(file.Media, "author", "authors"))}
}

func identityForCompanion(file catalog.SourceFile) treeIdentity {
	return treeIdentity{title: normalizeComparableTitle(mediaString(file.Media, "title", "album")), author: normalize(mediaString(file.Media, "author", "authors"))}
}

func consistentTreeIdentity(files []catalog.SourceFile) treeIdentity {
	var selected treeIdentity
	for _, file := range files {
		identity := identityForTreeFile(file)
		if selected.title == "" {
			selected.title = identity.title
		} else if identity.title != "" && identity.title != selected.title {
			selected.title = ""
		}
		if selected.author == "" {
			selected.author = identity.author
		} else if identity.author != "" && identity.author != selected.author {
			selected.author = ""
		}
	}
	return selected
}

func crossTitleConflict(audio []catalog.SourceFile, identities []treeIdentity) bool {
	for index, file := range audio {
		filename := normalizedTreeFilename(file.RelativePath)
		if filename == "" || identities[index].title == "" || filename == identities[index].title {
			continue
		}
		for other := range identities {
			if other != index && filename == identities[other].title {
				return true
			}
		}
	}
	return false
}

func authorConflict(identities []treeIdentity) bool {
	authorsByTitle := make(map[string]map[string]struct{})
	for _, identity := range identities {
		if identity.title == "" || identity.author == "" {
			continue
		}
		if authorsByTitle[identity.title] == nil {
			authorsByTitle[identity.title] = make(map[string]struct{})
		}
		authorsByTitle[identity.title][identity.author] = struct{}{}
		if len(authorsByTitle[identity.title]) > 1 {
			return true
		}
	}
	return false
}

func activeTreeFiles(files []catalog.SourceFile) []catalog.SourceFile {
	result := make([]catalog.SourceFile, 0, len(files))
	for _, file := range files {
		if file.MissingAt == nil {
			result = append(result, file)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result
}

func filesOfKind(files []catalog.SourceFile, kind string) []catalog.SourceFile {
	result := make([]catalog.SourceFile, 0)
	for _, file := range files {
		if file.FileKind == kind {
			result = append(result, file)
		}
	}
	return result
}

func referencesAudio(companion catalog.SourceFile, audio []catalog.SourceFile) bool {
	references := mediaStrings(companion.Media, "references")
	for _, reference := range references {
		base := strings.ToLower(path.Base(strings.ReplaceAll(reference, "\\", "/")))
		for _, file := range audio {
			if base == strings.ToLower(path.Base(file.RelativePath)) {
				return true
			}
		}
	}
	return false
}

func mediaString(media map[string]any, keys ...string) string {
	for _, key := range keys {
		values := mediaStrings(media, key)
		if len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func mediaStrings(media map[string]any, key string) []string {
	if media == nil {
		return nil
	}
	switch value := media[key].(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			return []string{value}
		}
	case []string:
		return value
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

func normalizedTreeFilename(relativePath string) string {
	name := strings.TrimSuffix(path.Base(relativePath), path.Ext(relativePath))
	name = numberedUnit.ReplaceAllString(name, " ")
	name = leadingTrackNumber.ReplaceAllString(name, "")
	fields := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' })
	for len(fields) > 0 {
		last := strings.ToLower(fields[len(fields)-1])
		allDigits := true
		for _, character := range last {
			if character < '0' || character > '9' {
				allDigits = false
				break
			}
		}
		if allDigits || last == "chapter" || last == "track" || last == "part" {
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	return normalizeComparableTitle(strings.Join(fields, " "))
}

func displayTreeStem(relativePath, normalized string) string {
	name := strings.TrimSuffix(path.Base(relativePath), path.Ext(relativePath))
	fields := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for len(fields) > 0 && normalizeComparableTitle(strings.Join(fields, " ")) != normalized {
		fields = fields[:len(fields)-1]
	}
	if len(fields) > 0 {
		return strings.TrimSpace(strings.Join(fields, " "))
	}
	return strings.TrimSpace(name)
}

func treeDisplayValue(files []catalog.SourceFile, keys ...string) string {
	var selected, normalized string
	for _, file := range files {
		current := mediaString(file.Media, keys...)
		if current == "" {
			continue
		}
		currentNormalized := normalize(current)
		if selected == "" {
			selected, normalized = current, currentNormalized
		} else if currentNormalized != normalized {
			return ""
		}
	}
	return selected
}

func companionDisplayValue(files []catalog.SourceFile, keys ...string) string {
	for _, file := range files {
		if value := mediaString(file.Media, keys...); value != "" {
			return value
		}
	}
	return ""
}

func hasDiscSubtree(scope string, audio []catalog.SourceFile) bool {
	for _, file := range audio {
		relative := strings.TrimPrefix(file.RelativePath, strings.TrimSuffix(scope, "/")+"/")
		parts := strings.Split(relative, "/")
		if len(parts) > 1 {
			folder := normalize(parts[0])
			if strings.HasPrefix(folder, "disc ") || strings.HasPrefix(folder, "disk ") || strings.HasPrefix(folder, "cd ") {
				return true
			}
		}
	}
	return false
}

func treeGroupingKey(tree Tree, cluster treeCluster) string {
	parts := []string{"tree", tree.RootID, tree.ScopePath, cluster.reason, cluster.identity.title, cluster.identity.author}
	for _, file := range cluster.audio {
		parts = append(parts, file.ID.String())
	}
	return stableGroupingKey(parts...)
}

func treeConfidence(reason string) float64 {
	switch reason {
	case "manual":
		return 1
	case "distinct_identity":
		return .85
	case "consistent_subtree":
		return .8
	case "folder_default":
		return .7
	case "conflicting_title", "conflicting_author", "probe_failed":
		return .25
	default:
		return .4
	}
}

func groupedFileCount(groups map[string][]catalog.SourceFile) int {
	total := 0
	for _, files := range groups {
		total += len(files)
	}
	return total
}

func sortTreeClusters(clusters []treeCluster) {
	sort.Slice(clusters, func(i, j int) bool {
		left := fmt.Sprintf("%s\x00%s\x00%s", clusters[i].identity.title, clusters[i].identity.author, clusters[i].audio[0].RelativePath)
		right := fmt.Sprintf("%s\x00%s\x00%s", clusters[j].identity.title, clusters[j].identity.author, clusters[j].audio[0].RelativePath)
		return left < right
	})
}
