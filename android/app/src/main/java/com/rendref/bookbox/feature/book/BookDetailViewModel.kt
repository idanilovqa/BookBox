package com.rendref.bookbox.feature.book

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.model.BookDetailDto
import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.GroupingFileDto
import com.rendref.bookbox.core.model.MetadataCandidateDto
import com.rendref.bookbox.core.model.ReorganizationPreviewDto
import com.rendref.bookbox.core.repository.GroupingDecision
import com.rendref.bookbox.core.repository.ReviewSource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

enum class ReviewPane {
    DETAIL,
    MATCHES,
    GROUPING,
}

data class GroupingDraft(
    val localId: Int,
    val bookId: String?,
    val fileIds: Set<String>,
    val title: String? = null,
    val companionFileIds: Set<String> = emptySet(),
    val groupingKey: String = bookId.orEmpty().ifEmpty { "group-$localId" },
    val targetFolder: String = "",
) {
    val id: Int get() = localId
}

data class BookReviewState(
    val pane: ReviewPane = ReviewPane.DETAIL,
    val detail: BookDetailDto? = null,
    val files: List<BookFileDto> = emptyList(),
    val groupingFiles: List<GroupingFileDto> = emptyList(),
    val groupingScopePath: String = "",
    val groupingReviewReason: String? = null,
    val groupingEvidence: Map<String, List<String>> = emptyMap(),
    val canReorganize: Boolean = false,
    val catalogGeneration: Long = 0,
    val reorganizationPreview: ReorganizationPreviewDto? = null,
    val pendingOperationId: String? = null,
    val matches: List<MetadataCandidateDto> = emptyList(),
    val searchTitle: String = "",
    val searchAuthor: String = "",
    val searchIsbn: String = "",
    val pendingMatchId: String? = null,
    val groups: List<GroupingDraft> = emptyList(),
    val canSaveGrouping: Boolean = false,
    val isLoading: Boolean = true,
    val isSaving: Boolean = false,
    val message: String? = null,
    val errorMessage: String? = null,
)

