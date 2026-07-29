# Use BookBox on Android 16

BookBox copies selected audiobooks from the Windows server to an Android folder. The Android copy remains independent from the Windows source. The app has no source upload, rename, move, delete, tag-edit, or folder-creation action.

## Connect and sign in

Start BookBox on Windows before opening the Android app. Keep both devices on the same trusted private LAN.

1. Open BookBox.
2. Leave **Server** as `bookbox.local`, or enter the server's private IPv4 address.
3. Enter the username from `deploy\config.yml`.
4. Enter the application password created during server setup.
5. Tap **Connect**.

The app adds `http://` and port `8787` when you enter a hostname or address without a scheme or port. You can also enter the full URL, such as `http://bookbox.local:8787` or `http://192.168.1.25:8787`.

Use the private-IP URL if the phone cannot resolve `bookbox.local`. A `.local` failure does not require changes to BookBox accounts or secrets.

BookBox v1 uses unencrypted HTTP. Another device on the LAN can capture the username, password, session tokens, catalog requests, and audiobook traffic. Do not connect through guest Wi-Fi, a public network, WAN access, or router port forwarding.

## Choose the Android destination

Before the first copy, tap **Choose Android folder**. In Android's system folder picker:

1. Open internal storage.
2. Create or select a dedicated folder such as `Audiobooks`.
3. Tap **Use this folder** and approve access.

BookBox stores access to that document tree. It does not request broad storage permission. Removing the permission in Android Settings, deleting the folder, or changing storage providers requires a new destination selection.

## Browse and copy

**Audiobooks** shows catalog cards with available covers and metadata. **Raw folders** shows the configured Windows roots, folders, and files. Both views use the server's read-only API.

Use **Search raw folders** to find nested folders by their full path. Search ignores case, spaces, punctuation, and other symbols, so `black box` also matches names such as `Black.Box` and `blackbox`.

To copy:

1. Select audiobook cards or folders.
2. Open **Copy to Android** and check the destination.
3. Tap **Copy**.
4. Keep the server running and both devices on the private LAN until the transfer finishes.

BookBox streams into temporary Android documents, resumes supported transfers from a checkpoint, and verifies the server manifest before finalizing each file. A later server scan does not alter a completed Android copy.

Android asks for notification permission after you start a visible transfer. If you deny it, progress remains in BookBox and Android's Task Manager, but the notification drawer may omit transfer progress.

## Destination conflicts

- **Replace** removes and replaces the conflicting Android destination file.
- **Keep both** keeps the existing file and creates a numbered BookBox copy.
- **Skip** leaves the existing Android file unchanged.

These choices do not change the Windows source. Cancelling a transfer retains partial Android data until you choose **Discard partial data**.

## Galaxy S23 Ultra battery setting

Start with Samsung's default battery settings. If One UI pauses long copies after the screen turns off:

1. Open **Settings > Battery and device care > Battery > Background usage limits**.
2. Remove BookBox from **Sleeping apps** or **Deep sleeping apps**.
3. Add BookBox to **Never sleeping apps** only if the pause continues.

Record the setting used in the [Galaxy S23 Ultra checklist](galaxy-s23-checklist.md).

## Change the saved connection

- **Sign in again** clears the current tokens and returns to credential entry.
- **Change server** clears tokens and the saved server after confirmation.
- **Reset saved connection** clears tokens and the saved server after confirmation.

These actions keep Android downloads and transfer history. The next connection still requires a password.

## Sideload a development build

Build and install from PowerShell:

```powershell
Set-Location android
.\gradlew.bat assembleDebug
adb devices
adb -d install -r app\build\outputs\apk\debug\app-debug.apk
```

Confirm the debugging authorization on the unlocked phone. Disable USB or wireless debugging after the installation if you no longer need it.
