# Changelog

## Unreleased

### Added
- Meeting guests, off by default (**Settings → Server → Meeting guests**; turning it on needs a
  recent sign-in). While on, KyCalendar may list outside attendees of a video meeting as opaque
  guest keys with the meeting's windows, and KyMessages answers a personal link per new key
  (`guest_urls`). From 10 minutes before each occurrence until it ends, the guest opens the link,
  enters a name and solves the usual browser check; KyMessages makes a temporary `guest-` chat
  account, signs it in to Element through a small page on Element's origin and knocks. Anyone in
  the meeting lets them in from Element's room header, also during a call; the guest then joins
  the call. A guest out of the call is removed and must knock again; at the window's end, on a
  dropped key, when the meeting closes or when guests are turned off, the account is removed and
  deactivated. Audited `guest.knock`, `guest.admit`, `guest.leave`, `guest.expire`,
  `guests.enable` and `guests.disable`, with IDs only. Rerun `matrix-init` and update the Compose
  files: Element now mounts `kymessages-guest.html` and turns on its "ask to join" feature.
  Store migration 25. Contract: `docs/KYCALENDAR.md` "Guests".
- Synapse loads a guest confinement module, `ky_guests.py`, which `matrix-init` writes beside
  `homeserver.yaml`; `docker-compose.matrix.yml` sets `PYTHONPATH=/config` on Synapse for it.
  Rerun `matrix-init` and update the Compose files together: a `homeserver.yaml` naming the
  module with an old Compose file stops Synapse at start. The module confines local accounts
  named `guest-…` (meeting guests) to the meeting room the console
  marks for them: no room creation, invites, DMs, aliases, publishing, uploads or directory
  results, and no dropping the " (guest)" name suffix. Everyone else is untouched. Local
  part `guest-` is reserved for these accounts.
- Chat from KyPost and KyCalendar. A paired KyPost (a message's sender or a contact) or
  KyCalendar (an attendee) can ask `POST /api/suite/chat` for a link to that person in Element,
  which offers "Message" and reuses an existing direct chat; KyMessages creates nothing. Both
  people need an everyday KyMessages account; administrators are never offered. KyPost pairs
  like KyCalendar: `KY_KYPOST_URL` and `KY_KYPOST_SECRET_FILE`, else KyIdentity's suite pair
  `kypost`, shown under **Settings → Server → KyPost**. See `docs/SUITE-CHAT.md`.
- Calls in rooms of three or more people, KyCalendar meeting rooms included: Element now
  shows its call buttons there. Element v1.12.30 shows them only with its widgets UI on, so
  `matrix-init` turns `UIFeature.widgets` on while calls are on (rerun it and restart Element).
  Integration managers stay unset and the sticker button defaults off. Adding a widget to a
  room still needs the room's moderator level; new meeting rooms need level 100, so only the
  console could. See `docs/evidence/2026-11-group-calls.md` for what the widgets UI exposes.
- Office networks can skip the per-address sign-in limit. Staff behind one NAT address share
  the 20-a-minute sign-in window (and the 10-a-minute password-change window) and can trip it
  together on a busy morning. An administrator lists the office's networks under
  **Settings → Server → Office networks**, or the operator sets
  `KY_RATE_LIMIT_EXEMPT_NETWORKS`; the two lists are combined. Requests from a listed network
  skip those two per-address windows only: per-account windows, proof of work and every other
  limit still apply. The list is empty by default. Ranges wider than /16 (IPv4) or /48
  (IPv6), loopback and invalid entries are refused (at startup for the variable, with 400 on
  save). The match uses the client address resolved through `KY_TRUSTED_PROXIES`, so a forged
  `X-Forwarded-For` cannot claim a listed network. Adding a network needs a sign-in from the
  last 10 minutes; changes are audited as `admin.rate_limit_networks`, and the screen counts
  the requests that skipped the limit. New admin routes GET and PUT
  `/api/settings/rate-limit-networks`. List only networks you control: never guest Wi-Fi, a
  VPN exit or the reverse proxy's address, since everyone behind a listed address loses the
  per-address limit.
- All listed networks together share 300 sign-ins and 300 password changes a minute past the
  per-address windows; beyond that their requests meet the ordinary per-address window again,
  so one host inside an office cannot make unbounded attempts or flood the limiter's memory.
- Sign-in now reserves its place in the per-account window (10 failures per account and
  client per 15 minutes) before the password is checked, and hands it back on success, so
  concurrent guesses at one account can no longer all be checked before any is counted.
- The Go toolchain moves from 1.27.3 to 1.27.4 (`go.mod` and the image builder), which
  fixes GO-2026-6859 in `net/http`.
- `ky-primitives` moves to the revision that adds its `netallow` package. That revision also
  carries the library's changes since the previous pin (recoveryclient, syncauth, oidcverify,
  scim, capsule and auditchain fixes); see its changelog.

### Removed
- Local two-factor sign-in (TOTP and recovery codes) for the console's local administrator.
  Local admins now sign in with a password only; use KyIdentity for a second factor. An
  account that had two-factor on signs in with its password, and nobody is locked out. The
  stored two-factor data stays in the database, unused. The `/api/auth/totp/*` and
  `/api/auth/mfa/*` routes, the login's `mfa_required` step and Settings > Account security
  are gone.

### Changed
- A SCIM account with the admin role signs in to the console through KyIdentity only when
  `KY_SCIM_ALLOW_ADMIN=true`, and SCIM turning an inactive admin back on needs it too. An
  empty `roles` from SCIM now removes the admin role.
- KyIdentity's role for KyMessages applies at each single sign-on, not only when its directory
  update arrives: a demoted admin is a member from their next sign-in, and their other
  sessions end. Only an explicit KyMessages `admin` app role grants administration: empty or
  missing `roles` means ordinary user, even for a global admin or an existing console admin.
  Before upgrading, assign the KyMessages app role to intended administrators in KyIdentity,
  deploy KyIdentity's app-specific token and webhook role source, then upgrade KyMessages.
  Installer-owned identities and SCIM-owned roles keep their existing ownership rules.
  See [the upgrade sequence](docs/RUNNING.md#kyidentity-administrator-upgrade).
- A refused single sign-on returns to the sign-in page with a sentence saying why and a
  "Try again" link, instead of a raw error page.

### Fixed
- `kymessages restore` turns scheduled backups off. Turn them back on in Backup once the
  restored server is the one in service.
- People whose KyIdentity sign-in adopts their SCIM account are no longer locked out of chat.
- Deactivating such a person in KyIdentity now locks them out of chat and calls too, and
  KyIdentity no longer reports a username conflict for them. They can also own agents.
- A KyIdentity directory update makes someone admin when `admin` is any of their KyMessages
  roles, not only the first.
- Meeting notes accept a second meeting while one runs (the limit counted the invite itself).
- A SCIM rename onto a username another account holds answers 409, not a 500 with database text.
