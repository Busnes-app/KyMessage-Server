# KyMessages product definition

Status: proposed product contract, grounded in the existing server base. Only the
small-team, encrypted-chat-first priority is user-confirmed; the defaults below are
recommendations, not implemented features. Protocol evidence is in
[KYMESSAGES-PROTOCOL-RESEARCH.md](KYMESSAGES-PROTOCOL-RESEARCH.md).

Implementation evidence: [the isolated browser proof](../mls-proof/README.md) tests
real MLS with local encrypted persistence and authenticated HTTP delivery, including
Chromium-to-Firefox exchange. It remains outside the deployed product; its remaining
gates keep milestone 0 open. The dependency comparison is in
[MLS-LIBRARY-RESEARCH.md](MLS-LIBRARY-RESEARCH.md).

The [device, room and delivery backend](MESSAGING-API.md) implements suite-authenticated
enrollment, approvals, revocations, consent-based room ACLs and an experimental
opaque HTTP log with reconnect cursors and declared epoch coordination. The proof
binds enrolled keys to MLS credentials, claims public KeyPackages through the API,
and validates transcript transitions after explicit fingerprint approval. The pool
has bounds and one-time allocation. An isolated clickable prototype now covers
test-account setup, device approval, invitations, independent fingerprint verification,
encrypted conversation history and durable send retries. Its OIDC mode exercises the
existing suite callback, cookie/CSRF transport and authenticated account-bound unlock
using a disposable local issuer. Pre-join package renewal preserves verified device
identity and delayed Welcomes. Explicit reinvitation/rejoin preserves earlier local
history and verifies a new membership generation before resuming chat. The chat tab
also polls verified delivery while visible and online, with failure backoff and
cancellation on lock. Owner-only removal controls expose invitation revocation,
immediate server access removal and the required encryption update before sending.
Account device revocation preserves local history without silently re-enrolling
revoked keys; losing every approved device requires an identity-reset flow that is
not implemented. Automatic replenishment, retention-gap recovery, live KyIdentity deployment and reviewed
product client integration remain open. This is not production E2EE.

## Promise and audience

**Private team conversations, on infrastructure you control.**

KyMessages is the Busnes.app real-time companion to KyPost. Its first job is to let
a small team exchange reliable encrypted messages without operating a distributed
communications platform. The initial design target is one organization, 5–50 people,
one application instance, and a responsive web client. These are planning targets,
not measured capacity or enforced account limits.

Small businesses are the first audience; homelab alerts and family calling remain
expansion paths. The first release competes on understandable privacy, reliable
delivery, simple operation, and suite identity. It does not promise Slack feature
parity, Zoom replacement, compliance archiving, or cross-vendor interoperability.
Avoid a licensing/pricing promise until the distribution terms have been reviewed.

## First-release experience

1. An operator deploys the app behind HTTPS, connects KyIdentity, and configures a
   local sealed-backup destination or KyRecovery. Setup explains what recovery saves.
2. A member signs in, enrolls their browser as a messaging device, and sees the
   device fingerprint and the consequences of clearing browser storage.
3. They create a DM or invitation-only room, verify unfamiliar device identities,
   and exchange messages. The UI distinguishes pending, accepted by server, and
   failed sends; server acceptance does not mean a recipient read the message.
4. After a connection interruption, the client resumes from its durable cursor
   without duplicate visible messages. A retention gap is shown explicitly.
5. A new browser is a new device. Approval by an existing device admits it to future
   conversations. A user who loses every device re-enrolls visibly and starts with
   new keys; signing in does not magically recover history.
6. Removing a member immediately revokes server access. The room also completes a
   cryptographic membership change before accepting new application messages.

The workspace shell has rooms and DMs, a conversation pane, a composer, and room
members. Account settings expose devices, fingerprint verification, and local data
removal. Admin settings expose identity, retention, storage usage, and backup health.
Use existing ky-ui tokens and Busnes Light/Dark defaults, preserve saved themes,
and make keyboard operation, focus behavior, narrow screens, and contrast release gates.

## Scope

| Ship in v1 | Follow after v1 | Separate feasibility work |
| --- | --- | --- |
| KyIdentity OIDC, DMs, invitation-only rooms | Workspace-discoverable channels and SCIM room mappings | Nextcloud Talk interoperability |
| MLS-encrypted text, basic Markdown with raw HTML disabled | Threads, reactions, edits, encrypted attachments | Cross-server MLS trust and delivery |
| Device enrollment, approval, verification and revocation | Approved device-to-device history transfer | Native shared crypto library and packaging |
| Offline reconnect, deduplication, retention gaps | 1:1 audio/video and screen share | SFrame SFU calls |
| SQLite, one instance, existing recovery facilities | Alert integrations and private wake-up push | PostgreSQL recovery and multi-node operation |

