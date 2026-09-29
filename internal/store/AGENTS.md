# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `DeviceStore`, `MessagingStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `MessagingStore.Usage` is a read-only operator snapshot over room metadata,
  including retired rooms and ciphertext awaiting cleanup. The ordered retained
  prefix makes `sequence - retained_from + 1` the active-event count; preserve this
  invariant when changing retention. One windowed SELECT returns global totals
  with at most 100 rooms, prioritizing rooms at 80% of any limit, then stored bytes.
  Never scan/return ciphertext or mutate retention to render usage. Append and
  reporting share the exported messaging capacity constants.
- `AuditStore.LatestAuditRecord(action)` reads the latest inserted row for one
  exact action, returning `ErrNotFound` when absent. Migration 13 indexes `(action,
  id)`; insertion order handles timestamp ties/backwards clocks without scanning
  unrelated activity. Backup status reads this append-only source, not a second
  shared last-result setting.
- `MessagingStore` owns migration 5's messaging device registry and room ACLs, separate from push/QR device pairing. Each operation rechecks the active suite-only account and live session in its transaction; device-gated operations additionally check the approved device token hash.
- Serialize messaging operations through a non-key update of the acting user row, then the session and relevant room/member rows. Keep the user update compatible with PostgreSQL foreign-key key-share locks; cross-invitations must not take a second account write lock.
- Only the first successfully verified device bootstraps trust. Retain verified-device tombstones after revocation so losing every device cannot silently bootstrap a replacement. Mutations and their success audits commit together.
- Migration 8 owns single-use recovery-authentication requests: at most four per
  account, five-minute expiry, hashed state and caller-sealed OIDC payload. Bind to
  the original session, pending device/key, suite subject and a sorted device-registry
  digest; derive bindings in the transaction. Recheck on read and completion, then
  delete and audit atomically. Session deletion cascades requests. Completion grants
  no device approval or reusable reset authority. Any registry change invalidates
  the snapshot. Migration 9 adds identity generations to devices, invitations and room
  ownership; reset consumes confirmed recovery state, revokes other devices, retires
  unclaimed packages, removes memberships and records an idempotent receipt atomically.
  A reset identity cannot inherit old room ownership or generation-bound invitations.
  Member listings retain reset notices while the old account is represented in the
  committed epoch; a removal commit clears that notice. They are server claims, not key verification.
- Migration 10 optionally binds KeyPackage publication to a room. Publishing into a
  room requires active membership; scope cannot change on retry. Claims prefer
  packages scoped to their room and may use legacy unscoped packages, but never
  another room's scoped material. Device lifetime/availability quotas remain shared.
- Migration 11 binds direct rooms to an immutable peer account ID. Creation and
  the peer invitation are one transaction; reject self and ineligible accounts.
  Owner invitations can only target that peer, including after removal or deletion.
  Keep the binding after peer deletion so a direct room cannot become a group.
- Migration 12 fixes each room's ciphertext retention at creation: 1, 7 or 30 days,
  default 30. Expiries stay ordered if the clock moves back. Clear expired event
  payloads and Welcomes under the room lock; retain hashes/sequence metadata so
  exact retries never append again. Keep the cursor monotonic and return
  `ErrMessagingHistoryGone` when a device missed the retained prefix. Ordinary
  remove/reinvite supplies a new generation and Welcome floor; never skip MLS state.
  Cap active data at 4,096 events/32 MiB and lifetime receipt rows at 1,000,000 per
  room. `ExpireMessages` sweeps idle rooms in short transactions; `store.Open`
  runs it after migration before returning, including restored databases. Close
  the database on failed initialization. These caps remain operational release gates.
