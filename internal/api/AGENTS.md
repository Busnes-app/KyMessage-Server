# API

## Purpose
Exposes HTTP REST routes, authentication endpoints, Single Sign-On callbacks, SCIM endpoints, backup restore drill handlers, and static React PWA hosting.

## Ownership
Owns HTTP routing, request parsing, session cookie validation, CORS headers, and error response formatting.

## Local Contracts
- `GET /api/admin/messaging/usage` is admin-only (separate from suite member
  messaging access), `no-store`, with a five-second query deadline. Return totals,
  shared per-room limits, a bounded room metadata list and sample time. Exclude
  ciphertext, device credentials, membership and audit contents. Failed reads
  return an error, never zero usage. Role regression coverage includes this route.
- `/api/messaging/` routes use `requireMessaging`: suite OIDC only (persisted provider `kysignon`, no local password), live session, matching browser Origin and account rate limits. Existing cookie CSRF applies. `X-KyMessages-Device` supplements the session for approval and room operations; enrollment, listing and revoking one's own devices need only the suite session.
- Messaging GETs and writes have separate per-account process-local budgets:
  2,400 reads/minute and 120 writes/minute. Receiving a busy room cannot exhaust
  device-management/sending capacity. Enrollment retains its 10/5-minute cap. Event
  appends are also capped at 5,000 per account per 24 hours (`messagingDailyEventLimit`).
- `messaging_handlers.go` owns bounded JSON parsing, Ed25519 enrollment challenges and public DTOs. `store.Messaging()` rechecks authorization transactionally. Follow `docs/MESSAGING-API.md` at the repository root for the wire contract; never expose device token hashes or session bindings in device listings.
- Room creation accepts optional `peer_user_id` for a direct conversation. Validate
  it like invitation account IDs; return the immutable binding in room DTOs. The
  store atomically invites that peer and enforces the two-account boundary.
  `retention_days` defaults to 30 and accepts only 1, 7 or 30; it is immutable for
  the room. Delivery exposes retention/floor metadata and event expiry timestamps.
  A missed expired prefix returns 410 with `code:history_expired`, never a partial
  ciphertext page that could be mistaken for complete MLS history.
- `messaging_recovery.go` owns recovery-authentication initiation and callback.
  Initiation requires the target pending device's token; callback keeps the original
  suite session and never issues another. Seal server-owned OIDC state with the
  `messaging-recovery-auth` derived key. Initiation sets a
  callback-scoped HttpOnly Lax binder cookie whose hash is sealed; the callback requires it,
  so a stolen session cannot finish a flow in the owner's browser. Check saved
  account/session/device/subject, callback/state and creation time before exchange; the store rechecks bindings and
  atomically consumes after verification. Explicit `confirm_identity_reset` binds the reset intent in both sealed state and
  the store; the default-off config gate is checked at initiation and callback.
  Reset consumes fresh authentication in the atomic reset transaction. Its session-bound
  receipt supports callback retry without another mutation; HTML success redirects
  only to `/`. Authentication-only success never becomes a later reset grant.
- `messaging_live.go` owns WebSocket wakeups for one active room per connection.
  Authenticate the suite session before upgrade, require the exact configured origin
  and no query, then receive the 64-byte hex device credential as the first text frame
  within five seconds. Never put credentials in URLs/subprotocols or emit message bodies.
  Recheck session/device/ACL before each notice; send only sequence, epoch and roster
  hash. Each connection owns a one-slot signal; cap four per account and 256 per server.
  Mutations wake readers after commit; a 15-second heartbeat catches missed changes
  and external/session revocations. Network and database operations have five-second
  deadlines. Extra data frames close the stream; compression stays disabled.
  Register through `tracked` before authentication. `StopMessaging` rejects new
  registrations and cancels existing streams before HTTP shutdown; `WaitDetached`
  drains them before the store closes. HTTP cursor reads remain the durable source.
