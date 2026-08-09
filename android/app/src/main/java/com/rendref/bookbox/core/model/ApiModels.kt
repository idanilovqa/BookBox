package com.rendref.bookbox.core.model

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

val ApiJson = Json {
    ignoreUnknownKeys = true
}

@Serializable
data class RootDto(
    val id: String,
    val name: String,
)

@Serializable
data class RootsDto(
    val roots: List<RootDto>,
)

@Serializable
data class BrowseEntryDto(
    val name: String,
    val relativePath: String,
    val kind: String,
    val size: Long? = null,
    val modifiedAt: String,
)

@Serializable
data class BrowseDto(
    val rootId: String,
    val path: String,
    val entries: List<BrowseEntryDto>,
)

@Serializable
data class SearchSnapshotDto(
    val rootId: String,
    val scanRunId: String,
    val completedAt: String,
    val catalogGeneration: Long = 0,
)

@Serializable
data class BookSearchGroupDto(
    val groupId: String,
    val duplicate: Boolean,
    val books: List<BookCardDto>,
)

@Serializable
data class RawSearchEntryDto(
    val id: String,
    val name: String,
    val relativePath: String,
    val kind: String,
    val size: Long? = null,
    val modifiedAt: String? = null,
    val bookId: String? = null,
    val duplicateGroupId: String? = null,
    val duplicateBook: Boolean = false,
)

@Serializable
data class RawSearchPageDto(
    val rootId: String,
    val query: String,
    val snapshot: SearchSnapshotDto,
    val entries: List<RawSearchEntryDto>,
    val nextCursor: String? = null,
)

@Serializable
data class BookFileDto(
    val id: String,
    val relativePath: String,
    val name: String,
    val size: Long,
    val modifiedAt: String,
)

@Serializable
data class BookFilesDto(
    val bookId: String,
    val rootId: String,
    val sourceFolder: String,
    val files: List<BookFileDto>,
)

@Serializable
data class GroupingContextBookDto(
    val bookId: String,
    val title: String,
    val fileIds: List<String>,
)

@Serializable
data class GroupingContextDto(
    val bookId: String,
    val rootId: String,
    val sourceFolder: String,
    val files: List<BookFileDto>,
    val books: List<GroupingContextBookDto>,
)

@Serializable
data class BookCardDto(
    val id: String,
    val title: String,
    val authors: List<String>,
    val publicationYear: Int? = null,
    val genres: List<String> = emptyList(),
    val series: String? = null,
    val narrators: List<String> = emptyList(),
    val coverUrl: String? = null,
    val reviewState: String,
    val reviewReason: String? = null,
    val sourceFolder: String,
    val rootId: String = "",
    val durationMs: Long? = null,
)

@Serializable
data class MetadataAttributionDto(
    val provider: String,
    val displayName: String? = null,
    val sourceUrl: String? = null,
)

@Serializable
data class BookDetailDto(
    val id: String,
    val rootId: String,
    val title: String,
    val authors: List<String>,
    val reviewState: String,
    val sourceFolder: String,
    val subtitle: String? = null,
    val narrators: List<String> = emptyList(),
    val series: String? = null,
    val seriesPosition: String? = null,
    val publisher: String? = null,
    val publishedDate: String? = null,
    val publicationYear: Int? = null,
    val genres: List<String> = emptyList(),
    val providerSubjects: List<String> = emptyList(),
    val identifiers: Map<String, String> = emptyMap(),
    val coverUrl: String? = null,
    val metadataSource: String? = null,
    val confidence: Double? = null,
    val attribution: MetadataAttributionDto? = null,
)

@Serializable
data class MetadataCandidateDto(
    val matchId: String,
    val provider: String,
    val providerId: String,
    val title: String,
    val authors: List<String>,
    val subtitle: String? = null,
    val publisher: String? = null,
    val publishedDate: String? = null,
    val genres: List<String> = emptyList(),
    val subjects: List<String> = emptyList(),
    val series: String? = null,
    val seriesPosition: String? = null,
    val isbn: String? = null,
    val coverUrl: String? = null,
    val score: Double,
)

@Serializable
data class MatchSearchDto(
    val bookId: String,
    val candidates: List<MetadataCandidateDto>,
)

@Serializable
data class AcceptedJobDto(
    val jobId: String,
    val status: String,
)

@Serializable
data class ManifestFileDto(
    val fileId: String,
    val sourcePath: String,
    val destinationPath: String,
    val size: Long,
    val modifiedAt: String,
    val etag: String,
    val sha256: String,
)

@Serializable
data class ManifestDto(
    val id: String,
    val files: List<ManifestFileDto>,
    val totalBytes: Long,
)

@Serializable
data class ApiErrorDetailDto(
    val code: String,
    val message: String,
)

@Serializable
data class ApiErrorDto(
    val error: ApiErrorDetailDto,
)
