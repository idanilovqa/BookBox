# Catalog Search, Grouping, Cover, and Pagination Repair Design

## Goal

BookBox will present audiobook identities instead of incidental audio tracks, return relevant search results, display available cover art, and keep compact scrolling stable when pagination fails.

This design combines the reported failures because they share the same catalog boundary: filesystem evidence creates books, enrichment augments those books, search finds them, and Android presents the result pages.

## Confirmed Failures

The current implementation has four distinct faults:

- Catalog fuzzy search admits unrelated records because `word_similarity` uses a `0.30` threshold without a word-boundary check. A search for `sanderson` scores unrelated text above that threshold.
- Raw search converts every nested file match to its parent folder. That removes useful standalone-book results from shared folders, while projection gaps can still expose nested files.
- The grouping engine treats counter-shaped embedded titles such as `- 01/16` as book identities. Sixteen Arcanum Unbounded tracks therefore become sixteen books, and the enrichment worker sends meaningless numeric titles to Open Library.
- Android retries a failed next-page cursor as soon as loading returns to idle. The loading row appears and disappears while the same request produces repeated HTTP 400 responses, which causes the compact-list jitter and prevents normal scrolling.

Oathbringer exposes a separate cover-discovery gap. Its title-named JPEG exists beside the M4B, but the scanner recognizes only images named `cover`, `folder`, or `front` as cover files.

## Work Streams

Implementation will use three bounded work streams with focused tests and commits:

1. Search precision and raw-result resolution.
2. Audiobook grouping, enrichment eligibility, and cover discovery.
3. Android pagination stability.

The streams share fixtures where useful, but each can be verified without depending on an Android device or a live Open Library response.

## Catalog Search Precision

Exact normalized substring matches will keep their current behavior. Fuzzy matches will use the normalized searchable text and must satisfy both of these PostgreSQL checks:

- `word_similarity(query, search_terms) >= 0.40`
- `strict_word_similarity(query, search_terms) >= 0.25`

The pair preserves expected misspellings such as `sandyrsen` for `sanderson` while rejecting unrelated records that passed the old `0.30` check. The repository will define the predicate and score expression once, then reuse them in the page query and cursor resolver. Candidate generation through the trigram index remains in place.

Live-library verification exposed borderline false positives (`Jefferson`, `Sandstorm`, and `Reasons and Persons`) even with that pair. Fuzzy candidates therefore also require bounded Levenshtein distance against a whole word or contiguous phrase: one edit for short queries, two for queries of eight or more characters. This preserves `sandyrsen` and transpositions such as `sandreson` without admitting unrelated shared syllables. The check runs after indexed candidate generation, supports compact multiword queries, and rejects fuzzy queries beyond the extension's 255-character limit while retaining exact search.

Tests will include exact title and author matches, the reported unrelated results, realistic author and title misspellings, stable ranking, and cursor continuation with the same predicate.

## Folder-First Raw Search

Raw search will return a navigation target rather than every matching track.

- A matching folder returns that folder.
- A file directly under the selected root remains a file result.
- A file inside a dedicated book folder resolves to that folder.
- Several matching files inside the same dedicated folder resolve to one folder.
- A standalone audio file that the catalog identifies as a separate book inside a shared folder may remain a file result.
- Several files assigned to the same book never become several raw-search results.

The projection will mark the result scope needed to distinguish a dedicated folder from a shared folder. The query will group resolved matches before ranking and pagination. Missing parent projection data will return an internal data error instead of exposing an arbitrary nested file.

This rule favors folders. The file exception requires catalog evidence for a separate standalone book, not filename similarity alone.

Tests will cover root files, dedicated folders, shared folders containing distinct standalone books, multi-file books in shared folders, deduplication, ranking, cursor pagination, and missing-parent rejection.

## Collection and Track Grouping

The grouping engine will classify embedded titles made only of separators and track counters, including forms such as `01/16`, `- 01/16`, and `Track 01 of 16`, as placeholder track metadata. Placeholder titles cannot define an audiobook identity or an enrichment query.

For a non-root folder with several audio files, the engine will prefer one folder-backed collection when all of these conditions hold:

- the files form a coherent numeric sequence through embedded track numbers, filename prefixes, or both;
- the files do not contain conflicting descriptive album identities;
- the files do not contain conflicting author identities;
- descriptive filename stems look like chapter or story names rather than repeated standalone-book identities.

The engine will infer the title from the meaningful folder name. For the reported folder, the result is one audiobook titled `Arcanum Unbounded` with sixteen assigned audio files. The author can come from reliable tags or a recognized author-folder ancestor. If neither is reliable, BookBox leaves the author empty and sends a title-only query.

