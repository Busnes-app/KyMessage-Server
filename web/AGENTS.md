# Web

## Purpose
React 19 + TypeScript + Vite PWA frontend embedding KySecurity color tokens (Busnes light/dark defaults plus `Patina Ky`, `Cyber`, `Nord`, `Paper`, `OLED`), 90-second ephemeral QR device pairing modals, client-side WebCrypto PoW CAPTCHA, and administrative management panels.

## Ownership
Owns user interface components, service worker caching, PWA installation manifests, and frontend theme switching.

## Local Contracts
- `MemberHome.tsx` is the non-admin page: account name, an "Open chat" link (new tab, `rel="noopener noreferrer"`) to `/api/settings` `chat_url` when set, otherwise the "Chat isn't available yet." notice, and sign out. Non-admins get `AppHeader` (theme switcher, sign out) with no navigation; tested by `MemberHome.test.tsx` and `AppHeader.test.tsx`.
- Product names, document title and manifest use KyMessages. The current embedded
  shell is the operator console; do not imply a successful backup/crypto review from static dashboard text.
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
- Admin calls that need a fresh sign-in use `adminFetch` (`api.ts`): a 403 `reauthentication_required` opens the single `ConfirmItsYou` dialog (mounted in the admin shell), which links a same-origin `/api/sso/` `reauth_url` in a new tab and retries only on the admin's Retry; Cancel returns the refusal. Concurrent refusals share one answer. A POST is never replayed after a redirect. Admin DTOs are validated with the `dto.ts` helpers.
- `Users.tsx` lists Matrix users (search, paging) and ends a user's sessions one by one (End all runs the per-session finish route in server order and stops when step-up is refused). It has no lock/unlock: access is controlled in KyIdentity. With Matrix off it says chat is not set up.
- `Backup.tsx` warns for as long as `database_driver` from `/api/backup/status` is not `sqlite`: only the SQLite path can snapshot a database into a capsule, so a Postgres deployment makes no capsules at all.
- `Backup.tsx` shows `capsule_size` (highlighting `warning` at 75% of the limit) and, with Matrix, `media_last_run` and its read error, beside the capsule attempt.
- `Dashboard.tsx` (Overview) has three cards, Chat health, Matrix users and Backups, each from its own admin route and each failing alone; its button calls `onNavigate('health'|'users'|'backup')`. With Matrix off the users card says chat is not set up.
- `Health.tsx` lists components from `/api/admin/health` (status, version, Compose pin, error) and embeds `NetworkCheck`. Versions render as text only; a `source` link is kept only when it is https under the upstream `element-hq` or `postgres` GitHub repos. `Audit.tsx` is a read-only, paged (50) view of `/api/admin/audit` filtered by kind (`auth|backup|matrix|scim`); both validate DTOs with `dto.ts`.
- `NetworkCheck.tsx` renders on the admin-only Health page: it fetches `/api/admin/network-check`, validates the DTO at the boundary (a malformed or failed response shows `Network check unavailable.`), aborts on unmount and lists five Pass/Warn marks: trusted proxy peer, trusted `X-Forwarded-Proto: https`, https `KY_APP_URL`, host match, and `KY_TRUSTED_PROXIES` naming only single addresses (`trusted_proxies_narrow`). Tested by `NetworkCheck.test.tsx`.

## Verification
- Browser setup: build the frontend, run `go build -o .browser/server ./cmd/server` at the repo root, then `cd web && npx playwright install chromium firefox && npm run test:browser`. CI also installs browser OS dependencies.
- `make test-web` or `cd web && npm ci && npm test`, then `npm run build` (vitest with jsdom; `src/pages/Backup.test.tsx` renders the recovery screen against a stubbed status route). Commit `web/dist` after a build; CI diffs it.

## Shared browser UI

- `src/ky-ui/` is generated from Busnes-app/ky-ui, pinned by `VERSION` file hashes. Change shared colors, navigation states and storage helpers upstream, then run its consumer sync with an explicit worktree map; do not hand-edit vendored files.
- Products own layout, routes, saved choice keys and named palettes. Busnes aliases consume shared tokens; mark primary navigation with `ky-nav-item` while preserving current-page semantics.
- Verify vendored files with `node src/ky-ui/check-vendor.mjs` from this document's directory. Builds/CI run that check. Rendered evidence and capture limitations are recorded in the repository-root `UI-VERIFICATION.md`.

## Child DOX Index
- [browser/AGENTS.md](./browser/AGENTS.md): Production-server browser regression harness and disposable test data.