Text v1 includes safe links and code blocks; fetching remote link previews on the
server is excluded. Search, if added, runs over locally decrypted messages. Typing
indicators, presence, read receipts, recordings, transcription, and moderation bots
are deferred. Each has privacy and lifecycle costs beyond its UI.

## Privacy contract

Say **end-to-end encrypted content**, rather than describing the whole service as
zero-knowledge. The server sees accounts, device public keys, room membership, routing
identifiers, timing, ciphertext sizes, expiry, and connection IP addresses. Proposed
v1 room names are server-visible metadata; the UI must say so. Audit administrative
events without bodies, attachment keys, plaintext notifications, or message previews.

Only enrolled room devices possess message decryption keys. OIDC proves account
authentication; it does not prove a messaging device is trustworthy. An administrator
can manage access but cannot silently add an invisible decryption device. Device
changes are visible and fingerprints can be compared out of band. The first release
must document what directory substitution it detects; key transparency is not assumed.

Encryption does not protect against compromised endpoints, malicious recipients,
screenshots, or a compromised web origin shipping a modified client. CSP, dependency
controls, reproducible release practices, and avoiding third-party runtime scripts
reduce browser risk but cannot remove that trust boundary.

### Keys, devices, and history

- Treat each device as an MLS client/leaf with its own credential and private state.
  Evaluate maintained MLS libraries in a browser proof before selecting one. No
  in-house MLS implementation or custom key-exchange substitute.
- Separate session tokens, device signing keys, MLS epoch secrets, local-storage
  encryption keys, server encryption keys, and suite recovery keys.
- Persist MLS state, consumed events, and the resume cursor crash-consistently.
  Prevent two browser tabs from independently mutating the same MLS state. Select
  the simplest supported single crypto-owner mechanism during the browser proof.
- Use browser-native persistent storage for the PWA; do not assume SQLite exists in
  every browser. Native clients may use encrypted SQLite later. Decide local key
  wrapping/unlock behavior in the proof: storing a wrapping key beside ciphertext
  is not protection against an attacker with the whole browser profile.
- Newly joined devices receive future messages. Earlier history transfer is an
  explicit later feature, never an accidental consequence of enrollment.
- Lost-device revocation blocks fetch and send immediately. Removal must advance
  the MLS group before new messages resume. Old ciphertext already copied to that
  device cannot be recalled. If no authorized client can commit, pause the room and
  explain that an authorized member must return or create a new room.
- Directory disablement and room membership are distinct from MLS state. SCIM may
  change eligibility; an authorized client must enact the corresponding group
  change. Never equate a database membership update with cryptographic removal.

### Retention and recovery

#### Losing every approved device

Proposed v1 contract; reset is not implemented. The prototype now provides recovery
help before unlock and reports when only one or no approved device remains. Approval
status does not prove the device is accessible: an offline or lost browser can still
be listed as approved. Never infer key loss from absence or inactivity alone.

With a usable approved device, use ordinary fingerprint-checked approval and revoke
lost devices. Without one, the current prototype permits revocation through a live
account session, but a replacement stays pending. Preserve surviving browser data;
an account-password reset or server restore cannot recover the local passphrase,
MLS secrets or message history. Local history already readable remains readable.

The future **Start a new messaging identity** flow must:

1. Require fresh interactive suite authentication, proof of the replacement key,
   and explicit confirmation of lost history and changed identity. Bind a single-use
   request to the account, target key and current identity generation. Ordinary
   session possession alone cannot complete reset. Validate the deployed issuer's
   reauthentication behavior before enabling this path.
2. Atomically advance an account's messaging identity generation, revoke all prior
   devices and pending enrollments, retire their unclaimed KeyPackages, and audit
   the old/new generation and replacement fingerprint. Keep historical tombstones;
   never delete devices to reuse first-enrollment approval. Retry the same request
   idempotently; stale generations fail without replacing a newer identity.
3. Make reset visible to remaining room clients. Old device access ends immediately;
   affected rooms pause until a surviving authorized client commits their removal.
   A new identity receives no inherited room eligibility or local verification pins.
   An operator may manage access but cannot vouch for the new decryption key.
4. Require fresh invitations and independent fingerprint comparison before adding
   the replacement to future room traffic. Bind the new identity generation to the
   authenticated MLS application profile so an old approval cannot cross the reset.
   Rejoining creates a new history floor; it does not transfer earlier messages.
