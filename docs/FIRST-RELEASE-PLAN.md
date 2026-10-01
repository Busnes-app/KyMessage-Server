**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

# Encrypted-chat first release
User-confirmed scope: complete the small-team encrypted-chat first release. Continue
through implementation and verification; commit each completed slice and before
any unavoidable break. Calls, bots, federation and native clients are outside this
release. `PRODUCT.md` owns the privacy contract and acceptance gates. Passing a
prototype test is not production approval.

## Execution order and exit checks

1. **Device recovery — implemented in the isolated prototype; release gates open.**
   Identity generations, atomic confirmed reset, idempotent receipts, prior-device
   and package revocation, generation-bound invitations/ownership, peer notices and
   independent replacement verification are implemented. SQLite/PostgreSQL race
   tests and Chromium/Firefox reset/rejoin drills pass. The default-off server gate
   still needs deployed issuer assurance and restore/rollback evidence.
2. **Durable conversations — implemented in the isolated prototype.** Isolated proof has verified opaque delivery,
   encrypted local history, outbox retry, rejoin and room switching. Each room owns
   its encrypted record/ratchet, scoped join packages and pending sends; local storage
   is bounded. Recent history now rolls at 256 messages/256 KiB instead of stopping
   receipt at capacity; setup and room notices explain permanent eviction and show
   a durable dropped-message count. Keys, cursors and pending sends are preserved. Unlock retains only a non-extractable wrapping key. Direct
   conversations with immutable peer accounts are implemented. Confirmed local-data
   removal deletes all vault entries, locks other tabs and prevents in-flight writes
   from reviving them; replacement approval remains required. Production integration
   remains gated under step 4. Saved conversations
   remain discoverable after membership removal; explicit passphrase-only history
   mode reads local ciphertext without server requests in an already loaded app. Client-only Markdown with HTML/images disabled and HTTP(S)-only links
   is implemented and browser-tested.
3. **Live delivery and retention — implemented in the isolated prototype.** Authenticated WebSocket wakeups
   now back the cookie-mode prototype with durable HTTP cursor reads. Origin checks,
   session/device revocation, connection quotas, shutdown and reconnect are tested.
   Foreground polling remains fallback. Server retention now purges expired events,
   Welcomes, retry receipts and event audit rows on access, startup and periodic
   sweeps; missed prefixes return 410. The client offers retention policy at creation,
   persists missing/rollback gaps and pauses until explicit verified recovery.
   Chromium/Firefox drills cover expired initial Welcomes and established sessions.
   New local transcript/inbox copies now expire on room access and unlocked timers,
   including offline. Confirmed room-history clearing retains keys and pending sends;
   legacy undated text has no timer but shares the explicit recent-cache limit. No plaintext notifications or server-side previews.
4. **Deployable client and identity — in progress.** Integrate the reviewed messaging client
   with the embedded UI, suite-only member access and isolated operator recovery.
   KyMessages identity, local image/Compose coordinates, reproducible embedded
   assets, installation documentation, the members' "My account" page, the admin
   network self-check, the proxy overlay (requires `KY_APP_URL`, `KY_SESSION_SECRET` and `KY_TRUSTED_PROXIES`)
   with its guide, the browser declaration and check, and the gated `chat-core` package
   are implemented. Integrating the reviewed client, real-device browser verification
   and deployed HTTPS evidence remain open. Keep the disposable fixture and
   experimental unaudited client out of deployment until their gates pass.
5. **Operations and recovery — in progress.** The people capsule (accounts, access,
   settings, no messaging rows) runs daily by default once a key and destination are set; the messages capsule is opt-in,
   on its own schedule. `restore` invalidates restored grants; `restore-messages` then
   imports rooms and history with approved devices suspended (no token). An owner resumes
   a device by re-proving its key after a fresh sign-in; an admin can revoke a suspended
   device first, and one not resumed within 30 days is revoked automatically. Scheduled
   local backups and a running-server drill pass. Live remote-deposit/deployment checks
   remain open; `docs/RESTORE.md` records the restore policy.
6. **Release evidence — open.** Run CI on both database engines, production browser
   regressions, dependency checks, recovery drills, declared-host load tests and
   protocol/application-binding security review. Record actual supported browsers
   and measured limits. Resolve MLS library maintenance/license/security and
   independent interoperability gates before declaring production E2EE.

## Evidence already available

- Device enrollment/approval/revocation; invitation ACLs; durable opaque event log;
  epoch/roster CAS; bounded one-use KeyPackages; independent fingerprint pins.
- Chromium/Firefox proof exchange, reload/crash/outbox recovery, concurrent commits,
  removal/reinvitation, device-loss guidance and ordinary suite OIDC cookie/CSRF flow.
- Recovery-auth state sealed at rest; original-session binding, expiry and concurrent
  single-use completion and atomic reset tested on SQLite and PostgreSQL; browser
  reset/rejoin tests cover future-only access, original-session preservation and
  generation-2 enrollment.
- CI green through `171591a`; current implementation remains a server scaffold plus
  an isolated MLS experiment, not a deployed encrypted-chat product.

## External evidence still required

KyMessages is not deployed yet (user-confirmed); live KyIdentity callback registration
and deployed reauthentication policy remain unverified; an
independent security assessment of the chosen MLS implementation/application profile;
interoperability against an independent implementation; and real supported-browser
and constrained-host measurements. Local tests may advance these gates but cannot
stand in for evidence they did not produce. Continue independent implementation when
one gate needs external input; never hide that gate or silently downgrade encryption.

