# Synapse admin client

## Purpose
Calls Synapse's admin API for the operator console, and the client API for KyCalendar meeting
rooms, with a short-lived console session token.

## Ownership
Owns `Client`, `Error` and the room types. `internal/api` builds requests from it through
`api.RoomAdmin` and `api.MeetingRooms`; `matrixsync.Client.AsConsole` supplies the token.

## Local Contracts
- Only paths under `/_synapse/admin/`, plus the client API's `createRoom`, `rooms/…`, `knock/…`
  and `profile/…` for meeting rooms and their guests (`allowedPath`); anything else is refused
  before a request is made.
- Meeting guests (`guests.go`): `AllowKnocks` (join rule `knock` and `invite` 0, nothing sent
  for what holds), `MarkGuest` (state `app.kymessages.guest`, key the guest's localpart, the
  marker the confinement module trusts from the console only), `SetDisplayName` and `Knock`
  (with the guest's own token), `MemberStates` (membership and sender, admin state API).
- Meeting rooms (`meetings.go`, used by `internal/api` `MeetingRooms`): `CreateMeetingRoom`
  (`private_chat` preset; initial state join rule `invite`, history `joined`, guest access
  `forbidden`, Megolm encryption; power levels organizer `OrganizerLevel` 50, default 0,
  `invite`/`kick` 50, room version 12 defaults plus `org.matrix.msc3401.call.member` 0, as stock
  Element sets with Element Call, and `im.vector.modular.widgets` 100, so nobody but the console
  may add a room widget; the console is the creator and is never listed; organizer `""` is a
  suite group room: `users` empty, so nobody but the console can invite or remove),
  `Memberships` (admin state API, `m.room.member` only), `Invite`, `Kick`, `SetRoomName`,
  `SetOrganizer` (only the organizer stays at 50; everything else kept; no write when that
  already holds, so `internal/api` calls it on every PUT) and
  `ExemptFromRatelimit` (`override_ratelimit` 0/0 for the console account), and
  `SetReadOnly` (suite group archive: `events_default`, `invite` and the call member event at
  100, state still readable; false restores 0/`OrganizerLevel`/0; no write when settled).
- The transport ignores proxy variables and follows no redirects: the token goes to the
  configured origin or nowhere. `api.NewServer` passes the Synapse origin its Health probes
  use (`health.ComposeTargets.Synapse`, `http://synapse:8008`).
- Errors carry method, path, status and Synapse's errcode and message (clipped to 200 bytes),
  never the token.
- `Rooms` orders by name and omits an empty search (Synapse refuses an empty `search_term`).
  `StateEvents` is the only size-like count Synapse reports; there is no byte size.
- `Close` and `Delete` are `DELETE /_synapse/admin/v2/rooms/{id}` with `block` true and
  `purge` sent explicitly (false, true): Synapse's purge defaults to true. Both start a
  background job and return its `delete_id`. Close makes every local member leave. With no
  members from other servers, this server is then out of the room and nobody can rejoin, so
  Close is final; with members from other servers (federation open), the room lives on there
  and Close only removes this server.
- `DeleteJobs` lists started jobs only (Synapse omits scheduled ones and gives no failure
  reason); no job is an empty list. Only 404 `M_NOT_FOUND` means "none"; `M_UNRECOGNIZED` or an
  errcode-less 404 is a routing error and stays an error.
- No method deletes media. Synapse's room media listing names any local media a plaintext
  event references, including media other rooms use, so it cannot scope a deletion.
- Messages are never read or sent. `AdminInvite` (the meeting-notes inviter check) reads room state by
  type and fetches only the given user's own earlier membership events, walking joins back to
  the invite; any other event it is handed (another user, room or type) ends the walk with no
  inviter. Admin means room creator (`m.room.create` sender or `additional_creators`) or power
  level 100. Lookup failures are errors, never a verdict.

## Verification
- `go test -race ./internal/synapseadmin/`
