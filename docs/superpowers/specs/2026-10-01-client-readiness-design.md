# Client readiness (release plan step 4, without shipping chat)

Date: 2026-10-01. Status: approved design, not yet planned.

## Intent

Remove every step-4 blocker except the independent review, without putting the
unreviewed chat client into the deployed binary.

- Members (non-admin KySignOn accounts) get a real place to land and can manage their
  own messaging devices.
- HTTPS deployment follows the suite's proxy model (KyPost's), with cloudflared as the
  reference proxy.
- The supported-browser set is declared and checked.
- The chat client's non-UI core becomes its own package, so the independent review
  covers the code that will ship.

Out of scope: any change to the messaging protocol or server messaging API, shipping a
chat UI, real-device Safari/iOS/Android verification, WebKit CI tracking (release step 6).
The unreviewed-encryption gate stays closed.

## 1. Members' page

- New "My account" page in the existing shell (`web/src/pages/MyAccount.tsx`). Default
  landing page for non-admins; one more sidebar entry for admins. Non-admins see no admin
  navigation (the server already returns 403 on admin routes).
- Shows:
  - account (display name, username, suite identity) from `/api/auth/me`, and Sign out;
  - notice: "Encrypted chat is not available on this server yet. It ships after an
    independent security review.";
  - **My messaging devices** from `GET /api/messaging/devices`: name, fingerprint, status
    (pending, approved, suspended with "Revoked automatically on <date>", revoked),
    created time; Revoke on any non-revoked device after `window.confirm`, through the
    existing owner revoke route with `secureFetch`;
  - browser support state from section 3a.
- Accounts the messaging API refuses (local password, including the bootstrap admin) see
  "Messaging needs a KySignOn account." instead of the device list, not an error.
- The device DTO is validated at the boundary, like `SuspendedDevices.tsx`.
- No server change.
- Tests: vitest for the DTO validation, the revoke flow and the non-suite account state;
  browser regression for a member signing in, seeing only "My account", and revoking a
  device.

## 2. HTTPS behind a proxy

Existing behaviour kept: the base `docker-compose.yml` is a loopback local preview
(`${KY_BIND:-127.0.0.1}`); `KY_ENV=production` with a non-https `KY_APP_URL` refuses to
start unless `KY_COOKIE_SECURE=false` is set deliberately; HSTS follows secure cookies.

- **`docker-compose.proxy.yml`** overlay (appended to `COMPOSE_FILE`, preserving existing
  overlay chains):
  - names the default network `${KY_NETWORK:-kymessages-net}` with subnet
    `${KY_NETWORK_SUBNET:-10.91.0.0/24}` (clear of KyPost's `10.89.0.0/24`);
  - publishes no port (`ports: !reset []`);
  - requires `KY_APP_URL` (no localhost default) and sets `KY_ENV=production`;
  - composes with `docker-compose.static-ip.yml` for a pinned container address.
- **Per-app networks, not one shared network.** The proxy is the only container on more
  than one app network; apps cannot reach each other. The proxy's compose file joins
  `kymessages-net` as `external: true` with a pinned address (for example `10.91.0.10`),
  routes the hostname to `http://kymessages:8080`, and `KY_TRUSTED_PROXIES` names that
  `/32`. Live messaging's WebSocket passes through cloudflared without extra settings.
- **Admin self-check** `GET /api/admin/network-check` (requireAdmin, no-store) returns the
  peer address, the resolved client IP, whether forwarded headers were trusted, whether
  `KY_APP_URL` is https, and whether this request's `Origin`/`Host` match `KY_APP_URL`.
  The Settings page shows each with a pass/warn mark. Admin-only on purpose: it describes
  the deployment's wiring.
- **`docs/Reverse_Proxy_Networking.md`**, modelled on KyPost's: cloudflared first; a short
  nginx variant (TLS, `proxy_pass`, `Upgrade`/`Connection` for WebSockets,
  `X-Forwarded-For`/`X-Forwarded-Proto`); why per-app networks; recovering from
  "incorrect label" and "Pool overlaps"; verifying with the self-check. README and root
  `AGENTS.md` link it.
- Tests: Go tests for the self-check (trusted and untrusted peer, https and http, origin
  match and mismatch); a CI check that `docker compose config` with the proxy overlay
  publishes no ports and names the network.

## 3a. Supported browsers

- `docs/BROWSER-SUPPORT.md`: supported are current desktop Chrome, Edge and Firefox;
  Safari, iOS and Android are "not yet verified", linking `docs/BROWSER-EVIDENCE.md`.
- `web/src/browserSupport.ts` returns:
  - `unsupported` — a required feature is missing: WebCrypto Ed25519 generate and sign
    with a non-extractable key, IndexedDB, `navigator.locks`, `isSecureContext`. Chat will
    refuse.
  - `unverified` — features present but the browser is outside the declared set
    (`navigator.userAgentData`, else the UA string). Chat will warn and name the
    supported browsers. The UA only chooses wording; it is never a security control.
  - `supported`.
  A single probe cannot catch WebKit's intermittent Ed25519 failure, which is why the
  declared set, not the probe, decides `unverified`.
- Shown on "My account" now; the chat page enforces it later.
- CI: add Firefox to the console's browser regressions (`web/playwright.config.mjs`);
  vitest covers the three states with stubbed APIs.

## 3b. `chat-core` package

- Move `delivery.ts`, `delivery-wire.ts`, `vault.ts`, `device.ts`, `markdown.ts` and
  `session.ts` from `mls-proof/src` to top-level `chat-core/src`. `ts-mls` and
  `markdown-it` move to `chat-core/package.json` (pinned, lockfile, `npm ci`).
- `session.ts` stops importing `web/src/api`; `chat-core` receives a CSRF-fetch function
  as a parameter and depends on nothing in `web/`.
- `mls-proof` keeps `chat.ts`, `main.ts`, its HTML, server fixture and tests, and imports
  `chat-core` by relative path. Every existing mls-proof suite (build, manual, delivery,
  OIDC on Chromium and Firefox) passes unchanged: that is the evidence the move changed
  nothing.
- Gate, enforced in CI by `scripts/check-chat-gate.sh`: nothing under `web/` imports
  `chat-core` or `ts-mls`; `web/package.json` does not list `ts-mls`; `.dockerignore`
  excludes `/chat-core/`. Lifting the gate is a deliberate, reviewed edit to that script.
- `chat-core/AGENTS.md` names the package as the independent-review target and states
  the gate. Root `AGENTS.md` (child index, verification), `mls-proof/AGENTS.md` and
  `docs/FIRST-RELEASE-PLAN.md` step 4 are updated.

## Review focus

- A member who is also an admin must still reach every admin page.
- A local-password account must never be offered device actions.
- The proxy overlay must not break the existing `static-ip`, `lan-dns` and `build`
  overlay chains.
- The self-check must not trust forwarded headers from an untrusted peer when reporting.
- The `chat-core` move must not change any IndexedDB key, record layout or wire format:
  saved vaults from before the move must still unlock.
