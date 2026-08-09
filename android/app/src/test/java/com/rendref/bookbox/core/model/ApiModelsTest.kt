package com.rendref.bookbox.core.model

import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.MissingFieldException
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

@OptIn(ExperimentalSerializationApi::class)
class ApiModelsTest {
    private val json = Json {
        ignoreUnknownKeys = true
    }

    @Test
    fun `book card decodes BookBox server contract and ignores unknown fields`() {
        val card = json.decodeFromString<BookCardDto>(
            """
            {
              "id": "b1",
              "title": "Dune",
              "authors": ["Frank Herbert"],
              "publicationYear": 1965,
              "genres": ["Science Fiction"],
              "reviewState": "confirmed",
              "sourceFolder": "Dune",
              "futureServerField": "ignored"
            }
            """.trimIndent(),
        )

        assertEquals("b1", card.id)
        assertEquals("Dune", card.title)
        assertEquals(1965, card.publicationYear)
        assertEquals(listOf("Frank Herbert"), card.authors)
        assertEquals(listOf("Science Fiction"), card.genres)
        assertNull(card.series)
        assertEquals(emptyList<String>(), card.narrators)
        assertNull(card.coverUrl)
        assertNull(card.durationMs)
        assertNull(card.reviewReason)
    }

    @Test
    fun `shared audiobook search contracts decode snapshots groups and durations`() {
        val snapshot = json.decodeFromString<SearchSnapshotDto>(
            """{
              "rootId":"audiobooks",
              "scanRunId":"00000000-0000-4000-8000-000000000001",
              "completedAt":"2026-08-08T12:00:00Z",
              "catalogGeneration":7
            }""",
        )
        val group = json.decodeFromString<BookSearchGroupDto>(
            """
            {
              "groupId":"00000000-0000-4000-8000-000000000002",
              "duplicate":true,
              "books":[
                {
                  "id":"00000000-0000-4000-8000-000000000003",
                  "rootId":"audiobooks",
                  "title":"Dune",
                  "authors":["Frank Herbert"],
                  "reviewState":"needs_review",
                  "reviewReason":"probe_failed",
                  "sourceFolder":"Dune/Edition One",
                  "durationMs":3600000
                },
                {
                  "id":"00000000-0000-4000-8000-000000000004",
                  "rootId":"archive",
                  "title":"Dune",
                  "authors":["Frank Herbert"],
                  "reviewState":"confirmed",
                  "sourceFolder":"Dune/Edition Two"
                }
              ]
            }
            """.trimIndent(),
        )

        assertEquals("audiobooks", snapshot.rootId)
        assertEquals("00000000-0000-4000-8000-000000000001", snapshot.scanRunId)
        assertEquals("2026-08-08T12:00:00Z", snapshot.completedAt)
        assertEquals(7L, snapshot.catalogGeneration)
        assertEquals("00000000-0000-4000-8000-000000000002", group.groupId)
        assertEquals(true, group.duplicate)
        assertEquals(3_600_000L, group.books.first().durationMs)
        assertEquals("audiobooks", group.books.first().rootId)
        assertEquals("probe_failed", group.books.first().reviewReason)
        assertNull(group.books.last().durationMs)
    }

    @Test
    fun `raw search page decodes folders files duplicate identity and cursor`() {
        val page = json.decodeFromString<RawSearchPageDto>(
            """
            {
              "rootId":"audiobooks",
              "query":"dune",
              "snapshot":{
                "rootId":"audiobooks",
                "scanRunId":"00000000-0000-4000-8000-000000000001",
                "completedAt":"2026-08-08T12:00:00Z",
                "catalogGeneration":1
              },
              "entries":[
                {
                  "id":"00000000-0000-4000-8000-000000000002",
                  "name":"Dune",
                  "relativePath":"Frank Herbert/Dune",
                  "kind":"folder",
                  "duplicateGroupId":"00000000-0000-4000-8000-000000000003",
                  "duplicateBook":true
                },
                {
                  "id":"00000000-0000-4000-8000-000000000004",
                  "name":"Dune.m4b",
                  "relativePath":"Frank Herbert/Dune/Dune.m4b",
                  "kind":"file",
                  "size":42,
                  "modifiedAt":"2026-08-08T11:00:00Z",
                  "bookId":"00000000-0000-4000-8000-000000000005"
                }
              ],
              "nextCursor":"next-page"
            }
            """.trimIndent(),
        )

        assertEquals("audiobooks", page.rootId)
        assertEquals("dune", page.query)
        assertEquals("audiobooks", page.snapshot.rootId)
        assertEquals("folder", page.entries.first().kind)
        assertNull(page.entries.first().size)
        assertNull(page.entries.first().modifiedAt)
        assertEquals(true, page.entries.first().duplicateBook)
        assertEquals(42L, page.entries.last().size)
        assertEquals("00000000-0000-4000-8000-000000000005", page.entries.last().bookId)
        assertEquals(false, page.entries.last().duplicateBook)
        assertEquals("next-page", page.nextCursor)
    }

