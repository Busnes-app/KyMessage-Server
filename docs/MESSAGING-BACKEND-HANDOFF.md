**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, push, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
Origin is `https://github.com/Busnes-app/KyMessage-Server.git`; GitHub redirected the
former Yoshiofthewire URL and the local remote now uses the canonical address.
Verified checkpoints through member-removal commit `2d24b28` were pushed.
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

The isolated chat UI offers account device revocation, including this browser.
Native confirmation names the target and explains downloaded history, the required
room encryption update and the risk of revoking the last approved device. A live
suite session remains sufficient even with unresolved local outbound ciphertext.
The UI refreshes after attempts, including lost responses, to show actual status.

A denied background delivery check refreshes account devices. When revocation is
observed, automatic checks stop and messaging controls disable. Unlocking a revoked
browser matches its existing account/key tombstone without enrollment, retaining
read-only local history. Revoking every approved device leaves replacements pending;
identity reset is not implemented. The API and server enforcement are unchanged.

## Verification

Proof typecheck/build passed. The full HTTP/UI suite passed 23 cases with one
intentional duplicate cross-engine skip; all 4 OIDC cases passed. Chromium and
Firefox both cover cancelled revocation, a lost successful revocation response,
automatic revocation detection, read-only history after reload without enrollment,
current-browser revocation and a replacement staying pending after the last
approved device is revoked. `git diff --check` passed. Fixtures stopped normally.
No dependency or server API changed.

GitHub CI for the previous member-removal commit `2d24b28` passed in full:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36355022705
That run includes SQLite/PostgreSQL race suites, production browser tests, smoke,
Docker, vulnerability checks and the MLS job (manual, HTTP/UI and OIDC). Image
publish/promote were intentionally skipped. Check the current head's own run before
treating that earlier green result as evidence for this revocation slice.

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

DOX: updated `mls-proof/AGENTS.md`, proof run instructions and product evidence.
Root/server/API/store/auth/web contracts intentionally remain unchanged because
revocation uses their existing authorization and delivery contracts. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
