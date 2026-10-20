# KyMessages installer boundary

Run `/app/kymessages apply-setup --file /app/data/installer/bundle.json` inside the selected
running container (any absolute path; stock Compose's writable data directory is
`/app/data`). `--check` compares retained authority/config and its runtime snapshot
without audits, account changes, migrations or key creation. `--capabilities` reads no
configuration. Setup CLI reads `KY_DATA_DIR` (default `./data`); it must match the server.

## With the stock Compose files

A refusal prints its reason, for example `product setup refused or is busy: installer refuses
existing SCIM configuration: ...`: the CLI reruns the server's configuration check in the
container. A refusal it cannot reproduce (another setup already applied, setup in progress)
points to the server log, where `docker logs kymessages` shows `[INSTALLER] setup refused: <reason>`. Each start re-applies
the stored setup, so a conflicting setting added later stops the server with
`Installer configuration unavailable: ...`. Setup refuses to replace an operator's choice:

- Leave `KY_SCIM_ENABLED` unset or `false` (SCIM is off by default; setup refuses it on), and
  `KY_BACKUP_KEEP` and `KY_BACKUP_DEPOSIT_INTERVAL` unset or equal to the setup's `backup`
  values; Compose passes them only when `.env` sets them. Setup turns SCIM off.
- `KY_KYIDENTITY_ISSUER`, `KY_KYIDENTITY_CLIENT_ID`, `KY_KYIDENTITY_SECRET` and
  `KY_KYIDENTITY_HMAC_SECRET` must be unset or equal the setup's values. The Matrix overlay
  requires `KY_KYIDENTITY_HMAC_SECRET`: set it to the content of the setup's directory secret file.
- `KY_BACKUP_DIR` is `/app/backups`, the container's only writable backup location; a setup
  `backup.directory` must be that or omitted.

The common `ky.product-setup/1` service is `kymessage`, distinct from the native product and
capsule name `KyMessages`. Inputs include `setupId` UUID, `sso` (canonical HTTPS issuer,
client ID, protected absolute client/directory credential paths, exact product URL plus
`/api/sso/kyidentity/callback`), at least one dedicated `admins` identity and optional
`everyday` identities. Every identity has exact issuer, subject and a valid unique username.
Files must be process-owned regular 0600 files; inputs contain references, never secrets.
Use a dedicated console OIDC client, assigned users only, with a directory webhook that
sends app role `kymessage.admin` for console administrators.

Configuration, new subjects, ownership and native SQL audit commit together. Existing
account names/subjects and conflicting configuration are refused, never adopted. The full
envelope is immutable; a rerun inspects it without duplicate accounts or audits. A drifted,
offboarded or missing owned identity requires explicit resolution and is never repaired.
Committed configuration awaiting runtime activation is `pending: activation/restart`.
`apply-setup --restart` requests graceful server shutdown through private IPC. The existing
HTTP/background/Matrix/detached-work drain remains intact; the workload restart policy must
start the server again. KyQuickStart verifies the activated snapshot before success.

Sign-in requires a verified ID token and exact owned issuer/sub/user/role. Global admin
claims, email and username do not grant access. Missing protected credentials fail closed.
Independent local recovery administrators keep their native login/password-replacement rules.
Ordinary accounts receive no console admin grant. Dedicated managed administrators have no
chat link, are inactive in the Matrix access projection, and cannot receive call grants;
MAS sweeps lock their upstream-linked chat accounts. Central KyIdentity composition must
also exclude dedicated administrators from MAS assignments: a sweep is not instantaneous
sign-in revocation. Existing mixed-use identities are refused rather than migrated by setup.

`POST /api/installer/directory` (also managed `/api/sso/kyidentity/sync`) accepts signed SCIM
User desired state. Canonical positive `meta.version` revisions and body/event digests are
retained transactionally. Exact retries succeed without mutation; stale/conflicting revisions
fail. Offboarding and `user.mfa_reset` revoke sessions, MFA challenges and device pairings with
their audit, and replace the account's credential snapshot in the same transaction, so a sign-in
or pairing already in flight cannot issue a grant afterwards. Offboarding also wakes the native
Matrix sweep. Deletion retains `deleted` status so MAS deactivates without
erasing messages; later activation is refused. The native console service account is separate.
A subject setup did not create is an ordinary chat member, applied as the native directory
does (per-subject revisions, deletion kept, a username another account holds refused with 409):
role `user` whatever roles arrive, and no console sign-in, which admits owned identities only.
Only these signed events create members; while delivered active they keep chat, and an
inactive or deleted event has the sweep lock (and end every session of) or deactivate them.

`GET /api/installer/network` exposes only client address, header trust and scheme, with
`no-store`. It shares `KY_TRUSTED_PROXIES` resolution with native rate/session/audit decisions.

Managed sealed backups require client/directory credentials and their original destination
map. Restore those protected files to their recorded locations before restarting the console.
Restore drills inspect the retained database and cannot omit credentials through a recipe.
The console and the CLI `deposit`, `export-capsule` and `backup-drill` load the stored setup
and seal these credentials. `backup-drill` exits non-zero when any check fails.

Matrix stack generation/deployment, MAS client/assignment composition, per-part domains and
media prerequisites, installer recovery enrollment, existing mixed-use migration, complete
Matrix acceptance and image/library provenance remain release/deployment gates. The common
setup package comes from ky-primitives v0.10.0. This adapter
has changed no live deployment and does not claim a complete installed chat stack.
