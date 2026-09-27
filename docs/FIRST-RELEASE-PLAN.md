# Encrypted-chat first release

**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

User-confirmed scope: complete the small-team encrypted-chat first release. Continue
through implementation and verification; commit each completed slice and before
any unavoidable break. Calls, bots, federation and native clients are outside this
release. `PRODUCT.md` owns the privacy contract and acceptance gates. Passing a
prototype test is not production approval.

## Execution order and exit checks

1. **Device recovery — in progress.** Fresh-auth verifier and durable, single-use
   session/device-bound authentication callback are implemented and tested on SQLite
   and PostgreSQL. Remaining: identity generations; atomic reset and idempotent
   receipts; original-key revocation; room eligibility reset; explicit confirmation;
   independent verification of replacement identities; browser loss/rejoin drills.
2. **Durable conversations — open.** Isolated proof has verified opaque delivery,
   encrypted local history, outbox retry and one-room rejoin. Remaining: multiple
   rooms/DMs per account, bounded room storage, safe Markdown, visible trust changes
   and a defined production unlock/local-data lifecycle.
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
  single-use completion tested on SQLite and PostgreSQL. It grants no reset yet.
- CI green through `30ac61d`; current implementation remains a server scaffold plus
  an isolated MLS experiment, not a deployed encrypted-chat product.

## External evidence still required

Live KyIdentity callback registration and deployed reauthentication policy; an
independent security assessment of the chosen MLS implementation/application profile;
interoperability against an independent implementation; and real supported-browser
and constrained-host measurements. Local tests may advance these gates but cannot
stand in for evidence they did not produce. Continue independent implementation when
one gate needs external input; never hide that gate or silently downgrade encryption.
