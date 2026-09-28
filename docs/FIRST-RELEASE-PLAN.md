# Encrypted-chat first release

**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User-confirmed scope: complete the small-team encrypted-chat first release. Continue
through implementation and verification; commit each completed slice and before
any unavoidable break. Calls, bots, federation and native clients are outside this
release. `PRODUCT.md` owns the privacy contract and acceptance gates. Passing a
prototype test is not production approval.

## Execution order and exit checks

1. **Device recovery — implemented in the isolated prototype; release gates open.**
   Identity generations, atomic confirmed reset, idempotent receipts, prior-device
   and package revocation, generation-bound invitations/ownership, peer notices and
   independent replacement verification are implemented. SQLite/PostgreSQL race
   tests and Chromium/Firefox reset/rejoin drills pass. The default-off server gate
   still needs deployed issuer assurance and restore/rollback evidence.
2. **Durable conversations — in progress.** Isolated proof has verified opaque delivery,
   encrypted local history, outbox retry, rejoin and room switching. Each room owns
   its encrypted record/ratchet, scoped join packages and pending sends; local storage
   is bounded. Unlock retains only a non-extractable wrapping key. Direct
   conversations with immutable peer accounts are implemented. The final production
   unlock/local-data lifecycle remains open. Saved conversations
   remain discoverable after membership removal; explicit passphrase-only history
   mode reads local ciphertext without server requests in an already loaded app. Client-only Markdown with HTML/images disabled and HTTP(S)-only links
   is implemented and browser-tested.
3. **Live delivery and retention — open.** Replace foreground-only polling with
   authenticated live wakeups backed by durable cursor reads. Verify session/device
   revocation, origin checks, replay/reconnect, retention gaps, expiry cleanup and
   explicit rejoin. No plaintext notifications or server-side previews.
4. **Deployable client and identity — open.** Integrate the reviewed messaging client
   with the embedded UI, suite-only member access and isolated operator recovery.
   Apply KyMessages identity/coordinates, HTTPS setup, responsive keyboard UX,
   browser support declaration and reproducible embedded assets. Keep the disposable
   fixture and experimental unaudited client out of deployment until their gates pass.
5. **Operations and recovery — open.** Wire the inherited sealed-backup adapter to
   product identity, verify schedules/local copies/receipts, include retention-aware
   SQLite restoration and prevent restored state from silently rolling clients back.
   Document upgrades, storage bounds, key loss, deployment and restore.
6. **Release evidence — open.** Run CI on both database engines, production browser
   regressions, dependency checks, recovery drills, declared-host load tests and
   protocol/application-binding security review. Record actual supported browsers
   and measured limits. Resolve MLS library maintenance/license/security and
   independent interoperability gates before declaring production E2EE.

## Evidence already available

- Device enrollment/approval/revocation; invitation ACLs; durable opaque event log;
  epoch/roster CAS; bounded one-use KeyPackages; independent fingerprint pins.
- Chromium/Firefox proof exchange, reload/crash/outbox recovery, concurrent commits,
  removal/reinvitation, device-loss guidance and ordinary suite OIDC cookie/CSRF flow.
- Recovery-auth state sealed at rest; original-session binding, expiry and concurrent
  single-use completion and atomic reset tested on SQLite and PostgreSQL; browser
  reset/rejoin tests cover future-only access, original-session preservation and
  generation-2 enrollment.
- CI green through `32aa3e8`; current implementation remains a server scaffold plus
  an isolated MLS experiment, not a deployed encrypted-chat product.

## External evidence still required

KyMessages is not deployed yet (user-confirmed); live KyIdentity callback registration
and deployed reauthentication policy remain unverified; an
independent security assessment of the chosen MLS implementation/application profile;
interoperability against an independent implementation; and real supported-browser
and constrained-host measurements. Local tests may advance these gates but cannot
stand in for evidence they did not produce. Continue independent implementation when
one gate needs external input; never hide that gate or silently downgrade encryption.
