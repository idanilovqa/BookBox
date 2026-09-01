# Folder-First Raw Search Design

## Goal

Raw-library search will show useful navigation targets instead of listing every matching file. A query that matches many files in one directory will produce one folder result.

## Result Rules

The server will apply these rules before ranking and pagination:

- A matching folder produces that folder.
- A matching file inside a subfolder produces its immediate parent folder.
- Several matching files with the same parent produce one folder.
- A matching file directly inside the selected library root remains a file result because it has no meaningful child folder to open.
- A folder that matches through its own name or path and through a child file still appears once.

Filename and associated audiobook metadata remain searchable. The result conversion changes presentation and navigation; it does not remove those search fields.

## Server Design

`GET /v1/browse/search` will convert matched file documents to folder documents inside the PostgreSQL search query. The query will join each non-root file match to the search document for its immediate parent folder. Root-level file matches will keep their file document.

The query will union direct folder matches with converted file matches, group by result identity, and retain each result's best rank and score. It will sort and paginate the grouped result set. This ordering prevents a page containing thousands of files from collapsing to one folder after pagination.

The endpoint will continue returning the existing raw-entry shape. Most entries will have `kind: "folder"`; only root-level file matches may have `kind: "file"`. Stable folder and file document IDs will continue serving as result IDs and cursor tie-breakers.

## Android Design

Android will keep its existing folder and file row behavior:

- A folder result opens that folder.
- A root-level file result opens the selected root and highlights the file.

The empty state will say `No matching folders or root files.` The result list will not perform its own grouping because server-side grouping preserves correct ranking and pagination.

## Failure Handling

The endpoint will preserve the current error, stale-snapshot, and index-building behavior. A matched file whose expected parent folder document is missing indicates an invalid search projection. The repository will return an internal data error instead of inventing a folder or exposing a source path. Root-level files do not require a parent document.

## Test Coverage

Server repository tests will cover:

- direct folder-name and folder-path matches;
- filename and linked-metadata matches converted to the immediate parent;
- several file matches deduplicated to one parent folder;
- a direct folder match merged with file-derived matches for the same folder;
- root-level file matches retained as files;
- ranking and cursor pagination after grouping;
- root isolation and stable result IDs;
- a missing parent folder document rejected as invalid projection data.

API tests will require folder entries except for root-level files and will preserve path-confidentiality checks. Android tests will verify folder opening, root-file highlighting, the revised empty state, and compatibility with paginated responses.

## Non-goals

This change will not alter audiobook search, catalog grouping, metadata enrichment, cover discovery, or normal raw-folder browsing. It will not create synthetic root-folder results or group files into inferred audiobooks.
