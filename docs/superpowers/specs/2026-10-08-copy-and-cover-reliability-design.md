# Copy and Cover Reliability Design

## Outcome

BookBox keeps a pending copy selection when the Android destination changes,
reports the real preparation failure, creates a useful destination folder for
books stored at the library root, and refreshes Open Library artwork reliably.

## Copy flow

Changing the persisted Android destination must not recreate the connected
application runtime. The runtime identity is the server origin plus username;
the destination remains mutable state read by `DestinationStore`.

Copy preparation distinguishes a missing or revoked Android document-tree grant
from manifest/server failures. A failed preparation remains on the setup screen
with the selection intact. Only a successfully persisted transfer navigates to
Transfers.

For a selected logical book whose `source_folder` is the library root, the
manifest must include only that book's assigned audio and companions. Its
destination folder is built from confirmed/enriched metadata as:

`Author - Title (Year)`

Missing metadata components are omitted cleanly. Multiple authors use the first
author. If usable enriched metadata is unavailable, the safe audio filename stem
is used. The existing Android filename sanitizer remains the authority for
forbidden characters and blank fallbacks. Nested books retain their existing
folder-preserving behavior.

## Cover flow

Open Library cover URLs may redirect from `covers.openlibrary.org` to another
HTTPS host. The isolated provider image client follows redirects while validating
every request hop as credential-free HTTPS. It never uses BookBox authentication.

Library cover models include the root catalog generation as a client-generated
cache revision. When cover publication triggers a scan and a new projection, the
model changes and Coil retries a URL that may previously have returned 404.
Only the exact BookBox cover route and one numeric `v` query parameter are
accepted. Open Library matches that genuinely have no cover remain placeholders.

## Verification

Regression tests cover destination changes without runtime replacement, typed
copy errors and retained drafts, metadata-derived root-level folders with safe
fallbacks, safe provider redirects, and catalog-generation cover revisions.
Focused Android and Go tests run before the nearest complete package/module gates.

## Delivery

After tests and review pass, audit tracked files and history-facing configuration
for secrets, create a public GitHub repository named `BookBox`, push the verified
branch as `main`, and attach the resulting repository URL. Publishing must not
include local attachments, deployment state, secrets, or unrelated uncommitted
changes.