Local packaging evidence: the full Go race suite, vet/module verification, seven
frontend tests, four production-CSP/responsive Chromium cases, dependency checks,
CLI/server smoke tests and final container HTTP/assets checks pass. Compose base,
source-build, DNS and static-IP overlays validate. The container serves the exact
committed frontend bundle and excludes the experimental MLS client. No image was
published and no deployment occurred.

Independent interoperability: native OpenMLS 0.9.0 and ts-mls 1.6.4 pass the named
exchange/reload/update/join/removal and group-secret agreement scenarios in both
browsers only with an explicitly MLS-1.0-only fixture advertisement. Unmodified
OpenMLS fails because ts-mls rejects an unknown advertised protocol version, contrary
to RFC 9420 capability handling. This dependency defect and the custom GroupInfo
application profile remain open gates. Reproduction and exact pins are recorded in
`MLS-INTEROP-RESEARCH.md`; no cryptographic dependency or signed incoming bytes were
patched. The ordinary 18 browser lifecycle tests still pass.

Backup outcome evidence: SQLite/PostgreSQL tests cover latest-action reads despite
clock rollback/unrelated activity, partial success and raw-detail exclusion. Full
store/API/cmd race suites and vet pass. Nine frontend tests and four real-server
Chromium cases pass, including local sealing and narrow/wide light/dark backup UI.
The three-minute scheduler acceptance check passes and now runs in CI.

Recent-cache evidence: 20 manual lifecycle cases pass across Chromium/Firefox,
including 260 real encrypted messages, reload, rolling replay hashes and an intact
pending outbox. Full HTTP/UI tests pass 55 cases with one intentional duplicate
engine skip; eight OIDC cases pass. A seeded display-cache boundary test preserves
live ratchets/cursor and pending ciphertext; it is not server load evidence. Escaped
control characters count toward the serialized-byte limit. Linux WebKit container checks now isolate an intermittent native Ed25519
`generateKey` failure on a blank page. Its 10 lifecycle and four OIDC cases passed,
but the full HTTP/UI run had two key-creation failures; WebKit remains outside the
verified subset. Controlled clocks now install before app startup. See
`BROWSER-EVIDENCE.md`; real Safari/iOS remains unverified.

Constrained-host transport evidence: the opt-in SQLite HTTP/WebSocket check uses
50 accounts and 100 connected devices, 60 seconds at 10/s then two seconds at 50/s.
A shared 120-request/minute budget first caused HTTP 429; reads now have their own
2,400/minute budget while writes retain 120/minute. The final independent-sender
run under a 2-CPU/2-GiB container quota delivered all 700 events to all devices.
Sustained p95 acceptance/receipt: 67.24/220.91 ms; burst: 52.73/158.43 ms. Metrics
include scheduling backlog and are checked separately by phase. This is synthetic
opaque transport, not browser MLS, deployed TLS or a soak test. Reproduce using
`MESSAGING-LOAD.md`; complete end-to-end release capacity remains open.
Full API race tests and vet pass on SQLite; all messaging API race tests pass on
an owned disposable PostgreSQL 17 instance. The new rate-limit regression covers
receive traffic above the old budget, retained write abuse limits, and reads after
write exhaustion. The opt-in load test remains separate from ordinary CI timing.

Operator storage evidence: the admin overview now reads `/api/admin/messaging/usage`
for global totals and up to 100 rooms, prioritizing 80%-of-limit rooms. A single
metadata SELECT keeps totals consistent with a truncated list; it neither scans
ciphertext nor performs cleanup. Shared constants keep append limits and reporting
aligned. Counts include retired rooms and pending cleanup; receipts (the lifetime append counter) are distinct
from retained data and physical disk use. SQLite/PostgreSQL tests cover access,
bounds, priority and actual retention rows. Full store/API race suites and vet pass;
15 frontend tests and four production-CSP responsive browser cases pass.

Self-review checkpoint: this is a review of our own implementation, not independent
cryptographic assessment. The original unlock/Lock-button confidentiality claim
was rejected (the button is hidden); real navigate-away/back checks did not reproduce
it. A narrower two-tab cancellation defect was reproduced and fixed: stale setup
could create a new vault after another tab removed local data. Deleted keys/history
were never recovered. The new regression failed before the generation guard and
passes in the ten-case Chromium/Firefox OIDC suite; a separate reviewer checked it.

Backup capacity fix (`b5ff3e7`): three bounded rooms produced a 74,539,008-byte
snapshot, exceeding the 64 MiB capsule member limit. Collection now blanks event
and KeyPackage payloads and removes Welcomes only in the private snapshot, retains
receipt hashes/topology/audits, advances retention floors and compacts it. The
regression proves live payloads/counters unchanged and actual capsule sealing.
Metadata/receipt/audit growth can still exceed 64 MiB; reject before full allocation.
The complete live database still needs scratch disk space. Full backup/API/cmd race
suites and vet pass; targeted projection, sealing and oversized export/drill checks
pass. The real running-server scheduler acceptance drill passes timer, retry,
live-disable, copy permissions/pruning and shutdown checks. This is capacity/restore
evidence, not a demonstrated malicious-member exploit.
Local audit artifacts remain at
`/home/yoshi/security-audit-skill/KyMessage-Server/run-1/REPORT.md`; broad hunting and
independent cryptographic assessment remain incomplete.
