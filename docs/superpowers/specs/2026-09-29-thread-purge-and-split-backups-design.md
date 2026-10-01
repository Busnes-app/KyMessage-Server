# Thread auto-purge and split people/message backups

Status: design approved in conversation 2026-09-29; this spec awaits review.

## Intent

- Threads (messaging rooms) choose how long the server keeps their messages, and purged
  messages are actually gone, metadata included.
- Backups protect **people** by default: accounts, access and settings come back after
  a server loss. Messages are a separate, opt-in capsule with their own restore step,
  and threads restored that way keep working for users whose browsers still hold keys.

What the user decided:
- purge choices Off, 1, 7, 30 or 90 days, default 90
- the owner may change it at any time
- a purge removes text and metadata
- two capsules
- restored devices must re-prove before they resume

Assumptions are marked **(assumed)**.

## 1. Per-thread auto-purge

### Behaviour
- `messaging_rooms.retention_days` accepts `0` (Off), 1, 7, 30 or 90. New rooms default
  to 90. Existing rooms keep their current value.
- `PATCH /api/messaging/rooms/{room}` `{retention_days}` changes it. It requires an
  approved device and an active membership, and the caller must be the owner at the
  current identity generation. In a direct room either participant may change it
  **(assumed: a direct room has no meaningful owner/guest split)**.
  - Each change is audited as `messaging.retention_changed` and wakes live streams.
  - Room DTOs and delivery state already expose `retention_days`; clients display it.
- A purge deletes, for every event whose age (`now - created_at`) exceeds the window:
  - the event row
  - its Welcomes
  - its retry receipts
  - its `messaging.event_accepted` audit row
  It then advances the existing `retained_from` floor past the purged prefix. Purges
  are always a prefix: events are deleted oldest first, so the floor stays exact.
- **Off** never purges.
- **Shortening** the window takes effect at the next sweep, within one minute.
  **Lengthening** only affects events not yet purged. Deleted messages never return.
- Clients behind the floor keep today's behaviour: 410 `history_expired` and an
  explicit gap.

### Consequences to accept
- **Retries of purged events.** Once receipts are gone, a client retrying a purged
  event would append it again. Clients therefore discard pending sends older than their
  room's window before retrying. The isolated client already bounds pending sends; this
  makes it explicit. A stale retry at an older epoch is still refused by the epoch
  check.
- **Expiry timestamps.** `expires_at` on events becomes derived as
  `created_at + window`, or absent when Off, instead of stored at append time. A
  changed window changes it for events not yet purged.

### Storage quotas (approved)
Today a room refuses writes at 4,096 active events or 32 MiB, which Off would hit.
- The limits become quotas of **100,000 active events and 512 MiB per room**. The
  lifetime receipt cap of 1,000,000 stays; receipts are now purged with their events.
- A full room refuses appends with the existing 409 capacity error. The owner must
  turn purge on or shorten it.
- The admin usage page shows the new limits and lists rooms near them.
- The per-account daily cap of 5,000 appends stays.

### Migration
- A migration rewrites the retention CHECK constraint to allow 0, 1, 7, 30 and 90.
  SQLite needs a table rebuild; Postgres needs a constraint swap.
- The sweep changes from "expires_at passed" to "created_at older than the room
  window".
- The same migration drops `messaging_events.expires_at` and its index, because
  expiry is now derived.

## 2. People and message backups

### People capsule (the existing capsule, narrowed)
- **Contents:** the SQLite snapshot with **every messaging table emptied** and
  `messaging.*` audit rows removed, plus the deployment key, pinned recovery key and
  settings reference, as today.
  - Messaging tables: `messaging_devices`, `rooms`, `members`, `events`,
    `epoch_devices`, `welcomes`, `key_packages`, `identities`, `recovery_auth` and
    `reset_receipts`.
  - People data kept: accounts, roles, MFA, SSO links, SCIM groups, settings,
    non-messaging audit and `directory_sync_state`.
- **Schedule:** unchanged; it keeps the existing admin schedule.
- **Restore:** today's `restore` command. Users come back with no messaging devices or
  threads. The existing grant invalidation still runs, and the tombstone rules for
  first-device approval stay. Messaging identity rows are absent, so the first device
  after a people-only restore bootstraps as on a new server **(assumed acceptable:
  there are no threads to protect)**.

