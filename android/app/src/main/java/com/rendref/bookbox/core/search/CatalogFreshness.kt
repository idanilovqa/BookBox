package com.rendref.bookbox.core.search

import com.rendref.bookbox.core.model.SearchSnapshotDto
import java.time.Clock
import java.time.Duration
import java.time.Instant

const val STALE_CATALOG_STATUS = "Search results may be out of date"

fun catalogIsStale(
    snapshots: Collection<SearchSnapshotDto>,
    clock: Clock,
): Boolean = snapshots.any { snapshot ->
    val completedAt = runCatching { Instant.parse(snapshot.completedAt) }.getOrNull()
        ?: return@any true
    Duration.between(completedAt, clock.instant()) > STALE_CATALOG_AGE
}

fun isOlderCatalogGeneration(
    candidate: Collection<SearchSnapshotDto>,
    current: Collection<SearchSnapshotDto>,
): Boolean {
    if (candidate.isEmpty() || current.isEmpty()) return false
    val candidateByRoot = candidate.associateBy(SearchSnapshotDto::rootId)
    val currentByRoot = current.associateBy(SearchSnapshotDto::rootId)
    if (candidateByRoot.keys != currentByRoot.keys) return false
    return currentByRoot.any { (rootID, snapshot) ->
        checkNotNull(candidateByRoot[rootID]).catalogGeneration < snapshot.catalogGeneration
    }
}

private val STALE_CATALOG_AGE: Duration = Duration.ofHours(12)
