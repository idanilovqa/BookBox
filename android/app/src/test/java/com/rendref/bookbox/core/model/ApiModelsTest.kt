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
    fun `book card decodes approved server contract and ignores unknown fields`() {
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
