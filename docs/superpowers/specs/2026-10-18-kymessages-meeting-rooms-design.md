# Meeting rooms for KyCalendar, with a guest waiting room

Date: 2026-10-18. Status: draft, awaiting review; not scheduled for the early-November 2026
release. Caller: KyCalendar, `docs/superpowers/specs/2026-10-17-kycalendar-v1b-invitations-design.md`
in Busnes-app/KyCalendar ("Meeting rooms", "Backend credentials").

## Intent

Every KyCalendar meeting with attendees gets a KyMessages room with a video call by default.
Attendees on this instance join from Element as members. Attendees from outside get a personal
link that works only during the meeting:

- they wait in a waiting room until someone in the room lets them in;
- if they leave, they wait again.

Nothing else about guests changes: outside meeting rooms, KyMessages still has no guests.

## Decisions (owner-approved 2026-10-18)

1. KyCalendar meetings get a KyMessages video room by default.
2. External attendees may join the room for the time of the meeting, through a waiting room;
   someone in the room must let them in, and leaving means being let in again. This overrides
   the 2026-10-03 decision "No public or guest callers" for meeting rooms only.
3. KyCalendar authenticates with a backend server credential, not per-user tokens.
4. Element stays stock (owner decision 2026-10-02). Stock Element configuration (`config.json`)
   may change; Element code may not.
5. A guest's window opens 10 minutes before each occurrence starts and closes at its end. At the
   end, guests are removed from the call even if members stay on.
6. Any internal member of the room (the organizer or an internal attendee) may let a guest in.

## Design

### API (KyCalendar only)

The pairing is made like KyRecovery's: KyMessages' console generates a pairing code (or the
installer provisions it through `ky.product-setup/1`), and KyCalendar's admin claims it. Every
request is signed with `ky-primitives/syncauth` (scheme v1, the same verifier as
`/api/sso/kyidentity/sync`). Event type `kycalendar.meeting.put` or `.close`; the event ID is
stable per change, so the replay guard absorbs retries.

- **`PUT /api/meetings/{id}`**, idempotent. `{id}` is KyCalendar's opaque meeting ID.
  - **Body:** `{organizer_sub, title, member_subs[], guests[{key}], windows[{start, end}]}`.
    - `guests[].key` is an opaque per-attendee key. KyMessages never receives guest names or
      addresses.
    - `windows` covers at most 90 days ahead. KyCalendar sends each occurrence as
      `[start - 10 min, end]` (decision 5); KyMessages enforces exactly what it is given.
  - **Creates or reconciles the room:**
    - members are invited and removed members are kicked;
    - the title becomes the room name;
    - windows are replaced;
    - removed guest keys revoke their links.
  - **Answers** `{member_url, guest_urls: {key: url}}`. A guest URL is returned only when its key
    is new or was re-issued.
  - **Refused (422):** an `organizer_sub` or `member_sub` that is not an active local MAS user
    linked to KyIdentity, a title over 255 bytes or with control characters, or more than 100
    members, 100 guests or 400 windows.
- **`DELETE /api/meetings/{id}`** closes the room the way the console's Close does today:
  blocked, members out, history kept. It revokes every guest link and ends every guest session.

### The room

- **Created by `kymessages-console`** through `AsConsole`, which today only reads rooms and tears
  them down. This adds room creation, invite, kick and state events.
- **Room state:**
  - join rule `knock`;
  - history visibility `joined`;
  - encryption on;
  - power levels: the console account 100, the organizer 50, members 0, `invite` 0 (so any member
    can admit a knock), `kick` 50.
- **Member link:** a matrix.to permalink to the room, served through Element.

### Guests

1. **The link** is `https://<admin-host>/meet/<token>`. The token is 256 random bits, stored only
   as a hash, and bound to one meeting and one guest key.
2. **Outside every window**, the page shows when it opens and creates nothing.
3. **Inside a window**, the guest enters a display name (1–64 characters, no control
   characters) and solves the existing proof-of-work challenge.
   - Limits: 10 attempts per IP per 15 minutes, and one live guest session per link at a time,
     so a forwarded link cannot seat two people at once.
4. **KyMessages creates the guest account:** an unlinked MAS user `guest-<random>`, the same
   mechanism as agent accounts (`CreateAgentUser`).
   - The display name is `<name> (guest)`, and the suffix is re-applied by the reconciler.
   - Its personal session expires at the end of the window.
   - The account then knocks on the room.
5. **Members see "asks to join" in Element**, and any internal member may approve it
   (decision 6). Approval is
   a Matrix invite, which the guest's client accepts.
