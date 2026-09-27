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

Explicit `delivery.rejoin()` and the UI's Accept reinvitation action require a
new invitation/newer membership generation and no unresolved send/commit. Fresh
join material preserves the enrolled signing identity. The old ratchet, cursor and
local transcript stay intact until a Welcome authenticates the expected generation,
a newer epoch and a later history floor. Sends/commits are blocked while waiting.
A lost invitation acceptance response reconciles against active membership on retry.

Unused join packages now survive a successful join inside the encrypted vault:
a newer package can remain unclaimed after an older Welcome is used, and the server
can offer that unused package on a later rejoin. Remove only the consumed package;
keep the existing 16-retained-package bound. Never reuse a consumed init key. This
corrects the prior checkpoint's discard-all rule. Previously discarded secrets
cannot be recovered by upgrading an old experimental vault.

Earlier local history remains visible; messages during removal are not downloaded.
The UI handles refreshing/unlocking while removed or reinvited and explains the
boundary. There is no device reapproval or automatic history reset.

## Verification

This slice: proof typecheck/build passed; HTTP/UI browser suite passed 21 cases with
one intentional duplicate cross-engine skip; OIDC suite passed all 4 cases.
Four new rejoin cases cover removal with/without an intervening commit, unresolved
outbox refusal, lost invitation acceptance, reload, stable fingerprint, stale floor
rejection, preserved history and chat after rejoin. Two use the real reinvitation
button. Renewal tests additionally rejoin using a retained unused package after a
delayed Welcome. Both Chromium and Firefox passed. `git diff --check` passed;
fixture and preview processes stopped normally.

Earlier checkpoint evidence: 16 manual browser cases passed; Go API/SSO/auth race
suite passed on SQLite. Prior backend work also passed full store/API PostgreSQL 17
checks. Those were not rerun for this proof-only change. No new dependencies.

## Next and limits

Automatic delivery, room switching, removal/reset UX, retention and restore rollback
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
this slice changes only the isolated client's rejoin lifecycle. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
