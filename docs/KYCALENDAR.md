# KyCalendar meeting rooms

KyCalendar asks KyMessages for a video room for a meeting; the console's service account makes
an encrypted, invite-only Matrix room and keeps its name and members in step with the event.
Members only: people outside the organisation cannot join yet (guests, guest windows,
`/meet/<token>` and the Synapse confinement module stay unbuilt; see
[the design](superpowers/specs/2026-10-18-kymessages-meeting-rooms-design.md)).

This is KyMessages' copy of section B of the suite wire contract
(`ky-redesign/integrations/suite-integrations-contracts.md`, wave 1, 2026-11-14) and is the
durable one. KyCalendar implements the other side; both sides change together.

## Deviations from the suite contract

- **Environment names** use this repo's `KY_` prefix: `KY_KYCALENDAR_URL` and
  `KY_KYCALENDAR_SECRET_FILE`. There is no `KY_KYCALENDAR_ALLOW_PRIVATE`: KyMessages never
  connects to KyCalendar, so the URL is only shown on the console and no private-address rule
  applies. Nothing on the wire changes.
- **The console is the room's creator, not a power-level-100 entry.** Synapse v1.162.0 creates
  room version 12 rooms, in which the creator outranks every power level and may not appear in
  `users` (MSC4289). The console therefore holds creator power; the organizer is listed at 50,
  everyone else defaults to 0, `invite` and `kick` are 50, exactly as the contract says.
- **The meeting ID in the path is not signed.** syncauth signs the event type, event ID,
  timestamp and body, not the URL. The replay guard keys on the event ID across both routes,
  so a captured request cannot be sent again to another meeting, but a request intercepted
  before it arrives could be redirected to another ID. Wave 2 should put `"id"` in the body.

## Pairing

- **Environment first.** `KY_KYCALENDAR_URL` (an https origin, no path) and
  `KY_KYCALENDAR_SECRET_FILE` (at least 32 bytes, trimmed) are set together or not at all, and
  only with the Matrix stack; any other combination stops start-up. The secret stays in memory.
- **Otherwise KyIdentity.** With Matrix on and no environment pair, start-up asks
  `suitedir.Peer(ctx, "kycalendar")` with the console's own suite sign-in client credentials
  (waiting at most 5 s), then refreshes hourly; a failure retries after 1 minute, doubling up
  to an hour. A refused signature on a KyIdentity pair asks again, at most once a minute. An
  unreachable KyIdentity keeps the current pair; no peer, refused credentials or an invalid
  URL unpair.
- **Console.** Settings → Server shows a KyCalendar row (`GET /api/admin/kycalendar`, admins):
  `connected`, `source` (`environment` or `kyidentity`), `url`, `reason` when not connected
  (`pending`, `no_kyidentity`, `no_peer`, `unauthorized`, `unreachable`, `invalid_peer`,
  `no_credentials`; `matrix_off` hides the row) and `checked_at`. Never the secret.

## Shared rules

- Signed with `ky-primitives/syncauth` v1 under the pair secret. The receiver checks the event
  type equals the route's, then verifies with a replay guard. A verified replay answers 200
  `{"outcome":"duplicate"}`; any other failure 401 `{"error","code":"bad_signature"}`
  (including a body over 64 KiB and a timestamp outside 5 minutes).
- The replay guard is a table (`meeting_events`), so a restart does not forget an event. An
  event verified but not acted on (429, 503) is forgotten, so KyCalendar's retry under the same
  ID is applied.
- Unpaired: 404 `kycalendar_off`.
- Bodies name the user by `"sub"` and `"issuer"`. The organizer must be an active everyday
  account (role `user`) linked to that KyIdentity subject at this server's issuer, else 404
  `no_user`. Administrators cannot organize.
- Refusals are `{"error","code"}`, code `[a-z0-9_]{1,64}`.
- Per-organizer rate limit: 120 requests an hour across both routes; 429 `rate_limited` with
  `Retry-After`.
- Audit: `meeting.put` and `meeting.close`, resource the meeting ID, details the outcome, room
  ID and counts only. Never the title, a subject, a name or the secret. Unverified requests
  (no pair, bad signature) are not audited, so an anonymous caller cannot fill the audit log.

| Event type | Route |
|---|---|
| `kycalendar.meeting.put` | `PUT /api/meetings/{id}` |
| `kycalendar.meeting.close` | `DELETE /api/meetings/{id}` |