### Messages capsule (new, opt-in)
- **Contents:** the messaging tables only, with retained ciphertext, devices and
  identities, as SQLite files under `data/messages/`.
  - `accounts.db` holds identities, devices, rooms, members and epoch devices. Only
    approved and revoked devices are exported, with no token, challenge or enrollment
    session. No deployment key, KeyPackages, recovery-auth or reset receipts.
  - `events-NNN.db` parts hold events and their Welcomes as contiguous per-room
    sequence ranges, so a large room spans parts. A part is cut below the 64 MiB
    per-file limit and refused above it after compaction.
  - Beyond the 256 MiB capsule total, the backup fails with an explicit error naming
    the largest rooms. The admin can shorten purge windows. Import accepts at most 8
    event parts.
- **Manifest:** carries `kind: "messages"` and the people-capsule-compatible app
  version.
- **Schedule:** its own admin schedule, **default off**. KyRecovery deposits reuse the
  same pairing, token and key pin. The capsule's schedule, last attempt and last
  receipt live under a `messages_` settings prefix through a wrapping `Settings`
  adapter, so the two kinds never overwrite each other's state.
- **Status and UI:**
  - The backup status route and screen show both kinds side by side: schedule, last
    run and last receipt.
  - "Back up messages" is a separate switch and schedule.
  - The drill runs per kind.

### Restoring messages
- **Order:** `restore-messages -capsule msgs.kycap -into <people-restored dir>` runs
  offline after a people restore and before serving. It verifies the capsule like
  `restore`, custodian shares on stdin, and refuses a capsule that is not a messages
  capsule (no `data/messages/accounts.db`, or a people database).
- **Import:** it imports the messaging rows into the restored database in one
  transaction.
  - Memberships, invitations and devices whose user ID is missing from the people
    database are dropped.
  - A room whose owner is missing is not imported, with its events, and is counted
    as `retired_rooms`; the result matches deleting the missing people under the
    schema's ON DELETE CASCADE rules.
  - Every imported approved device becomes **`suspended`** (token NULL); revoked
    devices stay revoked, and every session stays invalid.
  - KeyPackages, recovery-auth requests and reset receipts are not imported.
  - The import is audited as `restore.messages_imported`, with counts of dropped rows.
- **Pairing mismatch:** a messages capsule older or newer than the people capsule is
  allowed. Anything that no longer references a restored person is dropped, and the
  output reports it.

### Resuming a suspended device
- `POST /api/messaging/devices/{id}/resume` requires a suite session from the last 10
  minutes, the same step-up window as backups, on the owner's own suspended device at
  the account's current identity generation, and proposes a new token hash.
  - The server issues a challenge, and the device signs it with its Ed25519 enrollment
    key.
  - `.../resume/verify` rechecks the 10-minute window. On success the new token
    becomes active, the device returns to `approved` at its identity generation, and
    the resume is audited.
  - Revoked devices cannot resume.
- An admin view lists suspended devices, and admins can revoke any before users
  return. Suspended devices cannot read, append or approve.
- **Threads:**
  - A thread resumes for a device whose MLS state matches the restored epoch.
  - A thread that advanced after the snapshot stays paused for clients that are
    ahead, and the existing "start a new thread" outcome applies.
  - The server never rewinds client state.
- **Isolated client (`mls-proof`):** detect `suspended`, run fresh sign-in and resume,
  and then continue normally or show the paused/new-thread outcome.

### Docs and policy
- `docs/RESTORE.md` and `docs/PRODUCT.md` change from "restored rooms are always
  retired" to the two-step policy above. The release gates stay explicit.
- The independent MLS review still has to assess resumption.

## Testing
- **Purge:**
  - Each choice, purging on a real sweep.
  - Changing the window: shorten purges and lengthen doesn't resurrect.
  - Only the owner, or either participant in a direct room, may change it.
  - Receipts, Welcomes and audit rows are gone after a purge.
  - 410 for a cursor behind the floor.
  - The quota refuses writes at the new limits.
  - SQLite and Postgres.
- **People capsule:** no messaging rows or `messaging.*` audit rows are sealed.
- **Messages capsule:**
  - Splitting across files.
  - Over-limit failure.
  - The kind check.
  - The two kinds' schedules and receipts stay independent, including in
    `scripts/backup-acceptance.py`.
- **restore-messages:**
  - Imports into a people-restored database.
  - Drops rows for missing people.
  - Drops ownerless rooms.
  - Suspends every approved device.
  - A resume with a stale session or the wrong key fails; a correct one succeeds.
- **Browser:** the isolated OIDC suite covers resume after restore on an unchanged
  thread, and the paused outcome on an advanced one.

## Out of scope
- Restoring or rewinding browser MLS state. Server-side decryption. Per-message
  deletion. Changing KyRecovery.
