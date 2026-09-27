# KyMessages browser MLS proof

An isolated, disposable experiment using **ts-mls 1.6.4**, RFC 9420 suite 1.
The Go daemon and embedded React application do not import or deploy this code.
The proof is excluded from the Docker build context. Library selection and audit
limitations are in [MLS-LIBRARY-RESEARCH.md](../docs/MLS-LIBRARY-RESEARCH.md).

## Run

Use Node 24 or a version supported by the pinned Vite release.

```sh
cd mls-proof
npm ci
npx playwright install chromium firefox webkit
npm run build
npm test
```

On a supported Linux distribution, `npx playwright install --with-deps chromium
firefox webkit` installs browser prerequisites too. This machine is CachyOS; the
downloaded Ubuntu WebKit binary cannot start without `libicu74`, `libxml2`, and
`libflite1`. Run the available subset here with:

```sh
npm test -- --project=chromium --project=firefox
```

To inspect the manual harness, run `npm run preview` after building and open
`http://127.0.0.1:4178`. It binds loopback only. Use disposable browser profiles and
test messages. The production preview has a strict, same-origin CSP. `npm run dev`
is available for development but is not the tested CSP environment.

## Interactive chat prototype

Build with `npm run build` in this directory. Start two terminals:

```sh
# Terminal 1, repository root (temporary database, erased on exit)
go run -buildvcs=false -tags=mlsproof ./mls-proof/server

# Terminal 2, mls-proof/
MLS_PROOF_DELIVERY=1 npm run preview
```

Open `http://127.0.0.1:4178/chat.html` in two separate disposable browser profiles.
The page reuses the tested HTTP adapter and the shared Busnes light/dark tokens.
It is a local interactive prototype, excluded from the embedded app and Docker.

1. Create separate test accounts, such as `alice` and `bob`, and local passphrases
   of at least 16 characters. A passphrase protects the local vault; the fixture
   sign-in does not authenticate anyone. Anyone using this fixture can request a
   session for any synthetic account. Keep it on loopback and use no real data.
2. Alice creates a room and selects **Apply verified membership** to activate it,
   then invites Bob's test account. Room names and membership are server-visible.
3. Bob refreshes rooms, accepts the invitation and selects **Prepare to join**.
   Alice refreshes too. Both independently obtain the fingerprint shown in the
   other browser's own fingerprint panel, select the peer device, and verify it.
   The directory never supplies a prefilled verification fingerprint.
4. Alice applies verified membership again. Bob selects **Check for messages** to
   receive the Welcome. Both can now send encrypted text and check for messages.
   Checks are manual in this prototype; live updates remain product work.
5. A failed submission displays pending delivery and offers an identical retry.
   Reload locks the vault. Unlock with the original test account/passphrase to
   recover pending delivery and both sides of the conversation. Acceptance by the
   server is explicitly distinct from a read receipt. Message text is rendered as
   text, including HTML-looking strings; Markdown is not implemented here.
6. A second browser created for the same account starts pending. In the first
   browser, open **Your devices**, refresh, compare the second browser's own
   fingerprint independently, then approve it. Refresh the second browser. Server
   approval does not replace each room member's local fingerprint verification.
   The second browser can open an existing account room, prepare to join and be
   added after both browsers verify each other. It receives future messages only.

**Lock and disconnect** clears the visible transcript, drafts, fingerprints and
memory-only session/passphrase for the current tab. Other open tabs stay unlocked.
The encrypted vault survives. The theme follows
OS appearance until a local Light/Dark choice is saved. The UI does not export the
manual harness's `window.proof` or `window.delivery` test surfaces.

The unlocked chat tab checks the selected room automatically, waiting 10 seconds
between completed operations. Checks pause while hidden, offline, or waiting on an explicit send retry.
Failed checks back off to 20, 40 and at most 60 seconds; a successful action restores
the 10-second interval. Drafts and typing focus remain intact. Manual checks still
work. This is foreground HTTP polling, not WebSocket delivery or background push.
Each tab polls independently; the server's shared account rate limit still applies.

