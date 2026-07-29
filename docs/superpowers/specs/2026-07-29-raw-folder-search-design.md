# Raw Folder Search Design

## Goal

Let an Android user search every folder beneath the selected raw-library root and open a matching folder. Search must ignore case, spaces, punctuation, and other symbols, so a query such as `black box` matches paths containing `blackbox`, `black.box`, or `Black-Box`.

## Scope

The feature searches folder paths in the selected server root. It does not return files, search other roots, alter source data, or create a persistent search index.

## Matching

The server normalizes the query and each full relative folder path:

1. Convert Unicode letters to lowercase.
2. Keep Unicode letters and digits.
3. Remove every other character.

The server returns a folder when its normalized relative path contains the normalized query as a substring. A query that contains no letters or digits produces no results.

For example, the normalized query `author black box` matches the relative path `Author/Black.Box`.

## Server API

Add a read-only endpoint:

```text
GET /v1/browse/search?root=<root-id>&q=<query>
```

The endpoint resolves the configured root through the existing secure root resolver. It recursively walks directories beneath that root, does not include files, and does not follow directory symlinks. Existing path-containment rules remain in force.

The response contains at most 100 entries:

```json
{
  "rootId": "audiobooks",
  "query": "black box",
  "entries": [
    {
      "name": "Black.Box",
      "relativePath": "Author/Black.Box",
      "kind": "folder",
      "modifiedAt": "2026-07-29T00:00:00Z"
    }
  ]
}
```

The server collects matching folders, sorts them by case-insensitive relative path, uses the original relative path as a deterministic tie-breaker, and returns the first 100. Unknown roots and filesystem errors use the existing browse error response format.

## Android Data Flow

Add a folder-search method to the browse API, repository interface, and repository implementation. The network response uses the existing folder entry model where practical and a dedicated search response model for the root, query, and entries.

`FoldersState` gains:

- `searchQuery`
- `searchResults`
- `isSearching`
- `searchErrorMessage`

`FoldersViewModel` updates the query as the user types. It waits 300 milliseconds after the latest edit before sending a nonblank normalized query. A newer edit, root change, or query clear cancels the prior search and prevents a stale response from updating state.

Clearing the query restores the directory that the user had open before searching. Selecting another root clears the query, results, and search error. Tapping a result clears search state and loads that result path through the existing folder-navigation flow.

## Android UI

Place one full-width outlined search field below the root selector and above the breadcrumbs. Use `Search raw folders` as its label.

When the query contains letters or digits, replace the normal directory listing with search content:

- Show a compact progress indicator while a request is running.
- Show each matching folder name and its full relative path.
- Open the folder when the user taps its result.
- Show `No matching folders.` after a successful empty response.
- Show the search error inline after a failed request.

The breadcrumbs continue to show the directory that will return when the user clears search. Existing selection checkboxes do not appear on search-result rows; the user opens a result before selecting it.

## Failure Handling

A failed search keeps the query and current directory state. The user can edit or clear the query and continue browsing. Directory loading errors and search errors remain separate so one operation cannot replace the other operation's message.

## Testing

Server tests cover:

- case-insensitive, symbol-insensitive normalized substring matching
- full-relative-path matching
- folder-only results
- recursive traversal without following directory symlinks
- deterministic ordering and the 100-result cap
- queries with no letters or digits
- unknown roots and filesystem failures

Android unit and network tests cover:

- request encoding and response parsing
- the 300-millisecond debounce
- cancellation and stale-response protection
- clearing search
- changing roots
- opening a result
- search failures that preserve folder navigation

Compose tests cover search input, progress, empty, error, result-path display, and result taps.

Run focused Go and Android tests first, then the affected test suites. Build the installable debug APK with `assembleDebug`.
