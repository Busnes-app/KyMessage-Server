# KyCalendar meeting rooms

KyCalendar asks KyMessages for a video room for a meeting; the console's service account makes
an encrypted, invite-only Matrix room and keeps its name and members in step with the event.
People outside the organisation join only while an administrator has turned **Meeting
guests** on (default off; see "Guests" and
[the design](superpowers/specs/2026-10-18-kymessages-meeting-rooms-design.md)).

This is KyMessages' copy of section B of the suite wire contract
(`ky-redesign/integrations/suite-integrations-contracts.md`, wave 1, 2026-11-14) and of
section L (`suite-integrations-wave2.md`, wave 2, 2026-11-14), and is the durable one.
KyCalendar implements the other side; both sides change together.

## Calls

Element v1.12.30 hides its call buttons in any room with more than two joined members while
`UIFeature.widgets` is off (`useRoomCall.ts:346`), and the console stays joined, so a meeting
room always has three. `matrix-init` turns `UIFeature.widgets` on while calls are on (owner
decision 2026-11-14; integrations stay off), and members start Element Call from the room. A
room widget needs level 100 in meeting rooms, so only the console (which adds none) may add
one. Evidence: [2026-11-group-calls.md](evidence/2026-11-group-calls.md),
[2026-11-meeting-rooms.md](evidence/2026-11-meeting-rooms.md).

## Deviations from the suite contract

- **Environment names** use this repo's `KY_` prefix: `KY_KYCALENDAR_URL` and
  `KY_KYCALENDAR_SECRET_FILE`. There is no `KY_KYCALENDAR_ALLOW_PRIVATE`: KyMessages never
  connects to KyCalendar, so the URL is only shown on the console and no private-address rule
  applies. Nothing on the wire changes.
- **The console is the room's creator, not a power-level-100 entry.** Synapse v1.162.0 creates
  room version 12 rooms, in which the creator outranks every power level and may not appear in
  `users` (MSC4289). The console therefore holds creator power; the organizer is listed at 50,
  everyone else defaults to 0, `invite` and `kick` are 50, exactly as the contract says.