- `messaging_delivery.go` adds device-gated room state and event append/read routes. Canonicalize and bound base64 envelopes at the HTTP boundary; responses expose only the caller's Welcome. A successful append acknowledges durable opaque storage, not cryptographic validation or recipient delivery.
- `messaging_key_packages.go` accepts device-authenticated publication (canonical base64, 16 KiB decoded, expiry within seven days, optional canonical `room_id`) and room-authorized POST claims. Scoped publication requires active room access; caller JSON cannot move a published package to another room. The store derives the publishing device from its credential; JSON cannot choose an owner. MLS parsing, credential/key binding and signed lifetime validation remain client responsibilities.
- POST `/api/auth/change-password` accepts a restricted local session, current password and a different policy-valid new password. Browser CSRF and per-IP/account limits apply. Success revokes all sessions and requires sign-in again; flagged sessions get `password_change_required` on protected routes and public-only settings.
- All JSON API endpoints return structured errors `{"error": "message"}` upon failure.
- `/api/auth/me` returns `Cache-Control: no-store` for both signed-in and anonymous
  responses so browser device setup never uses cached account status.
- Non-API routes fall back to serving `web.Handler()` for client-side SPA routing.
- New routes are unauthenticated only by deliberate choice; privileged ones are registered wrapped in `s.requireAdmin` in `routes()`, so the trust level of every route is readable in one place.
- Export and drill collection return 413 for oversized snapshots; export also
  handles the same error from sealing.
- Backup capsule/status versions use `config.AppVersion`, shared with the CLI.
- Backup status includes the latest recorded `admin.backup_run` as `last_run`:
  outcome, trigger, recorded timestamp and capsule ID only. Classify the known audit
  prefix; legacy formats are unknown and partial destination/receipt failures are
  warnings. Keep raw audit/error details out of this DTO. A failed audit lookup is
  a visible `last_run_error`, not an invented success or empty history. This result
  is separate from the last successful remote receipt.
- Backup routes and theme writes are admin-only: capsules and settings carry site data and secrets. Export, pair-remote, deposit, unpair, pin-key and schedule use `requireFreshAdmin`: step-up is a sign-in from the last 10 minutes (`stepUpWindow`), which repeats password plus TOTP or the suite login for local and SSO admins alike; older sessions get 403 `reauthentication_required`. `Session.CreatedAt` is the credential time: a session minted by `pair/verify` inherits the initiating session's time via `IssueDerivedSession`, so pairing cannot refresh an old session. Drill, status and usage stay `requireAdmin`. The pin is write-once, so a stolen session pinning its own key first would own every later capsule. Routes are registered with method patterns, and because the SPA catch-all answers any method, tests pin that a wrong method never reaches a backup handler rather than expecting 405.

| Method | Path | Handler | Response |
|---|---|---|---|
| POST | `/api/backup/drill` | `handleBackupDrill` | `recoveryclient.DrillResult`; 409 when another HTTP/CLI drill holds the data-directory lock |
| POST | `/api/backup/export-capsule` | `handleExportCapsule` | `.kycap` attachment; POST so the CSRF check covers it |
| POST | `/api/backup/pair-remote` | `handlePairRemoteRecovery` | `{recovery_key_id, threshold, total_shares}` |
| POST | `/api/backup/deposit` | `handleRunBackup` | `recoveryclient.Result` (+`receipt_unrecorded`) |
| DELETE | `/api/backup/pairing` | `handleUnpair` | `{paired:false}`; URL and token rows only, key pin stays |
| POST | `/api/backup/pin-key` | `handlePinKey` | write-once; 409 on a different key |
| PUT | `/api/backup/schedule` | `handleSetSchedule` | `{interval_sec}` read back from the store |
| GET | `/api/backup/status` | `handleBackupStatus` | pairing, key, local copies, schedule, members, `database_driver`; never the token |