class BookDetailViewModel(
    private val bookId: String,
    private val reviews: ReviewSource,
) : ViewModel() {
    private val mutableState = MutableStateFlow(BookReviewState())
    val state: StateFlow<BookReviewState> = mutableState.asStateFlow()
    private var nextGroupId = 1
    private var searchGeneration = 0
    private var searchJob: Job? = null
    private var groupingGeneration = 0
    private var groupingJob: Job? = null

    init {
        viewModelScope.launch {
            try {
                val detail = reviews.bookDetail(bookId)
                val files = reviews.bookFiles(bookId).files
                mutableState.value = mutableState.value.copy(
                    detail = detail,
                    files = files,
                    searchTitle = detail.title,
                    searchAuthor = detail.authors.firstOrNull().orEmpty(),
                    searchIsbn = detail.identifiers["isbn"].orEmpty(),
                    isLoading = false,
                )
                if (detail.reviewState == "needs_review") {
                    openGrouping()
                }
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    errorMessage = "Unable to load audiobook details",
                )
            }
        }
    }

    fun showDetail() {
        cancelSearch()
        cancelGrouping()
        mutableState.value = mutableState.value.copy(
            pane = ReviewPane.DETAIL,
            pendingMatchId = null,
            isLoading = false,
        )
    }

    fun openMatchPicker() {
        val detail = mutableState.value.detail ?: return
        cancelGrouping()
        mutableState.value = mutableState.value.copy(
            pane = ReviewPane.MATCHES,
            searchTitle = detail.title,
            searchAuthor = detail.authors.firstOrNull().orEmpty(),
            searchIsbn = detail.identifiers["isbn"].orEmpty(),
            pendingMatchId = null,
        )
        searchMatches()
    }

    fun updateMatchSearch(title: String, author: String, isbn: String) {
        mutableState.value = mutableState.value.copy(
            searchTitle = title,
            searchAuthor = author,
            searchIsbn = isbn,
        )
    }

    fun searchMatches() {
        if (mutableState.value.pane != ReviewPane.MATCHES) return
        val snapshot = mutableState.value
        val generation = ++searchGeneration
        searchJob?.cancel()
        mutableState.value = snapshot.copy(
            isLoading = true,
            errorMessage = null,
            pendingMatchId = null,
        )
        searchJob = viewModelScope.launch {
            try {
                val result = reviews.searchMatches(
                    bookId,
                    snapshot.searchTitle,
                    snapshot.searchAuthor,
                    snapshot.searchIsbn,
                )
                if (
                    generation != searchGeneration ||
                    mutableState.value.pane != ReviewPane.MATCHES
                ) {
                    return@launch
                }
                mutableState.value = mutableState.value.copy(
                    matches = result.candidates,
                    isLoading = false,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                if (
                    generation != searchGeneration ||
                    mutableState.value.pane != ReviewPane.MATCHES
                ) {
                    return@launch
                }
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    errorMessage = "Unable to search metadata providers",
                )
            }
        }
    }

    fun requestMatchConfirmation(matchId: String) {
        if (mutableState.value.matches.none { it.matchId == matchId }) return
        mutableState.value = mutableState.value.copy(pendingMatchId = matchId)
    }

    fun cancelMatchConfirmation() {
        mutableState.value = mutableState.value.copy(pendingMatchId = null)
    }

    fun confirmCoverPublication() {
        val matchId = mutableState.value.pendingMatchId ?: return
        mutableState.value = mutableState.value.copy(isSaving = true, errorMessage = null)
        viewModelScope.launch {
            try {
                reviews.confirmMatch(bookId, matchId)
                mutableState.value = mutableState.value.copy(
                    pendingMatchId = null,
                    isSaving = false,
                    message = "Metadata confirmed. Cover publication was queued.",
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    errorMessage = "Unable to confirm this metadata match",
                )
            }
        }
    }

    fun openGrouping() {
        cancelSearch()
        cancelGrouping()
        val generation = ++groupingGeneration
        mutableState.value = mutableState.value.copy(
            pane = ReviewPane.GROUPING,
            groupingFiles = emptyList(),
            groupingScopePath = "",
            groupingReviewReason = null,
            groupingEvidence = emptyMap(),
            canReorganize = false,
            catalogGeneration = 0,
            reorganizationPreview = null,
            pendingOperationId = null,
            groups = emptyList(),
            canSaveGrouping = false,
            isLoading = true,
            errorMessage = null,
        )
        groupingJob = viewModelScope.launch {
            try {
                val context = reviews.groupingContext(bookId)
                val filesById = context.files.associateBy { it.id }
                val sharedCompanions = context.files
                    .filter { it.shared }
                    .mapTo(mutableSetOf()) { it.id }
                val groups = context.books.mapIndexed { index, book ->
                    val audioIds = book.fileIds.filterTo(mutableSetOf()) {
                        filesById[it]?.role == "audio"
                    }
                    val companionIds = book.fileIds.filterTo(mutableSetOf()) {
                        filesById[it]?.role != "audio"
                    }
                    GroupingDraft(
                        localId = index + 1,
                        bookId = book.bookId,
                        fileIds = audioIds,
                        title = book.title,
                        companionFileIds = companionIds + sharedCompanions,
                        groupingKey = book.groupingKey,
                    )
                }.toMutableList()
                if (groups.size == 1) {
                    groups += GroupingDraft(
                        2,
                        null,
                        emptySet(),
                        companionFileIds = sharedCompanions,
                        groupingKey = "new-2",
                    )
                }
                nextGroupId = (groups.maxOfOrNull { it.id } ?: 0) + 1
                if (
                    generation != groupingGeneration ||
                    mutableState.value.pane != ReviewPane.GROUPING
                ) {
                    return@launch
                }
                mutableState.value = mutableState.value.copy(
                    groupingFiles = context.files,
                    groupingScopePath = context.scopePath,
                    groupingReviewReason = context.reviewReason,
                    groupingEvidence = context.evidence,
                    canReorganize = context.canReorganize,
                    catalogGeneration = context.catalogGeneration,
                    groups = groups,
                    canSaveGrouping = validGrouping(groups, context.files),
                    isLoading = false,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                if (
                    generation != groupingGeneration ||
                    mutableState.value.pane != ReviewPane.GROUPING
                ) {
                    return@launch
                }
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    errorMessage = "Unable to load grouping context",
                )
            }
        }
    }

    fun addGroup() {
        if (mutableState.value.groups.size >= MAX_GROUPS) return
        val groupId = nextGroupId++
        updateGroups(
            mutableState.value.groups + GroupingDraft(
                groupId,
                null,
                emptySet(),
                companionFileIds = companionFiles()
                    .filter { it.shared }
                    .mapTo(mutableSetOf()) { it.id },
                groupingKey = "new-$groupId",
            ),
        )
    }

    fun assignFileToGroup(fileId: String, groupId: Int) {
        if (audioFiles().none { it.id == fileId }) return
        if (mutableState.value.groups.none { it.id == groupId }) return
        updateGroups(
            mutableState.value.groups.map { group ->
                val without = group.fileIds - fileId
                if (group.id == groupId) {
                    group.copy(fileIds = without + fileId)
                } else {
                    group.copy(fileIds = without)
                }
            },
        )
    }

    fun mergeGroups(sourceId: Int, targetId: Int) {
        if (sourceId == targetId) return
        val groups = mutableState.value.groups
        val source = groups.firstOrNull { it.id == sourceId } ?: return
        if (groups.none { it.id == targetId }) return
        updateGroups(
            groups.filterNot { it.id == sourceId }.map { group ->
                if (group.id == targetId) {
                    group.copy(
                        fileIds = group.fileIds + source.fileIds,
                        companionFileIds = group.companionFileIds + source.companionFileIds,
                    )
                } else {
                    group
                }
            },
        )
    }

    fun toggleCompanionForGroup(fileId: String, groupId: Int) {
        val file = companionFiles().firstOrNull { it.id == fileId } ?: return
        val target = mutableState.value.groups.firstOrNull { it.id == groupId } ?: return
        val removing = fileId in target.companionFileIds
        updateGroups(
            mutableState.value.groups.map { group ->
                when {
                    group.id == groupId && removing -> group.copy(
                        companionFileIds = group.companionFileIds - fileId,
                    )
                    group.id == groupId -> group.copy(
                        companionFileIds = group.companionFileIds + fileId,
                    )
                    !file.shared -> group.copy(
                        companionFileIds = group.companionFileIds - fileId,
                    )
                    else -> group
                }
            },
        )
    }

    fun saveGrouping() {
        val groups = mutableState.value.groups
        val expectedFiles = mutableState.value.groupingFiles
        if (!validGrouping(groups, expectedFiles)) return
        mutableState.value = mutableState.value.copy(isSaving = true, errorMessage = null)
        viewModelScope.launch {
            try {
                val generation = reviews.saveGrouping(
                    bookId,
                    groups.map {
                        GroupingDecision(
                            it.bookId,
                            it.fileIds.sorted(),
                            it.companionFileIds.toSortedSet(),
                            it.groupingKey,
                        )
                    },
                    expectedFiles,
                )
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    message = "Grouping saved in the catalog.",
                    catalogGeneration = generation,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    errorMessage = "Unable to save grouping",
                )
            }
        }
    }

    fun updateTargetFolder(groupId: Int, targetFolder: String) {
        if (mutableState.value.groups.none { it.id == groupId }) return
        updateGroups(
            mutableState.value.groups.map { group ->
                if (group.id == groupId) group.copy(targetFolder = targetFolder) else group
            },
        )
    }

    fun previewReorganization() {
        val snapshot = mutableState.value
        if (
            !snapshot.canReorganize ||
            snapshot.catalogGeneration <= 0 ||
            !validGrouping(snapshot.groups, snapshot.groupingFiles) ||
            snapshot.groups.any { !validReorganizationTargetFolder(it.targetFolder) } ||
            snapshot.groups.map { it.groupingKey }.distinct().size != snapshot.groups.size ||
            snapshot.isSaving
        ) {
            return
        }
        mutableState.value = snapshot.copy(
            isSaving = true,
            errorMessage = null,
            reorganizationPreview = null,
            pendingOperationId = null,
        )
        viewModelScope.launch {
            try {
                val preview = reviews.previewReorganization(
                    bookId,
                    snapshot.catalogGeneration,
                    snapshot.groups.map { group ->
                        GroupingDecision(
                            group.bookId,
                            group.fileIds.sorted(),
                            group.companionFileIds.toSortedSet(),
                            group.groupingKey,
                        )
                    },
                    snapshot.groups.associate {
                        it.groupingKey to normalizeReorganizationTargetFolder(it.targetFolder)
                    },
                )
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    reorganizationPreview = preview,
                    pendingOperationId = preview.operationId,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    errorMessage = "Unable to preview file reorganization",
                )
            }
        }
    }

    fun confirmReorganization() {
        val snapshot = mutableState.value
        val operationId = snapshot.pendingOperationId ?: return
        if (
            snapshot.isSaving ||
            snapshot.reorganizationPreview?.operationId != operationId
        ) {
            return
        }
        mutableState.value = snapshot.copy(isSaving = true, errorMessage = null)
        viewModelScope.launch {
            try {
                reviews.confirmReorganization(bookId, operationId)
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    reorganizationPreview = null,
                    pendingOperationId = null,
                    message = "File reorganization queued",
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isSaving = false,
                    errorMessage = "Unable to queue file reorganization",
                )
            }
        }
    }

    fun cancelReorganization() {
        if (mutableState.value.isSaving) return
        mutableState.value = mutableState.value.copy(
            reorganizationPreview = null,
            pendingOperationId = null,
        )
    }

    private fun updateGroups(groups: List<GroupingDraft>) {
        mutableState.value = mutableState.value.copy(
            groups = groups,
            canSaveGrouping = validGrouping(groups, mutableState.value.groupingFiles),
            reorganizationPreview = null,
            pendingOperationId = null,
        )
    }

    private fun cancelSearch() {
        searchGeneration += 1
        searchJob?.cancel()
        searchJob = null
    }

    private fun cancelGrouping() {
        groupingGeneration += 1
        groupingJob?.cancel()
        groupingJob = null
    }

    private fun audioFiles(): List<BookFileDto> =
        mutableState.value.groupingFiles.filter { it.role == "audio" }

    private fun companionFiles(): List<GroupingFileDto> =
        mutableState.value.groupingFiles.filter { it.role != "audio" }
}

