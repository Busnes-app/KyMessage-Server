# Browser Support for KyMessages Chat

## Supported Browsers

KyMessages chat targets **current desktop Chrome, Edge and Firefox**. This is the
declared list; branded Chrome and Edge have not been run in CI (see CI Coverage).

Chat requires:
- Ed25519 signing for MLS credentials
- IndexedDB for encrypted local persistence
- Web Locks for coordination
- Secure context (HTTPS)

## Unverified Browsers

The following browsers have not yet been verified:
- **Safari (macOS)**: Not yet tested. Linux Playwright WebKit has an open, intermittent native Ed25519 key-generation failure; see [BROWSER-EVIDENCE.md](BROWSER-EVIDENCE.md).
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

- CI runs Playwright's own Chromium and Firefox builds, not branded Chrome or Edge.
  The support check reports that Chromium build as `unverified`.
- The isolated chat proof (`mls-proof/`) runs its suites on both engines.
- The operator console browser regressions run on both engines.
- WebKit is not in CI.

## Future Improvements

- Safari support needs WebKit's intermittent Ed25519 key-generation failure resolved and real Safari tested. This is tracked in the release plan.
- Mobile browser support remains under evaluation; the UI is currently designed for desktop screens.
