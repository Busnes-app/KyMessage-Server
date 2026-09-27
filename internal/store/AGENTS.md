# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `DeviceStore`, `MessagingStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `MessagingStore` owns migration 5's messaging device registry and room ACLs, separate from push/QR device pairing. Each operation rechecks the active suite-only account and live session in its transaction; device-gated operations additionally check the approved device token hash.
- Serialize messaging operations through a non-key update of the acting user row, then the session and relevant room/member rows. Keep the user update compatible with PostgreSQL foreign-key key-share locks; cross-invitations must not take a second account write lock.
- Only the first successfully verified device bootstraps trust. Retain verified-device tombstones after revocation so losing every device cannot silently bootstrap a replacement. Mutations and their success audits commit together.
- Migration 8 owns single-use recovery-authentication requests: at most four per
  account, five-minute expiry, hashed state and caller-sealed OIDC payload. Bind to
  the original session, pending device/key, suite subject and a sorted device-registry
  digest; derive bindings in the transaction. Recheck on read and completion, then
  delete and audit atomically. Session deletion cascades requests. Completion grants
  no device approval or reusable reset authority. Any registry change invalidates
  the snapshot; identity generations remain a future reset requirement.
- Room invitations require owner authorization and explicit recipient acceptance. Delivery and membership mutations lock the room after the actor/session. Each accepted invitation increments the member generation, preventing remove/rejoin from restoring old log access.
- `messaging_delivery.go` and migration 6 own the bounded event log, declared epoch CAS, device-specific Welcome envelopes and history floors. Each append checks the current eligible roster against the requested hash; application events additionally require the committed roster. Exact retries return the original receipt without another audit. MLS transcript validity remains the receiving client's responsibility. Wire limits and lifecycle semantics live in `docs/MESSAGING-API.md` at the repository root.
- `messaging_key_packages.go` and migration 7 own the content-addressed, bounded KeyPackage pool. Preserve claimed/expired rows as anti-republication tombstones, including after account deletion; their device IDs intentionally have no cascading foreign key. Claims require an eligible current-epoch device (or initial room owner), bind retries to caller/request ID, room, target and membership generation, and audit atomically. PostgreSQL claims lock a candidate with `FOR UPDATE SKIP LOCKED` so different rooms cannot allocate the same row.
- `CompletePasswordChange` atomically compares the old password, updates a flagged local account, clears the flag, deletes sessions/MFA challenges/device pairings and records `auth.password_changed`. Session/MFA issuance locks the same user row against the verified hash; MFA challenges persist the creation-time password hash, and consumption returns that snapshot to reject stale completions. Migration 4 discards preexisting challenges because their credential snapshot is unknown.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
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
