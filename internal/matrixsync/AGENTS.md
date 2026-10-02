# Matrix sync

## Purpose
Keeps Matrix Authentication Service (MAS) in step with KyIdentity's directory as KyMessages
records it, so offboarding sticks after KyIdentity's back-channel logout cuts sessions.

## Ownership
Owns `Plan` and `Syncer` (`matrixsync.go`) and the MAS admin client (`mas.go`).
The client also serves the console through `api.MatrixAdmin` (sessions, finish, user, version,
`AsConsole`).
`cmd/server` wires it: one `NewClient` from `cfg.Matrix.Admin*` shared by the sweep and the API, a sweep at start and every
5 minutes, and `Wake` from the directory webhook. `store.UserStore.DirectoryStatuses`
supplies subject → status.

## Local Contracts
- Decision table, per non-deactivated MAS user, by the subject of its KyIdentity link:
  active → unlock if locked; deleted → deactivate; inactive or unknown to KyMessages →
  lock if unlocked; no link, or links to more than one subject (`Ambiguous`) → lock if
  unlocked (fail closed). A lock applied by hand in MAS to a user active in KyIdentity is
  undone by the next sweep; operators offboard in KyIdentity.
- The console account is MAS user `kymessages-console` (`ConsoleUsername`): no password, no
  upstream link, never MAS admin (`can_request_admin`): personal sessions do not need it, and it
  would let an interactive login as the account request `urn:mas:admin`. `EnsureConsoleUser`
  creates it on first use (never at start-up) and refuses it locked, deactivated, MAS admin or
  linked to any upstream identity (a linked one is a person), before any session is minted.
  `Plan` skips exactly that username while it has no link and is not `Ambiguous`, so the sweep
  never locks or unlocks it; linked, it is judged as a person. Locking it in MAS cuts the
  console's Synapse admin access.
- `AsConsole` mints one MAS personal session per action: scope exactly
  `urn:matrix:client:api:* urn:synapse:admin:*`, `expires_in` 300, `human_name`
  `KyMessages console`. The mint runs on a detached 10 s context, so a session MAS creates while
  the request dies is still known and revoked. It revokes the session afterwards on a detached 10 s context, also when
  the action fails or its context ends; a failed revoke (other than 409, already revoked) is
  logged with the session ID, never the token, and the session expires on its own.
- Deactivate always sends `{"skip_erase":true}`; messages are never erased.
- A deactivated MAS user is never reactivated or otherwise touched.
- MAS must have exactly one upstream provider; any other count is an error.
- Admin calls go only under `/api/admin/v1/`; `links.next` outside it is refused. Redirects
  are not followed. One token refetch on 401, then fail. The transport ignores proxy
  environment variables, so credentials go only to `AdminURL`. One token fetch at a time;
  callers waiting for it give up with their own context (`TestTokenWaitHonoursContext`).
- `FinishSession` treats MAS's 400 for an ended session as `already`, after reading the session
  back; a 400 whose read-back shows the session live stays an error. `Sessions` returns browser sessions first; an oauth2 session's device comes from its scope.
- No secret or token appears in errors, logs or audit rows. The sweep's audit actions are exactly
  `matrix.lock`, `matrix.unlock`, `matrix.deactivate` (`matrix.session_end` is written by
  `internal/api`), written for success and failure, on a
  context detached from the sweep's so a completed action is audited through shutdown.
- `Run` closes `done` only between sweeps so shutdown can wait before the store closes;
  `Wake` never blocks and coalesces.

## Verification
- `go test -race ./internal/matrixsync/`
