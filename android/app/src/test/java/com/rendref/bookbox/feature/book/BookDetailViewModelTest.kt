package com.rendref.bookbox.feature.book

import com.rendref.bookbox.core.model.AcceptedJobDto
import com.rendref.bookbox.core.model.BookDetailDto
import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.GroupingContextBookDto
import com.rendref.bookbox.core.model.GroupingContextDto
import com.rendref.bookbox.core.model.GroupingFileDto
import com.rendref.bookbox.core.model.MatchSearchDto
import com.rendref.bookbox.core.model.MetadataCandidateDto
import com.rendref.bookbox.core.model.ReorganizationMoveDto
import com.rendref.bookbox.core.model.ReorganizationPreviewDto
import com.rendref.bookbox.core.repository.GroupingDecision
import com.rendref.bookbox.core.repository.ReviewSource
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class BookDetailViewModelTest {
    private val dispatcher = StandardTestDispatcher()

    @Before
    fun setUp() = Dispatchers.setMain(dispatcher)

    @After
    fun tearDown() = Dispatchers.resetMain()

    @Test
    fun `details load and match picker searches current title and author on open`() =
        runTest(dispatcher) {
            val source = FakeReviewSource()
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()

            assertEquals("Dune", viewModel.state.value.detail?.title)
            assertEquals(2, viewModel.state.value.files.size)

            viewModel.openMatchPicker()
            advanceUntilIdle()

            assertEquals(ReviewPane.MATCHES, viewModel.state.value.pane)
            assertEquals(listOf(Triple(BOOK_ID, "Dune", "Frank Herbert")), source.searches)
            assertEquals(MATCH_ID, viewModel.state.value.matches.single().matchId)
        }

    @Test
    fun `match PUT is impossible until cover publication confirmation`() =
        runTest(dispatcher) {
            val source = FakeReviewSource()
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()
            viewModel.openMatchPicker()
            advanceUntilIdle()

            viewModel.requestMatchConfirmation(MATCH_ID)
            advanceUntilIdle()
            assertTrue(source.confirmedMatches.isEmpty())
            assertEquals(MATCH_ID, viewModel.state.value.pendingMatchId)

            viewModel.confirmCoverPublication()
            advanceUntilIdle()
            assertEquals(listOf(BOOK_ID to MATCH_ID), source.confirmedMatches)
            assertEquals(null, viewModel.state.value.pendingMatchId)
        }

    @Test
    fun `older match search cannot replace a newer completed search`() =
        runTest(dispatcher) {
            val first = CompletableDeferred<MatchSearchDto>()
            val second = CompletableDeferred<MatchSearchDto>()
            val source = FakeReviewSource(ArrayDeque(listOf(first, second)))
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()

            viewModel.openMatchPicker()
            runCurrent()
            viewModel.updateMatchSearch("Dune Messiah", "Frank Herbert", "")
            viewModel.searchMatches()
            runCurrent()

            second.complete(matchSearch(NEW_MATCH_ID, "Dune Messiah"))
            runCurrent()
            assertEquals(
                "Dune Messiah",
                viewModel.state.value.matches.single().title,
            )

            first.complete(matchSearch(MATCH_ID, "Dune"))
            advanceUntilIdle()
            assertEquals(
                "Dune Messiah",
                viewModel.state.value.matches.single().title,
            )
        }

    @Test
    fun `grouping requires two nonempty groups and every audio file exactly once`() =
        runTest(dispatcher) {
            val source = FakeReviewSource()
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()
            viewModel.openGrouping()
            advanceUntilIdle()

            val groups = viewModel.state.value.groups
            assertEquals(2, groups.size)
            assertFalse(viewModel.state.value.canSaveGrouping)

            viewModel.assignFileToGroup(FILE_TWO_ID, groups[1].id)
            assertTrue(viewModel.state.value.canSaveGrouping)
            viewModel.saveGrouping()
            advanceUntilIdle()

            assertEquals(1, source.savedGroupings.size)
            assertEquals(
                setOf(FILE_ONE_ID, FILE_TWO_ID),
                source.savedGroupings.single()
                    .flatMap(GroupingDecision::fileIds)
                    .toSet(),
            )
        }

    @Test
    fun `grouping loads sibling assignments and leaves loose folder audio assignable`() =
        runTest(dispatcher) {
            val context = GroupingContextDto(
                bookId = BOOK_ID,
                rootId = "audiobooks",
                sourceFolder = "Frank Herbert/Dune",
                files = listOf(
                    testBookFile(FILE_ONE_ID, "part-1.m4b"),
                    testBookFile(FILE_TWO_ID, "part-2.m4b"),
                    testBookFile(LOOSE_FILE_ID, "bonus.flac"),
                ),
                books = listOf(
                    GroupingContextBookDto(
                        BOOK_ID,
                        "Dune",
                        listOf(FILE_ONE_ID),
                    ),
                    GroupingContextBookDto(
                        SIBLING_BOOK_ID,
                        "Dune Messiah",
                        listOf(FILE_TWO_ID),
                    ),
                ),
            )
            val source = FakeReviewSource(groupingContext = context)
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()

            viewModel.openGrouping()
            advanceUntilIdle()

            assertEquals(listOf(BOOK_ID), source.groupingRequests)
            assertEquals(2, viewModel.state.value.files.size)
            assertEquals(3, viewModel.state.value.groupingFiles.size)
            assertEquals(
                listOf(BOOK_ID, SIBLING_BOOK_ID),
                viewModel.state.value.groups.map { it.bookId },
            )
            assertEquals(
                listOf("Dune", "Dune Messiah"),
                viewModel.state.value.groups.map { it.title },
            )
            assertTrue(
                viewModel.state.value.groups.none {
                    LOOSE_FILE_ID in it.fileIds
                },
            )
            assertFalse(viewModel.state.value.canSaveGrouping)
        }

    @Test
    fun `needs review automatically opens expanded grouping and confirms returned generation`() =
        runTest(dispatcher) {
            val context = expandedGroupingContext()
            val source = FakeReviewSource(
                detail = BookDetailDto(
                    id = BOOK_ID,
                    rootId = "audiobooks",
                    title = "Dune",
                    authors = listOf("Frank Herbert"),
                    reviewState = "needs_review",
                    reviewReason = "probe_failed",
                    sourceFolder = "Omnibus",
                ),
                groupingContext = context,
                savedGeneration = 8,
            )

            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()

            assertEquals(ReviewPane.GROUPING, viewModel.state.value.pane)
            assertEquals(listOf(BOOK_ID), source.groupingRequests)
            assertEquals("Omnibus", viewModel.state.value.groupingScopePath)
            assertEquals("probe_failed", viewModel.state.value.groupingReviewReason)
            assertEquals(
                listOf("Omnibus/book.cue"),
                viewModel.state.value.groupingEvidence["sidecar"],
            )
            assertFalse(viewModel.state.value.canReorganize)
            assertEquals(
                listOf(
                    "Omnibus/Disc 1/First.m4b",
                    "Omnibus/Disc 2/Second.flac",
                    "Omnibus/book.cue",
                    "Omnibus/cover.jpg",
                ),
                viewModel.state.value.groupingFiles.map { it.relativePath },
            )

            val (first, second) = viewModel.state.value.groups
            assertEquals(setOf(FILE_ONE_ID), first.fileIds)
            assertEquals(setOf(COMPANION_ID, COVER_ID), first.companionFileIds)
            assertEquals(setOf(FILE_TWO_ID), second.fileIds)
            assertEquals(setOf(COVER_ID), second.companionFileIds)

            viewModel.assignFileToGroup(FILE_TWO_ID, first.id)
            assertEquals(setOf(FILE_ONE_ID, FILE_TWO_ID), viewModel.state.value.groups[0].fileIds)
            assertTrue(viewModel.state.value.groups[1].fileIds.isEmpty())
            viewModel.assignFileToGroup(FILE_TWO_ID, second.id)
            viewModel.toggleCompanionForGroup(COMPANION_ID, second.id)
            assertFalse(COMPANION_ID in viewModel.state.value.groups[0].companionFileIds)
            assertTrue(COMPANION_ID in viewModel.state.value.groups[1].companionFileIds)
            assertTrue(viewModel.state.value.groups.all { COVER_ID in it.companionFileIds })
            assertTrue(viewModel.state.value.canSaveGrouping)

            viewModel.saveGrouping()
            advanceUntilIdle()

            assertEquals(8L, viewModel.state.value.catalogGeneration)
            assertEquals(context.files, source.savedExpectedFiles.single())
            assertEquals(
                setOf(COVER_ID),
                source.savedGroupings.single()[0].companionFileIds,
            )
            assertEquals(
                setOf(COMPANION_ID, COVER_ID),
                source.savedGroupings.single()[1].companionFileIds,
            )
        }

    @Test
    fun `reorganization preview requires capability generation valid grouping and safe targets`() =
        runTest(dispatcher) {
            val source = FakeReviewSource(groupingContext = reorganizableGroupingContext())
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()
            viewModel.openGrouping()
            advanceUntilIdle()

            val (first, second) = viewModel.state.value.groups
            viewModel.previewReorganization()
            advanceUntilIdle()
            assertTrue(source.previewRequests.isEmpty())

            viewModel.updateTargetFolder(first.id, "Frank Herbert/First")
            viewModel.updateTargetFolder(second.id, "../Outside")
            viewModel.previewReorganization()
            advanceUntilIdle()
            assertTrue(source.previewRequests.isEmpty())

            viewModel.updateTargetFolder(second.id, "Frank Herbert/Second")
            viewModel.previewReorganization()
            advanceUntilIdle()

            val request = source.previewRequests.single()
            assertEquals(8L, request.catalogGeneration)
            assertEquals(
                listOf("book-one", "book-two"),
                request.groups.map(GroupingDecision::groupingKey),
            )
            assertEquals(
                mapOf(
                    "book-one" to "Frank Herbert/First",
                    "book-two" to "Frank Herbert/Second",
                ),
                request.targetFolders,
            )
            assertEquals(OPERATION_ID, viewModel.state.value.pendingOperationId)
            assertEquals(OPERATION_ID, viewModel.state.value.reorganizationPreview?.operationId)
        }

    @Test
    fun `reorganization confirmation prevents double submission and remains under review`() =
        runTest(dispatcher) {
            val confirmation = CompletableDeferred<AcceptedJobDto>()
            val source = FakeReviewSource(
                groupingContext = reorganizableGroupingContext(),
                detail = defaultBookDetail().copy(
                    reviewState = "needs_review",
                    reviewReason = "probe_failed",
                ),
                queuedReorganizationConfirmation = confirmation,
            )
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()
            viewModel.state.value.groups.forEachIndexed { index, group ->
                viewModel.updateTargetFolder(group.id, "Frank Herbert/Book ${index + 1}")
            }
            viewModel.previewReorganization()
            advanceUntilIdle()

            viewModel.confirmReorganization()
            viewModel.confirmReorganization()
            runCurrent()
            assertEquals(listOf(BOOK_ID to OPERATION_ID), source.confirmedReorganizations)
            assertTrue(viewModel.state.value.isSaving)

            confirmation.complete(AcceptedJobDto(JOB_ID, "pending"))
            advanceUntilIdle()
            assertEquals("File reorganization queued", viewModel.state.value.message)
            assertEquals(ReviewPane.GROUPING, viewModel.state.value.pane)
            assertEquals("needs_review", viewModel.state.value.detail?.reviewState)

            viewModel.cancelReorganization()
            assertEquals(null, viewModel.state.value.pendingOperationId)
            assertEquals(null, viewModel.state.value.reorganizationPreview)
        }

    @Test
    fun `catalog only grouping remains available when reorganization is denied`() =
        runTest(dispatcher) {
            val source = FakeReviewSource(groupingContext = expandedGroupingContext())
            val viewModel = BookDetailViewModel(BOOK_ID, source)
            advanceUntilIdle()
            viewModel.openGrouping()
            advanceUntilIdle()

            viewModel.previewReorganization()
            advanceUntilIdle()
            assertTrue(source.previewRequests.isEmpty())

            viewModel.saveGrouping()
            advanceUntilIdle()
            assertEquals(1, source.savedGroupings.size)
        }

    @Test
    fun `pane switches ignore deferred results from the previous review operation`() =
        runTest(dispatcher) {
            val firstSearch = CompletableDeferred<MatchSearchDto>()
            val firstGrouping = CompletableDeferred<GroupingContextDto>()
            val matchesViewModel = BookDetailViewModel(
                BOOK_ID,
                FakeReviewSource(
                    queuedSearches = ArrayDeque(listOf(firstSearch)),
                    queuedGroupingContext = firstGrouping,
                ),
            )
            advanceUntilIdle()
            matchesViewModel.openGrouping()
            runCurrent()
            matchesViewModel.openMatchPicker()
            runCurrent()

            firstGrouping.complete(defaultGroupingContext())
            runCurrent()
            assertEquals(ReviewPane.MATCHES, matchesViewModel.state.value.pane)
            assertTrue(matchesViewModel.state.value.isLoading)

            firstSearch.complete(matchSearch(MATCH_ID, "Dune"))
            advanceUntilIdle()
            assertFalse(matchesViewModel.state.value.isLoading)

            val secondSearch = CompletableDeferred<MatchSearchDto>()
            val secondGrouping = CompletableDeferred<GroupingContextDto>()
            val groupingViewModel = BookDetailViewModel(
                BOOK_ID,
                FakeReviewSource(
                    queuedSearches = ArrayDeque(listOf(secondSearch)),
                    queuedGroupingContext = secondGrouping,
                ),
            )
            advanceUntilIdle()
            groupingViewModel.openMatchPicker()
            runCurrent()
            groupingViewModel.openGrouping()
            runCurrent()

            secondSearch.complete(matchSearch(MATCH_ID, "Dune"))
            runCurrent()
            assertEquals(ReviewPane.GROUPING, groupingViewModel.state.value.pane)
            assertTrue(groupingViewModel.state.value.isLoading)

            secondGrouping.complete(defaultGroupingContext())
            advanceUntilIdle()
            assertFalse(groupingViewModel.state.value.isLoading)
        }
}