6. **Call grant rule for a guest.** `handleRTCToken` issues one only when all three hold:
   - the account belongs to this meeting;
   - its membership state is exactly `join`. Today's member list does not tell join from invite
     or knock, so this check is new, and it applies to everyone;
   - now is inside a window.
7. **Leaving means knocking again.** `callMaintenanceLoop` kicks a guest from the Matrix room once
   they have been out of the LiveKit room longer than `departure_timeout` (20 s). Under `knock`, a
   kicked user must knock and be let in again.
8. **At the end of a window**, the loop removes guests from LiveKit, kicks them, ends their
   sessions and deactivates the accounts (`skip_erase`, as for directory deletes).

### Confinement

A guest account is a real local Matrix user for the length of a window. While it exists, it must
not be able to:

- create rooms;
- invite anyone;
- start a DM;
- search the user directory beyond the room;
- upload more than the room's call needs.

Synapse's user directory already returns only users who share a room unless `search_all_users`
is set. The rest needs Synapse's module callbacks (`user_may_create_room`, `user_may_invite`,
`user_may_send_3pid_invite`) keyed on the `guest-` localpart prefix, or the equivalent. This is a
ship gate (spike S3), not a follow-up.

### Spikes, run first

Each ends in a recorded pass or fail under `docs/evidence/`.

- **S1, signing in.** How a guest's browser signs into stock Element Web v1.12.30 with a session
  KyMessages minted through MAS, without a password or an upstream provider. Candidates: a MAS
  compatibility login token through Element's `m.login.token` path, or a KyMessages-run OIDC
  upstream that MAS trusts only for `guest-` users.
- **S2, approving.** Whether the pinned Element shows and approves knocks ("Ask to join") during
  an Element Call, and which `config.json` feature flag it needs.
- **S3, confinement.** The Synapse module callbacks above, on the pinned Synapse v1.162.0.
- **If S1 or S2 fails:** the fallback is a KyMessages guest page that joins LiveKit directly with
  a KyMessages grant, with admission through a console-bot notice in the room that a member
  approves. Its media cannot use Element Call's Matrix-keyed encryption, so choosing it is an
  owner decision, not a quiet substitution.

### Audit and logs

- **Audited:** `meeting.put`, `meeting.close`, `guest.knock`, `guest.admit` (with the admitting
  member), `guest.leave` and `guest.expire`.
- **IDs only:** meeting ID, guest key hash and account ID. Never display names, titles or tokens.

## Evidence

From origin/master `aae4dc5`:

- **Guests are off everywhere today:**
  - Synapse `allow_guest_access: false` (`internal/matrixinit/templates/homeserver.yaml.tmpl:33`);
  - Element `disable_guests: true` (`element.json.tmpl:10`);
  - `docs/PRODUCT.md` "No public sign-up, no guests".
  Matrix guest access stays off: these guests are ordinary, short-lived local accounts.
- **MAS admin** can already create unlinked users and mint and revoke personal sessions
  (`internal/matrixsync/agents.go:39-47, 76, 107, 125`; `mas.go:469-500`).
- **No code creates rooms, invites or handles knocks.** The Synapse admin client only reads and
  tears down (`internal/synapseadmin/synapseadmin.go:134-207`).
- **Call grants are 15 seconds.** `handleRTCToken` checks room membership through the admin
  member list without the membership state (`internal/api/rtc_handlers.go:132-152`). The
  reconciler runs every 5 s and removes people who are not members (`:240-301`;
  `cmd/server/maintenance.go:54-74`).
- **The only server-to-server inbound routes** are syncauth webhooks and SCIM
  (`internal/sso/kyidentity.go:164-167`, `internal/scim/handler.go:54-70`).

## Testing

- **Unit tests:**
  - window arithmetic;
  - link token hashing and binding;
  - the grant rule: knock, invite and leave each refused, `join` accepted, and refused outside the
    window;
  - kick-after-departure;
  - expiry deactivation;
  - API refusals and syncauth replay.
- **`make matrix-acceptance` gains a guest run.** A guest knocks, a member approves, both decode
  audio and video. Then:
  - the guest leaves and is refused a grant until approved again;
  - the window ends and the guest is removed and deactivated;
  - the guest is refused room creation and invites (S3).
- **The existing call acceptance must still pass unchanged** for members.

## Not in scope

- Guests outside meeting rooms.
- Recording.
- Dial-in.
- Federated guests (federated users keep the 2026-10-11 rule).
- Changing Element's code.
