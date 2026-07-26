# Goodreads List Matching Design

**Status:** Approved design draft for user review
**Date:** 2026-07-26
**Target release:** netStorage version two
**Depends on:** netStorage version-one catalog, PostgreSQL, Android app, and transfer workflow

## Purpose

The Goodreads feature narrows a large Windows audiobook catalog to books in the user's Goodreads library. The server imports a Goodreads CSV export, preserves its shelf memberships, matches each entry to one primary local audiobook, and exposes the result in Android.

The feature does not change source audio, tags, names, or folders. It stores imported list data and match decisions in PostgreSQL.

## Scope

Version two will:

- Watch one configured Windows CSV path for new Goodreads exports.
- Import the entire Goodreads library and all shelf names in the export.
- Let Android filter the saved list by shelf, search text, and match status.
- Match Goodreads entries to local audiobook cards and raw folders.
- Choose one primary local match per Goodreads entry.
- Show ranked options for ambiguous matches.
- Show **No matches found** when no credible candidate exists.
- Reuse prior matches across CSV updates.
- Match new entries without reprocessing unchanged entries.
- Preview removals and require confirmation before applying them.
- Retain removed entries and match decisions as hidden history.
- Restore hidden matches when a Goodreads entry returns.

## Non-Goals

The first Goodreads phase excludes:

- Goodreads credentials, cookies, or authenticated sessions.
- HTML page parsing or web scraping.
- Direct Goodreads API integration.
- Editing Goodreads shelves from netStorage.
- Writing imported metadata into audio tags.
- More than one primary local audiobook match per Goodreads entry.
- Replacing the existing Audiobooks or Folders navigation modes.

Authenticated page parsing may receive a separate design if CSV export stops meeting the need.

## Import Architecture

Docker Compose adds a read-only import mount:

```text
C:\netStorage-imports → /imports
```

The configured file is:

```text
C:\netStorage-imports\goodreads_library_export.csv
```

The catalog worker checks the path at startup and on a configurable interval. Before reading, it verifies that size and modification time remain unchanged across two checks. It computes SHA-256 and ignores a file hash already recorded in PostgreSQL.

The worker has read-only access to the import folder. It cannot edit, rename, or delete the CSV.

A new hash starts a staged import. The current active Goodreads list stays unchanged until parsing and validation finish. The server then calculates a diff against the active import.

Updates with no removals apply after validation. Updates with removals stay staged until Android confirms or rejects them. Rejection preserves the active list and records the rejected import hash so Android can display its status.

Android checks import status on app launch, manual refresh, and return to the Goodreads screen. Version two does not require push notifications.

## CSV Parsing

The importer accepts UTF-8, with or without a byte-order mark. It uses an RFC 4180-compatible CSV parser, including quoted commas, escaped quotes, and embedded line breaks.

The parser maps headers by normalized name rather than column position. It recognizes the standard Goodreads export identity and shelf fields, including:

- Book ID
- Title
- Author
- Author l-f
- Additional Authors
- ISBN
- ISBN13
- My Rating
- Average Rating
- Publisher
- Binding
- Number of Pages
- Year Published
- Original Publication Year
- Date Read
- Date Added
- Bookshelves
- Bookshelves with positions
- Exclusive Shelf
- My Review

The importer requires Book ID, Title, and Author columns. It preserves recognized optional values and ignores unknown columns while recording their names in the import report.

The server rejects:

- A missing required header.
- A file larger than 50 MB.
- More than 250,000 rows.
- Invalid UTF-8.
- Duplicate Book IDs with conflicting title, author, or ISBN identity.
- A row with no Book ID, title, or author.
- Invalid CSV quoting or a truncated record.

The server reports row numbers and reasons for invalid data. It applies no rows from a rejected import.

## Stored Data

PostgreSQL stores:

- Import runs, file hashes, source filename, timestamps, counts, and status.
- Active and staged Goodreads entries.
- Original Goodreads Book ID.
- ISBN and ISBN13 when present.
- Title, authors, publication years, ratings, and review data present in the CSV.
- Normalized identity fingerprint.
- Shelf definitions and entry-to-shelf memberships.
- One primary local book ID per entry.
- Ranked match candidates and scores.
- Match status and confirmation source.
- Hidden history for removed entries.
- Removal confirmation records.

One Goodreads export represents one saved **Goodreads Library**. Goodreads shelf names are filters within that library. A book may belong to several shelves.

The server keys entries by Goodreads Book ID. If an import lacks a usable ID in a future compatible format, it may fall back to ISBN13, ISBN, then normalized title plus author. The initial CSV format requires Book ID.

## Incremental Diff

For each staged import, the server classifies entries:

- **New:** Book ID absent from the active list and hidden history.
- **Returned:** Book ID absent from the active list but present in hidden history.
- **Changed identity:** Book ID exists but ISBN, normalized title, or normalized author changed.
- **Changed list data:** Only shelves, ratings, review, or other non-identity fields changed.
- **Unchanged:** Identity and list data match.
- **Removed:** Active Book ID absent from the new export.

The server handles each class as follows:

- New entries enter the matching queue.
- Returned entries restore the hidden primary match when that local book still exists.
- Changed-identity entries keep their previous primary visible but enter review.
- Changed-list-data entries update without re-matching.
- Unchanged entries retain all matches and receive no matching work.
- Removed entries wait for confirmation.

The removal preview lists each affected title and current local-match state. Confirming removes those entries from the active list and preserves them in hidden history. Cancelling leaves the prior import active.

If the same staged file remains on disk after rejection or cancellation, the worker does not create duplicate import runs. A different file hash creates a new staged import.