private class FakeReviewSource(
    private val queuedSearches: ArrayDeque<CompletableDeferred<MatchSearchDto>>? = null,
    private val groupingContext: GroupingContextDto = defaultGroupingContext(),
    private val queuedGroupingContext: CompletableDeferred<GroupingContextDto>? = null,
    private val detail: BookDetailDto = defaultBookDetail(),
    private val savedGeneration: Long = 1,
    private val reorganizationPreview: ReorganizationPreviewDto = testReorganizationPreview(),
    private val queuedReorganizationConfirmation: CompletableDeferred<AcceptedJobDto>? = null,
) : ReviewSource {
    val searches = mutableListOf<Triple<String, String, String>>()
    val groupingRequests = mutableListOf<String>()
    val confirmedMatches = mutableListOf<Pair<String, String>>()
    val savedGroupings = mutableListOf<List<GroupingDecision>>()
    val savedExpectedFiles = mutableListOf<List<GroupingFileDto>>()
    val previewRequests = mutableListOf<ReorganizationRequestCapture>()
    val confirmedReorganizations = mutableListOf<Pair<String, String>>()

    override suspend fun bookDetail(bookId: String) = detail.copy(id = bookId)

    override suspend fun bookFiles(bookId: String) = BookFilesDto(
        bookId = bookId,
        rootId = "audiobooks",
        sourceFolder = "Frank Herbert/Dune",
        files = listOf(
            testBookFile(FILE_ONE_ID, "part-1.m4b"),
            testBookFile(FILE_TWO_ID, "part-2.m4b"),
        ),
    )

    override suspend fun groupingContext(bookId: String): GroupingContextDto {
        groupingRequests += bookId
        return queuedGroupingContext?.await() ?: groupingContext
    }

    override suspend fun searchMatches(
        bookId: String,
        title: String,
        author: String,
        isbn: String,
    ): MatchSearchDto {
        searches += Triple(bookId, title, author)
        return queuedSearches?.removeFirst()?.await()
            ?: matchSearch(MATCH_ID, "Dune")
    }

    override suspend fun confirmMatch(bookId: String, matchId: String): AcceptedJobDto {
        confirmedMatches += bookId to matchId
        return AcceptedJobDto(JOB_ID, "pending")
    }

    override suspend fun saveGrouping(
        bookId: String,
        groups: List<GroupingDecision>,
        expectedFiles: List<GroupingFileDto>,
    ): Long {
        savedGroupings += groups
        savedExpectedFiles += expectedFiles
        return savedGeneration
    }

    override suspend fun previewReorganization(
        bookId: String,
        catalogGeneration: Long,
        groups: List<GroupingDecision>,
        targetFolders: Map<String, String>,
    ): ReorganizationPreviewDto {
        previewRequests += ReorganizationRequestCapture(
            bookId,
            catalogGeneration,
            groups,
            targetFolders,
        )
        return reorganizationPreview
    }

    override suspend fun confirmReorganization(
        bookId: String,
        operationId: String,
    ): AcceptedJobDto {
        confirmedReorganizations += bookId to operationId
        return queuedReorganizationConfirmation?.await()
            ?: AcceptedJobDto(JOB_ID, "pending")
    }

}

