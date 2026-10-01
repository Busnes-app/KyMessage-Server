# KyMessages product definition

The chat platform is Matrix; see docs/superpowers/specs/2026-10-01-matrix-platform-design.md. Product-level sections below are reworded in Matrix sub-project 2.

Status: proposed product contract, grounded in the existing server base. Only the
small-team, encrypted-chat-first priority is user-confirmed; the defaults below are
recommendations, not implemented features. Protocol evidence is in
[KYMESSAGES-PROTOCOL-RESEARCH.md](KYMESSAGES-PROTOCOL-RESEARCH.md).

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
90-day default ciphertext retention; SQLite for the first supported deployment. Validate
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
