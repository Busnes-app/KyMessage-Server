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
- **Safari (macOS)**: WebKit does not support Ed25519 signing. See [docs/BROWSER-EVIDENCE.md](BROWSER-EVIDENCE.md) for details.
- **iOS**: Requires WebKit, same limitation as macOS Safari.
- **Android**: WebKit support varies by version; Ed25519 is not consistently available.

If you attempt to use chat on an unverified browser, you will receive a warning but may continue. However, the client cannot guarantee correct operation.

## Browser Detection States

The browser support check produces three states:

- **`supported`**: All required features are present and the browser is declared in the verified list. Chat will run normally.
- **`unverified`**: All required features are present, but the browser has not been verified. Chat may run, but we have not tested it and cannot guarantee compatibility.
- **`unsupported`**: One or more required features are missing. Chat will refuse to run and will explain which features are unavailable.

User agent detection only affects the wording used in messages; it does not make a security decision. The feature checks are authoritative: if the browser lacks a required capability, chat will not start, regardless of browser type.

## CI Coverage

- The chat test suite runs on Chromium and Firefox.
- The operator console browser regression tests run on Chromium and Firefox.
- Full integrated testing on other browsers is not currently part of the release criteria.

## Future Improvements

- Safari support requires either WebKit implementing Ed25519 or the chat core using a different signing algorithm. This is tracked in the release plan.
- Mobile browser support remains under evaluation; the UI is currently designed for desktop screens.