private data class ReorganizationRequestCapture(
    val bookId: String,
    val catalogGeneration: Long,
    val groups: List<GroupingDecision>,
    val targetFolders: Map<String, String>,
)

private fun defaultBookDetail() = BookDetailDto(
    id = BOOK_ID,
    rootId = "audiobooks",
    title = "Dune",
    authors = listOf("Frank Herbert"),
    reviewState = "confirmed",
    sourceFolder = "Frank Herbert/Dune",
)

private fun expandedGroupingContext() = GroupingContextDto(
    bookId = BOOK_ID,
    rootId = "audiobooks",
    scopePath = "Omnibus",
    reviewReason = "probe_failed",
    evidence = mapOf(
        "audio" to listOf("Omnibus/Disc 1/First.m4b"),
        "sidecar" to listOf("Omnibus/book.cue"),
    ),
    canReorganize = false,
    files = listOf(
        groupingFile(FILE_ONE_ID, "Omnibus/Disc 1/First.m4b", "audio"),
        groupingFile(FILE_TWO_ID, "Omnibus/Disc 2/Second.flac", "audio"),
        groupingFile(
            COMPANION_ID,
            "Omnibus/book.cue",
            role = "sidecar",
            companionKind = "cue",
        ),
        groupingFile(COVER_ID, "Omnibus/cover.jpg", role = "cover", shared = true),
    ),
    books = listOf(
        GroupingContextBookDto(
            BOOK_ID,
            "First",
            listOf(FILE_ONE_ID, COMPANION_ID, COVER_ID),
        ),
        GroupingContextBookDto(
            SIBLING_BOOK_ID,
            "Second",
            listOf(FILE_TWO_ID, COVER_ID),
        ),
    ),
)

