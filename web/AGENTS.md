# Web

## Purpose
React 19 + TypeScript + Vite PWA frontend embedding KySecurity color tokens (Busnes light/dark defaults plus `Patina Ky`, `Cyber`, `Nord`, `Paper`, `OLED`), 90-second ephemeral QR device pairing modals, client-side WebCrypto PoW CAPTCHA, and administrative management panels.

## Ownership
Owns user interface components, service worker caching, PWA installation manifests, and frontend theme switching.

## Local Contracts
- `MessagingUsage.tsx` adds a read-only admin overview of server storage metadata.
  Validate the usage DTO at the HTTP boundary; distinguish unavailable/loading
  from zero usage, abort on unmount, refresh explicitly and preserve the opened
  room-details disclosure. Members neither render nor fetch this operator view.
  Explain the lifetime receipt counter separately from retention purge and actual disk use.
- `SuspendedDevices.tsx` renders after `MessagingUsage` on the admin dashboard. It
  validates the suspended-device DTO (including boolean `truncated`, shown as a note, and
  `expires_at`, which must be a representable date, shown as "Revoked automatically on <date>")
  at the boundary, revokes only after
  `window.confirm` through `secureFetch`, and shows a step-up refusal's same-origin
  `reauth_url` as a sign-in link. Tested by `SuspendedDevices.test.tsx`.
- `MyAccount.tsx` is the members' page: account, browser support and the member's own devices from `/api/messaging/devices` (validated at the boundary; any 403 shows the non-suite state), with owner revoke via `secureFetch` after `window.confirm`. Tested by `MyAccount.test.tsx`.
- Product names, document title and manifest use KyMessages. The current embedded
  shell is the operator console and explicitly states that encrypted chat is not
  included; do not imply a successful backup/crypto review from static dashboard text.
  Retain suite icon masters and existing theme choices.
- Web themes default to the Busnes.app cream/light and charcoal/dark palettes with orange accents, following the OS until a browser-local choice is saved. Preserve existing named themes and saved choices.
- A signed-in user with `must_change_password` sees only password replacement and sign-out. Replacement uses `secureFetch`, returns to login after session revocation, and never exposes the normal navigation before completion.
- Strict TypeScript type safety without unused imports.
- The authenticated shell uses a persistent sidebar; the selected page is marked by a quiet surface and slim accent rail, with a horizontal overflow navigation on small screens.
- Dynamic theme selection applies `data-theme` attribute to the root HTML document and persists to `localStorage`.
- Authenticated state-changing requests use `secureFetch` so the `ky_csrf` cookie is mirrored into `X-CSRF-Token`.
- Register the service worker from the production JS bundle; keep `script-src 'self'` intact. Pairing uses a native modal dialog for focus containment, Escape and focus restoration. It shows only the QR code (no typed PIN) and treats poll status `consumed` as linked.
- Worker caching is limited to the same-origin public shell, manifest and assets. HTML is network-first with offline fallback so deployments refresh; dynamic/auth routes stay uncached.
- `Backup.tsx` validates the latest-run DTO at the HTTP boundary and displays the
  recorded outcome independently of the older remote receipt. Local-only results,
  scheduled failures, partial success and unknown history must remain distinguishable.
- `Backup.tsx` renders a "Message backups" section (`MessagesBackup`) from `status.messages`, validated at the boundary like the people DTO, hidden when absent and a page-level error when malformed: opt-in explanation, own schedule select (Off by default), "Back up messages now", "Run message drill", last run, last receipt and local copies. It calls only `/api/backup/messages/*`.
- `Backup.tsx` shows the server's same-origin `reauth_url` as a sign-in link when a backup change is refused for step-up.
- `Backup.tsx` warns for as long as `database_driver` from `/api/backup/status` is not `sqlite`: only the SQLite path can snapshot a database into a capsule, so a Postgres deployment makes no capsules at all.
- `browserSupport.ts` detects whether the browser supports chat's required features (Ed25519 signing, IndexedDB, Web Locks, HTTPS) and validates against the declared browser list; it exports only WebCrypto feature detection with no imports from chat-core or ts-mls.

## Verification
- Browser setup: build the frontend, run `go build -o .browser/server ./cmd/server` at the repo root, then `cd web && npx playwright install chromium && npm run test:browser`. CI also installs browser OS dependencies.
- `make test-web` or `cd web && npm ci && npm test`, then `npm run build` (vitest with jsdom; `src/pages/Backup.test.tsx` renders the recovery screen against a stubbed status route). Commit `web/dist` after a build; CI diffs it.

## Shared browser UI

- `src/ky-ui/` is generated from Busnes-app/ky-ui, pinned by `VERSION` file hashes. Change shared colors, navigation states and storage helpers upstream, then run its consumer sync with an explicit worktree map; do not hand-edit vendored files.
- Products own layout, routes, saved choice keys and named palettes. Busnes aliases consume shared tokens; mark primary navigation with `ky-nav-item` while preserving current-page semantics.
- Verify vendored files with `node src/ky-ui/check-vendor.mjs` from this document's directory. Builds/CI run that check. Rendered evidence and capture limitations are recorded in the repository-root `UI-VERIFICATION.md`.

## Child DOX Index
- [browser/AGENTS.md](./browser/AGENTS.md): Production-server browser regression harness and disposable test data.
