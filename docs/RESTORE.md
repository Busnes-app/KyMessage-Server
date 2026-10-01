# KyMessages server restore

KyMessages is not deployed yet. These instructions cover the source-built server's
two-step SQLite capsule restore (people, then optionally messages), which is tested with disposable custodian keys. The MLS
client remains an isolated experiment. No published KyMessages image or production
identity/deployment verification is implied by this runbook. Do not substitute the
upstream `ky-server-base` image: it does not contain this repository's restore policy.

## What recovery can restore

The inherited `ky-primitives/recoveryclient` adapter seals these files:

| File | Contents |
|---|---|
| `data/ky_server.db` | SQLite snapshot: accounts, MFA state, settings, non-messaging audits and sealed recovery token; no messaging data |
| `data/encryption.key` | Deployment key needed to open stored MFA secrets and the recovery token; not an MLS message key |
| `data/recovery.pub` | Pinned suite recovery public key, when configured |
| `config/settings.json` | App name, URL, port and database driver for operator reference; not automatically loaded |

Custodians together can open the capsule, including its operational secrets and
metadata. KyRecovery cannot. Browser MLS secrets, local histories and pending sends
are not in the capsule. Losing every browser key still loses access to messages.
New capsules empty every messaging table (rooms, members, devices, events, Welcomes,
KeyPackages, identities) and drop `messaging.*` audit rows from the private
snapshot; live data is unchanged. Older capsules may still contain messaging rows and
ciphertext beyond room retention. Preparation still prunes expired payloads before
serving. The compacted snapshot has a 64 MiB limit; an oversized snapshot fails backup
explicitly. Initial snapshot scratch space still needs room
for the complete live database.

The opt-in messages capsule is separate: `data/messages/accounts.db` (messaging
identities, approved and revoked devices without their bearer tokens or enrollment
state, rooms, members and epoch devices) and `data/messages/events-NNN.db` parts
(events and Welcomes). It has no deployment key, people database, KeyPackages,
recovery-authentication requests or reset receipts.

Only SQLite capsule backup/restore is supported here. The collector refuses
PostgreSQL because it cannot produce its consistent snapshot. PostgreSQL store
tests do not establish a supported PostgreSQL disaster-recovery procedure. Raw
SQLite copies and database rollbacks bypass the preparation below and are unsupported.

## Restore offline

1. Obtain the intended capsule from KyRecovery using an operator session, or from
   the configured local backup directory. Product deposit tokens cannot download it.
   Record the trusted receipt's capsule ID, creation time and SHA-256 digest. Compare
   `sha256sum backup.kycap` with that receipt before restoration; an intact old capsule
   is not evidence of freshness. Know the exact service name used when it was sealed.
2. Obtain the ceremony's threshold number of custodian cards. Use a private terminal;
   do not paste shares into chat, argv, environment variables or shell history. The
   command reads shares from stdin. Never change or regenerate the suite recovery key
   to make a capsule open.
3. Build the intended checkout and restore into a new or empty directory:

   ```sh
   go build -o kymessages ./cmd/server
   ./kymessages restore -capsule ./backup.kycap -to ./restored -service 'KyMessages'
   ```

   Replace `KyMessages` with the capsule's exact service name (`KY_APP_NAME` at backup
   time). The current default is `KyMessages`; older test capsules may use `Busnes.app`. The service check runs before combining
   shares. Paste one `ky2-...` share per line, then Ctrl-D; do not supply them as flags.
   The library verifies the capsule and key binding and refuses a nonempty target.
   A messages capsule is refused here and its decrypted files removed; restore the
   people capsule first, then use `restore-messages`.
4. Compare the printed authenticated manifest's capsule ID, service, creation time,
   recovery key ID and payload hash with your trusted records. Payload hash and the
   downloaded container's SHA-256 are different checks. Preserve the receipt.

The product then requires a nonempty regular SQLite file and a valid 32-byte
hexadecimal deployment key. It opens/migrates the offline snapshot, prunes expired
ciphertext and prepares restored authority in a transaction:

- Delete sessions, MFA challenges, device pairings, messaging recovery-authentication
  requests and identity-reset receipts.
- Revoke every messaging device and its delivery token; retain verified-key
  tombstones so replacements cannot silently gain first-device approval.
- Expire KeyPackages and remove room memberships. Permanently retire restored room
  ownership, even if a later reset reaches an identity generation used before restore.
- Record `restore.grants_invalidated`. Close the store before reporting success.

A current people capsule holds no messaging rows, so the revocation and retirement
steps act only on capsules sealed before the split. `restore-messages` imports after
this preparation.

A preparation error says the files are **not ready to serve**. Keep the target
offline, investigate, and repeat restoration into another empty directory. Do not
use partially prepared files. The command never silently falls back to the old grants.

## Restore messages (optional)

Threads come back only from a messages capsule, imported into the prepared people
restore before it serves. Skip this section to return with no threads.

1. Obtain the messages capsule and check its receipt as in step 1 above. It may be
   older or newer than the people capsule; anything that no longer references a
   restored person is dropped and counted.
