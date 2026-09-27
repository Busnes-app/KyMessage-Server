**Repo:** KyMessage-Server
**Worktree:** /home/yoshi/git/busnes.app/KyMessage-Server (branch master)

The user explicitly requested completion of the encrypted-chat first release, with
commits at each completed slice or unavoidable break. Continue the full execution
plan in `docs/FIRST-RELEASE-PLAN.md`; do not stop at the device-recovery subset.
Remote: https://github.com/Busnes-app/KyMessage-Server.git. No deployment occurred.

## Completed checkpoint

Migration 9 and `messaging_reset.go` bind devices, invitations, ownership and reset
requests to identity generations. Confirmed reset atomically advances the generation,
revokes prior devices, retires unclaimed packages, removes memberships and stores an
idempotent original-session receipt. Old ownership is not inherited. Browser pins
and MLS metadata use generation-sensitive v2 bindings; accepted legacy v1 events
retain their original hash profile. This is an experimental protocol transition.

The recovery API seals explicit reset intent and requires freshly signed suite
reauthentication. `KY_MESSAGING_IDENTITY_RESET_ENABLED` defaults false and is enforced
both at initiation and completion. Authentication-only completion is never reset
authority. A completed reset callback replays its receipt without exchanging the
spent code or mutating again. HTML success returns only to `/`; no new session is
issued. The OIDC fixture enables the gate with disposable accounts, not production
identity assurance.

The prototype confirms lost access/ownership/history, locks before issuer navigation,
and unlocks the existing replacement vault afterward. Old readable local history
remains on revoked browsers. Remaining room members see a server-reported identity
change until the removal commit; a reinvited replacement requires independent
fingerprint approval and receives future messages only. Later device enrollment
uses the new generation immediately in its local self pin.

## Verification

- Full race suites passed for store, API, SSO and config.
- Messaging race suites passed on disposable PostgreSQL 17; fixture container removed.
- Go vet passed for changed backend and build-tagged fixture packages.
- Proof typecheck/build passed. Chromium/Firefox OIDC suite passed before the final
  added generation-2 enrollment scenario; that expanded reset scenario also passed
  on both browsers after correcting its selector and respecting other-device trust.
- Earlier complete delivery suite: 25 passed, one intentional duplicate-engine skip.
- CI for `ad21d59` passed: https://github.com/Busnes-app/KyMessage-Server/actions/runs/36359795108

## Next and constraints

Continue durable conversations: unwrap local encryption once per unlock without
retaining the passphrase, then separate room records/ratchets and support room
switching. Account-wide KeyPackage allocation currently assumes one room per browser;
solve package ownership/claim routing before splitting local room state. Never reuse
published init keys to create a new group. Preserve unresolved outboxes and history.

Live delivery, retention/gap recovery, product embedding, installation, restore and
load/security evidence remain open. The unaudited `ts-mls` experiment and private
GroupInfo extension stay out of production. Deployed issuer interaction, independent
protocol review/interop, full supported-browser evidence and restored metadata
rollback remain explicit release gates. Reset is not lost-history recovery.

DOX updated store/API/config/proof/fixture contracts and product/wire documents.
Root already records the full-release/commit preference; auth/SSO/web contracts and
child indexes stay unchanged because their ownership and ordinary login behavior
were not changed. Mirror this checkpoint to myslop before an actual pause.
