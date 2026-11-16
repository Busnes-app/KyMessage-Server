# Status from calendar and out-of-office

A member's status follows their calendar and out-of-office, and KyCalendar and KyPost show it
beside people. KyMessages holds the status and answers for organisation members only. Design
and reasons: [the design](superpowers/specs/2026-11-15-presence-design.md). This is the wire
contract. KyCalendar and KyPost implement the other side, and all sides change together.

## Matrix presence is off

Synapse's own presence is off (`homeserver.yaml`: `presence: {enabled: false}`), and Element
draws none (`config.json`: `enable_presence_by_hs_url`). Synapse would otherwise send presence
and last-active times to everyone who shares a room, meeting guests and federated users
included. Element therefore shows no status. An existing deployment needs a `matrix-init`
re-run, then `docker compose restart synapse element`.

## States

| State | Shown as | `until` |
|---|---|---|
| `available` | Available | none |
| `busy` | Busy (set by the user) | none |
| `in_a_meeting` | In a meeting until HH:MM | end of the current run of meetings |
| `away` | Away (set by the user) | none |
| `out_of_office` | Out of office until <date> | end of the period |
| `unknown` | Status unknown | none |

The order, first match wins: sharing off or not a member → `unknown`; a manual status that has
not expired; out-of-office with `start <= now < end`; inside a busy block (blocks that overlap
or touch count as one); a calendar push in the last 24 hours → `available`; else `unknown`.
No last-active time exists anywhere.

## Pairing

The existing pairs, nothing new: `kycalendar`↔`kymessage` ([KYCALENDAR.md](KYCALENDAR.md)
"Pairing") and `kymessage`↔`kypost` ([SUITE-CHAT.md](SUITE-CHAT.md) "Pairing").

## Routes

All `POST` on the KyMessages console, signed with `ky-primitives/syncauth` v1 under the pair's
secret, no session. Every body names the user by `sub` and `issuer`, and is decoded strictly:
**an unknown field is 400 `bad_body`**. Times are RFC 3339 at offset zero.

| Event type | Route | Pair | Budget per user an hour |
|---|---|---|---|
| `kycalendar.presence.busy` | `/api/suite/presence/calendar` | KyCalendar | 120 |
| `kypost.presence.ooo` | `/api/suite/presence/out-of-office` | KyPost | 60 |
| `kycalendar.presence.me` | `/api/suite/presence/me` | KyCalendar | 60 |
| `kycalendar.presence.lookup`, `kypost.presence.lookup` | `/api/suite/presence/lookup` | either | 300 |

### Busy blocks (KyCalendar)

```json
{"sub":"…","issuer":"https://id.example","revision":1794760000123,
 "busy":[{"start":"2026-11-16T14:00:00Z","end":"2026-11-16T15:00:00Z"}]}
```

- The user's whole busy list from now to 48 hours ahead: the start and end of every
  occurrence of an event that is opaque, not cancelled, and organised or accepted by the user.
  Nothing else: no title, location, attendees or event ID. KyCalendar's own out-of-office event
  is left out, because out-of-office comes from KyPost.
- `revision`: KyCalendar's compute time in milliseconds. An older one than stored is
  `{"outcome":"stale"}`, the same one is applied again.
- At most 200 blocks, each `start < end`, `end` at most 49 hours ahead. Blocks already over
  are dropped. `busy` is required: `[]` means free.
- KyCalendar sends it when one of the user's events changes, and for every linked user at
  start-up and every 6 hours. Nothing is scheduled per event.

### Out-of-office (KyPost)

```json
{"sub":"…","issuer":"https://id.example","revision":1794760000123,
 "away":true,"start":"2026-12-01T05:00:00Z","end":"2026-12-05T05:00:00Z"}
```

- The whole state on every save. `away:false` turns it off, with no period needed.
- With `away:true`, both times are required, `start < end`, at most 366 days apart.
- `revision` works as above. **Never the reply text**: `comment_*` is refused.

### The user's own settings (KyCalendar)

```json
{"sub":"…","issuer":"https://id.example",
 "share":false,
 "manual":{"state":"away","until":"2026-11-16T16:00:00Z"}}
```

