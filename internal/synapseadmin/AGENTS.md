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
- `Close` and `Delete` are `DELETE /_synapse/admin/v2/rooms/{id}` with `block` true and
  `purge` sent explicitly (false, true): Synapse's purge defaults to true. Both start a
  background job and return its `delete_id`. Close makes every local member leave; with
  federation off the server is then out of the room and nobody can rejoin, so Close is final.
- `DeleteJobs` lists started jobs only (Synapse omits scheduled ones and gives no failure
  reason); no job is an empty list. Only 404 `M_NOT_FOUND` means "none"; `M_UNRECOGNIZED` or an
  errcode-less 404 is a routing error and stays an error.
- `RoomMedia` lists only media non-encrypted events reference; `DeleteMedia` treats 404
  `M_NOT_FOUND` as done. Encrypted rooms' attachments cannot be attributed and are never deleted.
- Messages are never read: no method fetches events.

## Verification
- `go test -race ./internal/synapseadmin/`
