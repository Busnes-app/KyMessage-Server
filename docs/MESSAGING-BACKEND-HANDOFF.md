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

## Durable-conversation progress

`6b6eabe` derives the wrapping key only during unlock/setup and retains no passphrase
between operations. Lock cancels late unlock completion. `954b10d` adds migration 10's
optional KeyPackage publication room scope: active membership required, immutable
scope, no claims from another room, existing device quotas retained. Its CI passed:
https://github.com/Busnes-app/KyMessage-Server/actions/runs/36360773559

The latest client slice switches rooms using separate encrypted IndexedDB entries
and Web Locks. The legacy first room stays in `device`; subsequent `room:<UUID>`
entries authenticate their entry name as AES-GCM associated data. Up to 100 entries,
2 MiB per entry, existing 256-element list bounds; full storage refuses writes and
never silently deletes history. All entries use the root's non-extractable wrapping
key and salt with fresh IVs. The tab-local selected-entry hint contains no secret.

New rooms generate fresh init/HPKE keys under the same enrolled signing identity.
Publications are room-scoped; retained older unscoped publication retries remain
compatible. Already verified root pins carry their identity generation into new
rooms. Switching preserves unresolved sends and histories; drafts require explicit
confirmation before discard. Lost room-creation acknowledgement recovers by refreshing
and opening the empty owned room; published join material/nonzero epochs cannot be
used to initialize another group.

Final multi-room verification: typecheck/build; 18 manual MLS tests; 29 HTTP/UI tests
plus one intentional duplicate cross-engine skip; 6 OIDC tests, all passed across
Chromium/Firefox. New cases prove two-room history/outbox persistence, reload and
draft handling, lost creation acknowledgement, independent room locks and rejection
of swapped encrypted entries. Scoped package race tests passed on SQLite/PostgreSQL.
The stale device-loss wording assertions from the reset slice were fixed in `b93d89d`.

## Next and constraints

Continue durable conversations with client-side safe Markdown, direct-message UX
and discovery of saved local rooms after server membership is removed. Existing
archived entries are retained but only the last-selected one is currently reachable
without a server-listed membership. Only the selected room receives automatic checks.
Server room listings currently expose one page of 100 in the client. These remain
product work; do not imply a finished encrypted-chat release.

Live delivery, retention/gap recovery, product embedding, installation, restore and
load/security evidence remain open. The unaudited `ts-mls` experiment and private
GroupInfo extension stay out of production. Deployed issuer interaction, independent
protocol review/interop, full supported-browser evidence and restored metadata
rollback remain explicit release gates. Reset is not lost-history recovery.

DOX updated store/API/config/proof/fixture contracts and product/wire documents.
Root already records the full-release/commit preference; auth/SSO/web contracts and
child indexes stay unchanged because their ownership and ordinary login behavior
were not changed. Mirror this checkpoint to myslop before an actual pause.
