# Backup

## Purpose
Adapts the scaffold to `github.com/Busnes-app/ky-primitives/recoveryclient`, which owns the
KyRecovery pairing, sealing, deposit, restore and drill contract. This package supplies only
what differs per product: a `Settings` adapter over `store.SettingsStore`, a `Sealer` under the
deployment key, the payloads the scaffold seals (`Collect` for people, `CollectMessages` for
messages), and each kind's drill checks (`Checks`, `MessagesChecks`).

## Ownership
Owns the settings adapter (`settings.go`), payload collection (`payload.go`, `messages.go`),
restore-drill checks (`drill.go`, `messages.go`), serialized drill entry point (`run_drill.go`) and the
offline messages import (`import.go`). It holds no private key, no share, and no pairing state of its own — those
live in `recoveryclient` and in the settings rows it reads and writes through the adapter.

## Local Contracts
- `Settings` maps `store.ErrNotFound` to `recoveryclient.ErrNotFound`; every other error passes
  through unchanged.
- `NewSealer` seals the KyRecovery token under the deployment key with label
  `ky_server_base:setting:kyrecovery_token`, domain-separated so a row copied from another
  setting will not open.
- `Collect` snapshots SQLite with the lib's `SQLiteSnapshot` (`VACUUM INTO`; the store runs in
  WAL mode, so a plain file read misses uncheckpointed commits) and returns
  `ErrNoDatabaseSnapshot` for any other driver, so a capsule without a consistent database is
  never sealed. On the owned snapshot only, `Collect` seals people alone: it empties every table in
  `MessagingTables` (child-first) and drops `messaging.*` audit rows; accounts, access, settings and
  other audit records stay. Compact the copy and reject it above the shared capsule file limit before
  reading it into memory. Metadata/receipt/audit growth remains capped at 64 MiB;
  initial snapshot disk space still scales with the complete live database. It also carries the encryption key (`data/encryption.key`, required — restores
  a database whose MFA secrets are gone otherwise) and the pinned recovery public key
  (`data/recovery.pub`, only when paired).
- `CollectMessages` is the opt-in messages capsule, from the same `snapshotFile` snapshot and
  the same driver refusal. Members: `data/messages/accounts.db` (identities, devices, rooms,
  members, epoch devices; devices only `approved`/`revoked`, never `pending`/`unverified`,
  with `token_hash` NULL and `challenge`/`enrollment_session` empty)
  and `data/messages/events-NNN.db` parts (events plus their Welcomes). Parts are contiguous
  per-room sequence ranges cut at `messagesPartBudget` (file cap minus 4 MiB), counting every
  column's bytes plus a per-row allowance; each part is compacted and refused above
  `MaxCapsuleFileBytes`. Past `MaxCapsuleTotalBytes`, or past the import's `maxEventParts`, it
  fails with `ErrCapsuleTooLarge` naming the three largest rooms. No deployment key, `ky_server.db`, KeyPackages, recovery-auth or
  reset receipts. Recipe `kind: "messages"`; every member is required and SQLite-checked.
  `MessagesChecks` requires the kind, accounts.db, at most `maxEventParts` event parts and every
  `.db` member in `sqlite_paths`, so no capsule `ImportMessages` would refuse is sealed or drilled.
- `ImportMessages(ctx, dbPath, openedDir)` imports an opened messages capsule into a
  people-restored, migrated SQLite database. Before `BEGIN IMMEDIATE` it refuses a target with
  messaging rooms, devices or identities (`ErrMessagingDataPresent`; `CheckMessagesTarget` is the
  read-only preflight), members other than `accounts.db`/`events-NNN.db`, more than 8 parts, and any
  member that is not a regular, non-symlinked file inside `openedDir`; then it attaches them
  read-only. It repeats the messaging-data refusal after `BEGIN IMMEDIATE`, before any write, so
  two concurrent imports cannot both succeed. The result must equal deleting every missing person under the schema's ON DELETE
  CASCADE rules: identities, devices and memberships of missing people and rooms of missing owners
  (with their events) go; epoch devices and Welcomes naming unimported devices stay. Devices keep
  status with `token_hash` NULL (approved = suspended, `suspended_at` = import time, starting
  the 30-day resume window; revoked keep 0); `retained_bytes` is recomputed.
  KeyPackages, recovery-auth, reset receipts and anything session-bound are never imported.
  `PRAGMA main.foreign_key_check` must be empty before COMMIT; the audit row
  `restore.messages_imported` carries the `ImportCounts` (`dropped_rooms` counts rooms not
  imported because their owner is missing).
- `MessagesSettings` prefixes only `backup_interval_sec`, `backup_last_attempt` and
  `kyrecovery_last_deposit` with `messages_`; pairing, token and key pin are shared with people.
  `MessagesRunConfig` puts local copies in `<backup dir>/messages/` because the lib prunes by app
  prefix with one keep count. `MessagesRunAction` is the audit action.
- `Checks(dir, opened)` reads the opened capsule's manifest, normalizes JSON lists and
  fails malformed or incomplete recipes. Required files include all capsule members and
  the database, settings and encryption key; SQLite integrity and required environment
  checks cannot be disabled. File checks accept only clean relative manifest members;
  SQLite opens read-only and missing/empty databases fail.
- HTTP and CLI call `RunDrill` with the kind's checks function; it holds an OS advisory lock on `<data dir>/drill.lock`
  across scratch preparation and the library drill. Contention returns `ErrDrillBusy`;
  closing the descriptor or process exit releases ownership. Keep the lock file in place.
  The Unix lock matches the Linux container deployment.
- `DrillRoot` is under the data directory and forced to 0700; opened payloads stay in the
  library's private, disposable subdirectories. Drills use throwaway keys, not custodian shares.
- `TestMessagingTablesListsEveryMessagingTable` holds `MessagingTables` equal to the migrated
  `messaging_%` tables; add a new messaging table there or it leaks into the people capsule.
- `Members` names what `Collect` would seal now, for the status route and the screen; keep
  the two in step.
- `GET /api/settings`'s `extra_settings` never carries `kyrecovery_token_enc` or a legacy
  plaintext `kyrecovery_token`; the filter drops every key with the `kyrecovery_token`
  prefix, so a rename in the lib cannot leak. An admin sees the paired `kyrecovery_url`,
  never the credential.
- Pairing, the write-once key pin, `Run` (one seal, every destination), the schedule, local
  copies and their pruning, drill mechanics, restore and the decrypt guard are the lib's;
  their contracts are in the `recoveryclient` README. `client_test.go` pins only what this
  package's wiring buys: `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` admits RFC1918 and CGNAT and
  nothing else.

## Verification
- The decrypt guard allows only `restore` and `restoreMessages` in `cmd/server/restore.go` to invoke
  suite-key capsule opening. HTTP/scheduled product code never receives shares.
- `go test -v ./internal/backup/...` covers decoded seal/open checks, malformed recipes,
  messages-capsule splitting, row accounting and total limit, the messages import and its refusals (budgets lowered via
  `export_test.go`; `seedMessagingFixture` is the shared messaging fixture),
  subprocess lock contention/exit, scratch cleanup and the synthetic v0.5.0 pairing fixture
  in `testdata/pairing-v050.json`. The fixture uses a 32-byte 0x01 deployment key and retains
  no recovery private key.

## Child DOX Index
None.
