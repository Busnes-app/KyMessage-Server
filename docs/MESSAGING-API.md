# Messaging foundation API

Implemented scope: authenticated messaging devices, invitation-based room access
and an experimental opaque HTTP event log on SQLite and PostgreSQL. The log
coordinates declared epochs and device rosters and distributes bounded, one-time
KeyPackages. WebSockets and production MLS client integration remain unimplemented.
An interactive chat prototype exists only inside the isolated browser proof.
The isolated [`mls-proof/` HTTP adapter](../mls-proof/README.md#http-delivery-proof)
now exercises these endpoints with real MLS and enrolled-key binding. It is excluded
from deployment and is not production E2EE.
Its optional OIDC browser mode uses the real suite callback and cookie-authenticated
messaging requests, verified against a disposable local issuer.

## Authentication

Configure the existing suite OIDC integration with `KY_KYSIGNON_ISSUER`,
`KY_KYSIGNON_CLIENT_ID` and `KY_KYSIGNON_SECRET`; its callback is
`/api/sso/kysignon/callback`. The persisted provider name remains `kysignon`.
Messaging requires an active suite account with an OIDC subject and no local
password. Generic OIDC accounts and local bootstrap administrators are excluded.

Every route requires the existing session cookie or session Bearer credential.
Cookie writes also require the base's CSRF cookie/header pair. When supplied,
Origin must match `KY_APP_URL`. Authenticated messaging responses use `no-store`.
Account limits are 120 requests/minute, with enrollment additionally limited to
10 requests/5 minutes. These limits use the base's process-local limiter.

`GET /api/auth/me` supplies the authenticated account's immutable `user.id`, distinct
from its display name, username and OIDC subject. Both signed-in and anonymous
responses use `Cache-Control: no-store`. The isolated OIDC client binds its MLS
credential to this ID, rechecks it before network operations and refuses to unlock
another account's vault. Cookie transport reuses `web/src/api.ts`'s CSRF helper;
neither session cookies nor ID tokens enter JavaScript storage. This client behavior
supplements the server's account/device checks; it does not replace them.

Device-gated routes additionally require `X-KyMessages-Device: <device token>`.
This is a bearer credential belonging to an approved device of the same account;
it cannot replace the session. Requests are not individually signed with Ed25519.
All routes must be served over HTTPS outside local development.

## Device enrollment

1. Generate an Ed25519 key pair locally. Generate 32 random bytes for a separate
   device token, encoded as 64 lowercase hexadecimal characters. Persist the
   private key and token securely **before** enrollment; send neither in the body.
2. Compute `token_hash` as lowercase SHA-256 hex over the **UTF-8 bytes of the hex
   token string**, not the original random bytes. POST `/api/messaging/devices`
   with `{name, public_key, token_hash}`. `public_key` is canonical standard base64
   of the 32-byte Ed25519 public key.
3. A 201 response contains `{device, signing_input, expires_at}`. Decode the
   standard-base64 `signing_input`. Its exact UTF-8 JSON bytes contain `Domain`,
   `Origin`, `UserID`, `DeviceID`, `PublicKey`, `TokenHash`, `Nonce`, `ExpiresAt`.
   Before signing, check the expected account, origin, returned device ID, key,
   token digest and domain `KyMessages enrollment v1`; check expiry as well.
   Sign the original bytes, without reserializing the parsed JSON.
4. Within five minutes, using the **same session**, POST
   `/api/messaging/devices/{device}/verify` with `{signature}`, standard base64
   of the 64-byte Ed25519 signature. Success returns `{device}`.
5. The first successfully verified device is `approved`. Subsequent devices are
   `pending` until an existing approved device calls the approval endpoint.
   The client must show and compare the pending device's fingerprint before approval.

This first-device rule trusts the authenticated initial enrollment; it does not
provide key transparency or protect against a compromised identity provider at
bootstrap. The server verifies key possession, not an MLS credential or KeyPackage.

Keep the original token: verification never returns a new credential. If its
response is lost, GET devices to recover the state and use the already-persisted
token. Verification is single-use; a repeated submission fails. An enrollment
whose challenge response was lost can expire before a fresh enrollment is attempted.

Device states are `unverified`, `pending`, `approved`, `revoked`. Revocation clears
the credential and keeps a verified-device tombstone. Revoking every device does
**not** permit another automatic first-device approval. The explicitly confirmed identity-reset
flow below is separately gated by fresh suite authentication. A live suite session can revoke
its own lost device without possessing that device token.

Device responses expose `id`, `user_id`, `name`, `public_key`, `fingerprint`
(SHA-256 hex of raw public-key bytes), `status`, `approved_by`, `created_at`, `identity_generation`.
They omit token hashes, challenges and enrollment-session bindings.

## Identity generations

Migration 9 assigns a monotonically increasing messaging identity generation per
account. Device listings and delivery rosters expose `identity_generation`. Room
ownership and invitations bind the generation that received them; acceptance and
listing reject a stale invitation. Device reset cannot silently inherit ownership.

The store's reset transaction requires explicit confirmed recovery state and fresh
verified subject, then increments the generation, approves the replacement, revokes
all other devices, expires unclaimed KeyPackages and removes old memberships. It
keeps device/package tombstones and records a session-bound idempotent receipt.
Affected rooms pause until surviving MLS clients commit removal. The callback applies reset only for a confirmed reset request with the operator
opt-in enabled; ordinary authentication completion never grants reset authority.

The server roster hash is now `KyMessages roster v2`; its device struct field order
is `ID`, `UserID`, `PublicKey`, `Generation`, `IdentityGeneration`. The isolated
client emits `KyMessages MLS proof delivery v2` authenticated metadata and retains
v1 verification for already accepted historical events (implicit identity generation
1). Generation-sensitive local pins require a new independent fingerprint approval.
Unaccepted old v1 outboxes may require conflict recovery; this is a prototype protocol
transition, not a supported production upgrade promise.

## Recovery authentication and identity reset

The recovery-auth routes default to authentication-only. Explicit
`{confirm_identity_reset:true}` requests reset; the server refuses it unless
`KY_MESSAGING_IDENTITY_RESET_ENABLED=true` (default false). Register the exact
`/api/messaging/recovery-auth/callback` URL and verify deployed issuer interaction,
parameter tampering and clock alignment before enabling it. This opt-in is not
production approval of the experimental MLS client.

Initiation requires a canonical device UUID, the pending device's credential,
and a live suite session; browser CSRF and the messaging rate limit apply. Migration
8 stores only hashed random state plus an encrypted OIDC request under a dedicated
key derived from the server encryption key. The record binds the original session,
account/subject, pending device/public key and sorted account device-registry digest.
It expires after five minutes. There are at most four outstanding requests per
account; starting a new one prunes that account's expired records. Session deletion
cascades its requests. Device registry changes invalidate existing snapshots, even
if unrelated to the target; this deliberately conservative rule supplements identity
generation binding and remains fail-closed during reset.

Callback requires one canonical 64-character state and one nonempty code of at most
4096 bytes. It uses the original suite session, without a device token or a new login
session. Validate stored bindings before the OIDC exchange; require fresh signed
`auth_time`, matching subject and nonce; then recheck the live session, target and
registry while atomically deleting the request and auditing completion. OIDC calls
run outside database transactions. Confirmed reset calls the atomic generation/reset transaction instead; authentication-only
completion cannot consume a confirmed reset request. Disabling the opt-in while a
request is outstanding prevents its reset. Concurrent mutations have one winner. Expiry,
logout, account changes, revocation and registry changes cannot become an approval.

Invalid callbacks return 400; missing/different/expired original bindings return
403; changed registry returns 409; failed signed authentication or corrupt sealed
state returns 401. Discovery failure returns 502. A lost authentication-only acknowledgement requires a new request. A completed
reset returns its persisted receipt to the same still-live original session on
callback retry, without exchanging the consumed code or mutating again. Receipt
replay works even after disabling further resets. Browser HTML callbacks redirect
to `/`; JSON callbacks return `{identity_reset:true,receipt:{device_id,identity_generation,completed_at}}`.
No return URL or session is issued by recovery. Beginning and completing audit
`messaging.recovery_auth_started` and `messaging.recovery_auth_completed` using the
target device ID. Reset audits `messaging.identity_reset` with old/new generation
and fingerprint. Audits never contain tokens, verifier, nonce, raw state or plaintext keys.

## Routes

All paths below start with `/api/messaging`. Successful mutations return 200
unless marked 201. All routes require the suite session described above.

| Method and path | Body / response | Additional authorization |
|---|---|---|
| POST `/devices` | `{name,public_key,token_hash}` → 201 enrollment challenge | Own account |
| GET `/devices` | `{devices:[...]}` | Own account |
| POST `/devices/{device}/verify` | `{signature}` → `{device}` | Original enrollment session and signature |
| POST `/devices/{device}/approve` | `{approved:true}` | Approved device of same account; target pending |
| DELETE `/devices/{device}` | `{revoked:true}` | Own non-revoked device |
| POST `/devices/{device}/recovery-auth` | `{confirm_identity_reset?:boolean}` → 201 `{authorization_url,expires_at,identity_reset_available,reset_requested}` | Own pending device credential and original live suite session |
| GET `/recovery-auth/callback?state=…&code=…` | Authentication result or reset receipt above | Original suite session; fresh signed OIDC evidence and unchanged device registry |
| POST `/devices/key-packages` | `{payload,expires_at}` → `{package_id,expires_at}` | Approved publishing device |
| POST `/rooms` | `{name,peer_user_id?,retention_days?}` → 201 room | Approved device |
| GET `/rooms?offset=0` | `{rooms:[...]}` | Approved device; own invited/active memberships only |
| GET `/rooms/{room}/members` | `{members:[{user_id,status,identity_generation,current_identity_generation}]}` | Approved device and active membership |
| POST `/rooms/{room}/members` | `{user_id}` → `{invited:true}` | Approved device and room ownership |
| POST `/rooms/{room}/join` | `{joined:true}` | Approved device and own pending invitation |
| DELETE `/rooms/{room}/members/{user}` | `{removed:true}` | Approved device and room ownership; owner cannot remove self |
| GET `/rooms/{room}/live` | WebSocket wakeups (below) | Live suite session, exact Origin and approved device first frame; active membership |
| GET `/rooms/{room}/delivery` | Current epoch, sequence, roster hash, paused flag and eligible devices | Approved device and active membership |
| POST `/rooms/{room}/events` | Event envelope → `{sequence,epoch}` | Approved device, active membership and current epoch eligibility |
| GET `/rooms/{room}/events?after=0` | `{events:[...],next,start_sequence}` | Approved device included in accepted epoch and current membership generation |
| POST `/rooms/{room}/key-packages/claim` | `{device_id,request_id}` → `{package_id,device_id,payload,expires_at}` | Eligible existing epoch device, or room owner before epoch 1; target eligible for addition |

Member listings include active/invited accounts and reset identities still represented
in the committed epoch. A removed identity's notice disappears after its removal
commit. These are server-reported changes, never proof of a replacement key; clients
must independently verify its fingerprint. Current plus committed rosters bound the
listing, rather than retaining an unbounded history of departed accounts.

Room objects expose `id`, `name`, `owner_id`, `created_at`, `membership`, `peer_user_id`, `retention_days`.
An empty peer denotes an ordinary invitation-only room. Migration 11 adds direct
rooms: creation with an existing active suite-only peer atomically invites that
account. Self-targeting returns 409; invalid IDs return 400; unavailable peers 404.
The recipient must accept before access. Ownership remains with the creator, and
owner invitations can only target the original peer (third-account attempts return
403). Removing/deleting the peer never turns a direct room into a group. Reset and
reinvitation retain ordinary identity-generation and future-history rules. Multiple
rooms for the same pair are permitted; this API does not promise pair uniqueness.
All timestamps are Unix seconds. Room names, device names, keys and memberships
are server-visible metadata; room names are not encrypted by this API.

Membership progresses `invited` → `active` → `removed`. The owner starts active.
Invitations target existing active suite-only accounts; acceptance is explicit.
Removed members can be reinvited while below the current capacity limit. Invited
members see the room in their own list but cannot fetch its roster until joining.
The member list includes invited and active accounts, plus the bounded reset notices
described above; removed accounts never regain access through a notice.

Membership/device changes pause application appends until a commit declares the
current roster. These remain **server ACL and delivery transitions**: clients must
apply and verify the corresponding MLS change. Deleting an ACL cannot revoke
previously obtained message keys.

## KeyPackage publication and claims

An approved device publishes an unused MLS KeyPackage with POST
`/devices/key-packages`: `{payload,expires_at,room_id?}`. `payload` is canonical standard
base64 of 1–16,384 wire bytes; `expires_at` is Unix seconds, after now and no more
than seven days ahead. The entire JSON body is capped at 24 KiB. Publication returns
`{package_id,expires_at}`; `package_id` is SHA-256 hex of the decoded wire bytes,
**not** the MLS KeyPackageRef. The authenticated device owns the package; the caller
cannot specify another publishing device in JSON. Migration 10 adds optional room
scope: a canonical `room_id` requires active membership, remains immutable on retry,
and prevents claims from any other room. Claims prefer their own scoped packages
before legacy unscoped packages. Omitted/empty scope preserves the earlier API;
new multi-room clients should publish scoped packages and retain each room's private
join material in that room's encrypted record. Lifetime and available-package quotas
still apply across the whole device.

The server stores opaque bytes. Receivers must validate MLS format/ciphersuite,
credential identity, enrolled signature key, signatures and signed lifetime before
adding the package. The HTTP expiry is a delivery bound and does not replace the
signed MLS lifetime. Each published package needs fresh private join material that
the publishing client retains securely until it joins or discards that package.

POST `/rooms/{room}/key-packages/claim` accepts `{device_id,request_id}`, both
canonical UUIDs. Persist a fresh `request_id` before sending; it is scoped to the
claiming device across rooms. The caller must be an active member using a device
already included in the accepted epoch, or the owner before the room's first
commit. The target must be another currently eligible device in that room and not
already included for its current membership generation. An invited-only user,
unrelated room member, newly joined device, revoked device or local admin cannot
drain this pool.

The server atomically assigns an available unexpired package to that claim and
returns `{package_id,device_id,payload,expires_at}` only after committing its audit.
Concurrent claims from different rooms cannot allocate the same row. An exact
retry by the same eligible caller returns the original package, including after
the add commit, while the target membership generation and expiry remain valid.
Changing the claim context returns 409 (or access/not-found errors when that
context is no longer accessible). A new claim with no available package returns
404; on PostgreSQL, an in-flight competing claim may also make a candidate
temporarily unavailable.

A claim is one-time **allocation of exact published bytes**, not proof that an MLS
group used them exactly once. A claimed package never returns to the pool: lost
responses retry the same claim; abandoned claims require fresh join material.
Publication retries with identical bytes/expiry are idempotent while unclaimed;
different expiry, a different owner or republication after a claim returns 409.
The API does not release claims or parse init keys to detect a malicious publisher
reusing private join material across different packages.

Each device has at most 16 available unexpired packages and 128 lifetime package
rows, including expired/claimed records. Rows remain as anti-republication
tombstones under this prototype cap, even after account deletion. Expired packages are not handed out and expired
claim retries fail. Rotation/replenishment UX, archival policy and backup rollback
reconciliation remain pilot gates. Restoring a snapshot predating a claim does not
by itself preserve the knowledge that the claim occurred.

The isolated browser adapter publishes its single unused join package and claims
missing roster devices automatically after explicit local fingerprint approval.
It saves publication parameters, claim IDs and returned bytes in the encrypted
vault before creating a commit. `delivery.approveDevice(id, fingerprint)` pins a
peer locally; it is distinct from the server's same-account device-approval route.
The expected fingerprint must come from the peer through an independent channel,
not merely be copied from the same untrusted directory response.

## Opaque delivery protocol

GET `/rooms/{room}/delivery` returns `{epoch,sequence,roster_hash,paused,devices}`.
Each device contains `{id,user_id,public_key,generation}`. The eligible roster is
the approved devices of active, suite-only room members; pending invitations and
inactive accounts are excluded. `generation` increases on each accepted invitation.
The hash binds the room and sorted roster including these generations. Echo the
returned hash; clients need not reproduce the server's internal JSON encoding.

An event append body is:

```json
{
  "id": "b26f0028-e8c6-40a0-aac6-a7c752abcf9b",
  "kind": "application",
  "epoch": 1,
  "roster_hash": "<current SHA-256 hex>",
  "payload": "<standard base64 of opaque MLS wire bytes>",
  "welcomes": {}
}
```

`id` is a canonical UUID generated once for the event. Persist the exact request
alongside the client's staged MLS state before sending. `epoch` is the expected
**current** epoch, not the resulting epoch. `kind` is `application` or `commit`.
The server supplies the sender device from authentication, never from the body.

- A new room starts at epoch/sequence 0 and is paused. Its owner initializes it
  with a commit for epoch 0, resulting in epoch 1. The eventual MLS adapter must
  create the initial group at epoch 0 and produce a real transition to epoch 1;
  it must not relabel an epoch-0 application message as initialization.
- Later commits require a sender already included in the accepted epoch and still
  eligible in the current roster/membership generation. They advance the epoch by
  one. Applications retain the epoch and require `paused=false`.
- Commits require exactly one `welcomes[device_id]` envelope for every newly added
  device or rejoining generation, except the initial committer. No other Welcome
  entries are accepted. Each envelope must already be encrypted for that device by
  the MLS client; the server only checks routing and byte bounds.
- The accepted epoch roster becomes the current eligible roster. Approval of an
  additional device, revocation, member removal/join, or account deactivation makes
  a differing roster pause new applications. New devices cannot commit themselves
  into an established epoch. If no eligible old epoch device survives, the room
  stays blocked pending an explicit reset workflow, which is not implemented.
- Every accepted event increments the room sequence by one. Event, epoch, roster,
  Welcome routing and success audit commit atomically before acknowledgement.
  Competing commits with the same expected epoch have one winner; losers receive
  409 and must fetch/process the winner before generating a fresh commit.
- `(room,device,id)` identifies retries. The same normalized body returns the
  original receipt even after later commits. Reusing that key for changed content
  returns 409. Retries still require current session/device/membership access.
  Omitted, null and empty Welcome maps normalize to the same empty map.

The server accepts opaque bytes, including bytes which are not valid MLS. The
enrolled device key authenticates the delivery account; the server does not validate
MLS credentials/KeyPackages. The isolated client proof validates that binding and
checks room/group ID, sender, epoch, transcript and resulting MLS roster against
pinned identities. Production integration still needs review. A malicious member can submit
an invalid commit and stall this prototype. An accepted receipt proves durable
storage only; it does not prove successful encryption, processing or delivery.

### Reconnect and history boundaries

GET events returns at most 50 ordered entries, with at most 1 MiB of base64 payload
and Welcome strings per page. Entries contain `{id,device_id,sequence,epoch,kind,
roster_hash,payload,welcome,created_at,expires_at}`. For commits, `epoch` is the resulting
epoch. `welcome` is the caller's envelope, or an empty string; other devices'
envelopes are never returned. Commit bytes are included as well, so newly welcomed
clients must initialize from the Welcome rather than process the same commit twice.

Persist a receiver's successfully processed MLS state and cursor atomically, then
request `after=next`. The server has no mutable per-client read cursor. Empty pages
retain the supplied cursor; a cursor ahead of the room head returns 409. A newly
included device starts at its inclusion commit (`start_sequence`); requests below
that floor are advanced to it. Continuing devices preserve their floor across
commits. Removal/rejoin increments membership generation and requires a new Welcome
and floor, even if no intermediate commit removed the old device snapshot.

Revoked devices and removed/inactive members cannot read. Remaining eligible epoch
devices may read while sends are paused to catch up and produce a commit. Operations
overlapping a revocation can complete in their pre-revocation order; subsequent
operations recheck current eligibility. Already downloaded bytes cannot be recalled.
HTTP polling uses the shared 120/account/minute limit. The WebSocket wakeup stream
below preserves the same store checks; no mobile push is implemented.

### Prototype bounds

Event POST bodies are capped at 768 KiB; payload and individual Welcome values are
canonical standard base64 of 1–65,536 bytes each. Their aggregate encoded size is
at most 512 KiB. Each room retains at most 4,096 nonexpired events and 32 MiB of
encoded payload plus Welcome data. The cap includes commits and returns 409 without
evicting unexpired state. Retry receipt metadata has a separate lifetime cap of
1,000,000 accepted events per room; reaching it requires a new room. These are
bounded prototype defaults pending workload measurements, not measured capacity.

### Ciphertext retention

Migration 12 adds immutable `retention_days` at room creation: 1, 7 or 30; omitted
or zero selects 30. Existing rooms get 30 days and existing event expiries derive
from their original creation time. This policy applies to both application events
and MLS control/Welcome material. Delivery state exposes `retention_days` and
`retained_from`; event responses expose Unix-second `expires_at`. Expiry remains
ordered if the server clock moves backward, so a missing prefix cannot masquerade
as a complete transcript. Changing a room's policy requires creating a new room.

Room operations clear expired payloads and Welcomes under the same room lock;
`store.Open` sweeps before returning, and the daemon sweeps idle rooms each minute.
Sweeps use per-room transactions and a 30-second background deadline, then retry
on the next tick. An operation that returns an error can roll back its incidental
cleanup, but cannot return expired ciphertext; the independent sweep clears it.

Keep event IDs, request hashes, sequence/epoch/roster metadata and timestamps after
payload expiry. Exact retries still return the original receipt; changing their
bytes still conflicts. Retention does not mean those metadata, audit records or
backup copies disappear. Logical removal is not guaranteed physical disk erasure.

An authorized cursor behind an expired prefix returns 410 with
`{code:"history_expired",error:...}`. Never advance a client ratchet past that gap.
A caught-up client can continue; a missed Welcome or transcript needs ordinary
explicit remove/reinvite and a fresh verified Welcome, or a new room if no suitable
owner/peer can perform that sequence. A new membership floor excludes old history.
The isolated client persists 410/409 gaps, pauses sending/automatic reads and
requires explicit newer-generation rejoin or a new room. It preserves ratchets,
cursors and pending bytes. New local transcript/inbox copies expire at the earlier
of the server deadline and local receipt plus cached room policy. Deadlines are
operational metadata; they do not authenticate sender clocks. Legacy local text
without deadlines requires explicit clearing. Full restored identity/rollback reconciliation remains
release work; server pruning alone does not complete those gates.

## Limits and failure behavior

- JSON bodies: 8 KiB by default, 24 KiB for KeyPackage publication and 768 KiB
  for events; one object, unknown fields rejected. Names: 1–80 UTF-8 bytes,
  trimmed with no control characters. User IDs: 1–64 bytes, no control characters.
- At most 32 device records per account. Expired, never-verified enrollments are
  cleaned during new enrollment; verified tombstones count toward the limit.
- At most 100 owned rooms/account and 100 historical member rows/room. At the
  member cap, every invitation is refused, including reinvitations. These are
  foundation safety bounds, not measured product capacity or final billing limits.
- Room listing returns at most 100 rows ordered by creation time then ID; `offset`
  is an integer from 0 to 1,000,000. It is not a snapshot across requests.
- Errors use `{error:"message"}`: 400 malformed input, 401 unauthenticated,
  403 denied, 404 absent/inaccessible resource or unknown route/method,
  409 conflict/capacity, 429 throttled, 500 internal failure. A revoked/expired
  session detected inside a transaction can produce 403 after middleware ran.
- Repeated approval, verification, revocation or membership mutations need not
  succeed idempotently. Reconcile device/room state after uncertain responses.
- Successful mutations and `messaging.*` audit records commit together. Audit
  details exclude tokens, private keys and challenges. Rejected attempts do not
  generate success audit records.

## Verification

`go test -race ./internal/store ./internal/api` covers device possession and trust
transitions, enrollment races across independent database connections, room consent
and isolation, session revocation, HTTP boundaries and a real signed OIDC callback
using a local test issuer. Set `KY_TEST_POSTGRES_DSN` to run the same tests on
PostgreSQL. The OIDC test rejects a wrong nonce before testing successful enrollment.
Delivery tests cover competing commits on separate connections, retry equality,
changed-body conflicts, pagination, member generation boundaries, per-device
Welcomes, device revocation and account deactivation. Their payloads are synthetic
opaque bytes, so these tests establish delivery invariants rather than MLS validity.
The separate browser delivery suite runs actual MLS through these same handlers
using a disposable SQLite fixture and synthetic suite sessions. A separate OIDC
browser suite runs the real redirect/callback and cookie/CSRF path against a local
issuer, including account-bound unlock, reauthentication and pending-send recovery.
This supplements the Go tests; live KyIdentity integration remains unverified.
KeyPackage tests additionally cover cross-room allocation races, publication/claim
retry recovery, tombstones, capacity, expired/revoked targets and changed membership
generations. HTTP browser tests discover packages through the server and reject a
corrupted package response before staging MLS state.

## Live wakeups

`GET /api/messaging/rooms/{room}/live` upgrades an authenticated suite session.
Require the exact scheme/host from the configured application origin and a canonical
room UUID; query strings are refused. Browsers use their existing HttpOnly session
cookie. Within five seconds, send one text frame containing the 64-character hex
device credential. Headers remain available for native session authentication; never
put a session or device credential in URLs or subprotocols. No other data frames are
accepted. The read limit is 64 bytes and compression is disabled.

After checking the live session, approved device and current room membership, the
server sends `{kind:"wake",sequence,epoch,roster_hash}`. These server declarations
only prompt authenticated HTTP cursor reads; they never advance local MLS state or
carry message bodies. Subscribe before reading the first snapshot to avoid losing a
concurrent change. Every reconnect gets the current snapshot.

Limits: four connections per account and 256 per process, including incomplete
credential handshakes. Each owns one coalescing signal; mutation notifications follow
successful commits. A 15-second heartbeat catches missed notifications, out-of-process
changes and session/identity revocations; it also pings the peer. Database and network
operations have five-second deadlines. Revoked access fails subsequent HTTP reads
immediately; a quiet stream can take the heartbeat plus operation deadlines to close.
Shutdown cancels upgraded streams and drains their tracked handlers before store close.

The prototype uses this stream in cookie mode for its selected room and retains
10-second polling as fallback. It coalesces notices with foreground operations,
preserves drafts and unresolved sends, and closes on lock, room switch or hidden/
offline state. This is single-instance delivery, without a broker or durable socket
queue. The server uses pinned [coder/websocket](https://github.com/coder/websocket)
for framing and control messages; MLS content remains in the existing HTTP protocol.
