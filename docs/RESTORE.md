# KyMessages server restore

KyMessages is not deployed yet. These instructions cover the source-built server's
SQLite capsule restore, which is tested with disposable custodian keys. The MLS
client remains an isolated experiment. No published KyMessages image or production
identity/deployment verification is implied by this runbook. Do not substitute the
upstream `ky-server-base` image: it does not contain this repository's restore policy.

## What recovery can restore

The inherited `ky-primitives/recoveryclient` adapter seals these files:

| File | Contents |
|---|---|
| `data/ky_server.db` | SQLite snapshot: accounts, MFA state, grants, messaging metadata/ciphertext, audits, settings and sealed recovery token |
| `data/encryption.key` | Deployment key needed to open stored MFA secrets and the recovery token; not an MLS message key |
| `data/recovery.pub` | Pinned suite recovery public key, when configured |
| `config/settings.json` | App name, URL, port and database driver for operator reference; not automatically loaded |

Custodians together can open the capsule, including its operational secrets and
metadata. KyRecovery cannot. Browser MLS secrets, local histories and pending sends
are not in the capsule. Losing every browser key still loses access to messages.
Capsules have their own retention and may contain ciphertext older than a room's
live retention window. Preparation prunes expired payloads before the server serves.

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
   go build -o ky_server_base ./cmd/server
   ./ky_server_base restore -capsule ./backup.kycap -to ./restored -service 'Busnes.app'
   ```

   Replace `Busnes.app` with the capsule's exact service name (`KY_APP_NAME` at backup
   time). It is still this scaffold's default. The service check runs before combining
   shares. Paste one `ky2-...` share per line, then Ctrl-D; do not supply them as flags.
   The library verifies the capsule and key binding and refuses a nonempty target.
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

A preparation error says the files are **not ready to serve**. Keep the target
offline, investigate, and repeat restoration into another empty directory. Do not
use partially prepared files. The command never silently falls back to the old grants.

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

All users must sign in freshly. Messaging resumes through **new rooms**, not the
restored rooms. Preserve surviving browser profiles for local history and pending
text; never rewind their ratchets or copy server data into browser vaults. An
unresolved send in a retired room remains unresolved and must not be resent as the
same ciphertext in another room.

In a separate browser profile, enroll a replacement device and perform the confirmed,
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
trip, wrong-service/key/threshold refusals, startup expiry, restored-grant invalidation,
verified-key tombstones, future identity recovery and new-room creation. The store
policy is also tested on PostgreSQL; capsule extraction remains SQLite-only.
`backup-drill` uses a throwaway key, so a successful automated drill does not prove
that the real custodian cards are available. A controlled ceremony restore with the
actual cards, and the deployed identity checks, remain operator acceptance work.
