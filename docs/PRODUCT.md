# KyMessages product definition

KyMessages is a Ky-integrated Matrix deployment: stock Synapse, Matrix Authentication Service
(MAS), PostgreSQL and Element Web, run and backed up by the KyMessages control plane. Design:
[Matrix platform design](superpowers/specs/2026-10-01-matrix-platform-design.md). Setup and
operation: [README](../README.md). Restore: [RESTORE.md](RESTORE.md).

Sections below separate what ships (in code and proven by `make matrix-acceptance`) from what
is later work. KyMessages is not yet deployed or approved for private team use; no image is
published.

## Promise and audience

**Private team conversations, on infrastructure you control.**

- Small business teams first: one organization per deployment, self-hosted.
- Encrypted text chat first. Calls, widgets and integrations are off.
- Invite-only: only users assigned to the app in KyIdentity get in. No public sign-up, no
  guests.
- Ky-integrated: KyIdentity sign-in and offboarding, KyRecovery backups, Ky console.
- Not offered: Slack or Teams parity, calling, compliance archiving, federation.

## What ships

**Stack.**
- `kymessages matrix-init` generates Synapse, MAS, Element and Postgres configs and write-once
  secrets; `docker-compose.matrix.yml` adds the four upstream images, pinned by tag and digest,
  unmodified (AGPL rule below).
- One subdomain per part (Synapse, MAS, Element, console); user IDs stay `@alice:<server name>`
  through `.well-known` served by KyMessages. Nothing is published; cloudflared fronts it.
- Federation off (empty allow-list, no federation listener). Registration, guest access and
  password login off.

**Sign-in.**
- KyIdentity is the only upstream identity, through MAS. MAS's compatibility (legacy) login is
  not served; only native OIDC clients (current Element Web, Element X) sign in.
- Matrix localpart = KyIdentity username lowercased, characters outside `[a-z0-9._=-]` replaced
  by `_`. Accounts link by KyIdentity `sub`, so renames do not orphan them.
- The KyMessages console keeps a local operator login (bootstrap password must be replaced
  before privileged use); members reach chat only through KyIdentity.

**Encryption.**
- Synapse encrypts every new room by default (`encryption_enabled_by_default_for_room_type: all`).
- Label, exactly: "End-to-end encrypted in Element (not independently audited)". Never call
  suite or agent review an independent audit.
- Synapse does not enforce encryption: a client or script that does not encrypt can post
  plaintext into an encrypted room, and the server does not stop it. Members should use
  Element. Evidence: [CHAT-PLATFORM-OPTIONS.md](CHAT-PLATFORM-OPTIONS.md) section 7.
- The server sees metadata in plaintext: room names and topics, membership, display names,
  timestamps, devices and IPs.
- Users restore history on a new device from key backup with their own security key. The
  server and KyRecovery custodians cannot read message content.

**Offboarding** (driven from KyIdentity; detail in
[the offboarding design](superpowers/specs/2026-10-01-matrix-offboarding-design.md)).
- Disable or unassign: back-channel logout ends open Element sessions (bound 30 s, measured
  0.3-3 s), then KyMessages locks the MAS user. Re-enable unlocks; history stays.
- Delete: MAS deactivates without erasing; the user leaves their rooms, their messages stay
  readable. Never reactivated.
- A signed directory webhook triggers the change; a sweep every 5 minutes repairs failed MAS
  calls. A change sent while KyMessages was down waits in KyIdentity until an operator resumes
  it. Every action is audited.

**Backups** (detail in [the backups design](superpowers/specs/2026-10-01-matrix-backups-design.md)).
- One sealed server capsule to KyRecovery and/or the local backup directory, on the admin's
  schedule: app database and key, Matrix configs and secrets (not the Postgres superuser
  password), Synapse signing key, `pg_dump`s of MAS and Synapse.
- Media: an encrypted incremental mirror in `KY_BACKUP_DIR/media` after each scheduled run or
  `deposit`, plus monthly full archives (default newest 3).
- Capsule limit 256 MiB expanded; the run fails loudly past it and the screen warns from 75%.
- `restore` then `restore-matrix` rebuild a lost host; proven end to end in acceptance.

**Console.** Changes need a sign-in from the last 10 minutes and are audited.
- Overview: chat health, users, backups, KyIdentity sync cards.
- Users: Matrix users with their KyIdentity link and sessions; end one or all sessions. No
  lock or unlock: access is controlled in KyIdentity.
- Rooms: list, search, members (never messages). Close (final: members removed, rejoin
  blocked, history kept) or Delete permanently (typed name; media stays in the media store).
  Runs as the service account `@kymessages-console` through 5-minute MAS sessions.
- Health: per-load probes of Synapse, MAS, Element, Postgres and the app, versions compared
  with the Compose pins, links to each upstream source release.
- Audit: read-only log filtered by kind.
- Settings: product name and PNG logo; the logo reaches Element at once, a name after
  `docker compose restart element`. KyIdentity sync status: last accepted webhook, rejected
  deliveries, last sweep, with fix hints.

**Clients.** Element Web is branded by config only (name, logo, Busnes Light/Dark). Element X
stays Element-branded. Any Matrix client can connect; the encryption label covers Element only.

## Licensing

- Synapse, MAS and Element Web are used as unmodified official images pulled by Compose,
  configured only through config files and HTTP APIs: no patches, no template overrides. The
  console links to each component's exact upstream source release.
- KyMessages' own code is MIT.
- Pricing and distribution terms are not decided.

## Later or out of scope

- Teams interop: no maintained bridge exists; a self-built Teams bot is a separate project.
- Other bridges (mautrix): later, admin-enabled. A bridge decrypts, so a bridged
  conversation never carries the E2EE label.
- A lighter homeserver (Tuwunel) trial.
- Federation: off.
- Calls, widgets and integrations: disabled in Element's config.
- Console: room creation, membership editing, message moderation, colour editing, health
  history and alerting.

## Known risks

- MFA relies on KyIdentity's per-app policy; MAS ignores `acr`. Untested.
- A public deployment through cloudflared is untested; acceptance runs the shipped configs
  over HTTPS with a private CA on loopback.
- A busy server will pass the 256 MiB capsule limit; backups then fail until the limit is
  raised. Sealing holds the capsule in memory.
- The app can read the Matrix databases and secrets, and the configs it backs up hold the
  database owner passwords: a compromised app can write both databases and act as Synapse
  admin. It cannot decrypt messages.
- A held or abandoned KyIdentity webhook leaves a sessionless, sign-in-refused user unlocked in
  MAS until an operator resumes it or resyncs.
- `@kymessages-console` holds Synapse admin scope; sessions last 5 minutes and every use is
  audited.
- Operational weight: PostgreSQL plus several services, against the old single binary.
