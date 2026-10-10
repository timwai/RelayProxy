# RelayProxy Browser Sync — Chrome Extension (development)

This package runs **without the local RelayProxy Agent or Native Messaging Host**.

Current state on `feat/browser-session-sync`:
- Manifest V3 extension can load unpacked in desktop Chrome.
- Each Chrome Profile creates its own browser device signing and encryption key pairs in IndexedDB, with a random browser device ID.
- Popup stores a validated HTTPS Server origin and requests per-site HTTPS host permissions **only on an explicit click**.
- Background listens to allowed-domain Cookie change **metadata only**; **no Cookie values are read, stored, exported, or transferred**.

**Not implemented yet:** Server browser-device registration and approval, WSS authentication, pairing, HPKE E2EE, Cookie payload capture/application, synchronization, and logout. The popup clearly reports LOCAL_SETUP_ONLY. Do not use this as a functioning session synchronizer.

## Local developer test

1. Open `chrome://extensions` and enable Developer mode.
2. Choose “Load unpacked” and select `browser/chrome-extension`.
3. Open the extension popup; verify the browser device ID is generated.
4. Enter an HTTPS Server origin and save.
5. Enter an HTTPS test website URL, click Request site permission, and confirm the browser permission prompt.
6. Inspect `chrome.storage.local` and IndexedDB: only rules / public metadata and non-extractable CryptoKeys should be present; **no Cookie secrets**.

See [design v1.1](../../docs/browser-session-sync-design-development.md).
