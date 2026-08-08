package com.rendref.bookbox.core.search

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.transformLatest

sealed interface SearchUpdate<out Q, out P> {
    val query: Q
    val retainedPage: P?

    data class Loading<Q, P>(
        override val query: Q,
        override val retainedPage: P?,
    ) : SearchUpdate<Q, P>

    data class Inactive<Q, P>(
        override val query: Q,
        override val retainedPage: P?,
    ) : SearchUpdate<Q, P>

    data class Loaded<Q, P>(
        override val query: Q,
        val page: P,
    ) : SearchUpdate<Q, P> {
        override val retainedPage: P = page
    }

    data class Failed<Q, P>(
        override val query: Q,
        override val retainedPage: P?,
        val cause: Throwable,
    ) : SearchUpdate<Q, P>

    data class IndexBuilding<Q, P>(
        override val query: Q,
        override val retainedPage: P?,
        val cause: Throwable,
    ) : SearchUpdate<Q, P>
}

/**
 * Coordinates text-driven search without clearing the most recent successful page.
 * Inactive input is emitted immediately so it cancels pending and in-flight searches.
 */
@OptIn(ExperimentalCoroutinesApi::class)
fun <Q, P> Flow<Q>.debouncedSearchUpdates(
    debounceMillis: Long = 150,
    allowBlank: Boolean = true,
    text: (Q) -> String,
    eligible: ((Q) -> Boolean)? = null,
    isIndexBuilding: (Throwable) -> Boolean = { false },
    load: suspend (Q) -> P,
): Flow<SearchUpdate<Q, P>> {
    require(debounceMillis >= 0)
    var retainedPage: P? = null
    var generation = 0L

    return transformLatest { query ->
        val ownGeneration = ++generation
        val active = isActiveSearchText(text(query), allowBlank) &&
            (eligible?.invoke(query) ?: true)
        if (!active) {
            emit(SearchUpdate.Inactive(query, retainedPage))
            return@transformLatest
        }
        emit(SearchUpdate.Loading(query, retainedPage))
        delay(debounceMillis)
        try {
            val page = load(query)
            if (ownGeneration == generation) {
                retainedPage = page
                emit(SearchUpdate.Loaded(query, page))
            }
        } catch (failure: CancellationException) {
            throw failure
        } catch (failure: Throwable) {
            if (ownGeneration == generation) {
                if (isIndexBuilding(failure)) {
                    emit(SearchUpdate.IndexBuilding(query, retainedPage, failure))
                } else {
                    emit(SearchUpdate.Failed(query, retainedPage, failure))
                }
            }
        }
    }
}

private fun isActiveSearchText(text: String, allowBlank: Boolean): Boolean {
    if (text.isBlank()) return allowBlank
    var qualifyingCodePoints = 0
    var offset = 0
    while (offset < text.length) {
        val codePoint = text.codePointAt(offset)
        if (Character.isLetterOrDigit(codePoint) && ++qualifyingCodePoints >= 2) return true
        offset += Character.charCount(codePoint)
    }
    return false
}
