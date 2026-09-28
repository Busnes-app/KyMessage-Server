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
   Foreground polling remains fallback. Server retention now clears expired ciphertext
   and Welcomes on access, startup and periodic sweeps; missed prefixes return 410
   and retry receipts survive cleanup. The client offers retention policy at creation,
   persists missing/rollback gaps and pauses until explicit verified recovery.
   Chromium/Firefox drills cover expired initial Welcomes and established sessions.
   New local transcript/inbox copies now expire on room access and unlocked timers,
   including offline. Confirmed room-history clearing retains keys and pending sends;
   legacy undated text has no timer but shares the explicit recent-cache limit. No plaintext notifications or server-side previews.
4. **Deployable client and identity — open.** Integrate the reviewed messaging client
   with the embedded UI, suite-only member access and isolated operator recovery.
   KyMessages identity, local image/Compose coordinates, reproducible embedded
   assets and local installation/configuration documentation are implemented. HTTPS
   deployment, integrated messaging UX and supported-browser declaration remain open. Keep the disposable
   fixture and experimental unaudited client out of deployment until their gates pass.
5. **Operations and recovery — in progress.** SQLite capsule restoration now prunes
   expired ciphertext, invalidates restored grants and permanently retires old rooms.
   Fresh identity recovery and new rooms avoid resuming stale MLS state. A real
   sealed-capsule round trip and SQLite/PostgreSQL grant-policy tests pass. Product
   identity wiring and local scheduled-backup acceptance are implemented. The screen
   reports the latest recorded attempt separately from an older remote receipt.
   A running-server drill covers live schedule changes, failure retry timing, local
   copies/pruning and shutdown. Live remote-deposit/deployment checks remain open; the source-build restore runbook records the implemented recovery policy.
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
- CI green through `4549b82`; current implementation remains a server scaffold plus
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
