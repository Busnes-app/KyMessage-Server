# Sub-project 5b: console — rooms

Date: 2026-10-02. Parent: `2026-10-01-matrix-platform-design.md` (decision 11), split in
`2026-10-02-matrix-console-5a-design.md`. Builds on 5a (users, health, audit, step-up).

## Intent

A KyMessages admin sees every room and deals with a bad one from the console: closes it
(members removed, rejoin blocked, history kept), reopens it, or deletes it permanently.
Every change needs a fresh sign-in and is audited.

## Decisions (owner-approved 2026-10-02)

1. Synapse admin access is a MAS personal session for a dedicated service account
   `@kymessages-console`: created by KyMessages through the MAS admin API, no password, no
   KyIdentity link, Synapse admin. The offboarding sweep exempts exactly that username. Each
   room action mints a session with scopes `urn:matrix:client:api:* urn:synapse:admin:*` and a
   5-minute lifetime, uses it and revokes it.
2. Two separate removal actions: **Close** (`block=true`, `purge=false`; reversible by
   **Reopen**) and **Delete permanently** (purge; typed room-name confirmation).

## Evidence (source, MAS v1.26.0 and Synapse v1.162.0; unproven live until the plan's first task)

- MAS policy grants `urn:synapse:admin:*` only to interactive grants with a user that may
  request admin (`policies/authorization_grant/authorization_grant.rego`); client credentials
  cannot reach Synapse's admin API ("Synapse doesn't support user-less tokens yet").
- Synapse decides admin by `urn:synapse:admin:*` in the requester's scope
  (`synapse/api/auth/mas.py`); the shared secret serves only MAS's internal endpoints.
- MAS admin API `POST /api/admin/v1/personal-sessions` creates a session acting as a user with
  a given scope and `expires_in`; `.../revoke` ends it; `POST /api/admin/v1/users` and
  `/users/{id}/set-admin` exist.

## Section 1: components

- **Service account (`internal/matrixsync`).** Before the first room action KyMessages ensures
  `@kymessages-console` exists in MAS and is admin (idempotent). The sweep's `Plan` exempts
  exactly that username; every other unlinked user is still locked.
- **Personal sessions.** Per action: mint (two scopes, 5 minutes), call Synapse, revoke —
  revoke runs even when the call fails, on a context not tied to the request.
- **Rooms page** (new tab; Synapse admin API at the internal `http://synapse:8008`). List,
  paged and searchable: name, id, members, encrypted, public/invite-only, creator, size,
  blocked. Detail: members. Actions (fresh admin, audited):
  - **Close:** remove all members, block rejoin, keep history.
  - **Reopen:** unblock; members can be invited back, nobody is re-added.
  - **Delete permanently:** purge history and media; the admin types the room name.
  Audit actions `matrix.room_close`, `matrix.room_reopen`, `matrix.room_delete` (room id,
  admin, outcome). Delete is a Synapse background job: the row records it started; the page
  polls status.
- **Routes.** `GET /api/admin/matrix/rooms` (paged, `search`), `GET
  /api/admin/matrix/rooms/{id}` (admin); `POST /api/admin/matrix/rooms/{id}/close`,
  `/reopen`, `/delete` with `{"confirm":"<room name>"}` (fresh admin); `GET
  /api/admin/matrix/rooms/{id}/delete-status` (admin). Matrix off: 404.
- **Health.** A Synapse admin probe (mint and revoke a session) joins Health.

## Section 2: failures and proof

- Service account broken (locked, session refused): Rooms and Health say Synapse admin access
  is not working and why; no fallback credential.
- A session whose revoke fails still expires in 5 minutes.
- Delete in progress: "Deleting…" until complete or failed; a second delete of the same room is
  refused while one runs.
- Closing a closed room is success. Delete re-checks the confirmation against the room name
  server side.
- Member lists and room names are admin-only; messages are never shown.
- **Tests.** Service-account ensure (idempotent, admin); sweep exemption limited to that exact
  username; session mint (exact scopes, 5-minute lifetime) and revoke on success and failure;
  Synapse admin client against a fake (list, detail, close, reopen, delete, status); routes
  (role, freshness, audit, delete confirmation, Matrix off 404); vitest for the page, typed
  confirmation and status polling.
- **Acceptance.** Health shows Synapse admin up; the console lists the harness's group room
  as encrypted with members; Close removes Bob and refuses his rejoin; Reopen lets an invite
  work; Delete permanently of a separate throwaway room leaves no events for it in Synapse's
  database; audit rows present; `@kymessages-console` never locked by the sweep.
- **Browser regressions.** The Rooms tab across themes, widths and keyboard use.

## Out of scope

Settings and KyIdentity sync status (5c); room creation or membership editing; message
moderation; federation.

## Risks

- The whole approach is source-proven only; the plan proves it live first.
- A service account with Synapse admin is a standing high-value identity; it has no password
  or upstream link, sessions are minutes long, and every use is audited.
