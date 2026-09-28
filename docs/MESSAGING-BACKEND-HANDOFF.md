**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

The user explicitly requested completion of the encrypted-chat first release, with
commits at each completed slice or unavoidable break. Continue the full execution
plan in `docs/FIRST-RELEASE-PLAN.md`; do not stop at the device-recovery subset.
Remote: https://github.com/Busnes-app/KyMessage-Server.git. No deployment occurred.

## Completed checkpoint

Migration 9 and `messaging_reset.go` bind devices, invitations, ownership and reset
requests to identity generations. Confirmed reset atomically advances the generation,
revokes prior devices, retires unclaimed packages, removes memberships and stores an
idempotent original-session receipt. Old ownership is not inherited. Browser pins
and MLS metadata use generation-sensitive v2 bindings; accepted legacy v1 events
retain their original hash profile. This is an experimental protocol transition.

The recovery API seals explicit reset intent and requires freshly signed suite
reauthentication. `KY_MESSAGING_IDENTITY_RESET_ENABLED` defaults false and is enforced
both at initiation and completion. Authentication-only completion is never reset
authority. A completed reset callback replays its receipt without exchanging the
spent code or mutating again. HTML success returns only to `/`; no new session is
issued. The OIDC fixture enables the gate with disposable accounts, not production
identity assurance.

The prototype confirms lost access/ownership/history, locks before issuer navigation,
and unlocks the existing replacement vault afterward. Old readable local history
remains on revoked browsers. Remaining room members see a server-reported identity
change until the removal commit; a reinvited replacement requires independent
fingerprint approval and receives future messages only. Later device enrollment
uses the new generation immediately in its local self pin.

## Verification

- Full race suites passed for store, API, SSO and config.
- Messaging race suites passed on disposable PostgreSQL 17; fixture container removed.
- Go vet passed for changed backend and build-tagged fixture packages.
- Proof typecheck/build passed. Chromium/Firefox OIDC suite passed before the final
  added generation-2 enrollment scenario; that expanded reset scenario also passed
  on both browsers after correcting its selector and respecting other-device trust.
- Earlier complete delivery suite: 25 passed, one intentional duplicate-engine skip.
- CI for `ad21d59` passed: https://github.com/Busnes-app/KyMessage-Server/actions/runs/36359795108

## Durable-conversation progress

`6b6eabe` derives the wrapping key only during unlock/setup and retains no passphrase
between operations. Lock cancels late unlock completion. `954b10d` adds migration 10's
optional KeyPackage publication room scope: active membership required, immutable
scope, no claims from another room, existing device quotas retained. Its CI passed:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36360773559

The latest client slice switches rooms using separate encrypted IndexedDB entries
and Web Locks. The legacy first room stays in `device`; subsequent `room:<UUID>`
entries authenticate their entry name as AES-GCM associated data. Up to 100 entries,
2 MiB per entry, existing 256-element list bounds; full storage refuses writes and
never silently deletes history. All entries use the root's non-extractable wrapping
key and salt with fresh IVs. The tab-local selected-entry hint contains no secret.

New rooms generate fresh init/HPKE keys under the same enrolled signing identity.
Publications are room-scoped; retained older unscoped publication retries remain
compatible. Already verified root pins carry their identity generation into new
rooms. Switching preserves unresolved sends and histories; drafts require explicit
confirmation before discard. Lost room-creation acknowledgement recovers by refreshing
and opening the empty owned room; published join material/nonzero epochs cannot be
used to initialize another group.

Final multi-room verification: typecheck/build; 18 manual MLS tests; 29 HTTP/UI tests
plus one intentional duplicate cross-engine skip; 6 OIDC tests, all passed across
Chromium/Firefox. New cases prove two-room history/outbox persistence, reload and
draft handling, lost creation acknowledgement, independent room locks and rejection
of swapped encrypted entries. Scoped package race tests passed on SQLite/PostgreSQL.
The stale device-loss wording assertions from the reset slice were fixed in `b93d89d`.

## Next and constraints

Client-side Markdown is now implemented with pinned markdown-it 15.0.2, raw HTML
and image rendering disabled, and only HTTP(S) links active with no opener/referrer.
Four Chromium/Firefox cases passed for formatting, HTML/script links, tracking
images, reload and mobile layout; npm audit reported no vulnerabilities.

Saved conversations now enumerate encrypted room records independently of server
membership. Names are cached encrypted; unreadable entries are reported individually.
Explicit passphrase-only history mode disconnects delivery and makes no server
requests, hides sending/account controls, and clears history/keys on lock. This works
in an already loaded app offline; cold offline startup is not implemented. Root vault
corruption still prevents unlocking. Ordinary online unlock retains suite-account checks.