private fun reorganizableGroupingContext() = expandedGroupingContext().copy(
    catalogGeneration = 8,
    canReorganize = true,
    books = listOf(
        GroupingContextBookDto(
            BOOK_ID,
            "First",
            listOf(FILE_ONE_ID, COMPANION_ID, COVER_ID),
            groupingKey = "book-one",
        ),
        GroupingContextBookDto(
            SIBLING_BOOK_ID,
            "Second",
            listOf(FILE_TWO_ID, COVER_ID),
            groupingKey = "book-two",
        ),
    ),
)

private fun testReorganizationPreview() = ReorganizationPreviewDto(
    operationId = OPERATION_ID,
    rootId = "audiobooks",
    catalogGeneration = 8,
    moves = listOf(
        ReorganizationMoveDto(
            order = 1,
            fileId = FILE_ONE_ID,
            sourcePath = "Omnibus/Disc 1/First.m4b",
            destinationPath = "Frank Herbert/First/First.m4b",
            role = "audio",
            expectedSize = 100,
            expectedModifiedAt = "2026-07-26T00:00:00Z",
        ),
        ReorganizationMoveDto(
            order = 2,
            fileId = COMPANION_ID,
            sourcePath = "Omnibus/book.cue",
            destinationPath = "Frank Herbert/First/book.cue",
            role = "sidecar",
            expectedSize = 100,
            expectedModifiedAt = "2026-07-26T00:00:00Z",
        ),
    ),
)

