# Galaxy S23 Ultra release checklist

Status: unverified  
Device: Galaxy S23 Ultra  
Android: 16 / API 36  
Tester: ____________________  
Build filename: ____________________  
Build SHA-256: ____________________  
Test date: ____________________

Record evidence for each item. Leave failed or untested items unchecked. Add the observed result and any issue reference.

## Install and private LAN

- [ ] The APK signature verifies before sideload.
- [ ] Android installs and launches the selected v1.0.0 APK.
- [ ] The app reaches the reserved Windows address on trusted home Wi-Fi.
- [ ] The app cannot reach netStorage from mobile data or an untrusted external network.
- [ ] The router has no netStorage port-forwarding rule.

Evidence:

```text

```

## Identity and remembered connection

- [ ] The fingerprint on the Galaxy matches the full fingerprint printed on Windows.
- [ ] A one-character mismatch blocks sign-in and shows no credential request.
- [ ] The app remembers the approved server after process restart.
- [ ] The app restores sign-in after a Galaxy reboot without storing the plaintext password.
- [ ] A deliberate server certificate change requires a fresh comparison and approval.

Evidence:

```text

```

## Catalog and navigation

- [ ] Audiobooks mode displays cards with cover, title, author, year, and genre where available.
- [ ] Search finds a known book by title.
- [ ] Search finds known entries by author, genre, and year.
- [ ] Folders mode browses the raw hierarchy.
- [ ] **View raw files** opens the containing folder, highlights the book's assigned files, and keeps sibling files visible.
- [ ] A folder containing distinct `.m4b` books displays separate cards and copies them into separate Android subfolders.

Evidence:

```text

```

## Destination and copying

- [ ] Android's system picker grants a dedicated destination tree.
- [ ] The app remembers the destination after process restart.
- [ ] A copied audiobook appears under the expected Android-relative path.
- [ ] The copied bytes match the server transfer manifest.
- [ ] Deleting or moving the Android copy with another app does not change Windows.

Evidence:

```text

```

## Background and recovery

- [ ] A user-started transfer continues with the screen off.
- [ ] A fresh install requests notification permission in transfer context.
- [ ] Android displays the transfer notification while work runs.
- [ ] A Wi-Fi interruption pauses or retries without corrupting the final file.
- [ ] The transfer resumes from a verified byte range after Wi-Fi returns.
- [ ] A reboot leaves unfinished work visible and requires the documented user resume action.
- [ ] Samsung battery settings used during the test are recorded below.

Evidence and battery setting:

```text

```

## Destination conflicts

- [ ] **Replace** changes only the conflicting Android destination file.
- [ ] **Keep both** creates a deterministic alternate Android filename.
- [ ] **Skip** leaves the existing Android file unchanged.
- [ ] The app waits for a choice and does not resolve a destination conflict on its own.

Evidence:

```text

```

## Windows source boundary

- [ ] A before-and-after manifest proves that Android browse, search, review, and copy actions changed no Windows source file.
- [ ] The API container shows the audiobook bind as read-only.
- [ ] Android exposes no upload, source delete, source move, source rename, or tag-edit action.
- [ ] A worker cover publication changes only the expected recognized cover path.
- [ ] PostgreSQL contains the matching worker cover event and original-cover backup key.

Evidence:

```text

```

## Sign-off

- [ ] Every required item above passed.
- [ ] Failures have issue references and block release sign-off.

Tester signature: ____________________  
Release decision: ____________________