- **The meeting ID is also in the body.** syncauth signs the event type, event ID, timestamp
  and body, not the URL, so a request intercepted before it arrives could be sent to another
  meeting's path. Both bodies may carry `"id"`, the path's meeting ID; a body naming another ID
  is 400 `bad_body` (`kycalendar.SameID`). A body without `"id"` is still accepted, so a
  KyCalendar from before it sent one keeps working; once every KyCalendar sends it, it can
  become required.

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
{"id":"<the path's {id}>","sub":"<organizer>","issuer":"https://id.example","title":"Weekly sync",
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
- `guests` and `windows`: see "Guests". With Meeting guests off, a non-empty `guests` is 422
  `guests_off` (KyMessages before guests answered `guests_unsupported`); `windows` is still
  stored.
- History is visible from joining: a member never sees what was said before they joined.
- Room state: join rule `invite`, history visibility `joined`, guest access `forbidden`,
  encryption `m.megolm.v1.aes-sha2`. Power levels: the console is the creator (see the
  deviation), the organizer 50, members 0, `invite` 50, `kick` 50, and
  `org.matrix.msc3401.call.member` 0 so every member can join the call, as stock Element sets
  when Element Call is on. A new organizer takes over level 50; every PUT checks the power levels again, so a transfer
  interrupted by a failure is repaired by the next PUT.
- A room closed from the console's Rooms page closes the meeting: the PUT answers 409 `closed`.

```json
{"member_url":"https://chat.example/#/room/!abc:example","skipped_subs":["…"],"guest_urls":{}}
```

| Status | Code | When |
|---|---|---|
| 404 | `kycalendar_off` | Not paired. |
| 401 | `bad_signature` | |
| 400 | `bad_body` | Malformed, bad `{id}`, a body `id` other than `{id}`, title empty, over 255 bytes or with control characters, more than 100 members; a guest key that is not `[A-Za-z0-9_-]{16,64}` or repeats, more than 100 guests, more than 400 windows, a window not RFC 3339 UTC, not ending after it starts, or ending more than 91 days ahead. |
| 404 | `no_user` | Organizer has no active KyMessages account linked to that subject. |
| 409 | `closed` | The meeting was closed; a closed ID is never reopened. |
| 422 | `guests_off` | A non-empty `guests` while Meeting guests is off. |
| 429 | `rate_limited` | `Retry-After` set. |
| 503 | `unavailable` | Matrix stack unreachable; retry. |

## DELETE

Closes the room as the console's Close does (blocked, members out, history kept). The body is
read only for its signature and an `"id"` (KyCalendar sends `{"id","sub","issuer"}`); an `id`
other than the path's is 400 `bad_body`. 200 `{"outcome":"closed"}`; an unknown or already closed ID is
also 200. A closed meeting's room is checked again on every DELETE, so a Close job that failed
is retried. A close still running, or an unreachable Matrix stack, is 503 `unavailable`.
Closing revokes every guest link of the meeting and drops its windows; the reconciler ends any
live guest within 5 seconds.

## Guests

Owner decisions 2026-10-18 (the design) and 2026-11-14 (wave 2): outsiders join meeting rooms
only, from a waiting room, during the meeting, behind the administrator switch **Settings →
Server → Meeting guests** ("Let people outside the organisation join meetings"), off by
default. Turning it on needs a sign-in from the last 10 minutes (`POST
/api/admin/meeting-guests/enable`); off is one click (`…/disable`) and ends every live guest
before it answers. `GET /api/admin/meeting-guests` is `{"enabled","live"}`. Audited
`guests.enable` / `guests.disable`.

### Wire (contract L)

PUT body gains, beside section B's fields:

```json
{"guests":[{"key":"<opaque per-attendee key>"}],
 "windows":[{"start":"2026-11-15T08:50:00Z","end":"2026-11-15T10:00:00Z"}]}
```

- `key`: `[A-Za-z0-9_-]{16,64}`, at most 100, never a name or address. Stored only as its
  SHA-256.
- `windows`: at most 400, RFC 3339 UTC, start before end, ending at most 91 days ahead
  (KyCalendar sends 90 and refreshes daily). KyCalendar sends each occurrence as
  `[start - 10 min, end]`; KyMessages enforces exactly what it is given. Each PUT replaces them.
- The answer gains `guest_urls: {key: url}`, only for keys new in this call (a key dropped and
  listed again is new). A key dropped from the list revokes its link at once. A URL is shown
  once: KyMessages keeps only its hash, and an exact replay answers `duplicate`, so if
  KyCalendar loses an answer it sends that attendee under a new key.
- A PUT naming guests makes the room a waiting room: join rule `knock`, `invite` at 0, so any
  member may admit a knock (decision 6). It stays so.

### The guest's way in

1. **Link:** `https://<admin host>/meet#<token>`, 256 random bits, bound to one meeting and
   one key. The token is in the fragment, so no server or proxy log ever sees it (a small
   change from the design's `/meet/<token>`; KyCalendar treats the URL as opaque).
2. **Page:** the console's app at `/meet` asks `POST /api/meet/status {"token"}`: `open`
   (with `closes_at`), `waiting` (`opens_at`), `ended`, or 404 `not_found` for an unknown,
   revoked or malformed token, a closed meeting and guests off alike. 60 a minute per address.
3. **Join:** inside a window, `POST /api/meet/join {"token","name","captcha"}`: a name of 1–64
   characters with no control, format (bidi, zero-width), line-separator or private-use
   characters, and a solved proof-of-work challenge (always, whatever `KY_CAPTCHA_PROVIDER`
   says). 10 attempts per address per 15 minutes and 10 per link. KyMessages then:
   - ends the link's earlier account, if any (one seat per link: a forwarded link cannot seat
     two people);
   - creates an unlinked, passwordless MAS user `guest-<20 hex>` (as agents are made) and a
     personal session on device `KYGUEST…`, expiring at the window's end;
   - records it, sets its name to `<name> (guest)`, sets the console's
     `app.kymessages.guest` marker for it in the room, and knocks with its token;
   - answers `{"next":"https://<chat host>/kymessages-guest.html#<code>"}`, a one-time code
     held in memory for 2 minutes. Audited `guest.knock`.
   Outside a window: 409 `not_open`, and nothing is created.
4. **Bootstrap page** (spike S1): `kymessages-guest.html`, rendered by `matrix-init` and
   mounted read-only into Element beside `config.json`, runs only its own constant script (pinned by
   hash in its CSP; it reads the console's origin from its `data-admin` attribute; `connect-src` is
   the console only). It drops the code from the address bar,
   trades it at `POST /api/meet/session` (body: the code as text; only Element's origin may
   read the answer, no credentials, answered once), writes the session where Element restores
   sessions from (`mx_hs_url`, `mx_user_id`, `mx_device_id`, `mx_access_token`) and opens the
   room. A browser signed in to chat as a member is left alone ("open your meeting link in a
   private window"); an earlier guest's session and stores are replaced.
5. **Admission** (spike S2): members see "asks to join" in Element's room header
   (`features.feature_ask_to_join`, set by `matrix-init`), also during a call, and any member
   approves it (an invite, audited `guest.admit` with the member). The guest accepts and joins.

### While in the meeting

- **Call grant:** a guest gets one only for its own meeting's room, while its account is live,
  its link stands, guests are on, the meeting is open, now is inside one of its windows, and its
  membership is `join` (the admin member list holds joined members only). A guest only joins a
  call a member started. The call reconciler removes a guest from any other room's call.
- **Leaving means knocking again:** a joined guest out of the call for 20 seconds after being in
  it (2 minutes if it never joined it) is removed from the room (audited `guest.leave`) and must
  knock again. Not with calls off.
- **End:** at its window's end (also when a PUT shortens or drops the window it is in), when its link is revoked, when the meeting closes, or when
  guests are turned off, the reconciler (every 5 seconds) removes the guest from the call and
  the room, revokes its session and deactivates the account (audited `guest.expire` with the
  reason). The MAS sweep leaves a live guest's account alone and locks any other `guest-` one.
- **Confinement:** Synapse's `ky_guests` module (`internal/matrixinit/AGENTS.md`) holds every
  `guest-` account to the room the console marks for it.
- **Audit and logs:** IDs only: meeting ID, the first 16 hex digits of the key's hash, the
  guest's Matrix ID and, for an admission, the admitting member. Never a name, title, token or
  code.

## How it works

- `internal/kycalendar`: event types, body checks, `Verify`, the in-memory pair (`Link`).
- `internal/api/suite_pair.go`: the pair's lookup and the console status route, shared with
  the KyPost pair ([SUITE-CHAT.md](SUITE-CHAT.md)).
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

- `go test ./internal/api/ -run Guest`, `./internal/kycalendar/`, `./internal/store/`,
  `./internal/matrixsync/ -run Guest`, `./internal/matrixinit/ -run Guest` (the confinement rules
  and the bootstrap page).
- `make matrix-acceptance`, step `guests` (after `console`; CI's meeting-guests leg): off
  refuses; on, two links; a link outside its window shows when it opens and joining makes
  nothing; inside, the guest follows its link through the bootstrap page, alice admits its
  knock from inside her call, it joins the call and both decode audio and video; out of the call
  it is removed by the console and gets no grant there or elsewhere; its token cannot create a
  room, DM, find anyone, knock elsewhere or invite; a dropped key's link is refused; a second
  join ends the first account; at the window end the account is deactivated; turning guests off
  ends the live guest and refuses guests; closing revokes the links; every guest action is
  audited with IDs only.

- `go test ./internal/kycalendar/ ./internal/synapseadmin/ ./internal/store/ ./internal/config/ ./internal/api/ -run 'Meeting|KyCalendar|DecodePut|Verify|ValidID|SetOrganizer|PathGuard'`
- `make matrix-acceptance`, step `meetings` (after `prove`): a signed PUT makes the room for
  alice and bob (carol, never signed in to chat, skipped), both accept the invite in Element and
  read each other's encrypted messages (no call: see "Calls"); no plaintext in a
  `pg_dump`; the console has no device keys; a title change renames the room; an exact replay
  is `duplicate`; a PUT without bob removes him; DELETE closes and blocks the room; a PUT after
  is 409 `closed`.
- `.github/workflows/suite-meetings.yml` (nightly, on dispatch, and on pull requests touching
  the meeting code or the harness): the same harness with a real KyCalendar and KyIdentity built
  from their main branches, step `suite-meetings`. Both apps pair only through KyIdentity's
  `/api/suite/peer`. alice's event in KyCalendar's editor, with bob and carol, makes the room;
  the stored event carries `CONFERENCE;VALUE=URI;FEATURE=VIDEO;LABEL=KyMessages:<url>` and ends
  with `Join: <url>`; Join opens the room in Element; both accept and hold a video call there;
  carol (no chat account) is skipped, and after she signs in a rename invites her; removing her
  removes her; switching the meeting off, and deleting another event, close their rooms. Step
  `suite-guests` drives guests from KyCalendar, with a KyPost stand-in on the harness proxy that
  records what KyCalendar hands KyPost to mail: with Meeting guests on, an outside attendee's
  invitation, and only theirs, carries a guest link; the guest opens it, knocks, alice admits
  them from her call and both decode media; with it off, the next event keeps its member room
  and the editor names the attendee who cannot join. Evidence:
  `docs/evidence/2026-11-suite-wave2.md`. Not in `ci.yml`: an unpinned peer must not fail this
  repo's pull requests.
