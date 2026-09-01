# Local Covers and Compact Library Design

## Goal

Android will authenticate local cover requests, the catalog will discover plausible title-named artwork, and the Audiobooks screen will offer a dense full-width compact view.

## Cover Discovery

The inventory will recognize supported JPEG, PNG, and WebP files as image candidates. It will inspect bounded image headers for dimensions and format before considering an assignment.

BookBox will rank candidate images within an audiobook's physical inference scope. Positive evidence includes the standard names `cover`, `folder`, and `front`; normalized title or author agreement; portrait aspect ratio; useful resolution; and proximity to assigned audio. Negative evidence includes back-cover, booklet, disc, map, banner, logo, thumbnail, and numbered-alternate language, plus tiny or strongly landscape dimensions.

The catalog will assign a candidate at score 70 or higher when it leads the next candidate by at least 15 points. Shared folders may assign different images when filename agreement distinguishes books. Ambiguous candidates remain unassigned. BookBox will not rename, copy, or modify source images.

The cover endpoint will serve the catalog-assigned current file after validating its root, relative path, fingerprint, type, and regular-file status. During the first post-migration scan, it may fall back to the existing exact-name lookup.

## Android Cover Loading

Connected-app startup will install the existing authenticated Coil catalog loader built from the same serialized token-refreshing call factory as the catalog API. Local cover requests will carry a bearer token and retry once after an authorized refresh.

Open Library and Google Books candidate artwork will continue using the separate public HTTPS-only loader. BookBox credentials will never reach provider origins.

## Compact Audiobooks View

The Audiobooks screen will add a Grid/Compact control near the filters. Grid remains the first-run default. Android will persist the chosen mode on the device and keep independent scroll positions for both layouts.

Compact mode will render full-width horizontal pill cards 76 to 84 dp tall with 6 dp vertical spacing. Each card will contain:

- a 48 by 64 dp cover at the left;
- a one-line medium-weight title;
- a small one-line author;
- a subdued `labelSmall` series line formatted as `Series name · position`;
- small inline review or duplicate badges;
- a trailing selection checkbox and compact raw-files action.

The whole pill will open the audiobook. Series positions will preserve non-integer text such as `0.5`. A series without a position will show its name alone. Cards without series metadata will omit the series line and keep the same compact height.

The layout will use the existing Material theme and rounded raw-folder rhythm. The small cover acts as the visual book spine; the rest of the card remains quiet and dense.

## Failure and Empty States

A missing or rejected cover will render the existing theme placeholder without changing card size. Authentication failure will follow the normal session-refresh path. Compact and Grid modes will share loading, paging, selection, filtering, and empty states.

## Test Coverage

Server tests will cover image classification, bounded inspection, scoring, shared-folder assignment, ambiguity, migration fallback, missing files, and path confinement. Android tests will cover loader installation, authorization and refresh, provider-loader isolation, persisted view choice, compact dimensions and content, series formatting, actions, pagination, scroll restoration, and accessibility labels.