2. With the target still offline, run:

   ```sh
   ./kymessages restore-messages -capsule ./messages.kycap -into ./restored -service 'KyMessages'
   ```

   Shares are read from stdin exactly as for `restore`. The command requires
   `restored/data/ky_server.db` and refuses a target that already has messaging rooms,
   devices or identities, before asking the library to open anything. It opens the
   capsule into a private `restored/messages-*` directory, refuses anything that is
   not a messages capsule (no `data/messages/accounts.db`, or a people database, more
   than 8 event parts, unexpected member names, symlinks), migrates the people
   database and imports in one transaction. The opened directory is removed on
   success, on failure, and on Ctrl-C or SIGTERM, which roll the import back. If the
   process was killed hard (SIGKILL, power loss), delete any leftover
   `restored/messages-*` directory: it holds decrypted ciphertext and metadata.
3. Compare the printed manifest with your records, and keep the printed counts. The
   import is audited as `restore.messages_imported` with the same counts.

The import leaves the database as if every person missing from the people restore
had been deleted: their identities, devices and memberships are dropped, and a room
whose owner is missing is not imported at all, with its events (counted as
`retired_rooms`). Epoch-device and Welcome rows that name a device which was not
imported stay, as account deletion would leave them. Room epoch, sequence and
retained floor are kept; stored byte counts are recomputed. Messages past each
room's retention are purged when the server next starts. KeyPackages,
recovery-authentication requests, reset receipts and sessions are not imported.

Every imported approved device is **suspended**: it keeps its status and identity
generation but has no delivery token, so it cannot read, send or approve. Revoked
devices stay revoked. Before reopening access, an admin reviews the restored devices
on the dashboard's **Suspended devices** list (`GET
/api/admin/messaging/devices?status=suspended`) with their owners and revokes any
that are lost or unrecognized (`POST /api/admin/messaging/devices/{device}/revoke`,
which needs a sign-in from the last 10 minutes and works only on suspended devices).
An owner resumes a device after a suite sign-in from the last 10 minutes: `POST
/api/messaging/devices/{device}/resume` with a new `token_hash`, then `POST
/api/messaging/devices/{device}/resume/verify` with the device key's signature over
the returned challenge. The new token works only after the signature verifies; see
`docs/MESSAGING-API.md`. A device from before an identity reset cannot resume. A thread resumes for a device whose MLS state matches the
restored epoch. A thread that advanced after the snapshot stays paused for clients
that are ahead of it; the server never rewinds client state, so those members start
a new thread.

A failed import rolls back and leaves the people restore unchanged. Rerunning
`restore-messages` on a target it already imported into is refused and changes
nothing.

## Return to service

Do not run old and restored servers simultaneously behind the same origin. Stop the
old process before cutover; preserve its data directory and logs separately for
investigation. Never copy a snapshot over a running SQLite file or combine it with
old `-wal`/`-shm` files. Use the complete prepared directory.

Restore the deployment configuration deliberately: the same intended app/service
name, HTTPS public URL and identity callback registration. Review the reference
`config/settings.json`; it is not an automatically applied configuration file.
Use the restored `data/encryption.key`. Remove a stale `KY_ENCRYPTION_KEY` override
rather than generating a replacement key: changing it makes the restored encrypted
settings unreadable. Keep restored files private (0600, directories 0700). The
recovery public-key pin remains write-once; compare its fingerprint with ceremony
records. Token revocation at KyRecovery still applies to any restored pairing token.

This repository's production HTTPS/KyIdentity deployment gate remains open. Verify
that deployment separately before allowing real teams onto it.

All users must sign in freshly. A people restore alone leaves no threads or
messaging devices; without a messages import, messaging resumes through **new
rooms**. Preserve surviving browser profiles for local history and pending
text; never rewind their ratchets or copy server data into browser vaults. An
unresolved send in a retired room remains unresolved and must not be resent as the
same ciphertext in another room.

Without a messages import, or for a device that was not restored, enroll a
replacement device in a separate browser profile and perform the confirmed,
freshly authenticated identity-reset flow. This requires the operator-enabled
`KY_MESSAGING_IDENTITY_RESET_ENABLED` gate and verified KyIdentity reauthentication
policy. Create new rooms, invite members and independently verify replacement
fingerprints. Existing local history can still be read offline until local retention
or explicit clearing removes it. Restoration cannot recreate lost browser keys.

Review changes after the capsule's timestamp using surviving audit logs and identity
records: disabled accounts, passwords, MFA enrollment and SCIM state may be stale.
If compromise caused the incident, resolve those credentials and permissions before
reopening access. Automatic grant invalidation does not make stale account data current.
Check backup destinations, schedule, public-key fingerprint and last receipt after
recovery, then create a fresh sealed backup. Remove scratch plaintext restore copies
according to the operator's storage policy; deletion is not a physical-erasure guarantee.

## Verification

`go test -race ./cmd/server ./internal/store` includes a real 2-of-3 capsule round
trip, a people restore followed by `restore-messages` with a repeat refused,
wrong-service/key/threshold refusals, startup expiry, restored-grant invalidation,
verified-key tombstones, future identity recovery and new-room creation. The store
policy is also tested on PostgreSQL; capsule extraction remains SQLite-only.
`go test ./internal/backup` covers the import: dropped people, a room without its
owner, suspended devices, and refusals (too many or misnamed parts, symlinks, a
foreign-key failure) that write nothing.
`scripts/restore-messages-rehearsal.sh` repeats the whole path with the built binary
in disposable directories: API seeding, `export-capsule`, `deposit -messages` to a
local directory, `restore` and `restore-messages` with throwaway shares on stdin, and
a restored server that reports the device `suspended` and refuses its old token.
`backup-drill` uses a throwaway key, so a successful automated drill does not prove
that the real custodian cards are available. A controlled ceremony restore with the
actual cards, and the deployed identity checks, remain operator acceptance work.
