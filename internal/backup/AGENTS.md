# Backup

## Purpose
Adapts the scaffold to `github.com/Busnes-app/ky-primitives/recoveryclient`, which owns the
KyRecovery pairing, sealing, deposit, restore and drill contract. This package supplies only
what differs per product: a `Settings` adapter over `store.SettingsStore`, a `Sealer` under the
deployment key, the payload the scaffold seals (`Collect`) and its drill checks (`Checks`).

## Ownership
Owns the settings adapter (`settings.go`), payload collection (`payload.go`), Matrix dumps and
config collection plus expanded-size accounting (`matrix.go`, `size.go`), loading a restored
Matrix half into a fresh stack (`matrix_restore.go`),
restore-drill checks (`drill.go`), serialized drill entry point (`run_drill.go`). It holds no private key, no share, and no pairing state of its own — those
live in `recoveryclient` and in the settings rows it reads and writes through the adapter.

## Local Contracts
- `Settings` maps `store.ErrNotFound` to `recoveryclient.ErrNotFound`; every other error passes
  through unchanged.
- `NewSealer` seals the KyRecovery token under the deployment key with label
  `ky_server_base:setting:kyrecovery_token`, domain-separated so a row copied from another
  setting will not open.
- `Collect` snapshots SQLite with the lib's `SQLiteSnapshot` (`VACUUM INTO`; the store runs in
  WAL mode, so a plain file read misses uncheckpointed commits) and returns
  `ErrNoDatabaseSnapshot` for any other driver, so a capsule without a consistent database is
  never sealed. The server capsule is the whole application database, compacted (`VACUUM`) on the
  owned snapshot and rejected above the shared capsule file limit before reading it into memory. Metadata/receipt/audit growth remains capped at 64 MiB;
  initial snapshot disk space still scales with the complete live database. It also carries the encryption key (`data/encryption.key`, required — restores
  a database whose MFA secrets are gone otherwise) and the pinned recovery public key
  (`data/recovery.pub`, only when paired).
- With Matrix enabled `Collect` adds `data/media.key` (write-once, `MediaKeyPath`), `matrix/<sub>/<file>`
  for secrets, synapse, mas, element and postgres (dot-files skipped; never
  `secrets/postgres_password`, the superuser's: a fresh volume needs none and `matrix-init`
  regenerates it), and `pg_dump --format=custom`
  parts `matrix/dumps/{mas,synapse}.dump.NNN` (MAS first, 64 MiB parts) run as `kybackup`: the child
  gets only `PGPASSWORD` and `PGCONNECT_TIMEOUT`. The Synapse dump excludes `e2e_one_time_keys_json`
  data. A dump failure fails the run. Recipe key `pg_dumps` lists the bases; `Checks` requires
  gapless parts, the `matrixRequired` members (media key, signing key, Synapse and MAS configs,
  `init.sql`, `kybackup-role.sql`) and reads each joined dump in full (`pg_restore
  --file=/dev/null`, `DumpCheckTimeout` each).
- The expanded limit counts tar framing (`Measure`) and holds back 1 MiB, because ky-primitives
  v0.8.0 `Seal` undercounts against `Open` (Busnes-app/ky-primitives#20). A payload past it fails
  with `*SizeError` (wraps `capsule.ErrCapsuleTooLarge`; its message rounds the size up and
  names the enforced 255 MiB) before sealing. `CollectForRun` records
  the measured size in `backup_last_expanded_bytes`; `LastSize` warns from 75% of 256 MiB.
- `MatrixRestore.Run` (`kymessages restore-matrix`) checks everything before writing: both
  owner passwords from `matrix/secrets`, gapless parts with no stray, `pg_restore --list` per
  dump refusing every extension entry except MAS's trusted `pg_trgm` (`EXTENSION - pg_trgm` and
  `COMMENT - EXTENSION pg_trgm`, which the owner restores; no superuser),
  a full read of each dump (`pg_restore --file=/dev/null`; `--list` misses a truncated or
  missing last part),
  zero user relations in `mas` and `synapse` (`CountRelations`, host or host:port), and unless
  `SkipMedia` an empty media store owned by the process's uid:gid, the media key
  (`keyfile.Load`, so the process must own it) and every media file (`media.Restore` with
  write false). Then `pg_restore` as each owner, MAS first, `--no-owner --no-privileges
  --single-transaction --exit-on-error`, child env only `PGPASSWORD` and `PGCONNECT_TIMEOUT`;
  then media. No owner password reaches argv or an error, and tool stderr in errors drops
  CONTEXT/DETAIL lines (row data). A refusal on a populated stack names what `down -v` deletes.
- `Checks(dir, opened)` reads the opened capsule's manifest, normalizes JSON lists and
  fails malformed or incomplete recipes. Required files include all capsule members and
  the database, settings and encryption key; SQLite integrity and required environment
  checks cannot be disabled. File checks accept only clean relative manifest members;
  SQLite opens read-only and missing/empty databases fail.
- HTTP and CLI call `RunDrill` with `Checks`; it holds an OS advisory lock on `<data dir>/drill.lock`
  across scratch preparation and the library drill. Contention returns `ErrDrillBusy`;
  closing the descriptor or process exit releases ownership. Keep the lock file in place.
  The Unix lock matches the Linux container deployment.
- `DrillRoot` is under the data directory and forced to 0700; opened payloads stay in the
  library's private, disposable subdirectories. Drills use throwaway keys, not custodian shares.
- `Members` names what `Collect` would seal now, for the status route and the screen; keep
  the two in step.
- `GET /api/settings`'s `extra_settings` never carries `kyrecovery_token_enc` or a legacy
  plaintext `kyrecovery_token`; the filter drops every key with the `kyrecovery_token`
  prefix, so a rename in the lib cannot leak. An admin sees the paired `kyrecovery_url`,
  never the credential.
- Pairing, the write-once key pin, `Run` (one seal, every destination), the schedule, local
  copies and their pruning, drill mechanics, restore and the decrypt guard are the lib's;
  their contracts are in the `recoveryclient` README. `client_test.go` pins only what this
  package's wiring buys: `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` admits RFC1918 and CGNAT and
  nothing else.

## Verification
- The decrypt guard allows only `restore` in `cmd/server/restore.go` to invoke
  suite-key capsule opening. HTTP/scheduled product code never receives shares.
- `go test -v ./internal/backup/...` covers decoded seal/open checks, malformed recipes,
  subprocess lock contention/exit, scratch cleanup and the synthetic v0.5.0 pairing fixture
  in `testdata/pairing-v050.json`. The fixture uses a 32-byte 0x01 deployment key and retains
  no recovery private key.

## Child DOX Index
- [media/AGENTS.md](media/AGENTS.md): encrypted media mirror and monthly archives.