- Room invitations require owner authorization and explicit recipient acceptance. Delivery and membership mutations lock the room after the actor/session. Each accepted invitation increments the member generation, preventing remove/rejoin from restoring old log access.
- `messaging_delivery.go` and migration 6 own the bounded event log, declared epoch CAS, device-specific Welcome envelopes and history floors. Each append checks the current eligible roster against the requested hash; application events additionally require the committed roster. Exact retries return the original receipt without another audit. MLS transcript validity remains the receiving client's responsibility. Wire limits and lifecycle semantics live in `docs/MESSAGING-API.md` at the repository root.
- `messaging_key_packages.go` and migration 7 own the content-addressed, bounded KeyPackage pool. Preserve claimed/expired rows as anti-republication tombstones, including after account deletion; their device IDs intentionally have no cascading foreign key. Claims require an eligible current-epoch device (or initial room owner), bind retries to caller/request ID, room, target and membership generation, and audit atomically. PostgreSQL claims lock a candidate with `FOR UPDATE SKIP LOCKED` so different rooms cannot allocate the same row.
- `CompletePasswordChange` atomically compares the old password, updates a flagged local account, clears the flag, deletes sessions/MFA challenges/device pairings and records `auth.password_changed`. Session/MFA issuance locks the same user row against the verified hash; MFA challenges persist the creation-time password hash, and consumption returns that snapshot to reject stale completions. Migration 4 discards preexisting challenges because their credential snapshot is unknown.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
- `InvalidateRestoredGrants` runs only on an offline restored database. Atomically
  delete sessions, MFA challenges, device pairings, messaging recovery requests and
  reset receipts; revoke every messaging device without deleting verified-key
  tombstones; expire all packages and remove all room memberships. Set restored
  room ownership generation to zero (identities are strictly positive), permanently
  retiring those rooms even after later resets. Preserve ciphertext/receipt metadata
  subject to ordinary retention and audit `restore.grants_invalidated`. Fresh suite
  sign-in, confirmed identity recovery and new rooms are required. Current-generation
  ownership alone counts toward the 100-room creation limit.
- SQLite file-URI directory setup decodes the URI path; never create a literal
  `file:` directory. This permits read/write-only restoration of paths with reserved
  characters without accidentally opening a different database.
- `UpdateProfile` writes only username, email, display name, role and status. SCIM and the KySignOn webhook use it so a stale read cannot revert a concurrent password change or recovery-code redemption. `ListUsers` filters by one exact `UserFilter` field.
- `GetLocalUserByUsername` returns only `sso_provider = 'local'` rows, preferring an exact-case match. Usernames are unique only case-sensitively, so password login and `init-admin` must never resolve an SSO row.
- Migrations 15 and 16 add `directory_sync_state` (provider, subject, last applied directory revision). It is deliberately not a users column: it outlives deletion as a tombstone. Migration 16 discarded earlier timestamp-based rows. `ApplyDirectoryProfile`, `CreateDirectoryUser` and `DeleteDirectoryUser` advance it with a conditional upsert and write the user in the same transaction.
- Migration 14 rebuilds `device_pairings` without the six-digit code column; pending pairings (90 s) are dropped on upgrade.
- `store.Open(ctx, cfg)` initializes and auto-migrates the configured database backend.
- SQLite runs in WAL mode with foreign keys enabled.
- PostgreSQL queries are rebound dynamically from standard positional parameters.
- MFA challenges and device pairings are consumed with database state transitions that permit exactly one successful use.
- Recovery-code hash updates use optimistic concurrency so simultaneous redemption cannot reuse a code.

## Verification
- `go test -v ./internal/store/...`
- `go test -race ./internal/store` checks first-device enrollment across separate connections and concurrent cross-invitations; run with `KY_TEST_POSTGRES_DSN` as well as the SQLite default.
- Delivery tests cover competing commits across connections, deduplication, Welcome isolation, removal/rejoin history floors, device revocation and directory deactivation.
- KeyPackage tests cover cross-room claims on separate connections, lost-ack retries, rejoin/expiry/revocation denial, publication ownership and pool capacity.

## Child DOX Index
None.
