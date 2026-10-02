# Sub-project 5a: console — users, health, audit

Date: 2026-10-02. Parent: `2026-10-01-matrix-platform-design.md` (decision 11). Sub-project 5
is split (owner-approved 2026-10-02): **5a** users, health, audit, a true Overview and a
generic fresh sign-in; **5b** rooms (Synapse admin API via MAS, proven first); **5c** settings
(Element branding with a restart flow, KyIdentity sync status).

## Intent

A KyMessages admin runs day-to-day chat from the console without a shell: sees who has
Matrix access and ends a person's sessions, sees whether every component is healthy and on
its pinned version with links to its upstream source, and reads the audit log. Every change
needs a fresh sign-in and is audited.

## Decisions (owner-approved 2026-10-02)

1. KyIdentity stays the only access switch. The console lists users and ends sessions; it
   has no lock or unlock (the offboarding sweep would undo a console lock within 5 minutes).
   The Users page says access is controlled in KyIdentity and links there.
2. Health is probed when the page loads; no history.
3. The audit page is read-only for any admin.

## Evidence

- MAS v1.26.0 admin API: `GET /api/admin/v1/{oauth2-sessions,user-sessions,compat-sessions}`
  with `filter[user]`, `POST .../{id}/finish`, `GET /api/admin/v1/version`
  (`crates/handlers/src/admin/v1/mod.rs`).
- Today: no router (tabs in `web/src/App.tsx`); `requireFreshAdmin` has a 10-minute window and
  a backup-specific message; audit rows are written but never listed; no health endpoint;
  Overview says "Chat (Matrix) is not set up yet".

## Section 1: what the admin gets

- **Users** (new tab; MAS through the existing admin client). Every Matrix user with username,
  KyIdentity link and status (active, locked, deactivated, not linked); search and paging
  (MAS pages of 100). User detail lists sessions (client, device, last active, IP). Actions:
  end one session, end all of a user's sessions; fresh sign-in; audited as
  `matrix.session_end` (MXID, session id, outcome). A banner: access is controlled in
  KyIdentity, which the sync enforces.
- **Health** (new tab). One request probes, server side, in parallel with short timeouts:
  Synapse (`/health`, version), MAS (discovery, `/api/admin/v1/version`), Element (version
  file), Postgres (`SELECT version()` as `kybackup`), KyMessages (own version). Each running
  version is compared with the version pinned in Compose and a mismatch is flagged. Each
  component links to the exact upstream source release of its running version (AGPL rule).
  The network self-check moves here from Settings.
- **Audit** (new tab). Newest first, paged, filter by kind (auth, backup, matrix, scim); who,
  what, target, outcome, when, IP. Read-only, any admin.
- **Overview.** The stale text goes; cards for chat health, user count, last backup result,
  each linking to its page. KyIdentity sync status is 5c.
- **Fresh sign-in.** `requireFreshAdmin`'s message becomes generic; the console has one
  "Confirm it's you" prompt that sends the admin through KyIdentity again and retries.

## Section 2: build, failures, proof

- **Server.** `internal/matrixsync`'s MAS client gains list-sessions per kind, finish-session
  and version, sharing the token, the `/api/admin/v1/` path guard and the client with the
  sweep. New `internal/health`: one probe function per component with its own timeout, run in
  parallel under the request deadline, returning status, version and error text (never
  secrets). Routes: `GET /api/admin/matrix/users` (paged), `GET
  /api/admin/matrix/users/{id}/sessions` (admin); `POST
  /api/admin/matrix/sessions/{kind}/{id}/finish` (fresh admin); `GET /api/admin/health`
  (admin); `GET /api/admin/audit?offset&limit&kind` (admin). Matrix off: Matrix routes 404;
  Health shows the app and its database only. Pinned versions are generated from
  `docker-compose.matrix.yml` into a Go file by `go generate`; a test fails on drift.
- **Web.** New tabs Users, Health, Audit; Settings loses the network check; Overview cards read
  the same endpoints; one fetch helper turns `reauthentication_required` into the confirm
  prompt and retries. Existing design system and themes; no new dependencies.
- **Failures.** MAS unreachable or bad secret: Users shows the error and cause, bounded by
  timeouts. A component down: Health marks it with the probe error. Ending an already-ended
  session counts as success (idempotent). A failed end is shown and audited as a failure.
- **Tests.** MAS client against a fake (session lists, finish, version, path guard); health
  probes against fakes (timeout, down, version mismatch); routes (role, freshness, audit rows,
  paging, filter); the pin generator; vitest for each page, the retry and error states.
- **Acceptance (`scripts/matrix-acceptance.sh`).** A console admin session ends Alice's
  Element session and her live token is refused within 30 s; Health reports every component
  up with versions equal to the pins; the audit API shows the session-end row.
- **Browser regressions** (Chromium and Firefox): the new tabs across themes, layouts and
  keyboard use.

## Out of scope

Rooms (5b); Element branding and KyIdentity sync status (5c); lock/unlock in the console;
health history and alerting; audit export or retention.

## Risks

- Probing versions depends on each upstream exposing one without credentials (Element's
  version file, Synapse's endpoint); unproven until implementation.
- IP and device data in sessions is personal data shown to admins; it stays in the console
  and is not logged.