5. Keep a room paused when no authorized client retains its MLS state. If the lost
   identity owns the room, require a new room for this first version; ownership
   transfer and recovering abandoned groups are separate features. The new room
   needs new invitations and verification, with the history break clearly shown.

Reset exit evidence: a stolen ordinary session cannot reset; concurrent/replayed
requests cannot reset twice; old and pending devices cannot send, fetch or approve;
stale packages and identity pins cannot admit the replacement; returning peers see
the identity change before sending; replacement history excludes the prior identity;
and loss of the final room state has an explicit new-room outcome. Include lost
acknowledgements and restored older server metadata in the recovery drill. Until
these gates pass, expose guidance rather than a reset button or administrative bypass.

#### Retention and server backups

Proposed default: 30-day server ciphertext retention, with 24-hour and 7-day room
policies. Describe this as retention, not guaranteed auto-burn. Recipients may copy
content; browser cleanup is best effort. Expired events disappear from active fetch
and local views, and the client reports unavailable history when it falls behind.

Reuse `ky-primitives/recoveryclient` from the first deployable release. Preserve
manual key pinning, scheduled backups, local sealed copies, receipt checking,
write-once trust, unpairing, and drills. The suite ceremony supplies k-of-n; 3-of-5
is not hardcoded. Keep the existing minimum scheduling interval and admin control.

Server recovery restores configuration, database metadata, audit records, and any
retained ciphertext included in its snapshot. It restores neither browser MLS
secrets nor the ability to decrypt history after all client keys are lost. The
server's encryption key in a capsule is an operational key, not a message key.

The existing adapter snapshots the whole SQLite database, so future ciphertext
tables will be included unless collection changes. Backup copies may outlive room
retention; disclose their independent retention and custodian access to metadata.
Restore must prune expired content before serving traffic. Never roll back live MLS
client state to a server backup: detect missing/forked history, reconcile against
surviving clients, or require an explicit room reset. Drill this behavior.

## Architecture on this base

Start with one Go service, existing SQLite storage and embedded React UI. Use HTTPS
for bounded durable commands and cursor-based history; use one WebSocket stream for
live events and later call signaling. Add SSE only for a demonstrated deployment
need. Reconnect always reads durable history, so in-memory notifications are not a
second database. No broker, Redis, worker fleet, or media server in text v1.

| Existing location | Reuse | Product work |
| --- | --- | --- |
| `internal/sso`, `internal/auth`, `internal/scim` | OIDC PKCE, sessions, directory primitives | KyIdentity configuration and explicit device identity binding |
| `internal/devices` | Existing pairing flow as a UX starting point | Prove cryptographic device enrollment; pairing alone is not MLS trust |
| `internal/store` | SQLite/PostgreSQL abstractions and migrations | Room membership, device keys, opaque events, delivery cursors |
| `internal/api` | HTTP auth, limits and administrative endpoints | Authorized command, history and stream boundaries |
| `internal/backup`, `cmd/server` | Sealing adapter, scheduler, shutdown coordination | Product identity, bounded payloads, restore reconciliation |
| `web` | React shell, settings, ky-ui and browser checks | Conversation UI and isolated MLS state owner |

Keep transport validation and storage details at the boundary. Implement membership
and event rules as pure functions where practical. Add a messaging package when
there is actual domain code; avoid interfaces or empty modules for future phases.

### Delivery requirements

- Every command authenticates a session/device and checks current room access.
  Enforce origin checks on cookie-authenticated WebSockets, bounded frames and
  queues, connection quotas, and session-revocation disconnects.
- Store opaque MLS application messages and required handshake/control events.
  Carry routing and protocol-version metadata separately; validate encrypted
  content and application event schemas in receiving clients.
- Assign a monotonic per-room delivery sequence and deduplicate client event IDs
  scoped to the sending device. Commit before acknowledging. Do not claim
  exactly-once network delivery: retries are normal, visible duplication is not.
- Define the MLS epoch/commit concurrency profile in the proof. Stale commits must
  resync and retry, not overwrite a winning epoch. Server sequence numbers alone
  do not authenticate MLS history; clients verify the cryptographic transcript.
- Keep control material long enough for the supported offline window. When a
  device can no longer catch up, require explicit rejoin rather than inventing
  missing state. Bound upload size, retained bytes, devices, and key-package pools.
- Generate wake-up-only push later. Keep message content and decryption keys out
  of push payloads, URLs, logs, crash reports, and observability labels.