Verification: proof build passed; four selected HTTP/UI cases, four local lifecycle
cases and all six OIDC cases passed across Chromium/Firefox. The new case covers
removed membership, damaged sibling records, wrong passphrase, offline local selection,
no API requests during 60 seconds and lock clearing history.

Migration 11 adds immutable direct-room peer bindings. Creation and the invitation
commit together; a third account is denied even after peer removal/deletion. The
client reuses an existing visible pair, requires consent and fingerprints, persists
its counterpart, and rejects extra-account MLS rosters. Concurrent starts can create
separate rooms; ownership/reset rules remain unchanged. Ordinary rooms remain groups.

Verification: messaging race suites pass on SQLite and PostgreSQL 17, and Go vet and
proof build pass. The full browser run exposed an obsolete removed-member button
expectation and a duplicate key in the new injected-roster test; those expectations
were corrected without weakening server denial or roster validation. Both corrected cases pass in Chromium and Firefox; the other 31 cases passed in
the full run (one intentional duplicate-engine skip). Full store/API race suites
also pass. Only the selected room receives automatic
checks; client server-room discovery reads one page of 100.

WebSocket wakeups now serve the selected active room in cookie mode. The endpoint
requires a live suite session and exact configured Origin before upgrade, then the
64-byte hex device credential in a first text frame within five seconds. No URL
credentials. Four connections/account, 256/process, one coalescing signal each; every
notice rechecks session/device/ACL and contains only sequence/epoch/roster hash.
Successful mutations signal after commit; a 15-second heartbeat catches external
changes and revoked sessions. HTTP cursor reads still verify/persist actual MLS.

`StopMessaging` runs before HTTP shutdown and cancels upgraded connections; the
existing detached counter tracks them before authentication and drains before store
close. The fixture follows the same shutdown order. The proof opens no stream in
local-history or bearer-fixture mode and closes on lock/room switch/hidden/offline.
Queued notices coalesce with foreground work; cursor and directory-hash comparisons
avoid redundant requests. Polling remains fallback. Dependency: coder/websocket 1.8.15.

Verification: native WebSocket race tests pass on SQLite/PostgreSQL for origins,
first-frame limits, cross-account credentials, missing membership, quotas, reconnect,
revocation and shutdown. Full API/cmd race suites and vet pass. Eight Chromium/Firefox
OIDC cases pass, including frozen-timer live receive, offline catch-up, draft/focus
preservation, no plaintext socket frames and lock closure. All 35 delivery regressions also pass
(one intentional duplicate-engine skip). `govulncheck` reports no affected call paths
(three findings exist in required modules outside imported vulnerable packages).

Migration 12 implements server ciphertext retention: immutable per-room 1/7/30-day
policy (default 30), ordered expiry, retained prefix floor, payload/Welcome clearing,
and 410 `history_expired` for missed history. Preserve receipt hashes and sequence
metadata after expiry; exact retries still acknowledge the same event. Active limits
are 4,096 events/32 MiB per room; lifetime receipt limit is 1,000,000. Neither backup
copies nor audit/receipt metadata are erased by message retention. Client expiry
and gap UX remain open; the current proof still displays generic API errors.

Store initialization prunes before returning, including restored SQLite databases.
The daemon sweeps idle rooms every minute with a 30-second operation deadline. Its
completion joins the backup scheduler before the existing bounded shutdown drain;
failed store initialization closes its database handle. Operations use room locks
and short transactions; failed user operations can roll back incidental cleanup,
but cannot expose expired ciphertext. Independent sweeps complete physical row updates.

Verification: full store/API/cmd race suites passed. Messaging race suites also passed
on disposable PostgreSQL 17. Retention cases cover expired Welcome denial, receipt
replay/tampering, retained-byte accounting, explicit remove/reinvite history floors,
startup cleanup, idempotent sweeps, backward clocks and concurrent append/cleanup
across connections. Go vet and diff checks pass.

CI for the live-delivery checkpoint passed:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36364009571

Next: client retention controls, persisted history-gap state, explicit rejoin/new-room
help and local transcript expiry; then the final local-data lifecycle and restore
rollback protections. KyMessages is not deployed yet, confirmed by the user. Keep
live issuer checks open while independent implementation continues.

Retention/gap recovery, product embedding, installation, restore and
load/security evidence remain open. The unaudited `ts-mls` experiment and private
GroupInfo extension stay out of production. Deployed issuer interaction, independent
protocol review/interop, full supported-browser evidence and restored metadata
rollback remain explicit release gates. Reset is not lost-history recovery.

DOX updated store/API/config/proof/fixture contracts and product/wire documents.
Root already records the full-release/commit preference; auth/SSO/web contracts and
child indexes stay unchanged because their ownership and ordinary login behavior
were not changed. Mirror this checkpoint to myslop before an actual pause.