Delivery requests have a ten-second deadline. Lock aborts in-flight delivery and
clears the view; late results cannot reopen it. Cookie session loss detected by a
poll locks the UI without discarding encrypted state. Hidden/offline tabs and tabs
with pending sends detect session loss on their next network operation.

The prototype owns one room per browser profile. **Prepare to join** retries an
unexpired publication or renews an expired one with fresh join keys and the same
verified device identity. It retains up to 16 older packages for delayed Welcomes;
checking messages completes a join and removes the consumed package. Unused
packages remain encrypted because the server may offer them on a later rejoin. It
does not support room switching, deployed identity integration or identity reset.
Start with fresh profiles when the disposable fixture database is restarted. Preserve existing
profiles while that fixture runs to exercise reload and retry recovery.

Room owners can select an invited or active account under **Room members** and
choose **Remove member**. Confirmation explains that server access ends immediately,
earlier downloaded messages remain, and an active roster change requires **Apply
verified membership** before sending resumes. The UI refreshes even after a lost
removal response and shows the server's pause state. An unused invitation can be
revoked without a cryptographic change. Owners cannot remove themselves, and
unresolved outbound state blocks removal. Other members have no removal controls;
the API enforces ownership independently.

**Your devices** offers **Revoke device** for another browser or this one. Confirmation
warns that downloaded history remains and affected rooms need a verified membership
update. The device list refreshes even after a lost response. A revoked browser stops
automatic checks once detected; unlocking it again shows existing local history
without attempting enrollment. Sending and receiving remain disabled. Revoking the
last approved device leaves new browsers pending; identity reset is not implemented.
A live account session can still revoke devices even with unresolved local ciphertext.

