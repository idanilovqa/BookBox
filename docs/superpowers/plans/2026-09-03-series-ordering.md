# Series metadata and ordering implementation plan

**Goal:** Persist series names and numeric positions for audiobooks and expose Series sorting in the Android library.

**Architecture:** Reuse books.series and books.series_position and the matching API fields. Extract explicit series labels and reliable numbered-folder evidence; fill missing values without replacing existing metadata. Add a stable numeric series sort to both listing APIs and the existing Android sort menu.

**Constraints:** Preserve meaningful title numbers, standalone books, partial-volume labels, and confirmed metadata. Support decimal positions. No additional metadata provider dependency. Existing user changes in docs/roadmap.md are unrelated.

- [x] Add failing extraction tests for Wheel of Time provider titles, numbered folders, Discworld numbered titles, decimals and standalone author folders.
- [x] Implement series inference and populate metadata candidates before saving matches.
- [x] Backfill missing series fields during server maintenance, including confirmed books, with guarded short updates.
- [x] Add PostgreSQL numeric series sort and pagination regression coverage for both book APIs.
- [x] Add Android Series sort choice, verify API serialization and compile the app.
- [ ] Review, run focused suites, deploy server/worker and backfill existing records.
- [ ] Verify live series metadata and order; provide updated APK for the new sort control.

## Verification and publication details

- Metadata, worker and HTTP API package tests passed.
- PostgreSQL regressions passed for numeric order (1, 2, 2.5, 10), stale edits, search by series, stale projections/cursors, and audio file classification.
- Android LibraryViewModel tests and debug APK build passed.
- Backfill increments catalog generation and queues one guarded search publication per changed root. Interrupted passes recover from generation mismatch; existing values are preserved.
- Provider annotations precede folder evidence. Author, narrator, collection, disc and track noise is excluded; actual audio paths recover lost numbered folders for confirmed books.
- Live validation exposed omitted file classification in GetBook and noisy folder assignments. Both were fixed; the 54 affected new assignments were backed up and removed before the corrected pass.