## Local Audiobook Matching

The matcher uses the version-one local catalog. It scores evidence in this order:

1. Exact ISBN13, ISBN, or another confirmed edition identifier.
2. Normalized title and normalized primary author.
3. Title, author, and publication-year similarity.
4. Local source folder and filename evidence.

One high-confidence candidate with a clear lead becomes the primary match. Close credible candidates produce **Ambiguous**. No candidate above the minimum score produces **No matches found**.

Match states are:

- **Matched:** One active primary local book.
- **Ambiguous:** Two or more credible candidates need a choice.
- **No matches found:** No credible local candidate.
- **Local copy missing:** The prior primary book disappeared from the current local catalog.
- **Pending:** Matching work has not finished.

Android shows ranked local audiobook cards for an ambiguous entry. Each option includes cover, title, author, year, source folder, and **View raw files**. Choosing an option stores it as the confirmed primary.

Normal library rescans do not reevaluate confirmed primary matches. A rescan retries ambiguous, no-match, and local-copy-missing entries when relevant catalog books were added or changed.

## API Boundary

Version two adds authenticated endpoints for:

- Import status and staged diff.
- Goodreads shelves.
- Paginated Goodreads entries.
- Search and match-status filtering.
- Entry details and candidate options.
- Confirming a primary match.
- Retrying unresolved matches.
- Confirming or rejecting staged removals.

These endpoints read and change PostgreSQL list data. They expose no source-library upload, tag edit, rename, move, or delete operation.

Responses contain local relative source paths only when needed for raw navigation. They do not expose absolute Windows paths, import-folder paths, PostgreSQL details, or hidden entries unless a recovery operation requests them.

## Android Experience

Android adds **Goodreads** beside Audiobooks and Folders.

The Goodreads screen shows:

- Import state and last successful update time.
- **Update available** when a staged import exists.
- Search by Goodreads title or author.
- Shelf-name filters from the active export.
- Match-status filters.
- Goodreads entry data beside the linked local audiobook card.
- Selection controls for copying matched audiobooks.

Matched entries provide:

- **View audiobook**
- **View raw files**
- Select for copy

Ambiguous entries provide:

- Ranked local candidates.
- **Choose primary match**
- Raw-folder inspection for each candidate.

No-match entries remain visible with **No matches found** and a retry action. Local-copy-missing entries retain Goodreads data and show that the former Windows copy is unavailable.

The update preview shows added, returned, changed, unchanged, and removed counts. If removals exist, Android lists the affected books and requires confirmation. The message states that confirmation removes them from the active app list while keeping hidden match history.

The existing transfer workflow handles books selected from Goodreads. Destination naming, verification, resumption, and conflict rules remain unchanged.

## Error Handling

The server and Android app provide recovery for:

- Import file absent: retain the active list and show the expected Windows path.
- File still changing: wait for the next scan.
- Duplicate file hash: show that the file is already processed.
- Invalid CSV: reject the entire import and show row/header errors.
- Staged removal not confirmed: keep the previous list active.
- Local catalog scan incomplete: keep entries pending until the scan finishes.
- Match candidates tied: show Ambiguous and require a primary choice.
- No credible candidate: show No matches found.
- Confirmed local book missing: show Local copy missing and retry matching.
- Database transaction failure: keep the prior active import and report failure.

Logs exclude CSV review text, credentials, absolute Windows paths, and full user-provided content. Import reports may include row number, Goodreads Book ID, and a bounded title for identification.

## Testing

Server tests cover:

- UTF-8 and byte-order marks.
- Quoted commas, escaped quotes, and embedded line breaks.
- Header reordering and unknown columns.
- Required headers, duplicate IDs, malformed rows, size limits, and row limits.
- Whole-import transaction rollback.
- Shelf membership and multiple shelves per entry.
- File stability checks and duplicate hashes.
- New, returned, changed, unchanged, and removed diffs.
- New-only matching and unchanged-match preservation.
- Hidden-history removal and restoration.
- Exact, high-confidence, ambiguous, no-match, and missing-local results.
- Proof that importer and API requests change no audiobook source file.

Android tests cover:

- Goodreads navigation.
- Shelf, search, and status filters.
- Matched card and raw-folder navigation.
- Ambiguous candidate choice.
- No matches found and Local copy missing states.
- Update-available indicators.
- Removal preview, cancellation, and confirmation.
- Selecting matched Goodreads entries for the existing transfer workflow.

An integration test imports an initial fixture, confirms an ambiguous match, then imports a second fixture containing unchanged, added, changed-shelf, returned, and removed entries. It verifies that only new or unresolved entries invoke matching and that removal confirmation preserves hidden history.

## Acceptance Criteria

Version two is complete when:

1. The worker detects a stable new Goodreads CSV hash from the read-only import mount.
2. A valid full export creates one Goodreads Library with all shelf memberships.
3. Android filters the list by shelf, search text, and match status.
4. Clear matches connect to one primary local audiobook.
5. Ambiguous entries show options and save the chosen primary.
6. Entries without credible candidates show **No matches found**.
7. Goodreads entries open the connected audiobook card and raw files.
8. Matched entries use the existing copy workflow.
9. An unchanged entry keeps its match without new matching work.
10. A new entry receives matching work.
11. Shelf-only changes do not trigger re-matching.
12. Removals require Android confirmation.
13. Confirmed removals disappear from the active list and retain hidden history.
14. Re-added entries restore valid hidden matches.
15. Failed or rejected imports leave the active list intact.
16. Goodreads operations do not change source audiobook files.
17. PostgreSQL remains the only application database.