**Lost a browser or its passphrase?** remains available before unlock. It explains
replacement approval, revoking lost devices from a pending replacement and the
limits of server backups. The account banner distinguishes the only approved device,
a replacement needing approval, and an account with no approved devices. Approval
status cannot tell whether a listed browser is still accessible. The proposed reset
contract and acceptance gates live in [the product definition](../docs/PRODUCT.md#losing-every-approved-device);
the prototype provides guidance, not a reset action.

After removal and a new invitation, **Accept reinvitation** prepares this same
approved device for a new membership generation. An existing member applies the
membership change; **Check messages** verifies the new Welcome. Earlier local
history remains, but messages sent while removed are unavailable. Pending outbound
work blocks rejoin rather than being silently discarded. Sends remain disabled
until the new Welcome is verified. A lost invitation response can be retried after
reload. This cannot reapprove a revoked device or recover a retention gap.

`npm run test:delivery -- --project=chromium --project=firefox` includes DOM-driven
chat and same-account device-approval tests alongside the protocol tests. Screenshots
from the chat test are local artifacts under `test-results/`.

## Suite OIDC browser flow

The optional OIDC mode exercises the existing suite login/callback and session
handlers with a local disposable issuer. It does not use the direct session-issuing
fixture route, and it is not a test against a deployed KyIdentity service.

```sh
# Terminal 1, repository root; stop the ordinary fixture first
MLS_PROOF_OIDC=1 go run -buildvcs=false -tags=mlsproof ./mls-proof/server

# Terminal 2, mls-proof/ (after npm run build)
MLS_PROOF_DELIVERY=1 npm run preview

# Or run the automated suite from mls-proof/
npm run test:oidc -- --project=chromium --project=firefox
```

Open `http://127.0.0.1:4178/chat.html?auth=oidc` in fresh disposable profiles.
Choose **Sign in with suite identity**, enter a test identity on the local issuer,
and return through the real callback. The issuer has no passwords: anyone can
select a synthetic name. It exists solely to exercise discovery, RS256 verification,
nonce, S256 PKCE and session issuance; its key and database disappear on exit.
This fixture explicitly ignores host-configured live issuers and disables
`/proof-fixture/session/` in OIDC mode. HTTP/non-Secure cookies are loopback-only
fixture settings, not production deployment guidance.

The page displays the account ID returned by `/api/auth/me`. Create or unlock the
local device with a passphrase, then use the same room/verification/chat flow above.
Invite peers using their displayed **account ID**, not their name or provider subject.
The callback generates `usr_…` IDs; MLS credentials use those exact IDs. Identity
validation accepts 1–64 UTF-8 bytes without controls or malformed surrogate strings.
Roster hashing matches Go's HTML-safe JSON escaping, including U+2028/U+2029, and
is exercised with non-ASCII IDs against the real API in the delivery suite.

Session cookies stay HttpOnly. Cookie-mode requests use same-origin credentials
and the existing CSRF helper, with no Bearer header, ID token or session credential
in the vault, localStorage or sessionStorage. A boolean sessionStorage hint returns
the existing callback's `/` landing page to the fixed prototype URL; it is not a token
or configurable redirect. Account status is fetched without caching and rechecked
before messaging requests. Session loss or an account mismatch locks this tab when
an operation detects it; this is not a background revocation notification.

**Lock and disconnect** clears this tab's local secrets/display but keeps the server
session. **Sign out of suite**, available on the locked screen, also calls the server
logout endpoint. Reauthentication as the original account unlocks the existing vault,
including a pending ciphertext submission; a different account cannot unlock it through
this UI. The passphrase never leaves the browser. Previously copied local keys/history
are not revoked by server sign-out. Offline unlock, immediate cross-tab lock and
session changes during an unfinished enrollment remain product lifecycle work.

The OIDC suite verifies encrypted two-account exchange, HttpOnly cookies, CSRF denial,
reload, server logout, wrong-account unlock rejection and identical pending-send retry
after session loss. Altered nonce and PKCE challenges must fail the real callback
before a session exists. No production identity service or account is used.

## Manual two-device flow

1. Open two separate browser profiles (not just two tabs of one profile). Initialize
   `alice` and `bob` using separate test passphrases of at least 16 characters.
   Initialization returns each device's public KeyPackage; Status also returns it.
2. Copy Bob's KeyPackage into Alice's wire field and vice versa. Inspect and compare
   fingerprints using an independent channel, then explicitly Approve device on
   both sides. The proof supplies a strict identity/key validator to MLS.
3. Alice creates a room, then stages an add with Bob's KeyPackage. Copy the returned
   Welcome before accepting the staged commit. The manual operator acts as the
   trusted relay and accepts exactly one staged commit for an epoch.
4. Bob pastes the Welcome and selects Join Welcome. New devices obtain future keys,
   not earlier history. A KeyPackage is consumed after creation/join.
5. Alice sends synthetic text. Copy the returned base64 wire message to Bob's wire
   field, choose sequence 1, and Receive. Each receiver has its own contiguous
   sequence; increment it for every incoming message or commit. Then acknowledge
   those wire bytes on Alice, clearing the pending outbox entry.
6. Reload either browser, unlock it with its passphrase, and inspect Status. History,
   cursor, ratchet and any unacknowledged wire bytes survive. A resend copies the
   stored outbox entry; pressing Send again creates a new message.

For more members, every device first pins the new member and the new member pins
every existing one. Existing members receive the accepted add commit; the new
member receives its Welcome. Removal is also a staged commit distributed to peers.
No member is added solely because its identity string matches a directory name.

Discard staged commit means it lost to another commit. The device stays blocked
until it receives that winner. It cannot cancel and create another private commit
from its previous epoch. The manual test driver simulates the relay's choice;
the separate HTTP suite below uses the real backend's acceptance and room ACLs.

## HTTP delivery proof

```sh
npm run build
npm run test:delivery -- --project=chromium --project=firefox
```

This starts the real Go API on `127.0.0.1:4179`, with a temporary SQLite database,
and the proof preview on `127.0.0.1:4178` with an opt-in same-origin proxy. Both
processes stop after the suite. The fixture in `server/` requires the `mlsproof`
Go build tag and the whole proof remains excluded from production Docker builds.
The normal preview has no API proxy. Production auth and API handlers are unchanged.

The ordinary fixture provisions disposable suite accounts/sessions on a separate
test route. Those bearer-mode tests do **not** validate browser OIDC; the separate
OIDC mode above runs the full browser redirect/callback path. Use only synthetic
names, credentials and messages.
The browser test API is `window.delivery`, alongside the manual `window.proof`.
Use a fresh profile for each mode; the manual controls do not coordinate the HTTP
adapter's pending delivery state.

The HTTP adapter performs real Ed25519 enrollment with the MLS signature key and
requires the basic MLS identity to equal the authenticated account's ID. It checks
public KeyPackages against the enrolled roster and independently pinned identity/key
pairs. The HTTP test drivers exchange **fingerprints only** for explicit approval.
Joining browsers call `publishKeyPackage()`; the committer's `stageCommit()` claims
the missing devices' packages through the API. Public packages, commits, Welcome
messages and application ciphertext all travel through the Go API.

`directory()` returns `{peers,paused}`: device IDs, account IDs, displayed
fingerprints and the server's send-pause state. `members()` lists invited and
active accounts.
`approveDevice(id, expectedFingerprint)` requires a fingerprint independently
obtained from that peer's `ownFingerprint()`; copying the directory's own value
back into approval would not verify the directory. This local pin does not grant
the server's separate approval of a new device belonging to the same account.
Publication parameters and claim request IDs are saved before networking. Lost
publication/claim responses retry the same operation; claimed packages never return
to the pool. An expired cached claim gets a new durable request ID. A lost claim
response retains its ID until an explicit server 409 marks it unusable; the next
membership attempt uses a fresh ID. Other failures preserve the original request.
Renewal retains old private join material inside the encrypted vault because a
Welcome may already exist. Welcome processing matches exactly one retained MLS
KeyPackageRef before full signature, tree and metadata validation. Renewed packages
have a seven-day signed MLS lifetime and a one-hour HTTP allocation lifetime.
These are proof defaults, not the product retention policy. Cached packages are validated against their digest, device, identity,
signature key and MLS signature before creating a commit. The server also bounds
expiry and pool size; details are in [the API contract](../docs/MESSAGING-API.md).

The adapter binds the MLS group ID to the server room ID. Room/event/sender IDs,
kind, expected epoch, roster hash and full public device roster are placed in MLS
authenticated data. Welcome joins additionally check the signed GroupInfo extension
`0xff01` containing the same metadata and match its signer to the enrolled sender.
Receivers check the actual MLS sender, resulting epoch and ratchet-tree membership
before exposing plaintext or advancing state. This is an experimental application
profile, not a standardized federation/interoperability format. Its roster hash
encoding preserves exact UTF-8 account IDs and matches Go's HTML-safe JSON escaping.

Enrollment saves its token and then its challenge before submitting proof; a lost
verification acknowledgement is reconciled against the device list. Bearer-mode session
credentials stay in memory and must be reconnected after reload; OIDC sessions use
the existing HttpOnly cookie and a fresh account-status check. Event requests,
staged commit states and application ratchets are saved before network submission.
A lost acknowledgement retries identical bytes and the same event ID. Acceptance
does not advance the cursor: ordered processing of the accepted event does. A
losing commit waits for and processes the winner before another send/commit.
Receiver state, cursor, inbox, sent/received transcript and pending-request changes share the encrypted vault
transaction; aborted writes release neither plaintext nor a successful result.

Verified HTTP checks: two lifecycle cases pass in each of Chromium and Firefox
(four passes), and a Chromium-to-Firefox case passes once. Its duplicate in the
Firefox project is intentionally skipped. Those five protocol passes cover encrypted exchange,
Welcome join, enrollment/publication/claim/event acknowledgement loss, reload, concurrent application
sends, competing commits, removed-member access/decryption denial, metadata tampering
and a real aborted receiver transaction. Wrong fingerprints and corrupted package
responses are rejected before staging state. The server receives ciphertext rather than
test plaintext. Two additional cases verify non-ASCII credential IDs and Go roster
escaping (one per engine). A Chromium/Firefox mixed-engine exchange is verified; Safari,
WebKit and independent MLS-library interoperability remain unverified. The additional
four UI cases (two per engine) verify the visible invitation/chat/approval flow,
keyboard sending, plaintext-safe rendering, pending retry across reload, history
deduplication, lost invitation acknowledgements, second-browser room admission,
wrong passphrases, lock clearing, mobile overflow and saved themes. The own-device
cases also cover cancelled revocation, lost revocation replies, automatic detection,
read-only history after reload, current-browser revocation and a replacement staying
pending after the last approved device is revoked. Two lost-browser cases prove a
pending replacement can revoke its inaccessible predecessor without gaining
approval. Two owner-removal cases cover invitation cancellation, active-member
removal, lost responses and the required encryption update.

Six renewal cases (three per engine) cover expired cached allocations, lost claims,
publication acknowledgement loss, reload and delayed Welcomes after renewal,
including subsequent rejoin using an unused retained package. Four rejoin cases
exercise generation changes with/without an intervening removal commit, lost
acceptance responses, unresolved-outbox refusal, stale history-floor rejection,
identity preservation and encrypted chat across the gap. Two also use the actual
Accept reinvitation button and verify disabled sending while waiting.

This remains a one-room/profile proof that publishes only pre-join material and
cannot use that published material to initialize a different group. Its interactive
fixture UI, including OIDC mode, is not a deployed product client. It has no automatic pool
replenishment, signing-key rotation, retention-gap recovery, history reset or restore reconciliation. A stale-roster rejection without a winning commit deliberately
leaves the client waiting; room-creation response loss can leave an unused room.
The transcript starts with new sends/receives; older proof inboxes are not backfilled.
The adapter validates list capacity before persisting its connection record; a full
256-entry transcript stops progress rather than silently deleting history.
Those recovery paths and an independent application-binding review are required
before product integration. The underlying MLS library remains unaudited.

## Verified 2026-09-27

| Check | Chromium | Firefox | WebKit |
| --- | --- | --- | --- |
| Published suite-1 MLS exporter vector | Pass | Pass | Host cannot launch |
| Two independent contexts, reload, offline return, encrypted state and resendable outbox | Pass | Pass | Host cannot launch |
| Altered ciphertext, replay, trailing bytes, sequence conflicts and missing events | Pass | Pass | Host cannot launch |
| New member lacks old history; removed member cannot decrypt new epoch | Pass | Pass | Host cannot launch |
| Concurrent staged commits, reload, discard/winner/retry convergence | Pass | Pass | Host cannot launch |
| Unknown credentials and substituted device keys fail | Pass | Pass | Host cannot launch |
| Same-device tabs serialize; aborted storage transactions release no message/receipt | Pass | Pass | Host cannot launch |
| Manual controls run with strict CSP; sending issues no network request | Pass | Pass | Host cannot launch |

Playwright 1.63.0: Chromium build 1243 and Firefox build 1543. The manual tests use
separate contexts within each engine; the HTTP suite also tests Chromium/Firefox
together. Real Safari/iOS tests remain.
`npm run build` passes strict TypeScript checking and bundling. `npm audit` reported
zero known vulnerabilities for the pinned experiment dependency graph; this is not
a cryptographic audit. The all-engine run reports 16 passes and 8 launch failures,
not a green all-browser result.

The independent vector is the first suite-1 epoch in the MLS working group's
[key-schedule vectors at fd51ea7](https://github.com/mlswg/mls-implementations/blob/fd51ea702fe637e48452b8118123bff118767731/test-vectors/key-schedule.json).
Source file SHA-256: `05aa9a68bd2538ace72d8c53375984cc728ef62220ebf314df675708546d97a7`.
The vector tests the exporter only, not complete RFC conformance or cross-library
interoperability. Inputs and expected output are embedded in the test; test runs
do not fetch anything from that source.

## Storage and protocol decisions exercised

- Each browser profile owns a single device and one IndexedDB record. The record
  contains the MLS state, key-package private material before use, explicit public
  key pins, staged commit, ciphertext outbox, decrypted history and receive cursor.
- AES-256-GCM seals the whole record with a fresh random 96-bit IV, format-version
  associated data, and a key derived from a user-supplied passphrase using PBKDF2
  SHA-256 (600,000 iterations, random 128-bit salt). Only salt, IV, format version
  and ciphertext are persisted outside that envelope. The passphrase is memory-only
  and reload locks the device. This is a proof UX, not the final product unlock design.
- Web Locks serializes tabs sharing that one device. Each operation reloads the
  latest record under the lock, runs MLS, then waits for the strict IndexedDB write
  transaction to complete. Different profiles never share secret storage.
- Outgoing ciphertext and updated ratchet commit together before the caller receives
  wire bytes. Incoming ratchet, deduplication digest, cursor and history also commit
  together before an application result is returned. Aborted writes leave old state
  usable; tests inject real IndexedDB transaction aborts at that boundary.
- Group-state binary decoding reattaches trusted application configuration. MLS wire
  parsing rejects trailing bytes. Approved identity/signature-key pairs replace the
  upstream validator that otherwise accepts all credentials.
- Private commits remain staged until the simulated relay accepts one. On rejection,
  the client must receive a winning commit before generating new traffic. Version
  1.6.4 does not return the advanced old-epoch handshake tree from `createCommit`;
  recreating a discarded private commit could repeat a generation. This restriction
  avoids that path without modifying the MLS implementation.
- The proof retains no old epoch/generation message keys. Offline delivery must
  replay the ordered stream, including control events. Gaps fail explicitly. It
  caps each list at 256 entries, the vault plaintext at 2 MiB, and text at 4096 bytes.
  These are experiment limits, not proposed production capacity.

The library's root module references optional crypto providers. The proof imports
its public subpaths to avoid adding unused provider dependencies. Vite warns that
the HPKE dependency's Node `crypto` fallback is externalized; the browser tests
exercise WebCrypto and pass without that fallback. No CDN runtime imports are used.

## Gates still open

- A security review sufficient to approve the chosen MLS implementation and its
  application binding; upstream explicitly reports no formal security audit.
- WebKit on a supported host, real Safari/iOS and cross-library tests; only the
  Chromium/Firefox engine pair has cross-engine evidence so far.
- Full official vectors/fuzzing, persistent browser-process crash tests and device
  rejoin after an expired offline window. Reload and transaction-abort tests are
  useful evidence, not a power-loss/OS-crash durability proof.
- Live KyIdentity/operator deployment validation, key transparency/directory substitution policy,
  reviewed KeyPackage rotation/replenishment, production integration of the exercised
  delivery/epoch/room checks, bounded replay retention, server-restore reconciliation
  and history reset UX.
- A reviewed product unlock/recovery policy. Losing the test passphrase or browser
  storage loses the device. This does not test WebAuthn wrapping or device transfer.

An attacker controlling the origin or unlocked browser can access decrypted state.
The experiment cannot detect rollback of the entire local vault or guarantee physical
erasure of JavaScript strings, SSD pages, browser backups or old database versions.
Passphrase strength determines resistance to offline guessing. The test-only
`window.proof` / `window.delivery` APIs and fixture session issuer must never be
shipped as product APIs.

Milestone 0 remains open. The proof supports continuing toward product client
integration; it does not justify advertising or deploying production E2EE chat yet.
