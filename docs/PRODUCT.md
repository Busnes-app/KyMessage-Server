# KyMessages product definition

KyMessages is a Ky-integrated Matrix deployment: stock Synapse, Matrix Authentication Service
(MAS), PostgreSQL and Element Web, run and backed up by the KyMessages control plane. Design:
[Matrix platform design](superpowers/specs/2026-10-01-matrix-platform-design.md). Setup and
operation: [README](../README.md). Restore: [RESTORE.md](RESTORE.md).

Sections below separate what ships (in code and proven by `make matrix-acceptance`) from what
is later work. KyMessages has been installed on real hardware and passes its test suite; that is not a security
audit, and the limits recorded below and in the README still apply. Each release
publishes an attested image, `ghcr.io/busnes-app/kymessage-server` (README, "Container image").

## Promise and audience

**Private team conversations, on infrastructure you control.**

- Small business teams first: one organization per deployment, self-hosted.
- Encrypted chat, with voice and video calls enabled by default. Widgets and integrations are off.
- Invite-only: only users assigned to the app in KyIdentity get in. No public sign-up, no
  guests (approved, not shipped: KyCalendar meeting rooms admit external attendees through a
  waiting room for the time of the meeting; see "Later or out of scope").
- Ky-integrated: KyIdentity sign-in and offboarding, KyRecovery backups, Ky console.
- Not offered: Slack or Teams parity, compliance archiving, open federation.

## What ships

**Stack.**
- `kymessages matrix-init` generates Synapse, MAS, Element, LiveKit and Postgres configs and write-once
  secrets; `docker-compose.matrix.yml` adds the upstream images, pinned by tag and digest,
  unmodified (AGPL rule below).
- One subdomain per part (Synapse, MAS, Element, LiveKit signalling, console); user IDs stay `@alice:<server name>`
  through `.well-known` served by KyMessages. Signalling stays private behind the proxy; LiveKit publishes direct media ports.
- Federation closed by default (empty allow-list, no federation resource). Registration, guest
  access and password login off.

**Federation** (owner decisions 2026-10-11, plan `superpowers/plans/2026-10-11-federation-opt-in.md`).
- Three console levels: Closed (default), Manual approval (servers the operator adds by name),
  Curated list + approval (plus vetted public servers ticked from the list compiled into each
  release; an empty list says so in Settings, and servers are then added by hand). Allow-list only: both servers must list each other. Open federation is not
  offered: its only gains (working large public rooms, no admin step for unknown servers) come
  with remote media (including illegal content) cached on the operator's disk, database growth
  past the backup limit, spam from any server and Synapse's federation API open to everyone.
- Synapse applies a change after a restart; the console shows the command and a per-server
  connection status. Widening needs a recent sign-in; narrowing never does.
- Users of allowed servers may join calls a local user started; they never start calls.

**Calls.**
- Stock Element Call provides voice/video in DMs and group rooms; stock LiveKit routes media.
  Calling is enabled by default; members choose when to start or join and grant media access.
- KyMessages authorizes the pinned client's legacy and Matrix 2.0 token requests: local Matrix
  OpenID identity, active KyIdentity/MAS account, joined room. Grants cannot administer LiveKit.
- Direct media needs TCP 7881/UDP 7882 and an advertised reachable IPv4; HTTPS/WebSocket
  signalling can use cloudflared. Optional embedded TURN/TLS uses external TCP 443 and
  UDP 3478 and 30000–30100, a DNS-only hostname and a trusted certificate. No call recording, guests
  or third-party call service.
- Every 5 seconds the control plane removes offboarded participants and removed room members;
  each pass uses a 5-second context; detached admin-session minting and cleanup can extend it. A cached LiveKit token may reconnect between passes, and
  LiveKit refreshes connected users' tokens; an unavailable control plane cannot evict calls.
  Ending a MAS browser session alone does not end an already connected call.
