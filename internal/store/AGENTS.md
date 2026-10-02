# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `DeviceStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `AuditStore.ListAuditRecords(offset, limit, prefixes...)` pages newest first by `id`; prefixes match the
  literal action start (`substr`, not LIKE) and the count covers only matching rows.
- `AuditStore.LatestAuditRecord(action)` reads the latest inserted row for one
  exact action, returning `ErrNotFound` when absent. Migration 13 indexes `(action,
  id)`; insertion order handles timestamp ties/backwards clocks without scanning
  unrelated activity. Backup status reads this append-only source, not a second
  shared last-result setting.
- `CompletePasswordChange` atomically compares the old password, updates a flagged local account, clears the flag, deletes sessions/MFA challenges/device pairings and records `auth.password_changed`. Session/MFA issuance locks the same user row against the verified hash; MFA challenges persist the creation-time password hash, and consumption returns that snapshot to reject stale completions. Migration 4 discards preexisting challenges because their credential snapshot is unknown.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
- `InvalidateRestoredGrants` runs only on an offline restored database: atomically
  delete sessions, MFA challenges and device pairings and audit `restore.grants_invalidated`.
  Users sign in again.
- Migrations 5–12 and 17–19 built the retired messaging stack; migration 21 drops its tables
  (children first), `messaging.*` audit rows and the three `messages_*` backup settings, and
  is idempotent. Migrations are append-only: keep their SQL.
- SQLite file-URI directory setup decodes the URI path; never create a literal
  `file:` directory. This permits read/write-only restoration of paths with reserved
  characters without accidentally opening a different database.
- `UpdateProfile` writes only username, email, display name, role and status. SCIM and the KyIdentity webhook use it so a stale read cannot revert a concurrent password change or recovery-code redemption. `ListUsers` filters by one exact `UserFilter` field.
- `GetLocalUserByUsername` returns only `sso_provider = 'local'` rows, preferring an exact-case match. Usernames are unique only case-sensitively, so password login and `init-admin` must never resolve an SSO row.
- Migrations 15 and 16 add `directory_sync_state` (provider, subject, last applied directory revision). It is deliberately not a users column: it outlives deletion as a tombstone. Migration 16 discarded earlier timestamp-based rows and added `directory_sync_events`, the permanent set of delivered directory event IDs per provider (applied or superseded). It grows by one row per directory change, which is small for the target team size. `ApplyDirectoryProfile`, `CreateDirectoryUser` and `DeleteDirectoryUser` advance it with a conditional upsert and write the user in the same transaction.
- `DirectoryStatuses(provider)` maps each subject to its user status, or `deleted` for a tombstone row with no user; `matrixsync` plans from it. Subjects are not unique: of several rows, a non-active status wins (fail closed).
- Migration 20 renames the stored suite provider `kysignon` to `kyidentity` in `users.sso_provider`, `directory_sync_state` and `directory_sync_events`; its SQL is idempotent.
- Migration 14 rebuilds `device_pairings` without the six-digit code column; pending pairings (90 s) are dropped on upgrade.
- `store.Open(ctx, cfg)` initializes and auto-migrates the configured database backend.
- SQLite runs in WAL mode with foreign keys enabled.
- PostgreSQL queries are rebound dynamically from standard positional parameters.
- MFA challenges and device pairings are consumed with database state transitions that permit exactly one successful use.
- Recovery-code hash updates use optimistic concurrency so simultaneous redemption cannot reuse a code.

## Verification
- `go test -v ./internal/store/...`
- `migrations/migrations_test.go` builds a v16 database through the test-only
  `RunThrough` seam (`export_test.go`) and checks migrations 17, 19, 20 and 21 on both engines.

## Child DOX Index
None.
