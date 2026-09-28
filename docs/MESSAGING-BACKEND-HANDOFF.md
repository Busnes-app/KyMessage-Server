**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User scope: complete the small-team encrypted-chat first release; commit each
completed slice and before unavoidable breaks. Commit/push are authorized. Remote:
https://github.com/Busnes-app/KyMessage-Server.git. KyMessages is not deployed
(user-confirmed). Continue `docs/FIRST-RELEASE-PLAN.md`; never equate prototype test
passes with production E2EE approval.

## Current implementation

- Source-built KyMessages operator console, local Compose/image identity and
  reproducible embedded assets. The MLS client remains isolated in `mls-proof/`
  and excluded from production/image publication.
- Real MLS suite-1 prototype over authenticated opaque HTTP delivery: device
  verification, immutable direct peers, private rooms, membership CAS, scoped
  one-use join packages, independent fingerprint pins and authenticated bindings.
- Identity-generation reset requires confirmed fresh suite authentication, binds
  the original session and replacement key, revokes old devices and inherits no
  rooms. Production gate remains off pending deployed issuer assurance.
- Room-owned encrypted vault entries preserve ratchets, pending messages, cursor
  and history across reload and switching. Explicit local history works offline.
  Confirmed vault deletion locks other tabs and prevents late writes resurrecting
  deleted/replaced entries. Recent display history rolls at 256 entries/256 KiB;
  eviction is permanent and visible, with keys/pending traffic retained.
- Cookie-mode WebSocket wakeups lead to durable HTTP reads; polling is fallback.
  Server retention (1/7/30 days), local expiry, explicit gap recovery, rejoin,
  device/account removal and safe local Markdown are implemented and tested.
- SQLite capsule restore prunes expiry, invalidates grants and retires restored
  rooms. Fresh identities/new rooms avoid stale MLS rollback. Scheduled/local
  backups, latest recorded outcome UI and a real running-server scheduler drill
  are implemented. The drill is `scripts/backup-acceptance.py` (~3 minutes).

Authoritative contracts: `PRODUCT.md`, `MESSAGING-API.md`, `RESTORE.md`, and the
nearest DOX documents. CI is green through `4549b82`; recent implementation slices
are `195f949` (backup outcomes), `280ebcf` (interop), `e27924f` (packaging),
`3d26ccc` (local removal), `fc309a0`/`01ad7de` (restore).

## Browser checkpoint

Clock-controlled tests now install clocks before navigation. Chromium/Firefox pass
55 HTTP/UI cases (one intentional duplicate skip), eight OIDC cases and the added
native diagnostic (256 Ed25519 generations each). Earlier manual lifecycle suite:
20 cases passed, including 260 encrypted messages and retained pending sends.

Linux WebKit in the pinned supported container passes 10 manual lifecycle and four
OIDC cases, but the full HTTP/UI suite has two key-creation failures. A blank-page
native regression reproduces `generateKey('Ed25519', ...)` OperationError without
MLS/application code. Do not retry around this failure or claim Safari/iOS support.
`docs/BROWSER-EVIDENCE.md` records pins, commands and the container offline pitfall.

## Transport checkpoint

`internal/api/messaging_load_test.go` is opt-in (`KY_MESSAGING_LOAD=1`). It runs
real HTTP/cookie/CSRF and 100 WebSockets over disposable SQLite, with synthetic
accounts and 1,024-byte opaque events. It first reproduced HTTP 429 after 64
messages because reads/writes shared one 120/minute account budget. Reads now
have 2,400/minute/account; writes retain 120/minute and enrollment retains 10/5 min.
A regression keeps write limiting and read/write independence explicit.

The final constrained 2-CPU/2-GiB run uses 50 independent account senders and
reports phases separately, including scheduling backlog. 60 seconds at 10/s then
two seconds at 50/s produced 700 receipts and 70,000 ordered, exact deliveries.
Sustained p95 acceptance/receipt: 67.24/220.91 ms; burst: 52.73/158.43 ms. A previous
global serial sender produced artificial burst backlog; the final harness measures
the intended independent-account workload. See `docs/MESSAGING-LOAD.md` for exact
pins, procedure and scope. This does not measure MLS, deployed TLS or a soak.

Verification: full SQLite API race suite and vet pass; messaging API race tests
pass on an owned disposable PostgreSQL 17 instance. API DOX and wire documentation
now state separate budgets. Root/store/web DOX and child indexes intentionally stay
unchanged: no ownership, storage or UI contract changed in this runtime slice.

Next: verify the pushed transport commit in CI, then address remaining independent
operator acceptance work. Keep external/dependency gates below open. Production
client integration depends on reviewed cryptographic/application bindings, not
another prototype-only test pass.

## Open release gates

- Independent assessment of the MLS implementation and KyMessages application
  profile. ts-mls 1.6.4 remains unaudited; prototype success is not this evidence.
- Unmodified OpenMLS interoperability fails: ts-mls rejects valid unknown protocol
  version 999 in capabilities. The MLS-1.0-only fixture passes exchange, updates,
  add/remove and live-secret agreement in both browsers, but this is constrained
  evidence. Custom GroupInfo `0xff01` binding remains unverified independently.
  See `MLS-INTEROP-RESEARCH.md`; never strip signed incoming bytes as a workaround.
- Deployed HTTPS/KyIdentity callback and fresh-authentication semantics. No hostname
  has been provisioned; do not repeat the already answered deployment question.
- Reviewed production client integration, actual target-platform support, deployed
  end-to-end/load evidence, and live remote KyRecovery acceptance.

## Working constraints

- All host MLS suites share ports 4178/4179; run sequentially and don't rebuild dist
  during a suite. Container suites use their own network namespace.
- Never restore browser MLS state from a server capsule or bypass independent key
  verification. Keep synthetic test identities separate from live accounts.
- Preserve explicit legacy `KY_APP_NAME` values for existing recovery pins. No
  published product image exists; use the source-build Compose overlay for preview.
- Leave unrelated PostgreSQL containers and runtime data alone. Use owned disposable
  containers/directories for tests; `make clean` preserves data/backups.
- Mirror this file and `FIRST-RELEASE-PLAN.md` to shared folder
  `kymessages-product-definition` using myslop-handoff. Owner `copperfinch`, author
  note `Usagi / GPT-6 / copperfinch`. On actual handoff release to `open`, owner null.
