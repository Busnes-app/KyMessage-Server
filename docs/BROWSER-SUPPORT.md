# Browser Support for KyMessages Chat

## Supported Browsers

KyMessages chat is supported on **current desktop Chrome, Edge and Firefox**.

These browsers have been verified to provide all required cryptographic and storage features:
- Ed25519 signing for MLS credentials
- IndexedDB for encrypted local persistence
- Web Locks for coordination
- Secure context (HTTPS)

## Unverified Browsers

The following browsers have not yet been verified:
- **Safari (macOS)**: Not yet tested. See [docs/BROWSER-EVIDENCE.md](BROWSER-EVIDENCE.md) for details on WebKit limitations.
- **iOS**: Not yet tested; uses WebKit like macOS Safari.
- **Android**: Not yet tested; platform support for required cryptographic features varies.

The browser support check returns an `unverified` state when all required features are present but the browser has not been tested. The "My account" page and the future chat page use this check to inform users.

## Browser Detection States

The browser support check produces three states:

- **`supported`**: All required features are present and the browser is in the verified list (current desktop Chrome, Edge, or Firefox).
- **`unverified`**: All required features are present, but the browser has not been tested.
- **`unsupported`**: One or more required features are missing (Ed25519 signing, IndexedDB, Web Locks, or secure context).

Browser detection checks for actual capabilities first; the declared browser list is only for verification status. The feature checks are authoritative: if the browser lacks a required capability, it returns unsupported, regardless of browser type.

## CI Coverage

- The chat test suite runs on Chromium and Firefox.
- The operator console browser regression tests run on Chromium today, with Firefox added later.
- Full integrated testing on other browsers is not currently part of the release criteria.

## Future Improvements

- Safari support requires either WebKit implementing Ed25519 or the chat core using a different signing algorithm. This is tracked in the release plan.
- Mobile browser support remains under evaluation; the UI is currently designed for desktop screens.
