package com.rendref.bookbox.core.search

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class DebouncedSearchTest {
    private val dispatcher = StandardTestDispatcher()

    @Test
    fun `active queries wait 150 milliseconds and only load the latest query`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val loaded = mutableListOf<String>()
            val updates = mutableListOf<SearchUpdate<String, List<String>>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    text = { it },
                    load = {
                        loaded += it
                        listOf(it)
                    },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("du")
            runCurrent()
            assertTrue(updates.last() is SearchUpdate.Loading)
            advanceTimeBy(149)
            runCurrent()
            assertTrue(loaded.isEmpty())

            queries.emit("dune")
            advanceTimeBy(149)
            runCurrent()
            assertTrue(loaded.isEmpty())

            advanceTimeBy(1)
            runCurrent()
            assertEquals(listOf("dune"), loaded)
            assertTrue(updates.last() is SearchUpdate.Loaded)
            job.cancel()
        }

    @Test
    fun `raw mode suppresses short text but counts supplementary Unicode code points`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val loaded = mutableListOf<String>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    allowBlank = false,
                    text = { it },
                    load = {
                        loaded += it
                        it
                    },
                ).collect {}
            }
            runCurrent()

            queries.emit("")
            queries.emit("a")
            queries.emit(" ... ")
            runCurrent()
            assertTrue(loaded.isEmpty())

            val twoDeseretLetters = "\uD801\uDC00\uD801\uDC01"
            queries.emit(twoDeseretLetters)
            advanceTimeBy(150)
            runCurrent()
            assertEquals(listOf(twoDeseretLetters), loaded)
            job.cancel()
        }

    @Test
    fun `custom eligibility can restrict but cannot bypass the text threshold`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val loaded = mutableListOf<String>()
            val updates = mutableListOf<SearchUpdate<String, String>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    allowBlank = false,
                    text = { it },
                    eligible = { true },
                    load = { loaded += it; it },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("x")
            runCurrent()

            assertTrue(loaded.isEmpty())
            assertTrue(updates.last() is SearchUpdate.Inactive)
            job.cancel()
        }

    @Test
    fun `inactive input emits retained state immediately and cancels an active load`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val loadStarted = CompletableDeferred<Unit>()
            val loadCancelled = CompletableDeferred<Unit>()
            val updates = mutableListOf<SearchUpdate<String, String>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    allowBlank = false,
                    text = { it },
                    load = {
                        loadStarted.complete(Unit)
                        try {
                            awaitCancellation()
                        } finally {
                            loadCancelled.complete(Unit)
                        }
                    },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("dune")
            runCurrent()
            assertTrue(updates.last() is SearchUpdate.Loading)
            advanceTimeBy(150)
            runCurrent()
            assertTrue(loadStarted.isCompleted)

            queries.emit(".")
            runCurrent()

            assertTrue(loadCancelled.isCompleted)
            assertTrue(updates.last() is SearchUpdate.Inactive)
            job.cancel()
        }

    @Test
    fun `replacement loading and failure retain the last successful page`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val updates = mutableListOf<SearchUpdate<String, List<String>>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    text = { it },
                    load = { query ->
                        if (query == "foundation") error("offline")
                        listOf(query)
                    },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("dune")
            advanceTimeBy(150)
            runCurrent()
            queries.emit("foundation")
            advanceTimeBy(150)
            runCurrent()

            val replacementLoading = updates.filterIsInstance<SearchUpdate.Loading<String, List<String>>>().last()
            val failure = updates.filterIsInstance<SearchUpdate.Failed<String, List<String>>>().last()
            assertEquals(listOf("dune"), replacementLoading.retainedPage)
            assertEquals(listOf("dune"), failure.retainedPage)
            job.cancel()
        }

    @Test
    fun `new query cancels old load and rejects its stale completion`() =
        runTest(dispatcher) {
            val queries = MutableSharedFlow<String>()
            val oldStarted = CompletableDeferred<Unit>()
            val oldCancelled = CompletableDeferred<Unit>()
            val updates = mutableListOf<SearchUpdate<String, String>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    text = { it },
                    load = { query ->
                        if (query == "old") {
                            oldStarted.complete(Unit)
                            try {
                                awaitCancellation()
                            } finally {
                                oldCancelled.complete(Unit)
                            }
                        }
                        query
                    },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("old")
            advanceTimeBy(150)
            runCurrent()
            assertTrue(oldStarted.isCompleted)
            queries.emit("new")
            advanceTimeBy(150)
            runCurrent()

            assertTrue(oldCancelled.isCompleted)
            assertEquals("new", updates.filterIsInstance<SearchUpdate.Loaded<String, String>>().last().page)
            job.cancel()
        }

    @Test
    fun `typed index building failure has a distinct retained update`() =
        runTest(dispatcher) {
            class Building : Exception()
            val queries = MutableSharedFlow<String>()
            val updates = mutableListOf<SearchUpdate<String, String>>()
            val job = launch {
                queries.debouncedSearchUpdates(
                    debounceMillis = 150,
                    text = { it },
                    isIndexBuilding = { it is Building },
                    load = { throw Building() },
                ).toList(updates)
            }
            runCurrent()

            queries.emit("dune")
            advanceTimeBy(150)
            runCurrent()

            assertTrue(updates.last() is SearchUpdate.IndexBuilding)
            job.cancel()
        }
}
