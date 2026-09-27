**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch none; no Git metadata)

Historical proof checkpoint. For the subsequent device/room backend and current
next steps, read [MESSAGING-BACKEND-HANDOFF.md](MESSAGING-BACKEND-HANDOFF.md).

The user selected small teams and encrypted chat first, then requested continuation.
An isolated browser MLS proof now lives in `mls-proof/`; it is excluded from the
production Docker context and does not change the Go daemon or embedded web app.

Implemented: ts-mls 1.6.4 suite 1, explicit device key pins, passphrase-encrypted
IndexedDB persistence, atomic ratchet/outbox and receiver-state/cursor updates,
same-device tab serialization, staged commits and winner-before-retry handling.
The manual harness exchanges public wire bytes through copy/paste. Browser tests
use a trusted test driver as relay; there is no application delivery server yet.

Validation: strict TypeScript/build passed; eight tests passed in each of Chromium
and Firefox (16 total), including the published MLS exporter vector, removal,
identity substitution, replay/tampering, reloads and real storage-transaction aborts.
WebKit's eight cases could not launch on CachyOS: missing Ubuntu binary dependencies
`libicu74`, `libxml2`, `libflite1`. This is not a WebKit compatibility result. The
full command remains red here; the explicit Chromium/Firefox subset is green.

Next: run WebKit on a supported host, establish cross-engine/cross-library evidence,
then design authenticated delivery/epoch acceptance and OIDC device enrollment on
the existing base. Read `mls-proof/README.md` and `docs/MLS-LIBRARY-RESEARCH.md` first.
Milestone 0 remains open; upstream ts-mls is explicitly unaudited. Do not ship the
test-only `window.proof` API or advertise production E2EE from this evidence.

The remaining cryptographic, browser and product gates are listed in the proof
README. Root and proof DOX were updated; existing runtime child contracts remain
unchanged because those domains were not modified. No commit or PR was created.
