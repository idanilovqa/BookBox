package com.rendref.netstorage.core.model

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
    val sourceFolder: String,
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
