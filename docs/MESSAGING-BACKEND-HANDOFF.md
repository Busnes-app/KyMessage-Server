**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
No remote is configured; nothing was pushed or deployed. Generated TypeScript state
and inherited `.superpowers/` review scratch are ignored; embedded `web/dist` is tracked.

## Current implementation

The Go API supports authenticated device enrollment/approval/revocation, invitation
room ACLs, ordered opaque event delivery, epoch/roster coordination and one-time
KeyPackage publication/claims. The isolated MLS browser proof binds credentials to
enrolled account IDs and independently verified signature keys. The clickable UI
supports invitations, own-device approval, encrypted local conversation history,
lock/unlock and durable ciphertext retry. OIDC mode exercises the real suite
callback with a disposable local issuer, cookies and CSRF; no live identity service
was contacted. Read `docs/MESSAGING-API.md` and `mls-proof/README.md` before extending.
The proof remains outside the embedded React app and Docker.

## This slice

- `delivery.publishKeyPackage()` renews expired pre-join publications using the
  library's `generateKeyPackageWithKey`: fresh init/HPKE keys, same enrolled signing
  key and credential. Renewed packages use a seven-day signed lifetime and one-hour
  HTTP allocation lifetime. Those are experiment defaults.
- Keep at most 16 prior packages and their private join keys inside the encrypted
  connection record. Expired allocation does not prove no Welcome was issued.
  Match exactly one retained MLS KeyPackageRef, then perform the existing full
  Welcome/tree/signer/metadata validation. Clear retained join material atomically
  with the verified join. Capacity stops renewal rather than dropping secrets.
- Persist fresh publication parameters before networking. Lost acknowledgements
  retry identical bytes and expiry across reload. Joined clients cannot republish.
- Expired cached claims receive a new durable request ID. A lost claim response
  keeps its original ID until the server returns explicit 409; then save a fresh
  ID and ask for another membership attempt. Network failures never rotate IDs.
  Claimed packages are never requeued. This does not reset an existing MLS group.
- Prepare to join now supports renewal through the existing UI action.

## Verification

This slice: proof typecheck/build passed; HTTP/UI browser suite passed 17 cases with
one intentional duplicate cross-engine skip; OIDC suite passed all 4 cases. Six new
renewal cases cover cached and lost allocations, real server expiry, publication
acknowledgement loss, exact retry after reload, stable fingerprint, delayed Welcome
and subsequent encrypted chat on Chromium and Firefox. `git diff --check` passed.
Fixture and preview processes stopped normally.

Earlier checkpoint evidence: 16 manual browser cases passed; Go API/SSO/auth race
suite passed on SQLite. Prior backend work also passed full store/API PostgreSQL 17
checks. Those were not rerun for this proof-only change. No new dependencies.

## Next and limits

Implement explicit removed-device/offline rejoin with fresh join material,
authenticated membership generation and history floor, without silently resetting
pending ciphertext or ratchets. Automatic delivery, room switching, removal/reset
UX, retention and restore rollback reconciliation remain open. One room per
profile, 256-entry transcript limits, no silent history truncation. Fresh profiles
are needed after restarting the disposable fixture database.

Live KyIdentity deployment, supported WebKit/Safari, independent-library interop,
security review of ts-mls and experimental GroupInfo extension 0xff01, and product
unlock/recovery policy remain release gates. Milestone 0 remains open. Initial
manual-proof packages still use the library's broad default signed lifetime;
renewal does not constitute reviewed automatic pool replenishment or signing-key
rotation. Storage rollback and physical key erasure are not proven.

Other recovery gaps: stale-roster conflict without a winning commit, lost room
creation acknowledgement, session changes during unfinished enrollment. Account
changes are detected on network requests. Sign-out cannot erase copied secrets.

DOX: updated `mls-proof/AGENTS.md`, proof run instructions and product evidence.
Root and server/API/store/auth/web contracts intentionally remain unchanged because
this slice changes only the isolated client's pre-join lifecycle. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
