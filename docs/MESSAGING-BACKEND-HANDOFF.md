**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master; initial checkpoint)

The user selected small teams and encrypted chat first, then requested continuation.
The isolated chat prototype now exercises the existing suite OIDC browser flow.
Run instructions: `mls-proof/README.md#suite-oidc-browser-flow`. Open
`/chat.html?auth=oidc` against the local OIDC fixture; ordinary `/chat.html` retains
the direct synthetic-session demo. Neither client is deployed in the embedded React
app or Docker. No production identity service or account was contacted.

Current foundation: real authenticated device enrollment/approval/revocation,
invitation room ACLs, ordered opaque event delivery, epoch/roster coordination and
one-time KeyPackage publication/claims. The browser proof binds MLS credentials to
enrolled accounts, independently verified signature keys and authenticated metadata.
The clickable UI supports room setup, invitations, verification, own-device approval,
second-browser admission, sent/received history, lock/unlock and durable ciphertext
retry. Read `docs/MESSAGING-API.md` for server contracts and `mls-proof/README.md`
for the proof's boundaries and run commands.

New this slice:
- `src/session.ts` reads the immutable `user.id` from `/api/auth/me`, accepts only
  active suite accounts and reuses `web/src/api.ts` for cookie CSRF. `delivery.ts`
  supports explicit cookie or memory-only bearer modes. Cookie-mode network requests
  recheck the expected account and never send an Authorization header. Session
  cookies stay HttpOnly; no session credential or ID token enters JavaScript storage.
- The chat UI separates suite sign-in from passphrase-based local setup/unlock.
  It rejects a vault belonging to another account and clears visible state when a
  request detects an expired/changed session. The encrypted vault/outbox survives;
  signing in as its original account can retry the same ciphertext. Lock is tab-local
  and leaves the server cookie; a separate Sign out of suite action revokes it.
- The existing SSO callback still redirects to `/`. A boolean sessionStorage hint
  returns that landing page to the fixed prototype URL. It is not a credential or
  arbitrary redirect parameter. The production SSO callback was not changed.
- Device initialization accepts exact 1–64-byte UTF-8 account IDs without controls
  or malformed surrogates. The MLS credential uses the server ID, not username,
  display name or OIDC subject. Roster hashing matches Go's HTML-safe JSON encoding,
  including angle brackets, ampersand and Unicode line/paragraph separators.
- `server/oidc.go` is a build-tagged, loopback, disposable issuer. It signs RS256
  tokens, checks the fixed client/callback and S256 verifier and consumes codes once.
  It accepts synthetic names without passwords, so it is test infrastructure, not
  authentication for real people. `MLS_PROOF_OIDC=1` uses the actual suite callback
  and disables direct `/proof-fixture/session/` issuance. It never inherits a live
  issuer from the host environment. Only this fixture disables Secure cookies.
- The only production behavior change is `Cache-Control: no-store` on both signed-in
  and anonymous `/api/auth/me` responses, with a regression assertion.

Verification: strict TypeScript/build, Go vet for API/auth/SSO and the tagged fixture,
and `go test -race ./internal/api ./internal/sso ./internal/auth` passed on SQLite.
The OIDC suite passes 4 browser cases across Chromium and Firefox: redirect/signature
verification, HttpOnly cookie transport, CSRF rejection, encrypted two-account chat,
reload, logout, wrong-account unlock denial and identical pending retry after session
loss; altered nonce/PKCE fail before session creation. The HTTP/UI suite passes 11
cases with 1 intentionally skipped duplicate cross-engine case, including new actual
MLS exchange with non-ASCII/HTML-sensitive account IDs. The manual suite passes all
16 cases. Total: 31 passes plus that one skip. No new dependency, commit, PR or
deployment. PostgreSQL was not rerun for this header/client/fixture-only slice.
Tests stop their fixture/preview processes.

Next: KeyPackage renewal/replenishment and rejoin lifecycle, followed by automatic
message delivery, room switching, removal/reset UX, retention and restore rollback
reconciliation. Keep MLS outside deployment until its security/release gates close.
The prototype still owns one room and one join package per profile and caps local
transcript lists at 256 entries. No silent history truncation. Use fresh profiles
after restarting the disposable database. Session change during unfinished enrollment,
stale-roster conflict without a winning commit and lost room-creation acknowledgement
remain recovery gaps. Account changes are detected at network operations, not by
background push. Sign-out cannot revoke already-copied local secrets or plaintext.
Live KyIdentity deployment, WebKit/Safari, cross-library interoperability and an
independent review of ts-mls/GroupInfo extension 0xff01 remain unproven. Milestone 0
is still open; local issuer success does not close those gates.

DOX: root, API, proof and fixture contracts are updated; their child indexes remain
valid. README, product evidence, API docs and proof run instructions distinguish
OIDC test evidence from production readiness. SSO/auth/store/web child contracts are
intentionally unchanged because their implementation contracts did not change.
This checkpoint is mirrored to the existing kymessages-product-definition folder
using myslop-handoff.
