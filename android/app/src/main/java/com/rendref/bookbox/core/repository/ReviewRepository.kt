package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.model.AcceptedJobDto
import com.rendref.bookbox.core.model.BookDetailDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.GroupingContextDto
import com.rendref.bookbox.core.model.GroupingFileDto
import com.rendref.bookbox.core.model.MatchSearchDto
import com.rendref.bookbox.core.model.ReorganizationPreviewDto
import com.rendref.bookbox.core.network.ReviewApi
import com.rendref.bookbox.core.network.ReviewGroupingRequest

data class GroupingDecision(
    val bookId: String?,
    val fileIds: List<String>,
    val companionFileIds: Collection<String> = emptyList(),
    val groupingKey: String = bookId.orEmpty(),
)

interface ReviewSource {
    suspend fun bookDetail(bookId: String): BookDetailDto

    suspend fun bookFiles(bookId: String): BookFilesDto

    suspend fun groupingContext(bookId: String): GroupingContextDto

    suspend fun searchMatches(
        bookId: String,
        title: String,
        author: String,
        isbn: String = "",
    ): MatchSearchDto

    suspend fun confirmMatch(bookId: String, matchId: String): AcceptedJobDto

    suspend fun saveGrouping(bookId: String, groups: List<GroupingDecision>) {
        throw UnsupportedOperationException(
            "Grouping save is not implemented for $bookId (${groups.size} groups)",
        )
    }

    suspend fun saveGrouping(
        bookId: String,
        groups: List<GroupingDecision>,
        expectedFiles: List<GroupingFileDto>,
    ): Long {
        saveGrouping(bookId, groups)
        return 1
    }

    suspend fun previewReorganization(
        bookId: String,
        catalogGeneration: Long,
        groups: List<GroupingDecision>,
        targetFolders: Map<String, String>,
    ): ReorganizationPreviewDto {
        throw UnsupportedOperationException("Reorganization preview is not implemented")
    }

    suspend fun confirmReorganization(
        bookId: String,
        operationId: String,
    ): AcceptedJobDto {
        throw UnsupportedOperationException("Reorganization confirmation is not implemented")
    }
}

class ReviewRepository(
    private val api: ReviewApi,
) : ReviewSource {
    override suspend fun bookDetail(bookId: String) = api.bookDetail(bookId)

    override suspend fun bookFiles(bookId: String) = api.bookFiles(bookId)

    override suspend fun groupingContext(bookId: String) = api.groupingContext(bookId)

    override suspend fun searchMatches(
        bookId: String,
        title: String,
        author: String,
        isbn: String,
    ) = api.searchMatches(bookId, title, author, isbn)

    override suspend fun confirmMatch(bookId: String, matchId: String) =
        api.confirmMatch(bookId, matchId)

    override suspend fun saveGrouping(
        bookId: String,
        groups: List<GroupingDecision>,
    ) = api.saveGrouping(
        bookId,
        groups.map { it.toRequest() },
    )

    override suspend fun saveGrouping(
        bookId: String,
        groups: List<GroupingDecision>,
        expectedFiles: List<GroupingFileDto>,
    ) = api.saveGrouping(
        bookId,
        groups.map { it.toRequest() },
        expectedFiles,
    )

    override suspend fun previewReorganization(
        bookId: String,
        catalogGeneration: Long,
        groups: List<GroupingDecision>,
        targetFolders: Map<String, String>,
    ) = api.previewReorganization(
        bookId,
        catalogGeneration,
        groups.map { it.toRequest(includeGroupingKey = true) },
        targetFolders,
    )

    override suspend fun confirmReorganization(
        bookId: String,
        operationId: String,
    ) = api.confirmReorganization(bookId, operationId)
}

private fun GroupingDecision.toRequest(includeGroupingKey: Boolean = false) =
    ReviewGroupingRequest(
        bookId = bookId,
        groupingKey = groupingKey.takeIf { includeGroupingKey },
        fileIds = fileIds + companionFileIds,
    )
