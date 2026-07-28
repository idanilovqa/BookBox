# BookBox roadmap

This file records planned security and Windows control work. The current release provides HTTP sign-in with a username and password plus a headless mDNS discovery process. It does not provide QR pairing or a tray approval interface.

## QR-based HTTPS pairing

A future pairing flow should put this information in a QR code:

- BookBox server URL
- Server trust or identity data
- A short-lived single-use pairing token

The Android user should not type or compare a certificate fingerprint. The server must expire the token after use or timeout and bind the resulting device credential to the server identity.

The design must cover server identity rotation, device loss, token replay, clock differences, and a private-IP fallback when hostname resolution fails.

## Windows tray approval companion

A future Windows tray companion should:

- Show a local approve or deny prompt for a new device
- Display a short matching code on Windows and Android
- After approval, the Android user must still enter the BookBox username and password
- List paired devices
- Revoke a selected device

The companion may absorb the headless discovery process so one Windows component handles discovery and approvals. It does not need audiobook playback or a full Windows library interface.

The approval prompt must identify the requesting device and expire without user action. Approval does not replace username-and-password authentication.