## Follow-on features and their constraints

**Calls.** First prove authenticated 1:1 WebRTC calls, including relayed calls through
TURN. Bind signaling and endpoint fingerprints to the encrypted conversation.
DTLS-SRTP protects its endpoints; if those endpoints become an SFU, it is not alone
participant-to-participant E2EE. Group calls need SFrame, authenticated key distribution,
membership rekeying, and browser capability tests. Unsupported clients must fail
closed rather than silently downgrade encryption. Screen/system-audio capture is
platform-dependent. Pion supplies building blocks, not a finished congestion-managed
SFU. Account for TURN deployment, UDP reachability and bandwidth; the full calling
deployment may need more than the application container.

**Alerts.** A webhook recipient sees plaintext. Prefer a separately operated bridge
that receives alerts and joins a dedicated room as a visible MLS bot/device; its
operator is trusted with that room's contents. The messaging daemon remains blind.
Generic webhook support therefore adds a component and cannot be marketed as both
server-blind and a plaintext in-process sink. Start with links into KyPulse/KyYard;
action buttons later require destination-side authorization, confirmation and audit,
not ambient administrator credentials in chat messages.

**Federation.** First prove KyMessages-to-KyMessages identity, invitation, removal,
delivery and abuse handling. OCM discovery/invitations are not an MLS chat wire
protocol. Nextcloud Talk interoperability is a separate compatibility milestone,
requiring tests against named versions and an explicit encryption profile. Do not
promise transparent encrypted interop or silently introduce a decrypting bridge.
HTTP Message Signatures/JWKS support must follow negotiated peer capabilities.

**Native clients.** Build after the message/device protocol stabilizes. Prove the
chosen MLS implementation can share interoperable state formats and behavior across
WASM and native targets before committing to four client platforms.

## Delivery sequence and acceptance gates

| Milestone | Concrete outcome | Exit evidence |
| --- | --- | --- |
| 0 — feasibility | Two browsers exchange real MLS messages with the selected library | License/maintenance/security review; test vectors; refresh/crash recovery; concurrent commits; offline rejoin; device add/remove; browser storage/key-wrapping decision |
| 1 — product foundation | Product identity, KyIdentity enrollment and a durable opaque delivery path | Existing CI remains green; access-control tests; revoked sessions disconnect; replay/retry/cursor tests; no plaintext in server persistence/logs |
| 2 — private team pilot | DMs, rooms, device UI, reconnect, retention and recovery | Multi-user browser tests; lost-device drill; unauthorized join fails; membership pause/rekey works; backup restore cannot resurrect expired messages |
| 3 — first release | Documented installation, upgrades, storage limits and support matrix | Restore drill; security review of protocol binding; cross-browser checks; measured load and resource envelope |
| 4 — expansion | Calls, alerts, attachments and push in independently usable increments | NAT/TURN tests; bot trust disclosure; client-side attachment encryption; metadata-only push |
| 5 — interoperability | Federation and native clients | Named peer/version compatibility tests and device lifecycle parity |

Proposed pilot load: 50 accounts, 100 connected devices, rooms up to 50 accounts,
10 messages/second sustained and 50/second bursts. On a declared 2-vCPU/2-GiB test
host, target p95 server acceptance below 250 ms and foreground receipt below 1 s
on a controlled low-latency network. Record ciphertext sizes, database size and
network conditions. These are pass/fail targets to validate, not advertised results.
Measure binary/image size; remove the unverified less-than-35-MB promise.

## Decisions to settle in the feasibility milestone

The recommended defaults are: one organization per deployment; KyIdentity-only
member login; no anonymous guests; visible device approval; no historical key escrow;
30-day ciphertext retention; SQLite for the first supported deployment. Validate
these with the pilot team, especially the loss-of-all-devices experience.

The base currently has local administrator login. Decide and test an operator-only
bootstrap/recovery path that cannot enroll a messaging device or bypass member SSO
before enforcing exclusive KyIdentity login. Do not remove the existing recovery
path before a replacement has been demonstrated.

The technical go/no-go is the MLS browser proof, including a maintainable library,
supported browser matrix, device trust binding, durable state, and a documented
epoch-concurrency profile. If that proof fails, revisit scope/library choice; do not
ship plaintext chat beneath an E2EE label.

PostgreSQL already exists in the store, but the backup collector currently rejects it.
Keep it outside the initial supported product configuration until consistent backup
and restore are proven. PostgreSQL alone also does not provide multi-node fanout,
coordinated scheduling, or media routing.
