# MatrixRTC

## Purpose
Supplies Element Call's MSC4195 hashes and LiveKit grants and room administration.

## Ownership
Owns the LiveKit HTTP client and JWT signing. `internal/api/rtc_handlers.go` owns caller
identity, directory access, membership checks and reconciliation; `matrix-init` owns secrets
and the stock LiveKit config. Media encryption stays in Element.

## Local Contracts
- MSC4195 identifiers hash compact JSON string arrays with SHA-256 and standard unpadded
  base64. Keep the upstream test vector. Both legacy `/sfu/get` and `/get_token` use that room hash.
- Join grants last 15 seconds, name one room, allow publish/subscribe/data and prohibit
  administration, recording and participant metadata changes. Signed participant metadata
  is the verified Matrix ID, used for eviction; room metadata is the Matrix room ID.
- Room API requests ignore proxy environment variables and follow no redirects. Errors
  report operation/status without tokens, secrets or response bodies.
- Active calls are ephemeral: configs and keys are backed up; calls are not recorded or restored.

## Work Guidance
Use stock Element and LiveKit. Implement only the client protocol used by the pinned Element
build; client-managed delayed leave events handle disconnects, with no delegated leave service.

## Verification
- `go test -race ./internal/matrixrtc/ ./internal/api/ -run 'TestMSC4195|TestJoinToken|TestLiveKit|TestRTC'`
- `make matrix-acceptance` drives Element calls with fake camera/microphone, requiring both users
  to receive audio and decode video.

## Child DOX Index
None.