    @Test
    fun `raw search page defaults optional cursor and entry metadata`() {
        val page = json.decodeFromString<RawSearchPageDto>(
            """
            {
              "rootId":"audiobooks",
              "query":"dune",
              "snapshot":{
                "rootId":"audiobooks",
                "scanRunId":"00000000-0000-4000-8000-000000000001",
                "completedAt":"2026-08-08T12:00:00Z",
                "catalogGeneration":1
              },
              "entries":[{
                "id":"00000000-0000-4000-8000-000000000002",
                "name":"Dune",
                "relativePath":"Dune",
                "kind":"folder"
              }]
            }
            """.trimIndent(),
        )

        assertNull(page.nextCursor)
        assertNull(page.entries.single().bookId)
        assertNull(page.entries.single().duplicateGroupId)
        assertEquals(false, page.entries.single().duplicateBook)
    }

    @Test
    fun `book detail decodes metadata and optional defaults`() {
        val detail = json.decodeFromString<BookDetailDto>(
            """
            {
              "id": "b1",
              "rootId": "audiobooks",
              "title": "Dune",
              "authors": ["Frank Herbert"],
              "publicationYear": 1965,
              "genres": ["Science Fiction"],
              "publisher": "Chilton Books",
              "identifiers": {"isbn13": "9780441172719"},
              "reviewState": "confirmed",
              "sourceFolder": "Dune"
            }
            """.trimIndent(),
        )

        assertEquals("audiobooks", detail.rootId)
        assertEquals("Chilton Books", detail.publisher)
        assertEquals("9780441172719", detail.identifiers["isbn13"])
        assertNull(detail.subtitle)
        assertNull(detail.seriesPosition)
        assertEquals(emptyList<String>(), detail.narrators)
        assertEquals(emptyList<String>(), detail.providerSubjects)
        assertNull(detail.attribution)
    }

    @Test
    fun `root and browse contracts decode folders and files`() {
        val roots = json.decodeFromString<RootsDto>(
            """{"roots":[{"id":"audiobooks","name":"Audiobooks"}]}""",
        )
        val browse = json.decodeFromString<BrowseDto>(
            """
            {
              "rootId": "audiobooks",
              "path": "Frank Herbert",
              "entries": [
                {
                  "name": "Dune",
                  "relativePath": "Frank Herbert/Dune",
                  "kind": "folder",
                  "modifiedAt": "2026-07-26T12:00:00Z"
                },
                {
                  "name": "chapter01.mp3",
                  "relativePath": "Frank Herbert/chapter01.mp3",
                  "kind": "file",
                  "size": 42,
                  "modifiedAt": "2026-07-26T12:00:00Z"
                }
              ]
            }
            """.trimIndent(),
        )

        assertEquals(RootDto("audiobooks", "Audiobooks"), roots.roots.single())
        assertEquals("Dune", browse.entries.first().name)
        assertNull(browse.entries.first().size)
        assertEquals(42L, browse.entries.last().size)
    }

    @Test
    fun `manifest contract decodes every verified file field`() {
        val manifest = json.decodeFromString<ManifestDto>(
            """
            {
              "id": "m1",
              "files": [{
                "fileId": "signed-id",
                "sourcePath": "Dune/chapter01.mp3",
                "destinationPath": "Dune/chapter01.mp3",
                "size": 42,
                "modifiedAt": "2026-07-26T12:00:00Z",
                "etag": "\"abc\"",
                "sha256": "abc"
              }],
              "totalBytes": 42
            }
            """.trimIndent(),
        )

        assertEquals("m1", manifest.id)
        assertEquals(42L, manifest.totalBytes)
        assertEquals("\"abc\"", manifest.files.single().etag)
        assertEquals("abc", manifest.files.single().sha256)
    }

    @Test
    fun `api error contract keeps stable nested code and message`() {
        val response = json.decodeFromString<ApiErrorDto>(
            """
            {
              "error": {
                "code": "source_changed",
                "message": "source changed",
                "unknownDetail": 1
              }
            }
            """.trimIndent(),
        )

        assertEquals("source_changed", response.error.code)
        assertEquals("source changed", response.error.message)
    }

    @Test(expected = MissingFieldException::class)
    fun `required card fields do not silently default`() {
        json.decodeFromString<BookCardDto>(
            """
            {
              "id": "b1",
              "authors": [],
              "reviewState": "needs_review",
              "sourceFolder": "Unknown"
            }
            """.trimIndent(),
        )
    }
}