- Config, copied TURN TLS material and LiveKit keys are included in server backups; active calls are not restored.
- The call gate requires real audio received and video decoded by both Element clients,
  both directly and with relay-only ICE, before and after full restore.
  The existing text-encryption acceptance remains separate; do not extend its evidence to calls.

**Sign-in.**
- KyIdentity is the only upstream identity, through MAS. MAS's compatibility (legacy) login is
  not served; only native OIDC clients (current Element Web, Element X) sign in.
- Matrix localpart = the local part of the KyIdentity email, lowercased, characters outside
  `[a-z0-9._=-]` replaced by `_` (`Alice.Q+Ky@example.com` is `@alice.q_ky`), only once KyIdentity
  has verified the email (`email_verified`: a mailed activation or reset link was redeemed; any
  email change clears it). A user without a verified email cannot sign in, so chat needs
  KyIdentity mail delivery (a manually handed-over link does not verify), and a second user whose email has the same local part on another domain
  is refused. Accounts link by KyIdentity `sub` and keep their localpart, so later renames or
  email changes do not orphan them.
- The console also supports installer-owned dedicated KyIdentity administrators through
  container-local `apply-setup` ([contract](INSTALLER_SETUP.md)). These subjects are denied
  ordinary Matrix access by the directory projection; central MAS assignment exclusion remains
  an installer composition gate. The existing Matrix acceptance has not been rerun for this
  setup path.
- The KyMessages console keeps a local operator login (bootstrap password must be replaced
  before privileged use), with optional TOTP two-factor sign-in and one-time recovery codes
  under Settings > Account security; members reach chat only through KyIdentity.

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

**Agents** (owner decisions 2026-10-14, plan `superpowers/plans/2026-10-14-agent-accounts.md`).
- Admins create chat agent accounts (`@agent-<name>`) for OpenClaw or Hermes Agent, each owned
  by a member. Off by default: none exist until an admin creates one.
- Sign-in is a MAS personal access token scoped to the client API on one device, shown once
  with a config block per runtime and the `kymessages` skill. No password login is added.
- An agent follows its owner: offboarding the owner locks it in about 2 minutes (Synapse's token
  cache); re-enabling unlocks. Deleting an agent deactivates it; its messages stay. A token
  unused for 90 days is revoked; one in use never expires. Revoking deletes the agent's device,
  so its next token needs encryption set up again.
- An agent can rename itself; its Matrix ID always starts with `@agent-`.
- An agent decrypts, like a bridge: rooms keep Element's label, but the agent's machine and
  model see plaintext, and a hosted model takes room content off your infrastructure.
- Agents are text participants; they never get call grants.

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
- Chat device sign-in and encryption-key verification happen in Element. The console has no
  device-pairing control; its inherited session-pairing API does not connect Matrix clients.
- Users: Matrix users with their KyIdentity link and sessions; end one or all sessions. No
  lock or unlock: access is controlled in KyIdentity.
- Rooms: list, search, members (never messages). Close (final: members removed, rejoin
  blocked, history kept) or Delete permanently (typed name; media stays in the media store).
  Runs as the service account `@kymessages-console` through 5-minute MAS sessions.
- Health: per-load probes of Synapse, MAS, Element, LiveKit, Postgres and the app (LiveKit runtime version unknown), versions compared
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
- Open federation (no allow-list), an inbound "request to federate" queue, room-level server
  ACL editing, moderation bots, LAN-only peers, automatic Synapse restart.
- Widgets and integrations: disabled in Element's config.
- Console: room creation, membership editing, message moderation, colour editing, health
  history and alerting.
- KyCalendar meeting rooms (approved 2026-10-18, not scheduled): a room with a call per meeting,
  and external guests during the meeting only, admitted from a waiting room by someone in the
  room. Design: `docs/superpowers/specs/2026-10-18-kymessages-meeting-rooms-design.md`.

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
- Federation: allowed servers hold copies the operator cannot recall; a public server's rooms
  spread posts beyond the allow-list; the app can write Synapse's config through the federation
  file, so a compromised app can run code in Synapse. Encryption across servers and a real
  cross-server call are not covered by acceptance.
