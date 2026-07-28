# Galaxy S23 Ultra release checklist

Status: unverified

Device: Galaxy S23 Ultra

Android: 16 / API 36

Tester: ____________________

Build filename: ____________________

Build SHA-256: ____________________

Test date: ____________________

Record evidence for each item. Leave failed or untested items unchecked.

## Install and private LAN

- [ ] The APK signature and checksum verify before sideload.
- [ ] Android installs and launches the selected BookBox APK.
- [ ] Windows uses a Private network profile.
- [ ] Windows Firewall limits TCP 8787 and UDP 5353 to Private plus `LocalSubnet`.
- [ ] The router has no BookBox port-forwarding rule.
- [ ] The app cannot reach BookBox from mobile data, guest Wi-Fi, or an external network.

Evidence:

```text

```

## Discovery and sign-in

- [ ] `bookbox.local` resolves to the reserved Windows private address.
- [ ] The app connects with `bookbox.local`, username, and password.
- [ ] The app connects with the private-IP fallback.
- [ ] A wrong password fails without saving it.
- [ ] **Sign in again** requires the password.
- [ ] **Reset saved connection** clears tokens and the saved server but keeps downloads and history.

Evidence:

```text

```

## Catalog and navigation

- [ ] Audiobooks mode displays available covers and metadata.
- [ ] Search finds a known book by title and author.
- [ ] Folders mode browses the raw hierarchy.
- [ ] **View raw files** opens the containing folder and marks assigned files.
- [ ] Separate `.m4b` books in one source folder appear as separate catalog items.

Evidence:

```text

```

## Destination and copying

- [ ] Android's system picker grants a dedicated destination tree.
- [ ] The app remembers the destination after process restart.
- [ ] A copied audiobook appears at the expected Android path.
- [ ] The copied bytes match the server transfer manifest.
- [ ] Deleting or moving the Android copy does not change Windows.

Evidence:

```text

```

## Background and recovery

- [ ] A user-started transfer continues with the screen off.
- [ ] Android requests notification permission during a transfer.
- [ ] Android displays transfer progress when notification permission is granted.
- [ ] A Wi-Fi interruption does not corrupt the final file.
- [ ] The transfer resumes from a verified byte range after Wi-Fi returns.
- [ ] Reboot leaves unfinished work visible and requires the documented resume action.
- [ ] The tester records the Samsung battery setting used.

Evidence and battery setting:

```text

```

## Destination conflicts

- [ ] **Replace** changes only the conflicting Android destination file.
- [ ] **Keep both** creates a numbered Android filename.
- [ ] **Skip** leaves the existing Android file unchanged.
- [ ] The app waits for the user's conflict choice.

Evidence:

```text

```

## Windows source boundary

- [ ] A before-and-after manifest shows no source change from Android browse, search, review, or copy.
- [ ] The API container mounts the audiobook source read-only.
- [ ] Android exposes no source upload, delete, move, rename, or tag-edit action.
- [ ] A worker cover publication changes one recognized cover path.
- [ ] PostgreSQL contains the matching cover event and backup key.

Evidence:

```text

```

## HTTP risk acknowledgment

- [ ] The tester used a trusted private LAN controlled by the operator.
- [ ] The tester understands that HTTP exposes credentials, tokens, metadata, and audiobook bytes to LAN observers.

Evidence:

```text

```

## Sign-off

- [ ] Each required item passed.
- [ ] Issue references document failures and block release sign-off.

Tester signature: ____________________

Release decision: ____________________
