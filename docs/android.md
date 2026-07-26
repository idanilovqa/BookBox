# Use netStorage on Android 16

netStorage copies selected audiobooks from the Windows server to an Android folder. The Android copy is independent: deleting or reorganizing it with another Android app does not affect Windows. The client has no upload, source rename, source move, source delete, tag-edit, or source-folder creation action.

The catalog worker is the only component allowed to write into the Windows library, and only for validated cover-image publication. The public API that the Android app uses has a read-only library mount. See [Set up the netStorage server on Windows](setup-server.md) before connecting the app.

## Before connecting

1. Start the Docker services on the Windows computer.
2. Keep the Galaxy and the Windows computer on the same trusted home network.
3. Use the reserved LAN address and HTTPS port configured in `deploy\.env`, for example `https://192.168.1.50:8443`.
4. On Windows, print the certificate fingerprint:

   ```powershell
   docker compose --env-file deploy\.env -f deploy\compose.yml logs netstorage-api
   ```

   Find the line beginning `netStorage certificate SHA-256:`. Compare the fingerprint from this local Windows command, not from a message or another device.

Do not expose the service through router port forwarding. No automatic discovery is used; the app remembers the approved server after the first successful sign-in.

## First connection and sign-in

1. Open netStorage and enter the full HTTPS server address, including the port.
2. Tap **Inspect certificate**.
3. Compare every character of the fingerprint shown by the app with the fingerprint printed on Windows.
4. If they are identical, type the full fingerprint and tap **Approve certificate**. Reject the connection if any character differs.
5. Sign in with the username from `deploy\config.yml` and the app password used when the server was configured.

The password is used for sign-in and is not saved. The approved server and certificate pin are saved so the address is entered only once.

If the server certificate is deliberately rotated, the saved pin must not be silently replaced. Verify the new fingerprint on Windows, then use **Change server** and approve the new fingerprint as a fresh connection.

## Choose the Android destination

Before the first copy, tap **Choose Android folder**. In Android's system folder picker:

1. Navigate to internal storage.
2. Create or select a dedicated folder such as `Audiobooks`.
3. Tap **Use this folder** and approve access.

Choose a subfolder rather than the top-level Downloads or storage root. Android grants netStorage access only to the selected document tree; the app does not request broad storage permission. The folder choice is remembered. Use **Choose destination** in Settings to replace it later.

Removing netStorage's document access in Android Settings, deleting the destination folder, or switching storage providers requires choosing the destination again.

## Browse and copy

The app has two source views:

- **Audiobooks** shows searchable cards with covers, title, author, year, genre, and available catalog details. Search text is matched by the local server. Filters and sorting narrow the catalog.
- **Raw folders** shows the server's configured roots, breadcrumbs, subfolders, and files. Folders appear before files. This is a read-only source browser.

Every audiobook card and detail view includes **View raw files**. It opens the source folder, marks files assigned to that book with **This book**, and leaves sibling files visible. Metadata and grouping review update only catalog records, except that an explicitly confirmed metadata match may let the server's catalog worker publish a cover image into the Windows book folder. Audio files, their tags, and folder names are not changed.

To copy:

1. Select one or more audiobook cards or folders.
2. Open **Copy to Android** and check the destination. Exact file count and required space are validated from the server manifest when the copy starts; the app does not invent totals from the number of selected books or folders.
3. Tap **Copy**.
4. Keep the Windows server running and both devices on the home network until the transfer completes.

Android asks for notification permission from this visible copy action. Denying it does not block the copy: progress remains available in netStorage and Android's Task Manager, but the progress notification may not appear in the notification drawer.

Files are streamed to temporary Android documents, resumed when possible, and checked against the server manifest before being finalized. A completed Android copy is not linked back to Windows and is never changed by a later server scan.

## Background transfers and conflicts

Transfers run as Android user-initiated background jobs. When notification permission is granted they also show a progress notification. The transfer history records waiting, running, paused, failed, conflict, completed, and cancelled states. A network or process interruption can resume from the recorded checkpoint when the storage provider supports it.

If a different file already exists at the Android destination, choose:

- **Replace** - removes and replaces only that Android destination file.
- **Keep both** - keeps the existing Android file and creates a numbered netStorage copy.
- **Skip** - leaves the existing Android file untouched and does not copy the incoming file.

None of these choices changes the Windows source. Cancelling a transfer retains partial Android data until **Discard partial data** is explicitly chosen in the app.

### Galaxy S23 Ultra battery setting

Start with the default battery settings. If One UI repeatedly pauses a long transfer after the screen turns off:

1. Open **Settings > Battery and device care > Battery > Background usage limits**.
2. Make sure netStorage is not in **Sleeping apps** or **Deep sleeping apps**.
3. If the problem continues, add netStorage to **Never sleeping apps**.

Samsung notes that never-sleeping apps can use more battery, so use this exception only if background transfers are being interrupted. Menu wording can vary slightly by One UI release.

## Build and test with an API 36 emulator

The app's minimum and target SDK are API 36, which is Android 16.

1. In Android Studio, open the `android` directory.
2. Open **View > Tool Windows > Device Manager**, select **+**, then **Create Virtual Device**.
3. Choose a phone hardware profile and an API 36 phone system image. Download the image if necessary.
4. Start the virtual device.
5. Use the Windows computer's reserved LAN address when entering the server. It must be present in the server certificate and reachable from the emulator.
6. Run the `app` configuration, or build and install from PowerShell:

   ```powershell
   Set-Location android
   .\gradlew.bat assembleDebug
   adb -e install -r app\build\outputs\apk\debug\app-debug.apk
   ```

If more than one emulator or device is connected, replace `-e` with `-s <serial>` from `adb devices`.

Android's official guides explain [creating virtual devices](https://developer.android.com/studio/run/managing-avds) and [installing an APK with adb](https://developer.android.com/tools/adb#move).

## Sideload on the Galaxy

The safest development installation is from Android Studio or `adb` over an authorized USB or wireless-debugging connection:

```powershell
Set-Location android
.\gradlew.bat assembleDebug
adb devices
adb -d install -r app\build\outputs\apk\debug\app-debug.apk
```

Confirm the debugging authorization prompt on the unlocked Galaxy. Disable USB or wireless debugging when it is no longer needed.

If installing the APK through My Files or a browser instead, transfer only the APK you built from this repository. On the Galaxy, search Settings for **Install unknown apps**, allow only the app that will open the APK, install it, and turn that source permission off afterward. Samsung warns that installing from outside the Play Store or Galaxy Store increases security risk; see [Samsung's unknown-source installation guidance](https://www.samsung.com/us/support/troubleshoot/TSG10001913/).

## Sign out and change server

- **Sign out** clears the saved session. The server address and approved certificate remain, so the next launch returns to sign-in.
- **Change server** asks for confirmation, then clears the saved server, certificate pin, and session. It does not delete Android audiobook copies or transfer history. The next launch returns to server entry.

Settings shows only the saved host, an abbreviated certificate fingerprint, username, destination display name, versions, and scan status. Use **Copy full fingerprint** when a full comparison is needed; secrets, full document URIs, and Windows paths are not displayed.

For Samsung's current background-app controls, see [Sleeping apps on your Galaxy device](https://www.samsung.com/us/support/answer/ANS10003442/).