`{id}`: KyCalendar's opaque meeting ID, `[A-Za-z0-9_-]{16,64}`, random, never the event UID.

## PUT (idempotent)

```json
{"sub":"<organizer>","issuer":"https://id.example","title":"Weekly sync",
 "member_subs":["…","…"]}
```

- Creates the room on first call, reconciles after: room name = title, listed members invited
  (again, if they left), members no longer listed removed. The organizer is always a member.
  Only people a PUT listed are ever removed: someone the organizer invited by hand in Element
  stays. Every invite target is recorded before it is invited, so an invite that went through
  before a later failure is still removed by a later PUT.
- `member_subs` holds KyIdentity subjects. A subject with no KyMessages chat account (no
  unlocked Matrix account linked to it, or not active in the directory), or one banned from the
  room, is skipped, not refused, and reported in `skipped_subs`; a later PUT picks it up.
- A `guests` field that is present and non-empty: 422 `guests_unsupported`. `windows` is
  ignored.
- Room state: join rule `invite`, history visibility `joined`, guest access `forbidden`,
  encryption `m.megolm.v1.aes-sha2`. Power levels: the console is the creator (see the
  deviation), the organizer 50, members 0, `invite` 50, `kick` 50, and
  `org.matrix.msc3401.call.member` 0 so every member can join the call, as stock Element sets
  when Element Call is on. A new organizer takes over level 50.
- A room closed from the console's Rooms page closes the meeting: the PUT answers 409 `closed`.

```json
{"member_url":"https://chat.example/#/room/!abc:example","skipped_subs":["…"]}
```

| Status | Code | When |
|---|---|---|
| 404 | `kycalendar_off` | Not paired. |
| 401 | `bad_signature` | |
| 400 | `bad_body` | Malformed, bad `{id}`, title empty, over 255 bytes or with control characters, more than 100 members. |
| 404 | `no_user` | Organizer has no active KyMessages account linked to that subject. |
| 409 | `closed` | The meeting was closed; a closed ID is never reopened. |
| 422 | `guests_unsupported` | A non-empty `guests`. |
| 429 | `rate_limited` | `Retry-After` set. |
| 503 | `unavailable` | Matrix stack unreachable; retry. |

## DELETE

Closes the room as the console's Close does (blocked, members out, history kept). The body is
not read beyond its signature. 200 `{"outcome":"closed"}`; an unknown or already closed ID is
also 200. A closed meeting's room is checked again on every DELETE, so a Close job that failed
is retried. A close still running, or an unreachable Matrix stack, is 503 `unavailable`.

## How it works

- `internal/kycalendar`: event types, body checks, `Verify`, the in-memory pair (`Link`).
- `internal/api/kycalendar_link.go`: the pair's lookup and the console status route.
- `internal/api/meeting_handlers.go`: the two routes. Changes run one at a time under the
  console account (`matrixsync.Client.AsConsole`), on a context detached from the request so a
  dropped connection cannot leave a change unaudited. People are resolved as call grants
  resolve them: the MAS account linked to the subject (`matrixsync.Client.Users`), never an
  MXID derived from an email.
- `internal/synapseadmin/meetings.go`: `createRoom`, invite, kick, name and power levels
  through Synapse's client API (only `createRoom` and `rooms/…` are reachable), membership by
  the admin state API, and `override_ratelimit` for the console so a 100-person invite is not
  throttled.
- `meetings` (store migration 24): meeting ID → room ID, organizer subject, the Matrix IDs it
  listed, state `open`/`closed`.
- The console never reads or sends messages and holds no device keys, so no room key is ever
  shared with it (`docs/evidence/2026-11-meeting-rooms.md`).

## Verification

- `go test ./internal/kycalendar/ ./internal/synapseadmin/ ./internal/store/ ./internal/config/ ./internal/api/ -run 'Meeting|KyCalendar|DecodePut|Verify|ValidID|SetOrganizer|PathGuard'`
- `make matrix-acceptance`, step `meetings` (after `prove`): a signed PUT makes the room for
  alice and bob (carol, never signed in to chat, skipped), both accept the invite in Element,
  read each other's encrypted messages and decode each other's video; no plaintext in a
  `pg_dump`; the console has no device keys; a title change renames the room; an exact replay
  is `duplicate`; a PUT without bob removes him; DELETE closes and blocks the room; a PUT after
  is 409 `closed`.
