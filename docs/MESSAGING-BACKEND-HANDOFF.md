**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User priority: small teams, encrypted text first. Latest request: commit, push, continue.
The folder now has Git metadata. Initial checkpoint `f36cfde` records the scaffold,
product definition, authenticated messaging API and isolated browser prototype.
Origin is `https://github.com/Busnes-app/KyMessage-Server.git`; GitHub redirected the
former Yoshiofthewire URL and the local remote now uses the canonical address.
Verified checkpoints through recovery-guidance commit `63e6b6f` were pushed.
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

`internal/sso/reauthentication.go` adds a separate suite reauthentication URL and
code verifier. Requests send `prompt=login` and `max_age=0`. The verifier checks
callback state, the original subject and nonce, signed integer `auth_time`, and the
normal OIDC signature/issuer/audience/expiry. Authentication must occur at or after
request start, no later than issuance/local now. Requests expire after five minutes,
checked both before and after exchange. There is no iat fallback or clock-skew grace;
timestamps compare at Unix-second precision. Ordinary login stays unchanged.

These methods are component primitives, not an HTTP reset grant. Their request is
server-owned state; the future consumer must store it once, bind it to the original
live session and exact action/replacement key/identity generation, recheck bindings,
and atomically consume it. No HTTP reauthentication route, reset mutation or device
approval bypass was added. OIDC timestamps alone do not prove that this particular
interactive request was honored, especially within the same second; deployed issuer
policy and parameter-tampering tests remain necessary.

Read-only inspection of sibling `KyIdentity-server` at clean commit `47447e7` found
support for forced interactions and authentication evidence. Its local request,
interaction, silent/age and signed-evidence tests passed. No files in that repo were
changed. This does not prove the behavior of a deployed issuer or a complete reset.
The product document links the OIDC primary source and records the evidence/limits.

## Verification

- `go test -race ./internal/sso -count=1`: passed, including 21 signed-token cases,
  malformed/expired request rejection and ordinary-login compatibility.
- `go test -race ./internal/api -run 'TestMessaging|Test.*SSO|Test.*OIDC' -count=1`:
  passed. `go vet ./internal/sso` passed.
- All four Chromium/Firefox OIDC browser cases passed against the real suite callback.
- Sibling tests: `TestAuthenticationRequest`, `TestAuthorizationInteraction`,
  `TestAuthorizationSilentAndAge`, `TestAuthorizationPreservesAuthenticationEvidence`
  passed; the sibling worktree remains clean.
- No dependency, storage schema, HTTP API or UI changed. Unchanged MLS-only suites
  were not repeated locally. `git diff --check` passed.

Previous pushed guidance commit `63e6b6f` CI was still running at the last check:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36356605290
The earlier `b5c6e2c` run passed in full. Check each current head's own CI before
claiming all jobs passed. KyMessages image publishing remains disabled.

## Next and limits

Next recovery step: a single-use server-side reauthentication transaction bound to
the original live suite session, replacement key and current identity generation,
with a callback that rejects mismatched/expired/replayed bindings. Use the new SSO
methods; ordinary login/session creation is not step-up evidence. Prove this with
session changes and parameter tampering, then wire atomic identity generation reset
under `docs/PRODUCT.md`. Keep reset disabled until the entire contract holds.

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

DOX: updated `internal/sso/AGENTS.md` and product evidence. Root/API/store/auth/web
and proof contracts intentionally remain unchanged: no routes, session behavior,
schemas or browser workflow changed. Child indexes remain valid. Mirror this exact
checkpoint through myslop-handoff to `kymessages-product-definition` before closeout.
