# Sub-project 4: Matrix server backups

Date: 2026-10-01. Parent: `2026-10-01-matrix-platform-design.md` (decision 7). Builds on
sub-projects 2 (`2026-10-01-matrix-stack-design.md`) and 3
(`2026-10-01-matrix-offboarding-design.md`).

## Intent

An operator who loses the server rebuilds chat from k-of-n custodian shares: rooms,
memberships and encrypted history (the server never sees plaintext), MAS accounts, the
server name and signing key. Users then restore their message keys from key backup with their
own security keys. Media comes back from the local backup directory. The path is proven end to
end by the acceptance test, not assumed.

## Decisions (owner-approved 2026-10-01)

1. Size: one capsule, split dumps into 64 MiB parts, refuse a run past the library's 256 MiB
   expanded limit with a clear reason, report size and warn at 75%. Never drop data silently.
   Raising the limits or envelope backups is later work if a deployment approaches them.
2. Dumps are taken by the KyMessages app with `pg_dump` and a read-only `kybackup` role on
   `matrix-db`. Cost, recorded: a compromised app can read the Matrix databases (metadata and
   ciphertext) and the Matrix secrets, and, holding the owner passwords in the configs it backs
   up, write both databases and act as Synapse admin. It cannot decrypt E2EE messages. The
   Postgres superuser password is hidden from it and never backed up. No Docker socket.
3. Media: an encrypted incremental mirror on every run plus an encrypted monthly full archive;
   newest 3 archives kept.
4. Restore covers the whole stack (`restore` plus a new `restore-matrix`) and is proven by the
   acceptance test.

## Constraints from the code

- `ky-primitives` v0.8.0 capsule: `MaxFileBytes` 64 MiB, `MaxExpandedBytes` 256 MiB,
  `MaxContainerBytes` 384 MiB, all held in memory (`File.Data []byte`).
- Postgres snapshots do not span databases: the MAS and Synapse dumps are seconds apart. MAS
  is dumped first; MAS re-provisions a user missing from Synapse at sign-in.
- The app image is Alpine with no Postgres client; the app is not on `matrix-db`; `./matrix`
  and `matrix-media` are not mounted into it today.

## Section 1: components

- **Database access (`matrix-init`).** New write-once secret `kybackup_db_password`.
  `postgres/kybackup-role.sql`, rendered by `matrix-init` and idempotent (create if missing, set
  password, grants), creates role `kybackup` (`LOGIN`, `CONNECT` on `synapse` and `mas` only,
  `pg_read_all_data`). It sorts after `init.sql`, so the entrypoint applies it on a fresh
  volume; operators run it once on an existing stack through `docker compose exec -T postgres psql` (README upgrade step). The
  app image gains `postgresql17-client` (major version equal to the server's).
- **Compose (`docker-compose.matrix.yml`, app).** Joins `matrix-db`; the `kybackup` password
  as a Compose secret; `./matrix` read-only at `/matrix` with `/dev/null` masking
  `secrets/postgres_password`; `matrix-media` read-only.
- **Capsule (`internal/backup`).** With Matrix enabled, `Collect` adds `matrix/` config and
  secrets (`secrets/*` except `postgres_password`, `synapse/signing.key`, Synapse/MAS/Element configs, Postgres init SQL)
  and `matrix/dumps/mas.dump.NNN` then `matrix/dumps/synapse.dump.NNN` (`pg_dump -Fc`, split
  into 64 MiB parts). The media data key is `data/media.key` (write-once 0600), so it travels
  with `data/`. Over 256 MiB expanded fails the run with the measured size and the offending
  member; status reports the last capsule size and its share of the limit, warning at 75%. The
  drill also checks the dumps exist and read in full (`pg_restore --file=/dev/null`) and the
  Matrix files exist.
  "People capsule" wording becomes "server capsule".
- **Media (`internal/backup/media`).** Each run copies new or changed files from the media
  volume into `KY_BACKUP_DIR/media/mirror/`, each AES-256-GCM encrypted under the media key
  with its relative path as associated data, plus an encrypted index (path, size, mtime).
  Monthly: `media/full-YYYY-MM.tar` of the encrypted mirror and index (no further crypto);
  newest `KY_BACKUP_MEDIA_FULL_KEEP` (default 3) kept; after a new archive, mirror files for
  media deleted on the server are dropped.
- **Restore.** `kymessages restore` also extracts `matrix/` into the target; `kymessages
  matrix-init` then creates the missing superuser password and keeps every restored secret. New
  `kymessages restore-matrix`, run once via `docker compose run --rm` against a fresh stack:
  refuses unless both databases are empty and, unless `-skip-media`, the media store is empty;
  `pg_restore`s as the `synapse` and `mas` owners (no superuser; the one extension allowed is
  MAS's trusted `pg_trgm`, any other is refused), then decrypts the newest monthly archive and
  then the mirror into the media volume. It refuses and changes nothing on any failed check.
  It runs as `KY_MATRIX_UID`, so the operator restores as that user and, afterwards, chowns
  `./data` and `./backups` to root because the root app refuses key files it does not own.

## Section 2: failure handling and proof

- A dump failure fails the whole run before sealing (audit, last result, next run as today);
  no capsule ships without its data.
- Over the size limit: the run fails naming the size and member; status warns from 75%.
- Media failure mid-run: the capsule still ships; media records its own result and catches up
  next run (incremental); status shows the last media run separately.
- Disk full: the mirror writes to temporary names then renames, never leaving half files.
- Matrix not enabled: the backup is exactly today's.
- Restore into a non-empty stack: refused before writing anything.
- **Tests.** Dump split and rejoin; over-limit refusal and the 75% warning; media encryption
  round trip and a swapped file refused by its path binding; incremental copies only new or
  changed files; monthly archive and pruning; `restore-matrix` refuses a non-empty stack; the
  drill catches a missing or corrupt dump. `matrix-init` (role, secret, upgrade SQL) and the
  compose check (app on `matrix-db`, read-only mounts, `pg_dump` major equals the server's).
- **Acceptance (`scripts/matrix-acceptance.sh`).** Alice and Bob exchange encrypted messages
  and an image and set up key backup; take a server backup (capsule, mirror, a forced monthly
  archive) sealed to a throwaway suite key generated in the harness; `down -v`; rebuild an
  empty stack from the restored `matrix/`; `restore` with throwaway shares on stdin, then
  `restore-matrix`; Alice signs in on a new device, restores keys from key backup and reads
  Bob's earlier message and image; Bob's account and rooms are intact; server name and signing
  key unchanged.

## Out of scope

Raising capsule limits, envelope backups, streaming capsules (ky-primitives work); console
pages (sub-project 5); point-in-time recovery; backing up Element (stateless).

## Risks

- A busy server will pass 256 MiB; the run then fails loudly until later work raises limits.
- Sealing holds the whole capsule in memory (up to ~384 MiB plus dumps).
- The MAS and Synapse dumps are seconds apart; a user created in that window is re-provisioned
  at sign-in, untested beyond the acceptance run.
- The app reads the Matrix databases and secrets (decision 2). The rendered configs it must
  back up hold the `synapse` and `mas` owner passwords and the MAS-Synapse shared secret, so a
  compromised app can write both databases and act as Synapse admin; the read-only `kybackup`
  role does not bound it. It cannot decrypt E2EE messages. It cannot see the Postgres superuser
  password: Compose masks it, the capsule leaves it out, and after a restore `matrix-init`
  creates a new one before Postgres starts.
