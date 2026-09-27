**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: continue.
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

The isolated chat UI now polls the selected room after 10 seconds between completed
operations while unlocked, visible and online, with no pending send. One poll shares
the existing action serialization; failed checks back off to 20/40/60 seconds.
Manual actions remain available between checks. Polling only processes the existing
verified event stream; it never resends uncertain ciphertext or approves keys.
Drafts and typing focus survive background checks. Each tab polls independently.

Delivery requests have a ten-second deadline covering the cookie-account check and
messaging request. Disconnect aborts in-flight delivery. Lock stays available during
reads; a view-generation check stops late async renders from revealing a cleared
view. Page exit locks locally. Cookie session loss on a poll uses the existing
SessionError lock and preserves encrypted state. Hidden/offline tabs, tabs without
a room and tabs with pending sends wait until their next network operation to
detect session changes. This is foreground polling, not WebSockets or mobile push.

Previous lifecycle work remains: pre-join renewal, explicit reinvitation/rejoin,
new-generation Welcome validation, preserved earlier local history, bounded unused
join material and no access to messages sent during removal. Pending outbound work
blocks rejoin. Consumed join keys are removed; unused published keys remain in the
encrypted vault because the server may offer them later.

## Verification

This slice: proof typecheck/build passed; HTTP/UI browser suite passed 21 cases with
one intentional duplicate cross-engine skip; OIDC suite passed all 4 cases. The
chat regressions now exercise automatic receipt, a failed-read backoff, unchanged
draft/focus, offline pause, no overlapping reads and lock during a held response.
The OIDC regression also expires the session and checks that an automatic poll
locks the view and clears visible history. Both Chromium and Firefox passed.
Existing lifecycle/rejoin, exact pending retry and key-binding regressions passed.
`git diff --check` passed. No server code, database contract or dependency changed.

Earlier checkpoint evidence: 16 manual browser cases passed; Go API/SSO/auth race
suite passed on SQLite. Prior backend work also passed full store/API PostgreSQL 17
checks. Those were not rerun for this proof-only change. No new dependencies.

## Next and limits

WebSocket/push delivery, room switching, removal/reset UX, retention and restore rollback
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
Root and server/API/store/auth/web contracts intentionally remain unchanged because
this slice changes only isolated client polling and cancellation. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