private fun groupingFile(
    id: String,
    relativePath: String,
    role: String,
    companionKind: String = "",
    shared: Boolean = false,
) = GroupingFileDto(
    id = id,
    relativePath = relativePath,
    name = relativePath.substringAfterLast('/'),
    size = 100,
    modifiedAt = "2026-07-26T00:00:00Z",
    role = role,
    companionKind = companionKind,
    shared = shared,
)

private fun defaultGroupingContext() = GroupingContextDto(
    bookId = BOOK_ID,
    rootId = "audiobooks",
    sourceFolder = "Frank Herbert/Dune",
    files = listOf(
        testBookFile(FILE_ONE_ID, "part-1.m4b"),
        testBookFile(FILE_TWO_ID, "part-2.m4b"),
    ),
    books = listOf(
        GroupingContextBookDto(
            BOOK_ID,
            "Dune",
            listOf(FILE_ONE_ID, FILE_TWO_ID),
        ),
    ),
)

private fun testBookFile(id: String, name: String) = BookFileDto(
    id = id,
    relativePath = "Frank Herbert/Dune/$name",
    name = name,
    size = 100,
    modifiedAt = "2026-07-26T00:00:00Z",
)

private fun matchSearch(matchId: String, title: String) = MatchSearchDto(
    BOOK_ID,
    listOf(
        MetadataCandidateDto(
            matchId = matchId,
            provider = "open_library",
            providerId = "OL1",
            title = title,
            authors = listOf("Frank Herbert"),
            publishedDate = "1965",
            subjects = listOf("Science Fiction"),
            score = 0.96,
        ),
    ),
)

private const val BOOK_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
private const val MATCH_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
private const val NEW_MATCH_ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
private const val JOB_ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
private const val FILE_ONE_ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
private const val FILE_TWO_ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
private const val LOOSE_FILE_ID = "12121212-1212-4121-8121-121212121212"
private const val SIBLING_BOOK_ID = "13131313-1313-4131-8131-131313131313"
private const val COMPANION_ID = "14141414-1414-4141-8141-141414141414"
private const val COVER_ID = "15151515-1515-4151-8151-151515151515"
private const val OPERATION_ID = "16161616-1616-4161-8161-161616161616"
