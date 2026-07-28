# BookBox LAN Setup and Rename Design

**Status:** Approved

**Date:** 2026-07-27

**Repository:** `C:\Repositories\netStorage`

**Primary device:** Samsung Galaxy S23 Ultra running Android 16 (API 36)

## Purpose

This change renames netStorage to BookBox and simplifies first-time connection on a private home network. BookBox will use HTTP with username-and-password authentication. A small Windows discovery process will make the server available at `bookbox.local`.

The change removes the current self-signed certificate workflow. Users will no longer inspect, compare, enter, store, or approve a certificate fingerprint.

## Scope

The current release will deliver:

- A complete product and technical rename from netStorage to BookBox.
- An HTTP-only Android-to-server connection on the private LAN.
- `bookbox.local` as the default server hostname.
- A headless Windows discovery process that advertises `bookbox.local`.
- A first-run Android form that requires only server, username, and password.
- Updated connection recovery actions and error messages.
- Tests and documentation for the renamed HTTP deployment.
- A roadmap entry for QR-based HTTPS pairing and a Windows tray companion.

The change does not alter audiobook browsing, cataloging, cover handling, transfer behavior, source-library permissions, or Android destination semantics.

## Clean Rename

BookBox is a clean technical rename rather than a display-name alias. The implementation will replace netStorage naming in:

- Android application ID, namespace, package declarations, authorities, actions, preference names, and Keystore aliases.
- Go module path, import paths, command names, user agents, log messages, and generated binaries.
- Docker Compose project, service, image, network, volume, database, user, environment-variable, secret, and container names.
- PowerShell scripts, deployment paths, configuration examples, release artifacts, checksums, and metadata reports.
- Documentation, tests, UI copy, and sample data that identify the product.

Source control may retain the current repository directory name during implementation. The release documentation will call the project BookBox.

Android treats the new application ID as a different app. Docker treats renamed resources as a different deployment. The installer and release notes will identify this release as a clean reinstall boundary. BookBox will not silently reuse old Android preferences, tokens, Keystore entries, containers, or named volumes.

The Windows audiobook source remains untouched. Users can rebuild the catalog from that source after installing BookBox. Migration instructions may describe how to preserve user-owned data, but the implementation will not add an automatic netStorage-to-BookBox state migration.

## Private-LAN HTTP Architecture

The BookBox API will listen for HTTP requests and will not create or load a TLS identity. The default public address is:

```text
http://bookbox.local:8787
```

The deployment will keep the port configurable. Android will prefill `bookbox.local` and normalize a bare hostname to the default HTTP scheme and port. Users may enter another hostname or private IP address when their network requires it.

The implementation will remove:

- Server certificate generation and fingerprint logging.
- TLS certificate and key configuration.
- Docker TLS volumes and TLS backup or recovery behavior.
- Android certificate probing, comparison, approval, storage, and pinning.
- Certificate-mismatch screens and certificate-specific recovery actions.
- Certificate-focused tests and operating instructions.

The deployment will bind BookBox to the selected private-LAN interface. Setup will create or document a Windows Firewall rule limited to the private network profile and local subnet. BookBox documentation will prohibit router port forwarding and public or guest-network exposure.

HTTP sends credentials, session tokens, filenames, metadata, and audiobook bytes without transport encryption. Any device capable of monitoring the LAN may read or modify that traffic. The owner accepts this temporary private-LAN risk to reduce setup complexity.

## Hostname Discovery

A small headless Windows executable named `bookbox-discovery` will start with the server deployment. It will:

- Determine the configured private-LAN address and BookBox HTTP port.
- Advertise `bookbox.local` through multicast DNS.
- Advertise a `_bookbox._tcp.local` DNS-SD service with the configured port.
- Refresh its records when the selected LAN address changes.
- Stop cleanly when BookBox stops.
- Write diagnostic messages without exposing credentials or tokens.

The process will have no playback features, window, tray icon, approval controls, or account-management UI. Deployment scripts will install, start, verify, stop, and remove it. A future tray companion may absorb the discovery responsibility.

Setup must verify the hostname before reporting success. The Windows verification checks the selected LAN address and advertised records. Android acceptance testing confirms that the Galaxy S23 Ultra resolves `bookbox.local` and reaches the BookBox API through the advertised port.

If mDNS is blocked by the router, access point, guest-network isolation, VPN, or firewall, the Android app will retain manual hostname and private-IP entry. The error screen will identify hostname-resolution failures separately from server and authentication failures.

## Android Connection Flow

First launch will show:

- A server field prefilled with `bookbox.local`.
- A username field.
- A password field.
- A **Connect** action.

