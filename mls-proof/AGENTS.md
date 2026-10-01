# Purpose

Isolated browser feasibility experiment for KyMessages MLS messaging. This is not
part of the embedded web application or a production messaging implementation.

## Ownership

Owns the proof client, browser lifecycle tests, pinned experiment dependencies and
local build output. `chat.html` owns the interactive HTTP prototype; `index.html`
retains the manual wire harness. Root owns product decisions and research in `docs/`.

## Local Contracts

- Use the selected MLS library for wire encoding and cryptography; never substitute
  a home-grown protocol. Keep experiment dependencies out of `web/` and the Go binary.
- Use disposable identities and synthetic messages only. The manual suite relays
  KeyPackages and messages in the test driver. The HTTP suite exchanges only
  fingerprints out of band; browsers publish/claim packages and send encrypted
  traffic through the real Go API.
- Persist each room's state separately. Tabs of the same room serialize its ratchet;
  different rooms use different IndexedDB entries and Web Locks. The legacy `device`
  entry retains the first room and its original format/lock name. New `room:<UUID>`
  entries authenticate that entry name as AES-GCM associated data, preventing swaps.
  A short native allocation transaction caps total entries at 100; per-entry 2 MiB
  and non-history list bounds remain. The non-secret selected-entry hint is tab-local sessionStorage.
- Confirm before removing all local messaging data. Clear every room entry in one
  strict IndexedDB transaction, retain no unlocked key, and notify same-origin tabs
  to lock. Device revocation and suite sign-out remain separate explicit actions.
  The root envelope salt identifies the vault generation: check it in every room
  write transaction so encryption already in flight cannot resurrect a removed
  vault or append old rooms to a replacement. Serialize root creation/removal using
  the existing root lock; do not serialize unrelated room ratchets globally.
- Derive the non-extractable AES wrapping key only at setup/unlock; retain no
  passphrase between operations and never persist the key. Lock invalidates pending
  unlock/setup results so late KDF completion cannot reopen a locked device.
- Persist each room's ratchet, outbox, history and cursor in one encrypted IndexedDB record;
  release outbound bytes and received plaintext only after transaction completion.
- A discarded private commit requires applying a winning commit before further
  sends or commits. Retry a pending transport submission with identical wire bytes.
- Document failed gates and security limitations alongside successful checks.
- Optional OpenMLS interoperability uses only owned loopback native/gRPC processes,
  pinned by `docs/MLS-INTEROP-RESEARCH.md`. Keep RPC responses/private test material
  out of logs; `proof.interopState` exposes only hashes of synthetic group secrets.
  Unmodified OpenMLS currently exposes ts-mls's unknown-version capability decoder
  failure. `openmls-mls10-only.patch` changes the test peer's advertisement only;
  passing that constrained exchange never closes the extensibility/profile gates.
- `delivery.ts` binds MLS credentials/signature keys to enrolled accounts and
  independently pinned roster keys. Keep the room ID, event ID, sender, epoch and
  roster and identity generation in authenticated MLS metadata; Welcome joins validate the signed GroupInfo
  binding extension as well as its signer. This experimental profile is not a
  published interoperability contract. Emit roster/metadata v2; legacy v1 events
  retain their original hash profile and imply identity generation 1. Fingerprint
  pins include the identity generation, so resets require independent verification.
- Persist the delivery token, enrollment challenge, pending request and staged
  state inside the device vault. Fixture bearer sessions remain memory-only; OIDC
  sessions use the existing HttpOnly cookie and `web/src/api.ts` CSRF helper, never
  tokens in JavaScript storage. Acknowledgement alone
  does not advance the cursor: ordered, verified event processing does.
