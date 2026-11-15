# Chat from KyPost and KyCalendar

KyPost (a message's sender, a contact) and KyCalendar (an event's attendee) ask KyMessages for
a link that opens a chat with someone in the organisation. KyMessages answers with Element's
user view of that person, which offers "Message" and reuses an existing direct chat. The
console creates nothing.

This is KyMessages' copy of section H of the suite wire contract
(`ky-redesign/integrations/suite-integrations-wave2.md`, 2026-11-14, on wave 1's shared rules
in `suite-integrations-contracts.md`) and is the durable one. KyPost and KyCalendar implement
the other side; all sides change together.

## Deviations from the suite contract

- **Environment names** use this repo's `KY_` prefix: `KY_KYPOST_URL` and
  `KY_KYPOST_SECRET_FILE`, as `KY_KYCALENDAR_*` ([KYCALENDAR.md](KYCALENDAR.md)). There is no
  `_ALLOW_PRIVATE`: KyMessages never connects to either app.
- **503 `unavailable`** is added to the table: the answer when the Matrix stack (MAS) cannot
  say who has a chat account, or the replay guard cannot decide. Retry. The event is
  forgotten, so a retry under the same event ID is applied.

## Pairing

- **KyPost** is a second pair, exactly as KyCalendar's (see [KYCALENDAR.md](KYCALENDAR.md)
  "Pairing"): `KY_KYPOST_URL` (https origin) and `KY_KYPOST_SECRET_FILE` (at least 32 bytes)
  together, Matrix stack only; otherwise `suitedir.Peer(ctx, "kypost")` with the console's
  suite sign-in client, the same refresh, backoff, kick and unpair rules. KyIdentity must list
  the pair `{"kymessage","kypost"}` (contract G).
- **KyCalendar** chat uses the existing KyCalendar pair; nothing new to configure.
- **Console.** Settings → Server shows a KyPost row beside KyCalendar's
  (`GET /api/admin/kypost`, admins): the same `{connected, source, url, reason, checked_at}`,
  never the secret; `matrix_off` hides the row.

## The route

| Event type | Route on KyMessages console |
|---|---|
| `kypost.chat.open` | `POST /api/suite/chat` |
| `kycalendar.chat.open` | `POST /api/suite/chat` |

One route, two event types; each verifies only under its own pair's secret (the event type
picks the pair, then syncauth checks the type and the MAC). Any other event type is judged
against the KyPost pair, so it is `kypost_off` or `bad_signature`.

```json
{"sub":"<caller>","issuer":"https://id.example","target_sub":"<person to chat with>"}
```

200 `{"url":"https://chat.example/#/user/@bob:example"}`: the console's Element host
(`KY_MATRIX_CHAT_HOST`) and the target's Matrix ID.

| Status | Code | When |
|---|---|---|
| 404 | `kypost_off` / `kycalendar_off` | Not paired (for that event type's pair), or no Matrix stack. |
| 401 | `bad_signature` | Bad MAC, wrong event type, the other pair's secret, a timestamp outside 5 minutes, a body over 64 KiB. |
| 200 | `{"outcome":"duplicate"}` | A verified replay. The replay guard is meetings' `meeting_events` table, shared across both pairs. Sign each request with a fresh event ID. |
| 400 | `bad_body` | Not a JSON object; `sub`, `target_sub` not 1 to 255 bytes, `issuer` not 1 to 512, of UTF-8 without control characters. Unknown fields are ignored. |
| 404 | `no_user` | The caller is not an active everyday account (role `user`) linked to that subject at this server's KyIdentity issuer, or has no unlocked Matrix account. Administrators never. |
| 404 | `no_target` | The target fails the same rule, or is the caller. |
| 429 | `rate_limited` | Over 60 a caller an hour, both pairs together; `Retry-After` set. |
| 503 | `unavailable` | See deviations. |

## Rules

- People are resolved as meeting rooms and call grants resolve them (`meetingPeople`): the MAS
  account linked to the subject, unlocked, and reported active by the directory. Never an MXID
  derived from an address.
- Audit `chat.open` for verified requests only: the caller's user ID, resource the target's
  user ID on success, details the outcome and `via` (`kypost` or `kycalendar`). Never a subject,
  a name, an address or the secret.
- Callers (contract H): show "Chat" only when KyMessages is in the user's suite app list and
  `suitedir.Resolve` places the address in the organisation; `no_target` reads "This person
  isn't on KyMessages yet."; drop an answer whose URL is not `https://` on the paired
  KyMessages origin's Element host.

## How it works

- `internal/api/suite_pair.go`: both pairs (`suitePair`: environment, else KyIdentity lookup
  loop) and the console status routes.
- `internal/api/suite_chat.go`: the route.
- `internal/config`: `pairFromEnv` reads `KY_KYCALENDAR_*` and `KY_KYPOST_*`.
- `web/src/components/SuitePairPanel.tsx`: the console's KyCalendar and KyPost rows.

## Verification

- `internal/api/suite_chat_test.go`: every refusal in the table.
- `.github/workflows/suite-meetings.yml`, step `suite-chat`, with a real KyCalendar and
  KyIdentity: KyCalendar's Chat action on an attendee opens Element's user view of that person;
  people without a chat account read KyCalendar's "not on KyMessages" line; signed under the
  secrets KyIdentity issues, self and administrator targets and an administrator caller are
  refused, and `kypost.chat.open` works under the KyPost pair. KyPost's own Chat action is not
  exercised there (no KyPost runs; the harness signs as it). Evidence:
  `docs/evidence/2026-11-suite-wave2.md`.