internal fun validReorganizationTargetFolder(value: String): Boolean {
    val normalized = normalizeReorganizationTargetFolder(value)
    if (
        normalized.isEmpty() ||
        normalized.startsWith('/') ||
        WINDOWS_ABSOLUTE.containsMatchIn(normalized) ||
        normalized.any(Char::isISOControl) ||
        normalized.any { it in INVALID_TARGET_CHARACTERS }
    ) {
        return false
    }
    return normalized.split('/').none {
        it.isBlank() || it == "." || it == ".." || it.endsWith('.')
    }
}

private fun normalizeReorganizationTargetFolder(value: String): String =
    value.trim().replace('\\', '/')

private fun validGrouping(
    groups: List<GroupingDraft>,
    files: List<GroupingFileDto>,
): Boolean {
    if (groups.size < 2 || groups.any { it.fileIds.isEmpty() }) return false
    val assigned = groups.flatMap { it.fileIds }
    val expected = files.filter { it.role == "audio" }.map { it.id }
    if (assigned.size != assigned.distinct().size || assigned.toSet() != expected.toSet()) {
        return false
    }
    return files.filter { it.role != "audio" }.all { file ->
        val count = groups.count { file.id in it.companionFileIds }
        if (file.shared) count >= 1 else count == 1
    }
}
private const val MAX_GROUPS = 100
private val WINDOWS_ABSOLUTE = Regex("^[A-Za-z]:/")
private val INVALID_TARGET_CHARACTERS = setOf('<', '>', ':', '"', '|', '?', '*')