The user may enter a full HTTP URL, a hostname, or a private IP address. The app will reject HTTPS for this release so that it does not imply certificate validation that the server no longer provides.

When the user taps **Connect**, Android will:

1. Normalize and validate the server address.
2. Send the username and password to the BookBox session endpoint.
3. Store the resulting refresh token with Android Keystore protection.
4. Discard the password.
5. Open the audiobook library.

Later launches will refresh the saved session. Settings will show the current server and provide **Sign in again**, **Change server**, and **Reset saved connection**. Resetting the connection will clear saved tokens, server capabilities, and the saved server address after confirmation.

The app will distinguish:

- `bookbox.local` could not be resolved.
- The resolved server could not be reached.
- The username or password was rejected.
- The saved session expired or became invalid.
- The server API is incompatible.
- The audiobook library is unavailable.

No connection screen or settings screen will mention certificates or fingerprints.

## Authentication

BookBox will retain the existing username, Argon2id password verification, login throttling, access-token, refresh-token, and Android Keystore protections. Removing TLS does not replace or bypass authentication.

The server will never store the plaintext application password. Android will keep no password after login. Password reset and session invalidation remain server-side administrative operations.

## Deferred Security and Approval Features

The implementation will add a roadmap document with two separate future projects.

### QR-Based HTTPS Pairing

The future secure pairing project will restore HTTPS without manual fingerprint entry. A QR payload will carry the server URL, certificate or trust information, server identity, and a short-lived single-use pairing token. Android will verify the server from the QR data and store the resulting trust state.

The future design must specify certificate lifetime, rotation, lost-device recovery, replay protection, token entropy, and behavior when the server identity changes.

### Windows Tray Companion

The future Windows companion will run in the notification area and display new-device access requests. It will show the requesting device and a short matching code, then let the owner approve or deny the request.

Approval will only permit the device to reach the sign-in step. The Android user must still enter the BookBox username and password. The companion will also list and revoke approved devices. It may replace the headless discovery process after it provides the same mDNS and DNS-SD behavior.

Neither future feature will receive partial production code in the current release.

## Testing

Implementation will follow test-driven development.

### Rename Tests

Tests and release checks will verify:

- Android uses the BookBox application ID, namespace, authorities, actions, storage names, and visible label.
- Go commands and imports use the BookBox module path.
- Docker resources, database settings, scripts, environment variables, secrets, and artifacts use BookBox names.
- Runtime files contain no unintended netStorage identifiers.
- A dedicated migration note may mention the former product name.

### Server and Android Tests

Server tests will prove that the API starts over HTTP without certificate files or TLS configuration. Android tests will cover:

- Bare `bookbox.local` normalization to `http://bookbox.local:8787`.
- Explicit private hostname and IP address handling.
- Rejection of malformed and HTTPS addresses.
- First sign-in with server, username, and password.
- Saved-session refresh.
- Incorrect credentials.
- Hostname-resolution failure.
- Server-unreachable failure.
- Sign-in again, change server, and reset connection.
- Removal of certificate state and certificate-specific UI.

### Discovery Tests

The discovery component will test:

- `bookbox.local` A-record content.
- `_bookbox._tcp.local` service and port records.
- Address-change refresh.
- Clean startup and shutdown.
- Conflict reporting when another device owns `bookbox.local`.
- Deployment install, verification, stop, and removal behavior.

A Windows integration test will query the advertised records from the host network. Manual acceptance will connect from the Galaxy S23 Ultra using `bookbox.local`.

### Release Acceptance

The release gate will verify:

1. A clean BookBox deployment starts its database, API, catalog worker, and discovery process.
2. `bookbox.local` resolves to the configured private-LAN address.
3. Android connects with hostname, username, and password only.
4. Android remembers and refreshes the session without retaining the password.
5. The deployment requires no certificate, fingerprint, QR code, or Windows approval action.
6. The Windows Firewall rule limits inbound access to the private LAN.
7. The API retains a read-only audiobook mount.
8. Existing catalog, browsing, metadata, and transfer tests remain green under BookBox names.
9. Release artifacts and documentation use BookBox branding.
10. The roadmap records QR-based HTTPS pairing and the Windows tray companion as future projects.

## Documentation

BookBox documentation will explain:

- The clean reinstall boundary.
- Installation and startup on Windows.
- How `bookbox.local` discovery works.
- How to connect from Android with username and password.
- How to diagnose mDNS, firewall, and network-isolation failures.
- How to use a private IP address as a fallback.
- Why HTTP must remain limited to the trusted private LAN.
- How to stop, restart, rebuild, back up, restore, and reset BookBox resources.
- Which security and device-approval improvements remain on the roadmap.
