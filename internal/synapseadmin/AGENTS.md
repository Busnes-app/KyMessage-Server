# Synapse admin client

## Purpose
Calls Synapse's admin API for the operator console with a short-lived console session token.

## Ownership
Owns `Client`, `Error` and the room types. `internal/api` builds requests from it through
`api.RoomAdmin`; `matrixsync.Client.AsConsole` supplies the token.

## Local Contracts
- Only paths under `/_synapse/admin/`; anything else is refused before a request is made.
- The transport ignores proxy variables and follows no redirects: the token goes to the
  configured origin or nowhere. `api.NewServer` passes the Synapse origin its Health probes
  use (`health.ComposeTargets.Synapse`, `http://synapse:8008`).
- Errors carry method, path, status and Synapse's errcode and message (clipped to 200 bytes),
  never the token.
- `Rooms` orders by name and omits an empty search (Synapse refuses an empty `search_term`).
  `StateEvents` is the only size-like count Synapse reports; there is no byte size.

## Verification
- `go test -race ./internal/synapseadmin/`
