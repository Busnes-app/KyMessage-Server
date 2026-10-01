# Sub-project 1: remove the custom messaging stack

Date: 2026-10-01. Parent: `2026-10-01-matrix-platform-design.md` (decision 3).
Precondition: PR #9 (KyIdentity rename, migration 20) is merged; this branch starts from it.

## Intent

Delete the superseded custom MLS chat stack completely, leaving a smaller server that does
identity, console, people backups and proxy deployment correctly, ready for the Matrix stack.
Nothing is deployed, so no user data is at risk. Git history keeps everything.

## Removed

- **Directories:** `chat-core/`, `mls-proof/` (with their AGENTS.md files).
- **Go:** `internal/api/messaging_*.go` (+ tests), `internal/store/messaging*.go` (+ tests),
  `internal/store/migrations/messaging.go`, `internal/backup/messages*.go`, `import*.go`,
  `cmd/server/messaging_maintenance*.go`, `scripts/rehearsal/`. Edits in `server.go`
  (messaging routes, live registry, `/api/backup/messages/*`, `/api/admin/messaging/*`,
  `X-KyMessages-Device` CORS header), `backup_handlers.go` (messages kind and status block),
  `cmd/server/main.go` (`restore-messages`, maintenance loop, `StopMessaging`, messages kind,
  `-messages` flag), `cmd/server/restore.go` (messages-capsule refusal and hint,
  `restoreMessages`), `internal/backup/settings.go` (messages settings), `internal/store`
  (`Messaging()` from the interface, startup `ExpireMessages`), `internal/config`
  (`Messaging` config, `KY_MESSAGING_IDENTITY_RESET_ENABLED`), and the matching tests.
  `github.com/coder/websocket` leaves `go.mod`.
- **Web:** `MessagingUsage`, `SuspendedDevices`, `browserSupport` (+ tests); the message
  backup section of `Backup.tsx`; `MyAccount.tsx` (see below); browser-regression assertions
  for messaging storage and My account.
- **CI/scripts:** the `messaging-proof` job, the chat-gate and rehearsal steps (ci.yml,
  Makefile), `scripts/check-chat-gate.sh`, `scripts/restore-messages-rehearsal.sh`, the
  messages checks in `scripts/backup-acceptance.py`, `/mls-proof/` and `/chat-core/` in
  `.dockerignore`.
- **Docs (deleted):** `MESSAGING-API.md`, `MLS-INTEROP-RESEARCH.md`, `MLS-LIBRARY-RESEARCH.md`,
  `MLS-PROOF-HANDOFF.md`, `MESSAGING-BACKEND-HANDOFF.md`, `MESSAGING-LOAD.md`,
  `BROWSER-EVIDENCE.md`, `BROWSER-SUPPORT.md`, `FIRST-RELEASE-PLAN.md` (the Matrix design's
  sub-project list replaces it). `docs/superpowers/` history is kept.

## Changed, not removed

- **Database:** a new migration (21) drops the ten `messaging_*` tables (children first:
  `messaging_events`, `messaging_welcomes`, `messaging_epoch_devices`, then the rest), deletes
  `audit_records` rows with `action LIKE 'messaging.%'`, and deletes the settings keys
  `messages_backup_interval_sec`, `messages_backup_last_attempt`,
  `messages_kyrecovery_last_deposit`. Idempotent (`DROP TABLE IF EXISTS`), SQLite and
  Postgres. Migrations 5–12 and 17–19 stay (append-only); a fresh database creates then drops
  the tables. Test: a pre-migration database with rows in every messaging table migrates
  cleanly, twice.
- **People backup:** `payload.go` drops the `MessagingTables` emptying loop and its guard
  test; keeps the snapshot `VACUUM`.
- **Restore:** `InvalidateRestoredGrants` keeps sessions, MFA challenges, device pairings and
  its audit row; the messaging statements go.
- **Console for non-admins:** members no longer have anything to do in the console (chat will
  be Element). `MyAccount` is replaced by a short page: account name, Sign out, and "Chat
  isn't available yet." (the chat link arrives in sub-project 2). Admins keep their pages; the
  "My account" nav entry goes.
- **Partly kept docs:** `KYMESSAGES-PROTOCOL-RESEARCH.md` keeps "OCM and Nextcloud Talk" and
  "Calling, SFrame and connectivity", drops the MLS sections. `RESTORE.md` drops "Restore
  messages" and messaging mentions. `PRODUCT.md` drops the MLS/custom-backend sections ("Keys,
  devices, and history", "Retention and recovery", "Architecture on this base", "Delivery
  requirements") and points to the Matrix design; the product-level sections stay for
  sub-project 2 to reword. README and all AGENTS.md files drop messaging text; root
  AGENTS.md's child index loses chat-core and mls-proof.

## Unchanged

KyIdentity sign-in and directory webhook (messaging rows only went by `ON DELETE CASCADE`),
QR device pairing (`internal/devices`, unrelated), dashboard counts, people capsule,
KyRecovery pairing, proxy overlay and network self-check, `WaitDetached`/`tracked` backup
machinery.

## Verify

`grep -rni 'messaging\|mls\|chat-core\|mls-proof'` leaves only: the migration and its test,
historical `docs/superpowers/`, the `kymessages-net` network name and product naming. `make
ci`, Postgres `go test ./...`, console browser suite, `scripts/backup-acceptance.py`,
`scripts/check-compose-proxy.sh`. The people backup and restore drill pass.