- Persist KeyPackage publication parameters and claim request IDs before networking.
  Cache claimed bytes before creating an MLS commit; a lost response retries the
  same claim until a cached expiry or explicit HTTP 409 retires it. `approveDevice` requires an independently obtained fingerprint; the
  directory alone cannot pin a key. The proof renews an expired unused
  join package with fresh init/HPKE keys and the same enrolled signing key. Retain
  at most 16 prior packages inside the encrypted connection record for delayed
  Welcomes; match exactly one MLS KeyPackageRef and discard its consumed secrets
  with the verified, durable join. Retain unmatched unused packages for later
  allocation; the server can still offer them on rejoin. New publications are scoped
  to the selected room; preserve unscoped retry parameters from older records. New
  room records generate fresh init/HPKE keys with the same enrolled signing identity.
  Never use published material to initialize a group. Opening an empty owned room can
  initialize it after a lost creation acknowledgement; nonzero epochs cannot bootstrap.
- Explicit rejoin requires a newer membership generation after reinvitation and
  no unresolved outbox/commit. Keep old ratchet, cursor and transcript while fresh
  join material is published. Block sends/commits until a Welcome authenticates
  the expected generation and a newer epoch/history floor. Replace the ratchet
  atomically with that verified join; preserve earlier local history. Persist missing
  history (410) or rollback (409) as a local gap without changing ratchets/cursors.
  Pause sending and automatic reads until explicit recovery. Retention-gap rejoin
  requires a newer membership generation, including after an expired initial
  Welcome; retain attempted generations across repeated missed joins. A verified
  join clears the gap. Never rewind a cursor or epoch to fit restored server data.
  This does not recover revoked devices or the missing messages.
- New room/direct creation offers Off, 24 hours, 7, 30 or 90 days (default 90). The
  owner (or direct peer) changes it with `setRetention` (`PATCH /rooms/{room}`); the chat
  form shows the room's current value, is hidden from everyone else, and confirms any
  shortening (Off counts as longest) because it deletes history for everyone. Cache
  the policy encrypted and refresh it from every `/delivery` read; older records with
  1/7/30 still parse, and a pending without `createdAt` is stamped when loaded (so it
  expires one window later). Off means no local expiry deadline either. `submit`
  re-reads the window, then drops a pending send older than it (the server forgets
  purged receipts, so a retry would duplicate). Background polls and live wakes do not
  re-read it; the displayed value updates on refresh, open, send or submit.
  Distinguish server cleanup from recipient copies and backups.
- Keep fixture proxying opt-in with `MLS_PROOF_DELIVERY=1`; the normal manual
  harness stays offline. Production `web/` and authentication semantics remain untouched.
- `?auth=oidc` reads the immutable account ID from `/api/auth/me` before local setup
  or online unlock. Cookie-mode messaging requests recheck that account; session loss or
  mismatch locks the UI. The encrypted vault and pending ciphertext remain intact.
  Lock is local to this tab; suite sign-out separately revokes the server session.
  After the account lookup, recheck the view generation before starting setup or
  unlock: cross-tab removal cancels pending access and must not recreate a vault.
- Use exact account IDs of 1–64 UTF-8 bytes without controls or malformed surrogates.
  Match Go roster JSON field order and escaping, including `<`, `>`, `&`, U+2028/2029.
  The OIDC return hint in sessionStorage is a boolean fixed-path navigation hint,
  never a credential or arbitrary redirect URL.
- Direct conversations use room creation with one immutable peer. Reuse an existing
  visible pair when starting again; concurrent starts may create separate rooms.
  Recipient acceptance and independent fingerprint verification remain required.
  Cache the counterpart in the encrypted connection; reject changed bindings and
  any MLS roster containing another account even if its key was previously pinned.
  Direct-room invitation controls can only target the bound peer.
- Room switching preserves unresolved sends and histories in their original entries.
  Confirm before discarding an unsent draft; never carry a draft into another room.
  Pins copied from the first room remain bound to the verified account/key/generation.
  An unavailable saved room fails visibly and returns the next unlock to the original
  record without deleting the inaccessible entry. Saved-room discovery reads each
  encrypted entry without writes and reports unreadable entries individually. Cache
  room names inside their encrypted connection records; older unnamed entries show IDs.
