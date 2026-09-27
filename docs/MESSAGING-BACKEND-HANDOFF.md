**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, push, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
Origin is `https://github.com/Busnes-app/KyMessage-Server.git`; GitHub redirected the
former Yoshiofthewire URL and the local remote now uses the canonical address.
Verified checkpoints through `83757d7` and the CI correction `01bdfd0` were pushed.
No product deployment occurred. Generated TypeScript state and inherited
`.superpowers/` review scratch are ignored; embedded `web/dist` is tracked.

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

The existing checkpoint was pushed. Its first CI run failed the inherited
server-base image-coordinate assertion. `01bdfd0` scopes that assertion and image
publication/promotion to `Busnes-app/ky-server-base`, keeping KyMessages outside
production publishing until its identity/release gates close. A new CI job builds
the isolated proof and runs manual, HTTP/UI and OIDC suites on Chromium/Firefox.
README and root DOX document this split. The base Compose coordinates remain scaffold.

The isolated chat UI lists invited and active room accounts. Only owners see the
member-removal form, with a native confirmation naming the target and explaining
that server access is revoked immediately, encryption needs a verified membership
commit, and downloaded messages cannot be recalled. API authorization still owns
access control; owner self-removal is excluded by both UI and server.

Removal refuses unresolved local outbound/commit state. The UI refreshes in a
finally block, including after a lost removal response, so the current member list
and server pause flag expose what actually happened. Sending is disabled while
paused; Apply verified membership performs the existing MLS transition. Unused
invitations can be revoked without a rekey. Reinvitation/rejoin preserves earlier
local history and excludes messages sent during removal.

`delivery.members()` validates account IDs and invited/active status. The proof's
`directory()` now returns `{peers,paused}`; chat is its only code caller. Automatic
polling refreshes a previously paused directory after successful event processing
so a received membership commit can unblock the composer.

## Verification

This slice: proof typecheck/build passed; HTTP/UI browser suite passed 23 cases with
one intentional duplicate cross-engine skip; OIDC suite passed all 4 cases. The two
new removal cases cover native cancellation, invited-member revocation without an
unnecessary rekey, owner-only controls, lost removal acknowledgement, the send
pause, cryptographic removal, preserved earlier history and reinvitation/rejoin
without messages sent during removal. Chromium and Firefox both passed.
`git diff --check` passed. Fixtures stopped normally. No dependency or server API
changed in the removal slice.

GitHub CI for `01bdfd0` passed in full:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36354569810
That run includes SQLite/PostgreSQL race suites, production browser tests, smoke,
Docker, vulnerability checks and the new MLS job (manual, HTTP/UI and OIDC). Image
publish/promote were intentionally skipped. The removal commit is validated
locally as above; its fresh push gets its own CI run. Check the current head's run
before treating the earlier green run as evidence for a newer commit.

## Next and limits

WebSocket/push delivery, room switching, device reset UX, retention and restore rollback
reconciliation remain open. Rejoin covers removal followed by reinvitation of the
same still-approved device; it does not recover revoked devices, retention gaps,
lost keys, interrupted rejoin followed by another removal, or rolled-back state.
Unresolved outboxes deliberately block rejoin; recovery must not silently discard
uncertain ciphertext. One room per profile, 256-entry transcript limits and no
silent history truncation. Use fresh profiles after restarting the fixture database.

Live KyIdentity deployment, supported WebKit/Safari, independent-library interop,
security review of ts-mls and experimental GroupInfo extension 0xff01, and product
unlock/recovery policy remain release gates. Milestone 0 remains open. Initial
manual-proof packages use the library's broad default signed lifetime; fresh rejoin
packages have the renewal path's seven-day signed lifetime and one-hour HTTP expiry.
No automatic pool replenishment or signing-key rotation. Storage rollback and
physical key erasure are not proven.

Other recovery gaps: stale-roster conflict without a winning commit, lost room
creation acknowledgement, session changes during unfinished enrollment. Account
changes are detected on network requests. Sign-out cannot erase copied secrets.

DOX: updated root CI/publishing guidance, `mls-proof/AGENTS.md`, proof run instructions,
README and product evidence. Server/API/store/auth/web contracts intentionally remain
unchanged because removal uses their existing authorization and delivery contracts. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
