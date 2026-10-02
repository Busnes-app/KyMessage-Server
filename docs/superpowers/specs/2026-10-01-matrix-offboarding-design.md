# Sub-project 3: Matrix offboarding

Date: 2026-10-01. Parent: `2026-10-01-matrix-platform-design.md` (decision 6). Builds on
sub-project 2 (`2026-10-01-matrix-stack-design.md`).

## Intent

Disabling, unassigning or deleting someone in KyIdentity cuts their Matrix access within
seconds, including sessions already open in Element; re-enabling restores it with rooms and
history intact. Every action is audited, and the cut is measured by the acceptance test, not
assumed.

## Decisions (owner-approved 2026-10-01)

1. Disable or unassign locks the MAS user; re-enable unlocks. Delete deactivates at once with
   `skip_erase` (the user leaves their rooms; their messages stay readable).
2. The cut is KyIdentity's OIDC back-channel logout into MAS (`logout_all`). KyIdentity queues
   it in the same transaction that revokes the user's tokens on disable, delete, unassign and
   session revoke, and retries it.
3. The existing signed directory webhook (`/api/sso/kyidentity/sync`) makes the cut stick
   through the MAS admin API. SCIM pairing was considered and rejected: KyIdentity's SCIM
   push never says "deleted" and its repair runs at most hourly.
4. A sweep repairs failed MAS calls and fails closed.

## Evidence

- MAS v1.26.0: `POST /api/admin/v1/users/{id}/{lock,unlock,deactivate}`; lock makes
  introspection reject the user's tokens and refuses sign-in, without revoking; deactivate ends
  all sessions, asks Synapse to deactivate (`erase` unless `skip_erase`) and is not undone by
  reactivate; `on_backchannel_logout: logout_all` on an upstream provider; `client_secret_file`
  on clients; admin scope `urn:mas:admin` granted only to `policy.data.admin_clients`.
- KyIdentity: `offboardUserTx` (`internal/store/offboarding.go`) runs `revokeUserAccessTx`,
  which queues back-channel logout per client session; `revokeLostAppAccessTx`
  (`internal/store/app_access.go`) does the same on unassign. ID tokens carry `sid`.

## Section 1: components

- **Cut (`matrix-init`).** The KyIdentity provider in `mas.yaml` gets
  `on_backchannel_logout: logout_all`. `matrix-init` prints MAS's back-channel logout URL next
  to the redirect URI for the KyIdentity client registration. No KyMessages code is on this path.
- **Admin access (`matrix-init`, `docker-compose.matrix.yml`).** `matrix-init` generates a MAS
  admin client (ULID, write-once 0600 secret file, `client_secret_basic`) and lists it alone in
  `policy.data.admin_clients`. MAS gets a second listener with `adminapi` and `oauth`, bound
  only on the internal network `matrix-admin`, which only `mas` and `kymessages` join; the
  token request and admin calls never cross `kymessages-net`. KyMessages receives
  `KY_MATRIX_ADMIN_URL`, `KY_MATRIX_ADMIN_CLIENT_ID` and `KY_MATRIX_ADMIN_SECRET_FILE`. With
  the Matrix settings present and admin access missing, KyMessages refuses to start.
- **Sync (`internal/matrixsync`).**
  - MAS client: client-credentials token cached to expiry and refetched on 401; list users and
    their KyIdentity upstream links (cursor pagination); lock, unlock, deactivate (always
    `skip_erase: true`).
  - Pure `Plan`: for each MAS user, by KyMessages' directory record of its KyIdentity subject —
    active: unlock if locked; inactive, no record or no link: lock; deleted (tombstone):
    deactivate. A deactivated user is never reactivated.
  - Loop in `cmd/server`: sweep every 5 minutes and once at start; the webhook handler records
    the change, wakes the loop without blocking and returns. Joins the existing shutdown drain.
  - No new table: MAS holds lock state; the directory records are the input.
- **Audit.** One row per action (`matrix.lock`, `matrix.unlock`, `matrix.deactivate`) with
  MXID, subject, reason and outcome. A failing sweep logs once per failure streak.
- **Known race.** A user who signs in before their `user.created` webhook arrives is locked by
  a sweep and unlocked when the webhook lands. It fails closed.

## Section 2: failure handling and proof

- KyMessages down: back-channel logout still cuts sessions and KyIdentity still refuses
  sign-in. The start-up sweep does not repair a missed disable: KyMessages never received it,
  so its directory still says active. KyIdentity holds a delivery interrupted by the outage as
  an uncertain write ("operator recovery required"), does not retry it and queues that user's
  later events behind it, until an operator resumes it in KyIdentity (allowed after 60 s). The
  lock then lands within seconds.
- MAS down or an admin call failing: the webhook is acknowledged once recorded locally; the
  sweep retries.
- Bad admin secret or scope: the sweep logs and retries (console health is sub-project 5).
- KyIdentity abandons a webhook that failed cleanly (retries at 30/60/120/240 s, then failed):
  the user has no sessions and cannot sign in, but stays unlocked in MAS until a KyIdentity
  resync. The sweep's retry covers failed MAS calls only. Docs tell admins to check the
  system's deliveries after any outage, resume held ones, then resync.
- **Tests.** `Plan` decision table; MAS client against a fake (token cache, 401 refetch,
  pagination, `skip_erase` body); webhook wakes the loop; start refused without admin access;
  shutdown drain; `matrix-init` (admin client, policy, listener, write-once secret,
  `logout_all`, printed URL); compose check (`matrix-admin` internal, only `mas` and
  `kymessages`, admin port not on `kymessages-net`).
- **Acceptance (`scripts/matrix-acceptance.sh`, real Element sessions).**
  1. Disable a signed-in user in KyIdentity: their open session is refused within 30 seconds
     (time recorded); a fresh sign-in is refused.
  2. Reactivate: they sign in again; rooms and history intact.
  3. Delete: deactivated, out of their rooms; the other user still reads their messages.
  4. KyMessages stopped during a disable: back-channel logout still cuts and MAS stays
     unlocked; the harness proves the webhook was missed, resumes the held delivery in
     KyIdentity, and the user is then locked.
  5. The admin port is unreachable from a container on `kymessages-net`.
  If the cut misses 30 seconds (Synapse token caching, missing `sid`), the cause is recorded
  and taken back to the owner; the bound is not loosened silently.

## Out of scope

Server capsule backups (4), console pages including offboarding health (5), SCIM changes,
KyIdentity code changes.

## Risks

- MAS listener binding by network alias, Synapse's token cache and MAS's `sid` capture are
  unproven until the acceptance test runs.
- KyIdentity's netguard may refuse a back-channel URL that resolves only to a private address
  (no issue behind cloudflared); checked during implementation.
- A held or abandoned webhook leaves a sessionless, sign-in-refused user unlocked in MAS until
  an operator resumes it or resyncs.
