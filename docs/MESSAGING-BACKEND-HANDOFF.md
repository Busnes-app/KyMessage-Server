**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, push, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
Origin is `https://github.com/Busnes-app/KyMessage-Server.git`; GitHub redirected the
former Yoshiofthewire URL and the local remote now uses the canonical address.
Verified checkpoints through device-revocation commit `b5c6e2c` were pushed.
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

Defined the proposed last-device-loss contract in `docs/PRODUCT.md`: fresh
interactive suite authentication and replacement-key proof; atomic, idempotent
identity-generation reset; revocation of old/pending devices; visible identity
change; fresh room invitations and independent fingerprint comparison; future-only
history; and a new-room outcome when the owner or final usable group state is lost.
This is a proposed contract with explicit acceptance gates, not an enabled reset API.

The isolated chat UI now keeps generic recovery help available before unlock. It
explains how to preserve browser data, approve a replacement from an accessible
approved browser, and revoke lost devices from a pending replacement. Account
banners distinguish the only approved device, a replacement needing approval, and
no remaining approved devices. Approval status does not prove keys are accessible.
Account-specific text clears on lock. No data deletion or approval bypass was added.

## Verification

Proof typecheck/build passed. All eight chat UI cases passed across Chromium and
Firefox (six existing cases plus two new lost-browser drills); all four OIDC cases
passed. The new drill closes the only approved browser, enrolls a pending replacement,
revokes the lost browser through the real API and verifies the replacement stays
pending. Existing cases verify each recovery banner transition and clearing on lock.
Mobile overflow assertions passed and the Chromium mobile screenshot was inspected.
`git diff --check` passed. No server API, cryptography or dependency changed; the
unchanged protocol-only suite was not repeated locally for this guidance slice.

GitHub CI for device-revocation commit `b5c6e2c` passed in full:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36355716547
That run includes SQLite/PostgreSQL race suites, production browser tests, smoke,
Docker, vulnerability checks and the MLS job (manual, HTTP/UI and OIDC). Image
publish/promote were intentionally skipped. Check the current head's own run before
treating that earlier green result as evidence for the recovery-guidance slice.

## Next and limits

Next recovery step: prove the suite issuer's fresh-authentication binding before
implementing reset, then introduce identity generations through storage, API and
client verification under the product contract. The current OIDC fixture proves
login/callback/CSRF, not reset-grade reauthentication. Reset must not reuse ordinary
first-device approval or administrator privileges.

WebSocket/push delivery, room switching, identity reset, retention and restore rollback
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
this slice only explains their existing authorization and delivery contracts. Child indexes
remain valid. Mirror this exact checkpoint through myslop-handoff to the existing
`kymessages-product-definition` folder before closeout.