- Read-only history unlock is an explicit passphrase-only action in a loaded app.
  Disconnect delivery before unlocking; never fetch session/device/room data, poll,
  send, approve or revoke in this mode. Hide account controls and the composer.
  Saved-room selection verifies its account against the authenticated local vault
  identity and binds entry name to room ID. Lock clears inventory, history and keys.
  Ordinary online unlock still requires the matching live suite account.
- `markdown.ts` renders messages locally using pinned markdown-it with raw HTML,
  image rendering, plugins, custom highlighters and automatic linkification disabled.
  Only absolute HTTP(S) links are active; set noreferrer/noopener and open a new tab.
  Never pass message source directly to an HTML sink. Keep identifiers and status
  text in DOM text nodes. The chat prototype requires a fingerprint
  obtained from the peer's own browser. Never populate verification from discovery.
  Same-account device approval and local room-key verification are separate actions.
  Room selection reconciles an already-active membership after a lost join response
  and lets an approved second browser request admission to an existing account room.
- Account device revocation uses the existing live-session API, including when local
  outbound state is unresolved. Confirm the target and last-approved-device risk;
  refresh after lost responses. Revoked keys reopen local history without enrollment,
  stop automatic checks and cannot send or receive. Retain the server tombstone: a
  replacement needs an existing approved device or the separately gated identity-reset flow.
- A `suspended` device (approved, token dropped by `restore-messages`) is reused by
  enrollment, never re-enrolled. Polling stops, room tools hide and the room shows the
  suspended notice with `Resume this device`. `resumeDevice` saves a pending
  `resumeToken` in every saved entry of the device before networking (reusing one
  already saved), follows a `/api/sso/` reauthentication URL from either resume step
  with the return hint, verifies the `KyMessages resume v1` binding and signs with the
  vault's enrolled key. Only after verify succeeds does it promote the pending token
  to the active one in every entry. Each refresh, including unlock, reconciles: a
  pending token on a device the server reports `approved` is confirmed with one
  token-authenticated read and promoted everywhere (an explicit 403 discards it); a
  still-suspended device keeps it. Ratchets, cursors and history stay; the resumed
  device continues at the server's epoch.
- Recovery help remains available before unlock. Account-specific guidance uses
  the refreshed device list and clears on lock; approval never implies that a
  device is accessible. Follow `docs/PRODUCT.md`'s last-device-loss contract before
  changing reset. In OIDC mode a pending replacement without room state can confirm
  reset, authenticate freshly and return to unlock the same vault. The fixture enables
  the default-off server gate, not production deployment. Preserve old browser data;
  reset inherits no rooms or ownership. Room notices are labeled server reports, and
  replacement generation/fingerprint verification remains independent.
- Room member lists include invited and active accounts. Only the room owner sees
  removal controls; the server independently enforces ownership and forbids owner
  self-removal. Native confirmation names the account and explains the boundary:
  access revocation is immediate, encryption changes require a verified commit,
  and downloaded history cannot be recalled. Refuse removal with unresolved local
  outbound state. Refresh after the attempt, including a lost response, so the
  visible roster and server pause state determine the next action. Invitations
  can be revoked without a rekey when the eligible roster is unchanged.
- Persist sent/received transcript entries and pending send text only inside the
  encrypted vault, atomically with cursor/outbox changes. New confirmed transcript
  and inbox copies carry a deadline: the earlier of server-reported expiry and
  receipt time plus cached room retention. Server times are operational metadata,
  not authenticated sender time. Expire both copies on selected-room access and
  the unlocked chat timer, including disconnected history mode; preserve keys,
  cursor, pins, outbox and unresolved text. Suspended/locked rooms clean up when
  next opened. Legacy text without deadlines has no timer but shares cache bounds.
  After an action, keep the newest 256 transcript entries within 256 KiB of JSON
  bytes; trim the diagnostic inbox too. Sequenced inbox entries below a pruned
  transcript floor leave together. Legacy unsequenced copies share their own bound.
  Persist the dropped-transcript count and show the permanent-loss notice at setup
  and in the room. This is a recent cache, not an archive for the retention period.
  Trimming never changes ratchets, cursor, pending sends/commits or verification pins.
  Clear-history resets the count as well as both plaintext caches. The manual wire
  harness also rolls its last 256 retry hashes with an offset from the cursor;
  older replays fail closed rather than decrypting again.
  `Clear saved history in this room` confirms before clearing only this entry's
  transcript/inbox; it cannot recall other copies or reset its cryptographic state.
  The wire request contains
  ciphertext only. Server acceptance is not a read receipt. Lock clears visible
  history, drafts, fingerprints and the in-memory connection/wrapping key; the OIDC
  cookie remains until the separate suite sign-out action.
