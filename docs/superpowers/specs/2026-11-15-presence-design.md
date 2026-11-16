# Presence from calendar and out-of-office: design

Owner request 2026-11-15 (worker `g-presence`): a member's status follows their calendar and
out-of-office, and shows beside people in the other suite apps. This is the design. The wire
contract it creates is [docs/PRESENCE.md](../../PRESENCE.md), which is the durable copy.

## What Synapse does today (checked 2026-11-15, Synapse v1.162.0 as pinned)

- `homeserver.yaml.tmpl` has no `presence:` key. Synapse's default is **on**:
  `synapse/config/server.py` reads `presence.enabled`, falls back to `use_presence`, and
  defaults to `True`.
- With presence on, Synapse sends each user's state to **everyone who shares a room** with
  them. That includes meeting guests (local `guest-*` accounts in meeting rooms) and users of
  federated servers on the allow-list. Every update carries `last_active_ago` whenever a
  last-active time is recorded (`format_user_presence_state`), and Element 1.12.30's member
  list shows it ("Online for 5 minutes", "Last seen 2 hours ago").
- The only routing hook, a presence router module (`synapse/events/presence_router.py`),
  can only **add** recipients ("in addition to those they share a room with"). No setting
  removes room members from the audience, and none strips `last_active_ago`.
  `exclude_rooms_from_presence` is a static list read at start-up, and meeting rooms are made
  while the server runs.
- Element Web has no way to set your own status: it only sends online/idle from activity.

So stock Synapse with stock Element (owner decision 2026-10-02) cannot meet two hard
requirements of this feature: guests and outsiders see nothing, and no last-active time is
shown. It already breaks both today, because presence is on by default.

## Decision

1. **Matrix presence goes off, explicitly.** `homeserver.yaml` gets `presence: {enabled: false}`
   and Element's config gets `enable_presence_by_hs_url: {<homeserver>: false}`, so Element
   draws no presence dots and no "last seen". This closes today's leak to meeting guests and
   federated room members. `matrix-init` rewrites `homeserver.yaml` and `config.json`; an
   existing deployment picks it up on its next `matrix-init` run and Synapse/Element restart.
2. **The suite status lives in KyMessages**, keyed by KyIdentity subject, and is served only
   to organisation members, through signed suite routes. KyCalendar and KyPost show it beside
   people. A guest has no KyIdentity subject and a federated user has no account here, so
   neither can ask, and neither can be looked up.
3. **Coarse states only:** `available`, `busy`, `in_a_meeting`, `away`, `out_of_office`,
   `unknown`. `in_a_meeting` carries the end of the current meeting block and
   `out_of_office` the end of the period. No last-active time is stored or returned, because
   KyMessages never sees activity.

Showing the status inside Element would need a Synapse module that rewrites presence for each
recipient, which the module API does not offer. That is owner question 1. The default is no.

## Threat model

- **Who can read a status:** only a request signed under a paired app's secret, naming an
  active everyday KyMessages account (`role user`, at this server's KyIdentity issuer). The
  people looked up must pass the same rule. Anyone else reads as `unknown`, so an answer
  never says whether a subject exists. Administrator identities never pass either way.
- **Forged or replayed pushes:** syncauth v1, one event type per route, checked before the MAC;
  the persistent replay guard (`meeting_events`) is shared with meetings and chat. A newer
  `revision` wins, and an older one is `stale`, so a delayed retry cannot undo a later change.
- **The other app speaking for the wrong pair:** each route accepts only its pair's event
  types, and the event type picks the secret.
- **What leaves KyCalendar:** start and end of busy blocks only. No title, location,
  attendees, description or event ID. KyMessages decodes strictly: a body with any other field
  is refused, so a sender that starts leaking is caught by its own tests and by ours.
- **What leaves KyPost:** whether out-of-office is on, and the period. Never the reply text.
  A strict decode refuses `comment_*` and any other field.
- **Enumeration and scraping:** at most 50 people a call, 300 calls per caller an hour, and
  callers cache for at most 60 seconds.
- **Logs and audit:** IDs and outcomes only. Never a subject, a time, a title or a name.

## Data stored (table `presence`, migration 26)

One row per subject that has any data or has opted out:

| Column | Meaning |
|---|---|
| `subject` | KyIdentity subject (primary key) |
| `share_off` | 1 once the user turned "Show my status to colleagues" off |
| `manual_state`, `manual_until` | A status the user chose (`available`, `busy`, `away`) and when it expires |
| `ooo_revision`, `ooo_start`, `ooo_end` | KyPost's last out-of-office state (0 when off) |
| `cal_revision`, `cal_at`, `busy` | KyCalendar's last push: its revision, when it arrived, and the busy blocks (`start-end` Unix seconds, newline separated, at most 200, none ending in the past) |

Turning sharing off deletes the manual status, the out-of-office period and the busy blocks in
the same write, and later pushes are answered `sharing_off` and dropped. Nothing is kept in
memory. A busy block ends at most 49 hours ahead.

## Routes (all on KyMessages, all `POST`, all signed, no session)

| Event type | Route | Pair | Purpose |
|---|---|---|---|
| `kycalendar.presence.busy` | `/api/suite/presence/calendar` | KyCalendar | The user's busy blocks for the next 48 hours |
| `kypost.presence.ooo` | `/api/suite/presence/out-of-office` | KyPost | Out-of-office on/off and its period |
| `kycalendar.presence.me` | `/api/suite/presence/me` | KyCalendar | Read or change the user's own switch and manual status |
| `kycalendar.presence.lookup`, `kypost.presence.lookup` | `/api/suite/presence/lookup` | either | Up to 50 people's coarse status |

Bodies, answers and refusals are in [docs/PRESENCE.md](../../PRESENCE.md).

## How a status is worked out (at read time)

1. Sharing off, or not an organisation member: `unknown`.
2. A manual status that has not expired: that state. It wins over everything else.
3. Out-of-office with `start <= now < end`: `out_of_office`, until `end`.
4. Inside a busy block: `in_a_meeting`, until the end of the run of blocks that overlap or
   touch it.
5. A calendar push in the last 24 hours: `available`.
6. Otherwise `unknown`. Without a recent calendar push KyMessages does not know.

## Scheduling

Nothing is scheduled per event, so nothing is held in memory and nothing is lost on a
restart. KyCalendar sends the whole 48-hour busy list for a user when one of their events
changes, and for every linked user at start-up and every 6 hours. KyMessages works out the
status from the stored blocks when asked. A missed refresh shows as `unknown` after 24 hours,
never as a stale meeting.

## Defaults

- "Show my status to colleagues": **on** for every member (owner brief). Off makes the user
  `unknown` everywhere at once.
- The routes need a pair. KyCalendar's busy push and settings also need KyCalendar's own
  scheduling to be on. No administrator switch is added: the status reaches organisation
  members only, never outsiders.
- Manual statuses expire after at most 7 days. One that never expires is refused.

## Not built, and why

- **Presence inside Element** (see Decision). Owner question 1.
- **A manual status set in Element.** Element has no status setter, and Synapse presence is
  off. The manual status is set from KyCalendar's settings through `presence/me`. Owner
  question 2.
- **The KyPost out-of-office hook.** It waits for `f-ooo` (MySlop `kypost-out-of-office`) to
  finish. The KyMessages route is built and tested.
- **The 50-user load test** measured Synapse's presence cost with presence on, against the
  `off` this design ships. See [docs/PRESENCE.md](../../PRESENCE.md) "Load".
- **Busy/tentative distinctions, working hours and per-person sharing.** Not asked for.