- `POST /api/backup/deposit` is one `recoveryclient.Run`: seal once, deliver to the local directory and to KyRecovery when paired. 412 no key, key pin missing, no destination, no database snapshot, or a private destination with `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` off; 409 key mismatch or a run in flight; 413 over the capsule caps; 502 when KyRecovery refused (`recoveryclient.ErrRemote`, naming a local copy that was written, so the `ErrPrivateDestination` arm must stay above it: the lib wraps both on the dial path); 500 for a failure before a byte left; 200 with `receipt_unrecorded` when the store holds the capsule but the receipt was not written. It runs on a context detached from the request with a 16-minute write deadline; the acting admin is resolved before the upload and the audit row is written on that same detached context.
- The write-once or irreversible backup handlers (`handlePairRemoteRecovery`, `handlePinKey`, `handleRunBackup`, `handleUnpair`) run on `context.WithoutCancel(r.Context())` so a dropped connection cannot leave a pin, a pairing, a deposit or an unpair half-written with no audit row; `handleSetSchedule` stays on the request context. Every failure branch after input parsing is audited. Their routes are registered as `s.tracked(s.requireFreshAdmin(s.handleX))`, so the `detached` counter is incremented the moment `ServeHTTP` dispatches -- before `requireAdmin`'s session lookup, which is itself a store round-trip that `ReadTimeout` (15s) lets outlast `shutdownTimeout` (5s). Registering inside the handler was too late: `Shutdown` returns after its timeout with requests still active, and one still in the auth lookup would leave `WaitDetached()` reading zero and the store closing under a request about to pin a key. The header-read window before `ServeHTTP` is entered cannot be covered by any counter, because no handler goroutine exists yet; `Shutdown`'s own drain is what covers it. The counter is a mutex and a `sync.Cond`, not a `sync.WaitGroup`, which panics when an `Add` from zero races an in-progress `Wait` -- two admin requests at SIGTERM do exactly that. `WaitDetached()` is what `cmd/server` blocks on before closing the store, because `http.Server.Shutdown` returns without knowing these goroutines exist.
- Audit actions: `backup.paired`, `backup.pair_failed`, `admin.backup_run` (details start with `outcome="success|failure"`), `admin.backup_unpair`, `admin.backup_key_pin`, `admin.backup_schedule`, `admin.backup_export` (a downloaded capsule, resource the capsule ID). `AuditDetails` flattens the lib's details map into the bounded audit field with locally derived fields first and remote error text last; values are quoted and `=` is escaped before the final `AuditSafe` cut. `cmd/server` uses it for the scheduler and CLI rows. Details carry key or capsule IDs, digests and paths, never the token.
- Rate-limit keys for `login:`, `mfa:`, `pair:` and `password-change:` come from `auth.ClientIP` via `allowClientAttempt`, never from `RemoteAddr` or a raw header, so a limit is neither shared by everyone behind a proxy nor bypassable by forging `X-Forwarded-For`. IPv6 clients are keyed per /64. That map is capped and randomly evicts.
- Per-account windows (`login-user:`, `mfa-user:`, `password-change-user:`, `pair-init:`, `messaging-*`) use `allowAccountAttempt` with existing user IDs only, in a separate uncapped map that anonymous traffic cannot evict. Password login allows 10 attempts per account per 15 minutes, resolves only local accounts and runs a dummy hash for unknown names.
- Login, MFA and `pair/verify` mint sessions without a CSRF token, so `ServeHTTP` requires `Content-Type: application/json` on them (415 otherwise): a cross-site form cannot send it without a preflight that foreign origins fail. PoW solutions are single-use.
- `pair/verify` accepts only the QR `secret`, bounds device fields, returns one error for every miss and audits `device.paired`.
- CORS permits only the exact configured `KY_APP_URL` origin and credentialed browser writes require matching CSRF cookie/header tokens.
- API request bodies are capped at 1 MiB and all responses receive baseline CSP, anti-framing, MIME-sniffing, and referrer-policy headers.
- `GET /api/settings` tiers its payload: public fields for the login screen, `db_driver`/`scim_enabled` for any session, and `extra_settings` for admins only; KyRecovery tokens are omitted in both sealed and legacy plaintext forms, dropped by the `kyrecovery_token` key prefix rather than by literal key name.

## Verification
- `TestMessagingTransportLoad` is opt-in (`KY_MESSAGING_LOAD=1`), uses disposable
  SQLite, and requires the constrained-container procedure in
  `docs/MESSAGING-LOAD.md` for capacity claims. It measures opaque HTTP/WebSocket
  transport, not browser MLS or deployed TLS. Every sender/receiver owns its
  observations; merge after completion and include scheduling backlog in latency.
- `messaging_test.go` covers signed OIDC callback enrollment, challenge replay, cross-account device credentials, revocation, invitation consent, room isolation and HTTP boundaries on both database engines.
- `messaging_delivery_test.go` covers event input limits, retry receipts, cursor pagination and unauthorized access on both database engines.
- `messaging_key_packages_test.go` covers publication/claim HTTP boundaries, access checks and retry equality on both engines.
- `go test -v ./internal/api/...` (`authz_test.go` pins the per-role exposure of every privileged route; `backup_test.go` the backup routes, on SQLite only because a run snapshots the database)
- `scripts/smoke-test.sh` asserts the same boundaries against a running binary

## Child DOX Index
None.