- Both fields are optional. Without either, the request only reads. `manual: null` clears.
- `share` is "Show my status to colleagues", on by default. Off clears the stored calendar
  blocks, out-of-office period and manual status. Later pushes answer
  `{"outcome":"sharing_off"}` and are dropped, and lookups read `unknown`.
- `manual.state` is `available`, `busy` or `away`. `until` is required, in the future and at
  most 7 days ahead.
- Answer: `{"share":true,"manual":{"state":"away","until":"…"}}` or `"manual":null`.

### Lookup (KyCalendar, KyPost)

```json
{"sub":"<caller>","issuer":"https://id.example","subs":["…","…"]}
```

Answer, in request order:
`{"people":[{"sub":"…","state":"in_a_meeting","until":"2026-11-16T15:00:00Z"},{"sub":"…","state":"unknown"}]}`.

- At most 50 `subs`. More is 400 `too_many`.
- A subject that is not an active everyday account here (an administrator, a guest, an
  outsider, a typo) reads `unknown`, the same as someone who opted out.
- Callers cache an answer for at most 60 seconds and show a dot **and** a text label.
  Colour alone never carries the meaning.

## Answers

| Status | Code | When |
|---|---|---|
| 404 | `kycalendar_off` / `kypost_off` | That event type's pair is not up. |
| 401 | `bad_signature` | Bad MAC, wrong event type for the route, the other pair's secret, a timestamp outside 5 minutes, a body over 64 KiB. |
| 200 | `{"outcome":"duplicate"}` | A verified replay (the shared `meeting_events` guard). |
| 400 | `bad_body` | Not one JSON object, an unknown field, or a rule above broken. |
| 400 | `too_many` | Lookup over 50 people. |
| 404 | `no_user` | The user is not an active everyday account (role `user`) linked to `sub` at this server's KyIdentity issuer. Administrators never. |
| 409 | `sharing_off` | A manual status while sharing is off. |
| 429 | `rate_limited` | Over the budget; `Retry-After` set. The event is forgotten, so a retry goes through. |
| 500 | `error` | Storage failed; retry under the same event ID. |
| 503 | `unavailable` | The replay guard cannot decide. |
| 200 | `{"outcome":"applied"\|"stale"\|"sharing_off"}` | A push was stored, was older than the stored one, or was dropped. |

## Rules

- Verified requests are audited as `presence.calendar`, `presence.ooo`, `presence.me` and
  `presence.lookup`, with the user ID and outcome (lookups: `via` and `count`). The audit never
  holds a subject, a time, a title or a name.
- Storage: table `presence` (migration 26), one row per subject, Unix seconds. Nothing is
  kept in memory.

## Load

Measured 2026-11-15 on Major with the pinned Synapse v1.162.0 (SQLite, 2 CPUs), 50 users in one
room, each syncing as Element does (`set_presence=online`, 30-second long poll) and five of them
sending a message every 10 seconds, for 247 seconds:

| Presence | Synapse CPU | Peak memory | Syncs | Presence events delivered |
|---|---|---|---|---|
| on (Synapse's default) | 29 s (12% of one core) | 150 MiB | 5,500 | 7,500 |
| off (shipped) | 30 s (12%) | 149 MiB | 5,500 | 0 |

At this size presence costs nothing measurable, so load is not why it is off: it is off because
it would reach meeting guests and federated users with last-active times. Postgres and more
rooms per user were not measured.

## How it works

- `internal/presence`: event types, limits, strict decoding and `Status`.
- `internal/store/presence.go`: the `presence` table (`PresenceStore`).
- `internal/api/presence_handlers.go`: the four routes.
- `internal/matrixinit/templates`: presence off in `homeserver.yaml` and Element's config.

## Verification

- `internal/presence/presence_test.go`: decoding (titles, attendees, reply text, the caps,
  times) and every rule of `Status`.
- `internal/api/presence_test.go`: every refusal in the table, opt-out, manual override,
  members only (guests, outsiders, administrators), replay, the cap, the rate limit, unpaired.
- `internal/store/presence_test.go`: revisions, opt-out clearing, both engines in CI.
- `internal/matrixinit` `TestPresenceIsOff`.
