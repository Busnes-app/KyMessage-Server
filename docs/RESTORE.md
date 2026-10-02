# KyMessages server restore

KyMessages is not deployed yet. These instructions cover the source-built server's
SQLite capsule restore and the Matrix stack restore, which is tested with disposable custodian keys. No published
KyMessages image or production identity/deployment verification is implied by this
runbook. Do not substitute the upstream `ky-server-base` image: it does not contain
this repository's restore policy.

## What recovery can restore

The inherited `ky-primitives/recoveryclient` adapter seals the server capsule:

| File | Contents |
|---|---|
| `data/ky_server.db` | SQLite snapshot: accounts, MFA state, settings, audits and sealed recovery token |
| `data/encryption.key` | Deployment key needed to open stored MFA secrets and the recovery token |
| `data/recovery.pub` | Pinned suite recovery public key, when configured |
| `config/settings.json` | App name, URL, port and database driver for operator reference; not automatically loaded |
| `data/media.key` | With Matrix: the key that opens the media mirror |
| `matrix/` | With Matrix: secrets, Synapse signing key, Synapse/MAS/Element configs, Postgres init SQL |
| `matrix/dumps/{mas,synapse}.dump.NNN` | With Matrix: `pg_dump` parts, MAS first, seconds apart. Synapse's one-time keys are excluded, as Synapse's backup guide says |

Media is not in the capsule. It lives in `KY_BACKUP_DIR/media`: an encrypted mirror plus
`full-YYYY-MM.tar` archives. Copy that directory off the host together with the capsules;
only the capsule's `data/media.key` opens it.

Custodians together can open the capsule, including its operational secrets and
metadata. KyRecovery cannot. The expanded limit is 256 MiB (the library's cap, counted
with tar framing; the product reserves 1 MiB, see Busnes-app/ky-primitives#20). A larger
payload fails the run with its size, and the backup screen warns from 75%. Allow about
2 GiB of memory for the app near the limit (an estimate, not measured). Initial snapshot
scratch space still needs room for the complete live database.

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
   Record each capsule ID when it is deposited (`kyrecovery_last_deposit`, audit
   action `admin.backup_run`). After any refused attempt, restore into a fresh empty
   directory.
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
   The target's parent must already exist; symlinks in the parent path are resolved and
   the real path is used. Because another user could otherwise swap the target
   mid-restore, the command checks the whole path up to `/`: an existing target must be
   a real directory you own, every ancestor must be owned by you or root, and none may be
   writable by group or others, except a root-owned sticky directory such as `/tmp`. A
   nonempty target is refused before anything is read and is left untouched. A target
   that appears between the check and its creation is refused; retry. The opened
   directory is checked again against the same rule before extraction.
4. Compare the printed authenticated manifest's capsule ID, service, creation time,
   recovery key ID and payload hash with your trusted records. Payload hash and the
   downloaded container's SHA-256 are different checks. Preserve the receipt.

The product then requires a nonempty regular SQLite file and a valid 32-byte
hexadecimal deployment key. It opens/migrates the offline snapshot and, in one
transaction, deletes sessions, MFA challenges and device pairings and records
`restore.grants_invalidated`. It closes the store before reporting success.

If extraction fails, the library rolls it back. If preparation fails, or the target
path no longer names the directory extraction started in, the extracted files are
removed through a handle to that directory, never by path, and the command reports the failure; nothing is left to serve. Repeat restoration into an empty directory. The
command never silently falls back to the old grants.

## Restore the Matrix stack

Only on a new stack, and only for a capsule sealed with Matrix enabled. Matrix is proven
end to end by `make matrix-acceptance`: Alice read Bob's earlier message and image after
restoring her keys from key backup; the server name and signing key were unchanged.

1. Restore as the unprivileged user that owns `./matrix` (`KY_MATRIX_UID`, the user who ran
   `matrix-init`), as above. Move `restored/data` to `./data` and `restored/matrix` to
   `./matrix`. Copy the backup directory (with `media/`) to `./backups`; `./backups/media`
   must be readable by `KY_MATRIX_UID`, because the app writes it as root, owner-only.
2. Set `.env` as before, with `KY_MATRIX_UID` and `KY_MATRIX_GID` for that user. Build the
   image (build overlay).
3. Only if this stack is meant to be replaced: `docker compose down -v`, which deletes its
   Matrix database and media. Then `docker compose up -d postgres`, nothing else.
4. `docker compose run --rm restore-matrix`. Add `-skip-media` only if no media backup
   survived; uploads from before the backup are then missing. It runs as `KY_MATRIX_UID`
   with no capabilities and restores as the database owners, with no superuser.
5. In the deployment directory, `sudo chown -R root:root ./data ./backups`: the app runs as
   root and refuses key files it does not own. Then `docker compose up -d`.
6. Delete `./matrix/dumps`.
7. Members sign in on a new device and restore message keys from key backup with their own
   recovery key. KyRecovery cannot do this for them.

`restore-matrix` refuses, before writing anything, unless both databases are empty, a dump
part is missing or truncated, a dump creates any extension other than MAS's trusted
`pg_trgm`, and (unless `-skip-media`) the media store is empty and every media file opens
at its path. Every dump is read in full first. A failure after those checks leaves a
partial stack: remove it (`docker compose down -v`, which deletes the Matrix database and
media) and start again.

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

All users must sign in freshly; sessions, MFA challenges and device pairings from
before the backup are gone.

Review changes after the capsule's timestamp using surviving audit logs and identity
records: disabled accounts, passwords, MFA enrollment and SCIM state may be stale.
If compromise caused the incident, resolve those credentials and permissions before
reopening access. Automatic grant invalidation does not make stale account data current.
Check backup destinations, schedule, public-key fingerprint and last receipt after
recovery, then create a fresh sealed backup. Remove scratch plaintext restore copies
according to the operator's storage policy; deletion is not a physical-erasure guarantee.

## Verification

`go test -race ./cmd/server ./internal/store ./internal/backup/...` includes a real 2-of-3 capsule round
trip, wrong-service/key/threshold refusals, restored-grant invalidation and removal of
the extracted files when preparation fails. The store policy is also tested on
PostgreSQL; capsule extraction remains SQLite-only.
`backup-drill` uses a throwaway key, so a successful automated drill does not prove
that the real custodian cards are available. A controlled ceremony restore with the
actual cards, and the deployed identity checks, remain operator acceptance work.
The Matrix dump split, size limit, media mirror and the `restore-matrix` refusals are
covered by `go test`. `make matrix-acceptance` proves the full cycle: back up, lose the
host, restore from custodian shares on stdin, then read old history.
