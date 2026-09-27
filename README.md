# KyMessages

Private team conversations, on infrastructure you control.

This folder is the server-base starting point for KyMessages. The first product
priority is **small teams and end-to-end encrypted text chat**. Device enrollment,
approval/revocation and invitation-based room access now have authenticated backend
APIs. An experimental HTTP event log adds opaque delivery, reconnect cursors and
declared epoch coordination. The isolated browser proof exchanges real MLS messages
and discovers one-time KeyPackages through that API, including between Chromium
and Firefox, with explicit fingerprint approval. Its [clickable chat prototype](mls-proof/README.md#interactive-chat-prototype)
adds test-device setup, invitations, verification and durable message history.
Its [OIDC mode](mls-proof/README.md#suite-oidc-browser-flow) exercises the real suite
callback, session cookies and account-bound device unlock against a disposable issuer.
Production client integration remains open; the app still uses `ky_server_base` identifiers.

- [Product definition, proposed scope and release gates](docs/PRODUCT.md)
- [Implemented device, room and delivery API](docs/MESSAGING-API.md)
- [Protocol research and interoperability constraints](docs/KYMESSAGES-PROTOCOL-RESEARCH.md)
- [Runnable browser MLS proof and verification results](mls-proof/README.md)
- [MLS library comparison](docs/MLS-LIBRARY-RESEARCH.md)
- [Existing restore runbook](docs/RESTORE.md)
- [Repository contracts and verification](AGENTS.md)

The base supplies a Go service, React UI, identity/session handling, SQLite and
PostgreSQL storage, device pairing, and sealed recovery integration. KyMessages
will reuse these facilities. The current recovery collector supports SQLite only.

The existing Compose and publishing settings name the base product. Treat them as
scaffold configuration until the product-identity milestone is complete. The product
definition describes intended behavior, not current encryption or chat capabilities.

## Upgrading after the Busnes-app owner move

The GitHub organisation was renamed on 2026-09-16 and the image now lives at `ghcr.io/busnes-app/ky-server-base`. The project no longer controls `ghcr.io/busness-app`; GHCR does not redirect it, and anything served under that name must be treated as untrusted. If `KY_IMAGE` still names the old namespace or image name, re-pinning is required, not optional: inspect `git remote -v` before any `git pull`, `make ci`, or `docker compose` command, and replace a retired-owner remote with `https://github.com/Busnes-app/ky-server-base.git` (prefer a fresh clone plus a known commit). Then remove `KY_IMAGE` to follow the compose default or verify and pin a digest using `docs/RESTORE.md` before pulling.