- Cookie-mode chat adds one WebSocket for the selected active room. Send the device
  credential in the first frame; use the suite HttpOnly cookie, never URL credentials.
  Wakeups carry only sequence/epoch/roster hash and trigger ordinary verified HTTP
  reads. Coalesce notices with foreground actions and ignore already-applied cursors
  and directory hashes. Close on lock, hidden/offline tab, removed access or room
  change; guard stale asynchronous connection attempts with a separate generation.
  Retry after ordinary fallback checks. Read-only history opens no stream; the
  disposable bearer browser harness retains polling. No automatic send or approval.
- Automatic receive checks wait 10 seconds between completed operations in an unlocked,
  visible, online chat tab with a selected room and no pending send. Serialize
  polls with foreground actions; failures back off to 20/40/60 seconds. Polls
  process verified events only: no automatic resend, key approval or membership
  mutation. Drafts/focus survive checks; lock remains available during reads.
- Disconnect aborts in-flight delivery requests; each has a ten-second deadline
  including its cookie-account check. Guard late UI results with the local view
  generation so locking cannot be undone by a completed async render. Page exit
  locks locally; session failure on a poll uses the existing SessionError lock.
- Reuse vendored `web/src/ky-ui/tokens.css` without modifying shared tokens. The
  prototype follows the OS theme until its own saved appearance choice exists.

## Work Guidance

- Keep the proof small enough to discard after the library decision.
- Apply root boundary discipline and TypeScript skill instructions to the harness.

## Verification

- Install controlled Playwright clocks before navigation, before app timers exist.
- `tests/native-crypto.spec.ts` exercises repeated Ed25519 generation on a blank
  page without retries. Linux WebKit currently fails this native primitive; keep
  it outside the verified subset. Read `docs/BROWSER-EVIDENCE.md` before changing
  browser support claims. Actual Safari/iOS evidence is separate.

- `npm ci`, `npm run build`, and `npm test` from this directory.
- `npm run test:interop -- --project=chromium --project=firefox` requires the pinned
  external fixture paths documented in `docs/MLS-INTEROP-RESEARCH.md`; it is excluded
  from normal tests/CI until the compatibility failure is resolved.
- Browser installation on a supported Linux host:
  `npx playwright install --with-deps chromium firefox webkit`.
- `npm test -- --project=chromium --project=firefox` runs the verified host subset;
  WebKit requires the system dependencies listed in `README.md` on this machine.
- `npm run test:delivery -- --project=chromium --project=firefox` starts a disposable
  Go fixture and tests HTTP delivery, conflict recovery, key binding, removal,
  Chromium/Firefox interoperability and DOM-driven chat/device approval. The
  cross-engine case runs once in Chromium. Renewal cases exercise real server
  expiry, cached/lost claims, identical publication retry and delayed Welcomes.
  UI screenshots go to `test-results/`.
- `npm run test:oidc -- --project=chromium --project=firefox` exercises discovery,
  PKCE, signed callbacks, cookies/CSRF, account binding, reauthentication and pending
  send recovery and suspended-device resume against a disposable issuer. It is not
  live KyIdentity evidence.

## Child DOX Index

- [server/AGENTS.md](server/AGENTS.md): Build-tagged loopback Go API fixture,
  synthetic sessions and local OIDC issuer; excluded from production.
