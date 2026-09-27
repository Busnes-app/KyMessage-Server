**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, push, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
Origin is `https://github.com/Busnes-app/KyMessage-Server.git`; GitHub redirected the
former Yoshiofthewire URL and the local remote now uses the canonical address.
Verified checkpoints through fresh-authentication commit `4cb5748` were pushed.
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

Added real recovery-authentication initiation and callback routes, documented in
`docs/MESSAGING-API.md`. POST requires the pending replacement's device token plus
the live suite session. The callback keeps that original session and uses the SSO
fresh-authentication verifier; it never creates a new login session or provisions
an account. Success reports authentication only, with `identity_reset_available:false`.
The device remains pending; neither the result nor audit is reusable reset authority.
No UI starts this flow yet. Configure its exact callback URL in the issuer for use.

Migration 8 stores a hashed random state and encrypted OIDC request under the
`messaging-recovery-auth` derived server key. The record belongs to its account and
original session, pending device/public key, subject and sorted registry digest.
At most four requests per account, five-minute expiry, expired rows pruned at the
next initiation. Session deletion cascades requests. Reads and completion validate
bindings transactionally; completion deletes and audits once. OIDC networking is
outside the transaction. Concurrent completions have one winner; a lost successful
reply requires new authentication. Registry changes require restarting the flow.

Identity generations are not implemented. The registry digest is deliberately
conservative and cannot become a substitute for reset-generation semantics later.
Timestamp resolution, deployed issuer assurance and server-restore rollback remain
limits recorded in the product contract. Reset itself remains disabled.

## Verification

- Full `go test -race ./internal/store ./internal/api ./internal/sso -count=1` passed.
- `go vet ./internal/store ./internal/api ./internal/sso` passed.
- Targeted recovery storage/API race suites passed on a disposable PostgreSQL 17
  container; it was removed after testing. Existing unrelated containers were left alone.
- Store tests cover original-session/account/subject/key binding, registry changes,
  revocation, expiry, capacity and concurrent consumption across two connections.
- Eight signed-issuer API scenarios cover success, mismatched subject, missing/stale
  auth_time, logout or registry changes during exchange, a different session and
  unusable sealed state. They also check initiation credentials/CSRF and callback
  replay. A fresh API server instance completes persisted state; success does not
  replace the session or approve the target. The final API extension was rerun.
- `git diff --check` passed. No dependency or client code changed; browser/MLS-only
  suites were not repeated locally for this backend slice.

Previous fresh-authentication commit `4cb5748` CI passed in full:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36356925959
Check the new commit's own run before claiming all jobs passed. KyMessages image
publishing remains disabled.

## Next and limits

Next recovery step: identity generations and atomic reset semantics under
`docs/PRODUCT.md`, including visible peer identity changes and fresh room invitations.
The authentication callback currently consumes its request without granting reset;
when adding the mutation, consumption and reset must share the transaction, with
explicit confirmation and replacement-key/generation binding. Never authorize reset
from the completion audit or a browser-provided success flag. Prove deployed issuer
parameter integrity before exposing a user-facing reset action.

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

DOX: updated store/API/SSO owning contracts, API wire docs and product evidence.
Root/auth/web/proof instructions and child indexes remain unchanged because domain
ownership, ordinary session behavior and browser workflow did not change. Mirror
this exact checkpoint through myslop-handoff to `kymessages-product-definition`.