Distinct consistent album and author pairs remain strong evidence for separate books. Multiple standalone M4B files in a shared folder also remain separate unless stronger collection evidence exists.

A rescan will retire superseded active books and create the new grouping through the existing catalog finalization path. Old provider matches attached to retired counter-title books will not transfer to the collection.

Tests will cover counter placeholders, Arcanum-shaped collections, chapter and story filenames, conflicting album or author evidence, shared standalone M4Bs, stable grouping keys, and rescan retirement.

## Enrichment Eligibility and Open Library

The worker will enqueue enrichment only when the inferred book has a meaningful title. Counter-only, punctuation-only, and generic track-only titles will remain local and unreviewed without a provider request.

Eligible books will use the existing request order: ISBN, title and author, then title-only general query. `Arcanum Unbounded` can therefore use a title-only Open Library lookup if the author remains unknown. `Oathbringer (Unabridged)` with Brandon Sanderson can use title and author after normalizing audiobook-edition suffixes for the provider query.

Provider candidates remain reviewable. Automatic confirmation must meet the existing confidence policy using meaningful title and author evidence. A provider response cannot turn a placeholder track into a confirmed book.

When a confirmed provider candidate supplies a cover URL, the cover publication job will retrieve and store that image under the existing HTTPS and size limits. Local cover art takes precedence for display while it exists and passes validation; the provider cover acts as the fallback.

Deployment verification also exposed Windows publication failures. Canonical backup paths on a different volume are outside the library and must be accepted. File data remains flushed before publication, but the unsupported Windows directory-flush operation is skipped. Native Windows publication, backup, and rollback tests are now mandatory in the release pipeline alongside the Linux suite.

Tests will cover eligibility rejection, title-only collection lookup, audiobook-suffix normalization, candidate scoring, and provider-cover publication.

## Local Cover Discovery

The scanner will inventory supported JPEG, PNG, and WebP files as image candidates instead of limiting discovery to three exact stems. It will inspect bounded headers for file type and dimensions without decoding full images.

The catalog will rank candidates inside the inferred book scope. Standard cover names, normalized title agreement, portrait proportions, useful dimensions, and proximity to assigned audio add evidence. Back covers, maps, booklets, logos, thumbnails, numbered alternates, tiny images, and strong landscape proportions subtract evidence.

The catalog will assign one image when its score clears the assignment threshold and leads the next candidate by the required margin. The title-named Oathbringer JPEG beside its M4B qualifies through title agreement and proximity. Ambiguous images remain unassigned.

The cover endpoint will serve the assigned file after checking root ownership, relative path, fingerprint, supported type, and regular-file status. It will retain the exact-name fallback during migration. Android will continue using the authenticated catalog image loader for local cover URLs and the public loader for provider URLs.

Tests will cover title-named art, standard names, negative filenames, dimensions, ambiguity, shared folders, path confinement, stale fingerprints, authorization, and provider-origin credential isolation.

## Compact Pagination Stability

Android will associate a failed load-more attempt with the cursor that failed. The near-end observer may request a cursor once. If that request fails, the view model will keep the loaded books and expose a stable pagination error state without starting another automatic request for that cursor.

Compact and grid layouts will show a fixed-height row labeled `Couldn't load more` with a `Retry` action. A user retry clears the failed-cursor latch and makes one new request. A successful response clears the error and advances the cursor. A search, filter, sort, refresh, or catalog reload also clears the latch because it creates a new result sequence.

The row will keep its height while visible, preventing list extent changes from moving the near-end boundary. The full-screen error state remains reserved for an initial load with no catalog content.

Tests will cover one automatic request per cursor, failure latching, manual retry, success, reset after query changes, preservation of loaded books, fixed error-row presentation, and parity between grid and compact modes.

## Rollout and Verification

The server and worker changes require a fresh build and restart. The catalog worker must rescan affected roots so it can replace counter-title books and assign new image candidates. The Android change requires a new APK installation.

Verification will use repository tests first, then local API checks for:

- `sanderson` returning Brandon Sanderson records without the reported unrelated entries;
- a misspelled Sanderson query returning the intended records;
- Arcanum Unbounded appearing once with all sixteen source files;
- Oathbringer returning a successful local cover response;
- enriched books exposing provider metadata and provider cover state where available;
- failed page requests producing one server request until the user taps Retry.

Live Open Library availability will not decide the automated test result. Tests will use deterministic provider fixtures, and the local smoke check will report provider unavailability without discarding local metadata.

## Non-goals

This repair will not infer arbitrary anthologies across unrelated folders, merge distinct books based on author alone, expose source paths, replace the provider-review model, or run an unbounded enrichment sweep. It will not modify source audio or image files.
