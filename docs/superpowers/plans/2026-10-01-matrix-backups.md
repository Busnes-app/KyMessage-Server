# Matrix Server Backups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator who loses the server rebuilds chat (rooms, memberships, encrypted history, MAS accounts, server name, signing key, media) from k-of-n custodian shares plus the local backup directory, proven end to end by the acceptance test.

**Architecture:** With Matrix enabled, `backup.Collect` adds matrix-init's files and `pg_dump -Fc` output of MAS then Synapse (taken by the app as a read-only `kybackup` role on `matrix-db`), split into 64 MiB members and refused past the capsule's expanded limit. A new `internal/backup/media` package keeps an AES-256-GCM mirror of Synapse's local media in `KY_BACKUP_DIR/media` with monthly tar archives; the scheduler and `deposit` run it after each capsule. `restore` already extracts `matrix/`; a new `restore-matrix` command, run as a profiled Compose service against a fresh stack, `pg_restore`s as the database owners and writes media back.

**Tech Stack:** Go 1.26 (stdlib `crypto/cipher`, `archive/tar`, `os.Root`, `os/exec`), `ky-primitives` v0.8.0 (`capsule`, `recoveryclient`, `keyfile`, `recoverykey`), pgx v5 stdlib driver, Postgres 17 client tools (Alpine `postgresql17-client`), Docker Compose profiles, bash + Playwright acceptance harness.

**Spec:** `docs/superpowers/specs/2026-10-01-matrix-backups-design.md` (parent: `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`, decision 7).

## Open questions for the controller

Each item is a place where the real code makes a spec point impossible, unsafe or ambiguous. The plan below implements the recommendation; reject one and the named task changes.

1. **`postgres/backup-role.sql` would break every fresh stack.** `./matrix/postgres` is mounted as `/docker-entrypoint-initdb.d` (`docker-compose.matrix.yml:48`), and the entrypoint runs every `*.sql` there in name order with `ON_ERROR_STOP`. `backup-role.sql` sorts before `init.sql`, so on a new volume it would `GRANT CONNECT ON DATABASE synapse` before that database exists, and init would abort. **Recommendation:** render one idempotent file, `postgres/kybackup-role.sql`. It sorts after `init.sql`, so the entrypoint applies it on fresh volumes, and the operator runs the same file once on an existing stack. `init.sql` stays unchanged. This deviates from "init.sql creates the role" only in which file creates it (Task 1).
2. **ky-primitives v0.8.0 seals payloads that it then refuses to open.** `capsule.Seal` counts only content against `MaxExpandedBytes`. `capsule.Open` counts the whole tar stream: a 512-byte header per member, content padded to 512, the file list, and a 1 KiB trailer. Verified 2026-10-01 with a scratch test: four members of `MaxFileBytes-512` sealed, and `Open` failed with `expanded past 268435456 bytes`. **Recommendation:** the product budget counts framing (`memberBytes`) and holds back 1 MiB for the list and trailer (Task 3). File a ky-primitives fix separately. Status reports the share of the full 256 MiB.
3. **Whether `pg_restore` needs a superuser.** It does not if the dumps contain no `EXTENSION` entries and ownership and ACLs are skipped. `restore-matrix` connects as the owners (`synapse`, `mas`, passwords from the restored `matrix/secrets`) with `--no-owner --no-privileges --single-transaction --exit-on-error`. It refuses a dump whose `pg_restore --list` shows an `EXTENSION`. This is **unproven** for MAS 1.26 and Synapse 1.162 until the Task 7 acceptance run. If it fails there, stop and ask; do not fall back to the `postgres` superuser silently.
4. **How `restore-matrix` writes the media volume.** The app keeps `matrix-media` read-only. A separate `restore-matrix` Compose service under `profiles: [restore]` mounts it read-write. It mounts `./data`, `./backups` and `./matrix` read-only, joins only `matrix-db`, and has `cap_drop: ALL` with `cap_add: [CHOWN, DAC_OVERRIDE]`. It writes files as the owner of `./matrix` (that owner is `KY_MATRIX_UID`). `docker compose run --rm restore-matrix` starts only `postgres` (Task 6).
5. **How the monthly schedule is tracked.** No setting row. A run archives when `media/full-YYYY-MM.tar` for the current UTC month does not exist, so the filesystem is the state and a crash re-runs safely. The first media run in any month makes the archive, which is the acceptance test's "forced" archive (Task 4).
6. **One-time keys and media scope (the spec is silent; Synapse's backup guide is explicit).** `e2e_one_time_keys_json` must not be restored, or reissued keys break decryption. The Synapse dump therefore uses `--exclude-table-data=e2e_one_time_keys_json`. Only `local_content/` and `local_thumbnails/` are mirrored; remote media and URL previews are caches, and federation is off. Source: https://github.com/element-hq/synapse/blob/develop/docs/usage/administration/backups.md (Tasks 3, 4).
7. **Memory headroom (estimate, unproven).** At the limit, sealing holds the members (~255 MiB), the tar.gz (~255 MiB, since dumps are already compressed), the ciphertext (~255 MiB), base64 (~341 MiB) and the JSON container (~341 MiB). That is roughly 1.5 GiB at peak, and the drill adds an `Open`. **Recommendation:** document "allow 2 GiB for the app near the limit" in RESTORE.md and README, set no Compose memory limit, and treat the 75% warning as the signal. The plan adds no test.
8. **Media is not mirrored by the HTTP "Run now" route.** A first mirror can take longer than the route's write budget. Media runs after the scheduled capsule and after `kymessages deposit`. Status shows its last result separately (Task 5).
9. **Shutdown budget.** Dumps add up to `backup.DumpTimeout` (3 min) before sealing. `backupWaitTimeout` goes from 17m to 20m and `stop_grace_period` from 20m to 21m. `TestComposeGracePeriodCoversTheShutdownBudget` keeps them in step (Task 5).
10. **The acceptance cannot literally `down -v`.** That would also delete the throwaway KyIdentity's volume. The harness copies the backup directory out, then removes the app and Matrix containers and their volumes (`matrix-postgres`, `matrix-media`, `app-data`, `app-backups`) and keeps KyIdentity. That models losing the KyMessages host (Task 7).
11. **Restored Element config mode.** Capsules clamp every member to owner-only, but Element's nginx reads `element/config.json` as another user. `restore` chmods that one file back to 0644 (Task 6).
12. **Fail closed at startup.** With Matrix enabled, `KY_MATRIX_DIR`, `KY_MATRIX_MEDIA_DIR` and `KY_MATRIX_BACKUP_DB_PASSWORD_FILE` are required, because a backup that silently omitted chat would be worse than a refusal. The Compose overlay sets all three (Task 2).

## Global Constraints

- One capsule. Dumps are split into members of `capsule.MaxFileBytes` (64 MiB) named `matrix/dumps/mas.dump.NNN` then `matrix/dumps/synapse.dump.NNN`, from `.000`, MAS first.
- A run past the expanded limit fails before sealing, and its error names the measured size and the member that crossed. Status reports the last size and its share of `capsule.MaxExpandedBytes` (256 MiB), and warns at 75%. Never drop data silently.
- Dumps: `pg_dump --format=custom` as role `kybackup`, which has `LOGIN`, `CONNECT` on `synapse` and `mas` only, and `pg_read_all_data`. The password is the write-once secret `kybackup_db_password` and is never in argv or logs. The child process gets only `PGPASSWORD` and `PGCONNECT_TIMEOUT`.
- A dump failure fails the whole run before sealing. No capsule ships without its data.
- Matrix not enabled: the capsule is exactly today's (no `matrix/`, no `data/media.key`, no media run).
- Media: AES-256-GCM, a 12-byte random nonce prefix, and associated data `"kymessages-media/v1\x00" + <relative path>`. The index (`mirror/index`, JSON `{path: {size, mtime}}`) uses associated data `"kymessages-media-index/v1"`. The key is `data/media.key`: 32 bytes, hex, write-once, 0600, created by `Collect` and sealed in the capsule.
- Media writes go to a temporary name (`.<name>.tmp-*`) and are renamed into place. Monthly archives `full-YYYY-MM.tar` (UTC) hold the encrypted index first, then `mirror/<path>`. The newest `KY_BACKUP_MEDIA_FULL_KEEP` (default 3, at least 1) are kept. After a new archive, mirror files for media deleted on the server are pruned, with the index rewritten first.
- A media failure never fails the capsule run. Media has its own audit action, `admin.backup_media`.
- `restore-matrix` refuses unless both databases hold no user relations and the media store is empty. It checks every dump and every media file before writing anything.
- The app image installs `postgresql17-client`, whose major version must equal the `postgres:17.x` service (the compose check enforces this).
- Never log or audit a password, a key or file contents. Errors may name paths.
- "People capsule" wording becomes "server capsule" in code and docs.
- AGPL rule: Synapse, MAS and Element are touched only through files, SQL and their CLIs and APIs.
- Commits end with a blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **A dump whose size is an exact multiple of 64 MiB, or 0 bytes with exit 0.** Expected: no empty trailing part, and an empty dump is an error, not a capsule. Tested in Task 3 (`TestCollectSplitsAtExactBoundaries`).
2. **`pg_dump` dies mid-stream after writing some parts (server restart, auth failure).** Expected: the run fails naming the database and pg_dump's stderr, and nothing is sealed. Tested in Task 3 (`TestCollectFailsWhenADumpFails`).
3. **A media file deleted by Synapse between scan and copy.** Expected: skipped as gone, and the run succeeds; not a failure every night. Tested in Task 4 (`TestRunSkipsAFileDeletedMidRun`).
4. **The mirror opened with a different media key (a restored `data/` from another server, or a hand-made key).** Expected: the run refuses with "index does not open under the media key" and never overwrites the mirror under a new key. Tested in Task 4 (`TestRunRefusesAMirrorUnderAnotherKey`).
5. **Restore with a stray dump part (`.002` without `.001`) or a non-empty media volume.** Expected: refused before any `pg_restore` or media write. Tested in Task 6 (`TestMatrixRestoreRefusesBeforeWriting`).

---

### Task 1: `matrix-init` writes the kybackup secret and role SQL

**Files:**
- Modify: `internal/matrixinit/matrixinit.go:161-177` (`secretSpecs`), `:225-246` (render list)
- Create: `internal/matrixinit/templates/kybackup-role.sql.tmpl`
- Modify: `cmd/server/matrixinit.go:66-68`
- Test: `internal/matrixinit/matrixinit_test.go`, `cmd/server/matrixinit_test.go`

**Interfaces:**
- Produces: secret `secrets/kybackup_db_password` (hex, write-once, 0600) and rendered `postgres/kybackup-role.sql` (0600, idempotent). Task 2 mounts the secret as the Compose secret `kybackup_db_password`. Task 7 runs the SQL file twice.

- [ ] **Step 1: Write the failing tests.** In `internal/matrixinit/matrixinit_test.go`, append:

```go
// The backup role is created by its own idempotent file, which must sort after init.sql: the
// Postgres entrypoint runs /docker-entrypoint-initdb.d/*.sql in name order and stops at the
// first error, and the role's grants name databases init.sql creates.
func TestBackupRoleSQL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Rendered, "postgres/kybackup-role.sql") {
		t.Fatalf("rendered %v", res.Rendered)
	}
	if !slices.Contains(res.Created, "secrets/kybackup_db_password") {
		t.Fatalf("created %v", res.Created)
	}
	if !("kybackup-role.sql" > "init.sql") {
		t.Fatal("kybackup-role.sql would run before init.sql")
	}
	pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "kybackup_db_password"))
	b, err := os.ReadFile(filepath.Join(dir, "postgres", "kybackup-role.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, want := range []string{
		"CREATE ROLE kybackup;",
		"EXCEPTION WHEN duplicate_object THEN NULL;",
		"ALTER ROLE kybackup WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '" + string(pw) + "';",
		"GRANT pg_read_all_data TO kybackup;",
		"GRANT CONNECT ON DATABASE synapse, mas TO kybackup;",
		"REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("kybackup-role.sql lacks %q:\n%s", want, sql)
		}
	}
	for _, bad := range []string{"pg_write_all_data", " SUPERUSER", "CREATEDB ", "GRANT ALL"} {
		if strings.Contains(sql, bad) {
			t.Errorf("kybackup-role.sql grants %q", bad)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "postgres", "kybackup-role.sql")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600: it holds the password", fi.Mode().Perm())
	}
}
```

Add `"slices"` to the imports if it is not there. In `TestInitIsWriteOnceForSecrets` add `"kybackup_db_password"` to the `[]string{"mas_admin_client_id", "mas_admin_client_secret"}` loop, and add `"postgres/kybackup-role.sql"` to the mode-check path list.

In `cmd/server/matrixinit_test.go`, in the second-pass block after the `back-channel logout URI` assertion, add:

```go
	if !strings.Contains(s, "-f /docker-entrypoint-initdb.d/kybackup-role.sql") {
		t.Errorf("second pass does not say how to apply the backup role to a running stack:\n%s", s)
	}
```

The existing loop that refuses printed secrets already covers `kybackup_db_password`.

- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./internal/matrixinit/ ./cmd/server/ -run 'BackupRole|WriteOnce|MatrixInit' -v`. Expected: FAIL (`postgres/kybackup-role.sql` not rendered, secret not created, output lacks the psql line).

- [ ] **Step 3: Implement.** Append to `secretSpecs`:

```go
	{"kybackup_db_password", hexSecret}, // read-only backup role; KyMessages reads it as a Compose secret
```

In `Run`'s render list, after the `pg-init.sql.tmpl` line, add:

```go
		// After init.sql by name: the entrypoint runs both on a new volume; operators run it once
		// on an existing stack. Idempotent.
		{"kybackup-role.sql.tmpl", "postgres/kybackup-role.sql", 0o600},
```

Create `internal/matrixinit/templates/kybackup-role.sql.tmpl`:

```sql
-- Generated by kymessages matrix-init. Idempotent. On a new data volume Postgres runs it after
-- init.sql; on an existing stack run it once:
--   docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql
-- kybackup is KyMessages' backup login: it reads synapse and mas and writes nothing.
DO $$
BEGIN
  CREATE ROLE kybackup;
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;
ALTER ROLE kybackup WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD {{ sqlq .S.kybackup_db_password }};
GRANT pg_read_all_data TO kybackup;
GRANT CONNECT ON DATABASE synapse, mas TO kybackup;
-- PUBLIC may connect to these by default; kybackup may connect to synapse and mas only.
REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;
```

In `cmd/server/matrixinit.go`, replace the last `fmt.Fprintln` with:

```go
	fmt.Fprintln(w, "If the stack is running, apply the new configs with: docker compose restart synapse mas element")
	fmt.Fprintln(w, "and, once per existing stack, the backup role:")
	fmt.Fprintln(w, "  docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql")
```

- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./internal/matrixinit/ ./cmd/server/ -v -run 'Matrix|Init|MAS|Backup'`. Expected: PASS. Then run `go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `git add internal/matrixinit cmd/server/matrixinit.go cmd/server/matrixinit_test.go`, then commit with the message `matrix-init: read-only kybackup role and its write-once password`.

---

### Task 2: Config, image and Compose give the app read-only Matrix access

**Files:**
- Modify: `internal/config/config.go:32-44` (`MatrixConfig`), `:100-112` (`BackupConfig`), `:177-180`, `:243-248`, `:268-313` (`matrixFromEnv`)
- Test: `internal/config/config_test.go:223-273`, `internal/config/matrix_internal_test.go`
- Modify: `Dockerfile:21-22`, `docker-compose.matrix.yml:1-33,131-146`, `scripts/check-compose-matrix.sh`

**Interfaces:**
- Produces: `config.MatrixConfig{…; Dir, MediaDir, DBHost string; BackupDBPassword string \`json:"-"\`}` and `config.BackupConfig.MediaFullKeep int`. Compose gives the app `KY_MATRIX_DIR=/matrix`, `KY_MATRIX_MEDIA_DIR=/matrix-media`, `KY_MATRIX_DB_HOST=postgres`, `KY_MATRIX_BACKUP_DB_PASSWORD_FILE=/run/secrets/kybackup_db_password` and `KY_BACKUP_MEDIA_FULL_KEEP`. It also mounts `./matrix:/matrix:ro` and `matrix-media:/matrix-media:ro`, and joins `matrix-db`.

- [ ] **Step 1: Write the failing config tests.** In `matrix_internal_test.go`, add three entries to `base` in `TestMatrixConfigRequiresAdminAccess`, so the good config stays good. First create the password file next to `secret`:

```go
	backupPW := filepath.Join(t.TempDir(), "kyb")
	if err := os.WriteFile(backupPW, []byte("kybpw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
```

The three entries are `"KY_MATRIX_DIR": "/matrix"`, `"KY_MATRIX_MEDIA_DIR": "/matrix-media"` and `"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": backupPW`. Then add these cases to the refusal map:

```go
		"no matrix dir":         {"KY_MATRIX_DIR": ""},
		"relative matrix dir":   {"KY_MATRIX_DIR": "matrix"},
		"no media dir":          {"KY_MATRIX_MEDIA_DIR": ""},
		"relative media dir":    {"KY_MATRIX_MEDIA_DIR": "media"},
		"no backup pw file":     {"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": ""},
		"missing backup pw":     {"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": backupPW + ".nope"},
		"db host is an option":  {"KY_MATRIX_DB_HOST": "-oProxyCommand=x"},
		"db host with a space":  {"KY_MATRIX_DB_HOST": "postgres x"},
```

After the good-config assertion, add:

```go
	if m.Dir != "/matrix" || m.MediaDir != "/matrix-media" || m.DBHost != "postgres" || m.BackupDBPassword != "kybpw" {
		t.Fatalf("backup fields: %+v", m)
	}
```

Before the test's final `for k := range base` loop, reuse the existing `empty` file:

```go
	set(map[string]string{"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": empty})
	if _, err := matrixFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_BACKUP_DB_PASSWORD_FILE") {
		t.Errorf("empty backup password: %v", err)
	}
```

In `config_test.go`'s `TestMatrixConfigFromEnv`:
- `set` also sets `KY_MATRIX_DIR=/matrix`, `KY_MATRIX_MEDIA_DIR=/matrix-media` and `KY_MATRIX_BACKUP_DB_PASSWORD_FILE` (a 0600 temp file holding `kybpw`).
- The unset loop also blanks those three.
- `want` gains `Dir: "/matrix", MediaDir: "/matrix-media", DBHost: "postgres", BackupDBPassword: "kybpw"`.

Add:

```go
func TestMediaFullKeepFromEnv(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	cfg, err := config.LoadFromEnv()
	if err != nil || cfg.Backup.MediaFullKeep != 3 {
		t.Fatalf("default: %v %v", cfg, err)
	}
	t.Setenv("KY_BACKUP_MEDIA_FULL_KEEP", "0")
	if _, err := config.LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_BACKUP_MEDIA_FULL_KEEP") {
		t.Errorf("keep 0: %v", err)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./internal/config/ -v`. Expected: FAIL to compile (`m.Dir` undefined).

- [ ] **Step 3: Implement config.** Add these fields to `MatrixConfig` after `AdminSecret`:

```go
	// Backups: matrix-init's output and Synapse's media store, both mounted read-only, the
	// Postgres host on matrix-db, and the read-only kybackup role's password.
	Dir              string `json:"dir"`
	MediaDir         string `json:"media_dir"`
	DBHost           string `json:"db_host"`
	BackupDBPassword string `json:"-"`
```

Add to `BackupConfig`:

```go
	// MediaFullKeep is how many monthly media archives to keep (Matrix only).
	MediaFullKeep int `json:"media_full_keep"`
```

Next to `backupKeep`, add:

```go
	mediaKeep := getEnvInt("KY_BACKUP_MEDIA_FULL_KEEP", 3)
	if mediaKeep < 1 {
		return nil, fmt.Errorf("KY_BACKUP_MEDIA_FULL_KEEP: must be at least 1, got %d", mediaKeep)
	}
```

Then set `MediaFullKeep: mediaKeep,` in the `Backup:` literal. In `matrixFromEnv`, read the new values into the struct literal:

```go
		Dir:      getEnv("KY_MATRIX_DIR", ""),
		MediaDir: getEnv("KY_MATRIX_MEDIA_DIR", ""),
		DBHost:   getEnv("KY_MATRIX_DB_HOST", ""),
```

Change the all-unset test to:

```go
	backupPWFile := getEnv("KY_MATRIX_BACKUP_DB_PASSWORD_FILE", "")
	if m == (MatrixConfig{}) && secretFile == "" && backupPWFile == "" {
		return m, nil
	}
```

Before `return m, nil` at the end, add:

```go
	for _, d := range []struct{ env, v string }{{"KY_MATRIX_DIR", m.Dir}, {"KY_MATRIX_MEDIA_DIR", m.MediaDir}} {
		if !filepath.IsAbs(d.v) {
			return MatrixConfig{}, fmt.Errorf("%s must be an absolute path (the Matrix overlay sets it)", d.env)
		}
	}
	if m.DBHost == "" {
		m.DBHost = "postgres"
	}
	if !dbHostRE.MatchString(m.DBHost) {
		return MatrixConfig{}, errors.New("KY_MATRIX_DB_HOST must be a host name")
	}
	pw, err := os.ReadFile(backupPWFile)
	m.BackupDBPassword = strings.TrimSpace(string(pw))
	if err != nil || m.BackupDBPassword == "" {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_BACKUP_DB_PASSWORD_FILE %q must name a readable, non-empty file: %v", backupPWFile, err)
	}
```

At package level, add the following and import `regexp`:

```go
// dbHostRE keeps KY_MATRIX_DB_HOST a plain host name: it is passed to pg_dump as --host=.
var dbHostRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
```

- [ ] **Step 4: Run the config tests.** Run `go test ./internal/config/ -v`. Expected: PASS.

- [ ] **Step 5: Write the failing compose checks.** In `scripts/check-compose-matrix.sh`:
- Change the secrets assertion to `[ "$(jq -c '[.services[] | .secrets // [] | .[].source] | sort' <<<"$out")" = '["kybackup_db_password","mas_admin_client_secret","postgres_password"]' ] || bad "secrets go to a service other than app (admin, backup) and postgres"`.
- Before `exit $fail`, add:

```bash
# Backups: the app dumps on matrix-db as kybackup and reads ./matrix and the media store read-only.
[ "$(jq -c '.services.app.networks | keys' <<<"$out")" = '["default","matrix-admin","matrix-db"]' ] || bad "app is not on default, matrix-admin and matrix-db"
[ "$(jq -r '[.services | to_entries[] | select(.value.networks | has("matrix-db")) | .key] | sort | join(",")' <<<"$out")" = app,mas,postgres,synapse ] \
  || bad "matrix-db members are not app,mas,postgres,synapse"
for kv in KY_MATRIX_DIR=/matrix KY_MATRIX_MEDIA_DIR=/matrix-media KY_MATRIX_DB_HOST=postgres \
  KY_MATRIX_BACKUP_DB_PASSWORD_FILE=/run/secrets/kybackup_db_password KY_BACKUP_MEDIA_FULL_KEEP=3; do
  [ "$(jq -r --arg k "${kv%%=*}" '.services.app.environment[$k]' <<<"$out")" = "${kv#*=}" ] || bad "app ${kv%%=*} is not ${kv#*=}"
done
[ "$(jq -r '.secrets.kybackup_db_password.file' <<<"$out")" = "$root/matrix/secrets/kybackup_db_password" ] || bad "kybackup secret source"
appvol() { jq -r --arg t "$1" '.services.app.volumes[] | select(.target == $t) | [.type, .source, (.read_only // false)] | @tsv' <<<"$out"; }
[ "$(appvol /matrix)" = "$(printf 'bind\t%s\ttrue' "$root/matrix")" ] || bad "app does not mount ./matrix read-only at /matrix"
[ "$(appvol /matrix-media)" = "$(printf 'volume\tmatrix-media\ttrue')" ] || bad "app does not mount matrix-media read-only at /matrix-media"
# pg_dump must match the server's major version: the image's client package against the postgres tag.
client=$(grep -oE 'postgresql[0-9]+-client' "$root/Dockerfile" | grep -oE '[0-9]+')
server=$(jq -r '.services.postgres.image' <<<"$out" | sed -E 's/^postgres:([0-9]+).*/\1/')
{ [ -n "$client" ] && [ "$client" = "$server" ]; } || bad "Dockerfile installs postgresql${client}-client but postgres runs major $server"
```

After the block, add the same network assertion for the static-IP render, which must keep the app on all three networks:

```bash
[ "$(jq -c '.services.app.networks | keys' <<<"$both")" = '["default","matrix-admin","matrix-db"]' ] || bad "static-IP overlay drops an app network"
```

- [ ] **Step 6: Run the check and confirm it fails.** Run `bash scripts/check-compose-matrix.sh`. Expected: `app is not on default, matrix-admin and matrix-db`, plus the other new messages, and exit 1.

- [ ] **Step 7: Implement the image and Compose changes.** In the `Dockerfile`, change the runtime stage's `apk` line to:

```dockerfile
# postgresql17-client: pg_dump/pg_restore for Matrix backups; its major must equal the
# postgres service's (scripts/check-compose-matrix.sh).
RUN apk --no-cache add ca-certificates tzdata postgresql17-client
```

In `docker-compose.matrix.yml`, replace the `app:` block with:

```yaml
  app:
    environment:
      - KY_MATRIX_SERVER_NAME=${KY_MATRIX_SERVER_NAME:?Set KY_MATRIX_SERVER_NAME}
      - KY_MATRIX_HOST=${KY_MATRIX_HOST:?Set KY_MATRIX_HOST}
      - KY_MATRIX_CHAT_HOST=${KY_MATRIX_CHAT_HOST:?Set KY_MATRIX_CHAT_HOST}
      # Signs KyIdentity's directory webhook; without it offboarding never reaches MAS.
      - KY_KYIDENTITY_HMAC_SECRET=${KY_KYIDENTITY_HMAC_SECRET:?Set KY_KYIDENTITY_HMAC_SECRET (the KyIdentity suite_webhook secret)}
      # Offboarding sync: MAS's admin API, reachable only on matrix-admin.
      - KY_MATRIX_ADMIN_URL=http://mas-admin:8081
      - KY_MATRIX_ADMIN_CLIENT_ID=${KY_MATRIX_ADMIN_CLIENT_ID:?Set KY_MATRIX_ADMIN_CLIENT_ID (printed by matrix-init)}
      - KY_MATRIX_ADMIN_SECRET_FILE=/run/secrets/mas_admin_client_secret
      # Server backups: pg_dump as the read-only kybackup role on matrix-db, plus matrix-init's
      # files and the media store, both read-only. Cost, recorded: the app can read the Matrix
      # databases (metadata and ciphertext) and the Matrix secrets.
      - KY_MATRIX_DIR=/matrix
      - KY_MATRIX_MEDIA_DIR=/matrix-media
      - KY_MATRIX_DB_HOST=postgres
      - KY_MATRIX_BACKUP_DB_PASSWORD_FILE=/run/secrets/kybackup_db_password
      - KY_BACKUP_MEDIA_FULL_KEEP=${KY_BACKUP_MEDIA_FULL_KEEP:-3}
    secrets: [mas_admin_client_secret, kybackup_db_password]
    networks:
      default: {}
      matrix-admin: {}
      matrix-db: {}
    volumes:
      - ./matrix:/matrix:ro
      - matrix-media:/matrix-media:ro
```

Add the following under `secrets:`:

```yaml
  kybackup_db_password:
    file: ./matrix/secrets/kybackup_db_password
```

Extend the header comment (lines 1-11) with one sentence: "The app also joins matrix-db and reads ./matrix and the media volume read-only, for server backups (docs/RESTORE.md)." The base file's `./data` and `./backups` mounts merge with these by target, so do not repeat them.

- [ ] **Step 8: Run the checks.** Run `bash scripts/check-compose-matrix.sh && bash scripts/check-compose-proxy.sh && shellcheck scripts/*.sh`. Expected: all pass. Mutation check: change `./matrix:/matrix:ro` to `./matrix:/matrix`, re-run and expect `app does not mount ./matrix read-only`, then revert. Then run `docker build -t kymessages:check .` followed by `docker run --rm --entrypoint pg_dump kymessages:check --version`. Expected: `pg_dump (PostgreSQL) 17.x`.

- [ ] **Step 9: Commit.** Run `git add internal/config Dockerfile docker-compose.matrix.yml scripts/check-compose-matrix.sh`, then commit with the message `config, image, compose: read-only Matrix access for server backups`.

---

### Task 3: The server capsule carries Matrix dumps and config, within the size limit

**Files:**
- Create: `internal/backup/matrix.go`, `internal/backup/size.go`, `internal/backup/export_test.go`, `internal/backup/matrix_test.go`, `internal/backup/size_test.go`
- Modify: `internal/backup/payload.go:32-84` (`Collect`), `:103`, `:141-148` (`Members`)
- Modify: `internal/backup/drill.go:174-222` (`Checks`)
- Test: `internal/backup/drill_test.go`

**Interfaces:**
- Consumes: `config.MatrixConfig.{Dir, DBHost, BackupDBPassword}` (Task 2), `postgres/kybackup-role.sql` (Task 1, at run time).
- Produces:
  - `backup.Collect(ctx, cfg, appVersion)`: unchanged signature. With Matrix enabled it adds `data/media.key`, `matrix/<sub>/<file>` and the dump parts, and sets the recipe key `pg_dumps`.
  - `type SizeError struct{ Bytes int64; Member string }`, which unwraps to `capsule.ErrCapsuleTooLarge`.
  - `Measure(files []recoveryclient.File) int64`.
  - `const DumpTimeout = 3 * time.Minute`.
  - `CollectForRun(ctx context.Context, cfg *config.Config, s recoveryclient.Settings, appVersion string) (recoveryclient.Payload, error)`.
  - `const LastSizeSetting = "backup_last_expanded_bytes"`.
  - `type SizeStatus struct{ Bytes, Limit int64; Percent int; Warning bool }` and `LastSize(s recoveryclient.Settings) (SizeStatus, bool, error)`.
  - Internal: `mediaKeyFile(cfg) string`, `openFiles(paths []string) (io.Reader, func(), error)`, `restoreTOC(ctx, r io.Reader) (string, error)` and type `tail`, all used by Tasks 5 and 6.

- [ ] **Step 1: Test seams.** Create `internal/backup/export_test.go`:

```go
package backup

import "testing"

// SetLimitsForTest shrinks the dump part size and the expanded limit, so splitting and the
// over-limit refusal are tested without allocating 256 MiB.
func SetLimitsForTest(t testing.TB, part, limit int64) {
	op, ol := partBytes, expandLimit
	partBytes, expandLimit = part, limit
	t.Cleanup(func() { partBytes, expandLimit = op, ol })
}
```

- [ ] **Step 2: Write the failing tests.** Create `internal/backup/matrix_test.go`:

```go
package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// fakeTool puts an executable shell script named name first on PATH. The real binaries get an
// empty environment, so the scripts use only shell builtins and absolute paths.
func fakeTool(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// matrixInstance is a SQLite instance with Matrix enabled: a matrix-init tree, and a fake
// pg_dump that prints masDump or synapseDump and logs its arguments one line per call.
func matrixInstance(t *testing.T, masDump, synapseDump []byte) (*config.Config, string) {
	t.Helper()
	cfg, _ := sqliteInstance(t)
	mdir := t.TempDir()
	for rel, body := range map[string]string{
		"secrets/synapse_db_password":         "spw",
		"secrets/.synapse_db_password.123":    "matrix-init temp file",
		"synapse/signing.key":                 "ed25519 a_abcd seed",
		"synapse/homeserver.yaml":             "server_name: example.com",
		"mas/config.yaml":                     "clients: []",
		"element/config.json":                 "{}",
		"postgres/init.sql":                   "CREATE USER synapse;",
		"postgres/kybackup-role.sql":          "CREATE ROLE kybackup;",
	} {
		p := filepath.Join(mdir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Matrix = config.MatrixConfig{ServerName: "example.com", Dir: mdir, MediaDir: t.TempDir(),
		DBHost: "postgres", BackupDBPassword: "pw-never-in-argv"}
	fx := t.TempDir()
	mas, syn, log := filepath.Join(fx, "mas"), filepath.Join(fx, "synapse"), filepath.Join(fx, "log")
	if err := os.WriteFile(mas, masDump, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syn, synapseDump, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, "pg_dump", fmt.Sprintf(`echo "$* env=$PGPASSWORD" >> %s
case "$*" in *--dbname=mas*) exec /bin/cat %s ;; *) exec /bin/cat %s ;; esac
`, log, mas, syn))
	return cfg, log
}

func dumpOf(n int) []byte { return append([]byte("PGDMP"), bytes.Repeat([]byte{7}, n-5)...) }

func members(p recoveryclient.Payload, prefix string) []recoveryclient.File {
	var out []recoveryclient.File
	for _, f := range p.Files {
		if strings.HasPrefix(f.Path, prefix) {
			out = append(out, f)
		}
	}
	return out
}

func joined(files []recoveryclient.File) []byte {
	var b []byte
	for _, f := range files {
		b = append(b, f.Data...)
	}
	return b
}

func TestCollectSplitsDumpsAndRejoins(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	mas, syn := dumpOf(2500), dumpOf(700)
	cfg, log := matrixInstance(t, mas, syn)
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range members(p, "matrix/dumps/") {
		names = append(names, f.Path)
	}
	want := []string{"matrix/dumps/mas.dump.000", "matrix/dumps/mas.dump.001", "matrix/dumps/mas.dump.002", "matrix/dumps/synapse.dump.000"}
	if !slices.Equal(names, want) {
		t.Fatalf("dump members %v, want %v (MAS first, from .000)", names, want)
	}
	if !bytes.Equal(joined(members(p, "matrix/dumps/mas.")), mas) || !bytes.Equal(joined(members(p, "matrix/dumps/synapse.")), syn) {
		t.Fatal("parts do not rejoin to the dumps")
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--dbname=mas") || !strings.Contains(lines[1], "--dbname=synapse") {
		t.Fatalf("pg_dump calls: %q", lines)
	}
	if !strings.Contains(lines[1], "--exclude-table-data=e2e_one_time_keys_json") || strings.Contains(lines[0], "exclude-table-data") {
		t.Errorf("one-time keys must be excluded from the Synapse dump only: %q", lines)
	}
	for _, l := range lines {
		args, env, _ := strings.Cut(l, " env=")
		if strings.Contains(args, "pw-never-in-argv") || env != "pw-never-in-argv" {
			t.Errorf("password must reach pg_dump by PGPASSWORD only: %q", l)
		}
		if !strings.Contains(args, "--username=kybackup") || !strings.Contains(args, "--host=postgres") || !strings.Contains(args, "--format=custom") {
			t.Errorf("pg_dump args %q", args)
		}
	}
	for _, path := range []string{"matrix/secrets/synapse_db_password", "matrix/synapse/signing.key", "matrix/mas/config.yaml",
		"matrix/element/config.json", "matrix/postgres/init.sql", "matrix/postgres/kybackup-role.sql", "data/media.key"} {
		if findFile(p.Files, path) == nil {
			t.Errorf("payload lacks %s", path)
		}
	}
	if findFile(p.Files, "matrix/secrets/.synapse_db_password.123") != nil {
		t.Error("collected a matrix-init temp file")
	}
	if dumps, _ := p.VerificationRecipe["pg_dumps"].([]string); !slices.Equal(dumps, []string{"matrix/dumps/mas.dump", "matrix/dumps/synapse.dump"}) {
		t.Errorf("recipe pg_dumps = %v", p.VerificationRecipe["pg_dumps"])
	}
}

// The media key is created once, 0600, and the same key rides in every capsule.
func TestCollectSealsAWriteOnceMediaKey(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(10), dumpOf(10))
	a, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	ka, kb := findFile(a.Files, "data/media.key"), findFile(b.Files, "data/media.key")
	if ka == nil || kb == nil || len(ka.Data) != 65 || !bytes.Equal(ka.Data, kb.Data) {
		t.Fatalf("media key %q then %q", ka, kb)
	}
	fi, err := os.Stat(filepath.Join(cfg.Database.DataDir, "media.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("data/media.key: %v %v", fi, err)
	}
}

func TestCollectSplitsAtExactBoundaries(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(2000), dumpOf(1000))
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(members(p, "matrix/dumps/mas.")); n != 2 {
		t.Errorf("2000 bytes in 1000-byte parts gave %d parts, want 2 (no empty third)", n)
	}
	if n := len(members(p, "matrix/dumps/synapse.")); n != 1 {
		t.Errorf("1000 bytes gave %d parts, want 1", n)
	}
	cfg, _ = matrixInstance(t, dumpOf(10), nil)
	if _, err := backup.Collect(context.Background(), cfg, "1.0.0"); err == nil || !strings.Contains(err.Error(), "pg_dump synapse wrote nothing") {
		t.Errorf("empty dump: %v", err)
	}
}

func TestCollectRefusesPastTheLimit(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(100), dumpOf(5000))
	full, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	limit := backup.Measure(full.Files) - 3000 // crosses inside the Synapse dump
	backup.SetLimitsForTest(t, 1000, limit)
	_, err = backup.Collect(context.Background(), cfg, "1.0.0")
	var se *backup.SizeError
	if !errors.As(err, &se) || !errors.Is(err, capsule.ErrCapsuleTooLarge) {
		t.Fatalf("err = %v, want a SizeError wrapping ErrCapsuleTooLarge", err)
	}
	if se.Bytes != backup.Measure(full.Files) {
		t.Errorf("measured %d, want the full size %d", se.Bytes, backup.Measure(full.Files))
	}
	if !strings.HasPrefix(se.Member, "matrix/dumps/synapse.dump.") || !strings.Contains(se.Error(), se.Member) || !strings.Contains(se.Error(), "MiB") {
		t.Errorf("error %q does not name the size and the member", se.Error())
	}
}

func TestCollectFailsWhenADumpFails(t *testing.T) {
	cfg, _ := matrixInstance(t, dumpOf(10), dumpOf(10))
	fakeTool(t, "pg_dump", `case "$*" in *--dbname=mas*) echo 'PGDMP partial'; echo 'pg_dump: error: connection to server lost' >&2; exit 1 ;; esac
exec /bin/cat /dev/null
`)
	_, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "pg_dump mas") || !strings.Contains(err.Error(), "connection to server lost") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectWithoutMatrixIsUnchanged(t *testing.T) {
	cfg, _ := payloadConfig(t)
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(members(p, "matrix/")) != 0 || findFile(p.Files, "data/media.key") != nil || p.VerificationRecipe["pg_dumps"] != nil {
		t.Fatalf("Matrix members without Matrix: %v", p.Files)
	}
	if _, err := os.Stat(filepath.Join(cfg.Database.DataDir, "media.key")); !os.IsNotExist(err) {
		t.Error("media.key created without Matrix")
	}
}

// Measure must count what capsule.Open counts: a tar header per member and 512-byte padding.
func TestMeasureMatchesTarFraming(t *testing.T) {
	var files []recoveryclient.File
	for _, n := range []int{0, 1, 511, 512, 513, 4096} {
		files = append(files, recoveryclient.File{Path: fmt.Sprint("f", n), Data: make([]byte, n)})
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.Path, Mode: 0o600, Size: int64(len(f.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := backup.Measure(files), int64(buf.Len()); got != want {
		t.Fatalf("Measure %d, tar stream %d", got, want)
	}
}
```

Create `internal/backup/size_test.go`:

```go
package backup_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky_server_base/internal/backup"
)

func TestCollectForRunRecordsTheSizeEvenWhenRefused(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(100), dumpOf(5000))
	_, st := sqliteInstance(t)
	s := backup.Settings(context.Background(), st.Settings())
	p, err := backup.CollectForRun(context.Background(), cfg, s, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := backup.LastSize(s)
	if err != nil || !ok || got.Bytes != backup.Measure(p.Files) {
		t.Fatalf("after success: %+v %v %v", got, ok, err)
	}
	backup.SetLimitsForTest(t, 1000, 4096)
	if _, err := backup.CollectForRun(context.Background(), cfg, s, "1.0.0"); err == nil {
		t.Fatal("over the limit accepted")
	}
	if got, _, _ = backup.LastSize(s); got.Bytes != backup.Measure(p.Files) {
		t.Errorf("after refusal: recorded %d, want the measured %d", got.Bytes, backup.Measure(p.Files))
	}
}

func TestLastSizeWarnsFrom75Percent(t *testing.T) {
	_, st := sqliteInstance(t)
	s := backup.Settings(context.Background(), st.Settings())
	if _, ok, err := backup.LastSize(s); ok || err != nil {
		t.Fatalf("fresh: %v %v", ok, err)
	}
	limit := capsule.MaxExpandedBytes
	for _, tc := range []struct {
		bytes   int64
		percent int
		warn    bool
	}{{limit * 3 / 4, 75, true}, {limit*3/4 - 1, 74, false}, {limit, 100, true}} {
		if err := s.Set(backup.LastSizeSetting, strconv.FormatInt(tc.bytes, 10)); err != nil {
			t.Fatal(err)
		}
		got, ok, err := backup.LastSize(s)
		if err != nil || !ok || got.Percent != tc.percent || got.Warning != tc.warn || got.Limit != limit {
			t.Errorf("%d bytes: %+v %v", tc.bytes, got, err)
		}
	}
}
```

In `drill_test.go`, append a drill test that runs the real `Checks` against a fake `pg_restore`:

```go
// fakePgRestore accepts --list on a stream starting with PGDMP and echoes the rest as the TOC.
func fakePgRestore(t *testing.T) {
	fakeTool(t, "pg_restore", `[ "$1" = --list ] || exit 3
IFS= read -r first || [ -n "$first" ] || exit 1
case $first in PGDMP*) ;; *) echo 'pg_restore: error: input file does not appear to be a valid archive' >&2; exit 1 ;; esac
while IFS= read -r line; do echo "$line"; done
`)
}

func matrixScratch(t *testing.T, p recoveryclient.Payload, skip string, corrupt string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range p.Files {
		if f.Path == skip {
			continue
		}
		data := f.Data
		if f.Path == corrupt {
			data = []byte("not a dump\n")
		}
		full := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDrillChecksDumps(t *testing.T) {
	t.Setenv("KY_PORT", "8080")
	t.Setenv("KY_DB_DRIVER", "sqlite")
	backup.SetLimitsForTest(t, 1000, 1<<20)
	fakePgRestore(t)
	cfg, _ := matrixInstance(t, append(dumpOf(1500), []byte("\n; TABLE public users\n")...), dumpOf(10))
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	passed := func(checks []recoveryclient.Check) bool {
		for _, c := range checks {
			if !c.Passed {
				return false
			}
		}
		return len(checks) > 0
	}
	byName := func(checks []recoveryclient.Check, name string) *recoveryclient.Check {
		for i := range checks {
			if checks[i].Name == name {
				return &checks[i]
			}
		}
		return nil
	}
	ok := backup.Checks(matrixScratch(t, p, "", ""), manifestFor(p))
	if !passed(ok) || byName(ok, "Postgres Dump: matrix/dumps/mas.dump") == nil || byName(ok, "Postgres Dump: matrix/dumps/synapse.dump") == nil {
		t.Fatalf("complete dumps: %+v", ok)
	}
	if c := byName(backup.Checks(matrixScratch(t, p, "", "matrix/dumps/synapse.dump.000"), manifestFor(p)), "Postgres Dump: matrix/dumps/synapse.dump"); c == nil || c.Passed {
		t.Errorf("corrupt dump passed: %+v", c)
	}
	if passed(backup.Checks(matrixScratch(t, p, "matrix/dumps/mas.dump.001", ""), manifestFor(p))) {
		t.Error("missing dump part passed")
	}
	// The recipe must account for every dump member.
	bad := p
	bad.VerificationRecipe = maps.Clone(p.VerificationRecipe)
	delete(bad.VerificationRecipe, "pg_dumps")
	if c := backup.Checks(matrixScratch(t, p, "", ""), manifestFor(bad)); len(c) != 1 || c[0].Name != "Verification Recipe" {
		t.Errorf("recipe without pg_dumps: %+v", c)
	}
}
```

Add `"github.com/Busnes-app/ky-primitives/recoveryclient"` to `drill_test.go`'s imports if it is not there (`maps` already is).

- [ ] **Step 3: Run them and confirm they fail.** Run `go test ./internal/backup/ -v`. Expected: FAIL to compile (`SetLimitsForTest`, `Measure` and `CollectForRun` undefined).

- [ ] **Step 4: Implement `internal/backup/matrix.go`.**

```go
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// mediaKeyPath encrypts the local media mirror (internal/backup/media). It rides in the
// capsule, so only k custodians together can open the mirror.
const mediaKeyPath = "data/media.key"

func mediaKeyFile(cfg *config.Config) string { return filepath.Join(cfg.Database.DataDir, "media.key") }

// DumpTimeout bounds both pg_dump runs together. cmd/server's backupWaitTimeout counts it.
const DumpTimeout = 3 * time.Minute

// partBytes is one dump member: the capsule's per-file limit.
var partBytes = capsule.MaxFileBytes

// expandLimit is the most a payload may expand to. capsule.Open counts tar framing (a 512-byte
// header per member, content padded to 512, the file list and a 1 KiB trailer) against
// MaxExpandedBytes; capsule.Seal counts content only, so ky-primitives v0.8.0 seals payloads
// it then refuses to open. memberBytes counts the framing; 1 MiB is held back for the list and
// trailer.
var expandLimit = capsule.MaxExpandedBytes - 1<<20

// matrixDirs are the matrix-init subdirectories a rebuilt stack needs: secrets, the signing
// key and every rendered config. dumps/ exists only in a restored tree and is never collected.
var matrixDirs = []string{"secrets", "synapse", "mas", "element", "postgres"}

// dumps run MAS first: Postgres snapshots do not span databases, and MAS re-provisions a user
// created in between at sign-in. Synapse's backup guide: one-time keys must not be restored.
var dumps = []struct {
	db, base string
	extra    []string
}{
	{"mas", "matrix/dumps/mas.dump", nil},
	{"synapse", "matrix/dumps/synapse.dump", []string{"--exclude-table-data=e2e_one_time_keys_json"}},
}

func dumpBases() []string {
	out := make([]string, 0, len(dumps))
	for _, d := range dumps {
		out = append(out, d.base)
	}
	return out
}

// SizeError is a payload past the expanded limit: Bytes is the full measured size, Member the
// first member past the limit.
type SizeError struct {
	Bytes  int64
	Member string
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("backup: the capsule would expand to %d MiB, past the %d MiB limit, at %s",
		e.Bytes>>20, capsule.MaxExpandedBytes>>20, e.Member)
}

func (e *SizeError) Unwrap() error { return capsule.ErrCapsuleTooLarge }

func memberBytes(n int64) int64 { return 512 + (n+511)/512*512 }

// Measure is the expanded size of files as capsule.Open counts it, file list and trailer aside.
func Measure(files []recoveryclient.File) int64 {
	var n int64
	for _, f := range files {
		n += memberBytes(int64(len(f.Data)))
	}
	return n
}

// budget counts members as they are collected and remembers the first one past the limit.
type budget struct {
	used int64
	over string
}

func (b *budget) add(path string, n int64) {
	b.used += memberBytes(n)
	if b.used > expandLimit && b.over == "" {
		b.over = path
	}
}

func (b *budget) err() error {
	if b.over == "" {
		return nil
	}
	return &SizeError{Bytes: b.used, Member: b.over}
}

// collectMatrix returns matrix-init's files, then the dumps. Past the limit the dumps keep
// draining, so the error carries the full size, but no more data is kept.
func collectMatrix(ctx context.Context, m config.MatrixConfig, b *budget) ([]recoveryclient.File, error) {
	var files []recoveryclient.File
	for _, sub := range matrixDirs {
		err := filepath.WalkDir(filepath.Join(m.Dir, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Regular files only; matrix-init's in-flight temp files start with ".".
			if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(m.Dir, path)
			if err != nil {
				return err
			}
			name := "matrix/" + filepath.ToSlash(rel)
			b.add(name, int64(len(data)))
			files = append(files, recoveryclient.File{Path: name, Data: data, Mode: 0600})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("backup: Matrix config: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, DumpTimeout)
	defer cancel()
	for _, d := range dumps {
		parts, err := dumpParts(ctx, m, d.db, d.base, d.extra, b)
		if err != nil {
			return nil, err
		}
		files = append(files, parts...)
	}
	return files, nil
}

// dumpParts runs pg_dump as kybackup and cuts its output into <base>.000, .001, ...
func dumpParts(ctx context.Context, m config.MatrixConfig, db, base string, extra []string, b *budget) ([]recoveryclient.File, error) {
	args := append([]string{"--format=custom", "--host=" + m.DBHost, "--username=kybackup", "--dbname=" + db, "--no-password"}, extra...)
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	// Only what pg_dump needs: the password never reaches argv, and no inherited PG* variable
	// can point the dump elsewhere.
	cmd.Env = []string{"PGPASSWORD=" + m.BackupDBPassword, "PGCONNECT_TIMEOUT=10"}
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("backup: pg_dump %s: %w", db, err)
	}
	var files []recoveryclient.File
	var readErr error
	for i := 0; ; i++ {
		part, err := io.ReadAll(io.LimitReader(out, partBytes))
		if err != nil {
			readErr = err
			break
		}
		if len(part) == 0 {
			break
		}
		name := fmt.Sprintf("%s.%03d", base, i)
		b.add(name, int64(len(part)))
		if b.over == "" {
			files = append(files, recoveryclient.File{Path: name, Data: part, Mode: 0600})
		}
		if int64(len(part)) < partBytes {
			break
		}
	}
	if err := errors.Join(readErr, cmd.Wait()); err != nil {
		return nil, fmt.Errorf("backup: pg_dump %s: %w: %s", db, err, stderr.String())
	}
	if len(files) == 0 && b.over == "" {
		return nil, fmt.Errorf("backup: pg_dump %s wrote nothing", db)
	}
	return files, nil
}

// tail keeps the last 2 KiB written to it: enough for a tool's error, never its data.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.b)) }

// openFiles reads paths in order as one stream; close releases them all.
func openFiles(paths []string) (io.Reader, func(), error) {
	var readers []io.Reader
	var opened []*os.File
	closeAll := func() {
		for _, f := range opened {
			f.Close()
		}
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		opened = append(opened, f)
		readers = append(readers, f)
	}
	return io.MultiReader(readers...), closeAll, nil
}

// restoreTOC is pg_restore --list over a custom-format dump read from r.
func restoreTOC(ctx context.Context, r io.Reader) (string, error) {
	cmd := exec.CommandContext(ctx, "pg_restore", "--list")
	cmd.Stdin = r
	cmd.Env = []string{}
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pg_restore --list: %w: %s", err, stderr.String())
	}
	return string(out), nil
}
```

- [ ] **Step 5: Wire `Collect` and `Members`.** In `payload.go`, add imports for `github.com/Busnes-app/ky-primitives/keyfile`. Replace lines 63-83, from the `recovery.pub` block to `return payload, nil`, with:

```go
	if pub, err := os.ReadFile(recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)); err == nil {
		files = append(files, recoveryclient.File{Path: recoveryPubPath, Data: pub, Mode: 0600})
	}
	var b budget
	for _, f := range files {
		b.add(f.Path, int64(len(f.Data)))
	}
	recipe := map[string]any{
		"check_sqlite_integrity": true,
		"sqlite_paths":           sqlitePaths,
		"expected_env":           []string{"KY_PORT", "KY_DB_DRIVER"},
		"expected_ports":         []int{cfg.Server.Port},
	}
	if cfg.Matrix.Enabled() {
		key, err := keyfile.LoadOrCreate(mediaKeyFile(cfg), 32)
		if err != nil {
			return recoveryclient.Payload{}, fmt.Errorf("backup: media key: %w", err)
		}
		mk := recoveryclient.File{Path: mediaKeyPath, Data: []byte(hex.EncodeToString(key) + "\n"), Mode: 0600}
		clear(key)
		b.add(mk.Path, int64(len(mk.Data)))
		mx, err := collectMatrix(ctx, cfg.Matrix, &b)
		if err != nil {
			return recoveryclient.Payload{}, err
		}
		files = append(append(files, mk), mx...)
		recipe["pg_dumps"] = dumpBases()
	}
	if err := b.err(); err != nil {
		return recoveryclient.Payload{}, err
	}
	recipe["required_files"] = requiredFiles(files)
	return recoveryclient.Payload{
		ServiceName: cfg.Server.AppName,
		AppVersion:  appVersion,
		Files:       files,
		Dependencies: map[string]any{
			"ports": []int{cfg.Server.Port},
			"env":   []string{"KY_PORT", "KY_DB_DRIVER"},
		},
		VerificationRecipe: recipe,
	}, nil
```

Update the `Collect` doc comment: with Matrix enabled the payload also carries matrix-init's files, the MAS then Synapse dumps and `data/media.key`, and a payload past the expanded limit returns `*SizeError`. Change line 103's comment to `// The server capsule carries the whole application database.`. Extend `Members`:

```go
	if cfg.Matrix.Enabled() {
		m = append(m, mediaKeyPath, "matrix/secrets/", "matrix/synapse/", "matrix/mas/", "matrix/element/", "matrix/postgres/",
			"matrix/dumps/mas.dump.NNN", "matrix/dumps/synapse.dump.NNN")
	}
```

- [ ] **Step 6: Add the drill checks.** In `drill.go`, add near the top:

```go
// matrixRequired are the Matrix members a rebuilt stack cannot start without.
var matrixRequired = []string{mediaKeyPath, "matrix/synapse/signing.key", "matrix/synapse/homeserver.yaml",
	"matrix/mas/config.yaml", "matrix/postgres/init.sql"}
```

In `Checks`, just before `checks := fileChecks(dir, required, sqlitePaths)`, add:

```go
	var bases []string
	if raw, present := recipe["pg_dumps"]; present {
		if bases, err = recipeStrings(raw); err != nil {
			return recipeFailure("pg_dumps: " + err.Error())
		}
		for _, name := range matrixRequired {
			if !slices.Contains(required, name) {
				return recipeFailure("required_files omits " + name)
			}
		}
	}
	if message := dumpMembersFailure(opened, bases); message != "" {
		return recipeFailure(message)
	}
```

Before `return checks`, add `checks = append(checks, dumpChecks(dir, bases)...)`. Then add:

```go
// dumpMembersFailure requires every matrix/dumps/ member to be a part of a listed base, numbered
// from .000 without a gap; it returns the recipe failure, or "".
func dumpMembersFailure(opened capsule.Manifest, bases []string) string {
	members := map[string]bool{}
	dumpMembers := 0
	for _, f := range opened.Files {
		members[f.Path] = true
		if strings.HasPrefix(f.Path, "matrix/dumps/") {
			dumpMembers++
		}
	}
	counted := 0
	for _, base := range bases {
		for i := 0; members[fmt.Sprintf("%s.%03d", base, i)]; i++ {
			counted++
		}
	}
	if counted != dumpMembers {
		return "pg_dumps does not account for every dump part from .000"
	}
	return ""
}

// dumpChecks reports each base's parts, joined in order, passing pg_restore --list.
func dumpChecks(dir string, bases []string) []recoveryclient.Check {
	var checks []recoveryclient.Check
	for _, base := range bases {
		name := "Postgres Dump: " + base
		var paths []string
		for i := 0; ; i++ {
			full, _ := drillPath(dir, fmt.Sprintf("%s.%03d", base, i))
			if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
				break
			}
			paths = append(paths, full)
		}
		if len(paths) == 0 {
			checks = append(checks, recoveryclient.Check{Name: name, Message: "No parts"})
			continue
		}
		r, closeAll, err := openFiles(paths)
		if err != nil {
			checks = append(checks, recoveryclient.Check{Name: name, Message: "Part unreadable"})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err = restoreTOC(ctx, r)
		cancel()
		closeAll()
		if err != nil {
			checks = append(checks, recoveryclient.Check{Name: name, Message: "pg_restore --list failed: " + recoveryclient.AuditSafe(err.Error())})
			continue
		}
		checks = append(checks, recoveryclient.Check{Name: name, Passed: true, Message: fmt.Sprintf("pg_restore --list read %d parts", len(paths))})
	}
	return checks
}
```

Add the imports `context` and `time`. A missing part after `.000` is caught earlier, by `memberFailure` and `fileChecks`, because every member is required.

- [ ] **Step 7: Add `internal/backup/size.go`.**

```go
package backup

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// LastSizeSetting holds the last measured expanded size of the server capsule, in bytes.
const LastSizeSetting = "backup_last_expanded_bytes"

// CollectForRun is Collect for a backup run: it records the measured size, over the limit or
// not, for the status screen. A failed write only costs the screen its number.
func CollectForRun(ctx context.Context, cfg *config.Config, s recoveryclient.Settings, appVersion string) (recoveryclient.Payload, error) {
	p, err := Collect(ctx, cfg, appVersion)
	var se *SizeError
	switch {
	case err == nil:
		_ = s.Set(LastSizeSetting, strconv.FormatInt(Measure(p.Files), 10))
	case errors.As(err, &se):
		_ = s.Set(LastSizeSetting, strconv.FormatInt(se.Bytes, 10))
	}
	return p, err
}

// SizeStatus is the last measured size against the capsule's expanded limit.
type SizeStatus struct {
	Bytes   int64 `json:"bytes"`
	Limit   int64 `json:"limit"`
	Percent int   `json:"percent"`
	Warning bool  `json:"warning"` // at or past 75% of Limit
}

// LastSize reads LastSizeSetting; ok is false before the first run.
func LastSize(s recoveryclient.Settings) (SizeStatus, bool, error) {
	v, err := s.Get(LastSizeSetting)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		return SizeStatus{}, false, nil
	}
	if err != nil {
		return SizeStatus{}, false, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return SizeStatus{}, false, fmt.Errorf("backup: %s is %q", LastSizeSetting, v)
	}
	limit := capsule.MaxExpandedBytes
	return SizeStatus{Bytes: n, Limit: limit, Percent: int(n * 100 / limit), Warning: n*4 >= limit*3}, true, nil
}
```

- [ ] **Step 8: Run the tests and confirm they pass.** Run `go test -race ./internal/backup/ -v`. Expected: PASS, including the existing `TestNothingInTheServerDecrypts` and `TestDrillRejectsMalformedRecipes`. Then run `go test ./...`. Expected: PASS.

- [ ] **Step 9: Commit.** Run `git add internal/backup`, then commit with the message `backup: server capsule carries Matrix dumps and config within the expanded limit`.

---

### Task 4: `internal/backup/media`: encrypted mirror and monthly archives

**Files:**
- Create: `internal/backup/media/media.go`, `internal/backup/media/media_test.go`, `internal/backup/media/AGENTS.md`
- Modify: `internal/backup/AGENTS.md` (Child DOX Index)

**Interfaces:**
- Produces:
  - `media.Run(ctx context.Context, src, dir string, key []byte, keep int, now time.Time) (media.Result, error)`
  - `media.Restore(ctx context.Context, dir string, key []byte, dst string, uid, gid int, write bool) (int, error)`
  - `type Result struct{ Copied, Unchanged, Pruned int; Archive string }`
  - `var Sources = []string{"local_content", "local_thumbnails"}`
  - `ErrBusy`, `ErrNoBackup`
  - `dir` is `<KY_BACKUP_DIR>/media`, holding `mirror/`, `mirror/index`, `full-YYYY-MM.tar` and `.lock`.

- [ ] **Step 1: Write the failing tests.** Create `internal/backup/media/media_test.go`. It is an internal package test, so it can reach `lock`, `writeIndex` and `afterScan`:

```go
package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	key   = bytes.Repeat([]byte{3}, 32)
	jan   = time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC)
	feb   = time.Date(2026, 2, 15, 3, 0, 0, 0, time.UTC)
	files = map[string]string{
		"local_content/ab/cd/one":   "first upload",
		"local_thumbnails/ab/cd/t1": "thumb",
	}
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// store is a Synapse media store with the kept files, a cache dir and a symlink out.
func store(t *testing.T) string {
	src := t.TempDir()
	for rel, body := range files {
		write(t, src, rel, body)
	}
	write(t, src, "remote_content/ex/am/remote", "cache")
	if err := os.Symlink("/etc/hostname", filepath.Join(src, "local_content", "ab", "escape")); err != nil {
		t.Fatal(err)
	}
	return src
}

func restored(t *testing.T, dst string) map[string]string {
	t.Helper()
	got := map[string]string{}
	_ = filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dst, p)
			got[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return got
}

func TestRunMirrorsEncryptedAndRestores(t *testing.T) {
	src, dir := store(t), t.TempDir()
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 2 || res.Archive != "full-2026-01.tar" {
		t.Fatalf("first run %+v", res)
	}
	for rel, body := range files {
		sealed, err := os.ReadFile(filepath.Join(dir, "mirror", rel))
		if err != nil || bytes.Contains(sealed, []byte(body)) || len(sealed) != len(body)+28 {
			t.Errorf("%s mirrored as %d bytes (%v); want ciphertext of %d+28", rel, len(sealed), err, len(body))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror", "remote_content")); !os.IsNotExist(err) {
		t.Error("mirrored remote media cache")
	}
	dst := t.TempDir()
	if n, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), false); err != nil || n == 0 || len(restored(t, dst)) != 0 {
		t.Fatalf("check-only restore wrote files or failed: %d %v", n, err)
	}
	if _, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	if got := restored(t, dst); len(got) != len(files) || got["local_content/ab/cd/one"] != "first upload" {
		t.Fatalf("restored %v", got)
	}
}

// A ciphertext moved to another path does not open: its path is its associated data.
func TestSwappedFileIsRefused(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	one, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one"))
	if err := os.WriteFile(filepath.Join(dir, "mirror", "local_thumbnails/ab/cd/t1"), one, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "full-2026-01.tar")); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), false)
	if err == nil || !strings.Contains(err.Error(), "local_thumbnails/ab/cd/t1") {
		t.Fatalf("swapped file: %v", err)
	}
}

func TestIncrementalCopiesOnlyNewOrChanged(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil || res.Copied != 0 || res.Unchanged != 2 || res.Archive != "" {
		t.Fatalf("unchanged rerun %+v %v", res, err)
	}
	write(t, src, "local_content/ab/cd/one", "edited upload")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(src, "local_content/ab/cd/one"), later, later); err != nil {
		t.Fatal(err)
	}
	write(t, src, "local_content/zz/yy/two", "second upload")
	if res, err = Run(context.Background(), src, dir, key, 3, jan); err != nil || res.Copied != 2 || res.Unchanged != 1 {
		t.Fatalf("after change %+v %v", res, err)
	}
}

func TestMonthlyArchiveAndPruning(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(src, "local_content/ab/cd/one")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	// Same month: the archive exists, so the mirror keeps the deleted media.
	if res, err := Run(context.Background(), src, dir, key, 3, jan); err != nil || res.Pruned != 0 {
		t.Fatalf("same month %+v %v", res, err)
	}
	res, err := Run(context.Background(), src, dir, key, 3, feb)
	if err != nil || res.Archive != "full-2026-02.tar" || res.Pruned != 1 {
		t.Fatalf("new month %+v %v", res, err)
	}
	if !slices.Contains(tarNames(t, filepath.Join(dir, "full-2026-02.tar")), "mirror/local_content/ab/cd/one") {
		t.Error("the new archive lacks media deleted since the last one")
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror", "local_content/ab/cd/one")); !os.IsNotExist(err) {
		t.Error("deleted media still mirrored after the new archive")
	}
	for _, m := range []int{3, 4, 5} {
		if _, err := Run(context.Background(), src, dir, key, 3, time.Date(2026, time.Month(m), 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := archives(dir)
	if want := []string{"full-2026-05.tar", "full-2026-04.tar", "full-2026-03.tar"}; !slices.Equal(got, want) {
		t.Fatalf("archives %v, want %v", got, want)
	}
}

func tarNames(t *testing.T, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// Restore applies the newest archive, then the mirror, which wins.
func TestRestorePrefersTheMirrorOverTheArchive(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	write(t, src, "local_content/ab/cd/one", "newer upload")
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(src, "local_content/ab/cd/one"), later, later)
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	if got := restored(t, dst)["local_content/ab/cd/one"]; got != "newer upload" {
		t.Fatalf("restored %q", got)
	}
}

func TestRunSkipsAFileDeletedMidRun(t *testing.T) {
	src, dir := store(t), t.TempDir()
	afterScan = func() { _ = os.Remove(filepath.Join(src, "local_content/ab/cd/one")) }
	t.Cleanup(func() { afterScan = func() {} })
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil || res.Copied != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunRefusesAMirrorUnderAnotherKey(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one"))
	other := bytes.Repeat([]byte{4}, 32)
	if _, err := Run(context.Background(), src, dir, other, 3, jan); err == nil || !strings.Contains(err.Error(), "does not open under the media key") {
		t.Fatalf("other key: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one")); !bytes.Equal(before, after) {
		t.Error("mirror rewritten under another key")
	}
	if _, err := Restore(context.Background(), dir, other, t.TempDir(), os.Getuid(), os.Getgid(), false); err == nil {
		t.Error("restore with another key accepted")
	}
}

func TestRunSweepsCrashLeftoversAndIsSingleFlight(t *testing.T) {
	src, dir := store(t), t.TempDir()
	write(t, dir, "mirror/local_content/ab/cd/.one.tmp-123", "half")
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror/local_content/ab/cd/.one.tmp-123")); !os.IsNotExist(err) {
		t.Error("crash leftover not swept")
	}
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent run: %v", err)
	}
}

func TestRestoreRefusesAnIndexOutsideTheStore(t *testing.T) {
	dir := t.TempDir()
	a, err := newAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "mirror"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../escape", "local_content/../../x", "/etc/passwd", "remote_content/x"} {
		if err := writeIndex(a, filepath.Join(dir, "mirror", "index"), Index{bad: {}}); err != nil {
			t.Fatal(err)
		}
		if _, err := Restore(context.Background(), dir, key, t.TempDir(), os.Getuid(), os.Getgid(), false); err == nil {
			t.Errorf("index entry %q accepted", bad)
		}
	}
	if _, err := Restore(context.Background(), t.TempDir(), key, t.TempDir(), 0, 0, false); !errors.Is(err, ErrNoBackup) {
		t.Errorf("empty backup dir: %v", err)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./internal/backup/media/ -v`. Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement `internal/backup/media/media.go`.**

```go
// Package media keeps an encrypted copy of Synapse's local media in the backup directory: a
// mirror brought up to date on every run, and a monthly archive of it. Each file is
// AES-256-GCM under the media key with its relative path as associated data, so a ciphertext
// moved to another path does not open. Nothing here is in the capsule except the key.
package media

import (
	"archive/tar"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	mirrorDir  = "mirror"
	indexName  = "index"
	lockName   = ".lock"
	tmpMarker  = ".tmp-"
	fileAAD    = "kymessages-media/v1\x00"
	indexAAD   = "kymessages-media-index/v1"
	archiveFmt = "full-2006-01.tar"
)

// Sources are the media-store subtrees kept: uploads and their thumbnails. Remote media and
// URL previews are caches (Synapse's backup guide), and federation is off.
var Sources = []string{"local_content", "local_thumbnails"}

var (
	ErrBusy     = errors.New("media: another media backup holds the lock")
	ErrNoBackup = errors.New("media: no media backup here (no monthly archive and no mirror index)")
)

// Entry tells a changed file from an unchanged one.
type Entry struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"` // Unix nanoseconds
}

// Index maps a slash path under the media store to its Entry.
type Index map[string]Entry

// Result is one run: Archive names the monthly archive it wrote, if any.
type Result struct {
	Copied, Unchanged, Pruned int
	Archive                   string
}

// afterScan is a test seam between listing the store and copying from it.
var afterScan = func() {}

// Run brings dir (<KY_BACKUP_DIR>/media) up to date with src, Synapse's media store. New or
// changed files are encrypted into the mirror. Once per UTC calendar month the mirror and its
// index are archived, mirror files whose media is gone from src are dropped, and only the
// newest keep archives remain. ctx is honoured between files; an interrupted run resumes.
func Run(ctx context.Context, src, dir string, key []byte, keep int, now time.Time) (Result, error) {
	var res Result
	a, err := newAEAD(key)
	if err != nil {
		return res, err
	}
	if keep < 1 {
		return res, errors.New("media: keep must be at least 1")
	}
	if err := os.MkdirAll(filepath.Join(dir, mirrorDir), 0o700); err != nil {
		return res, err
	}
	unlock, err := lock(dir)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := sweepTemp(dir); err != nil {
		return res, err
	}
	indexPath := filepath.Join(dir, mirrorDir, indexName)
	old, err := readIndex(a, indexPath)
	if err != nil {
		return res, err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return res, err
	}
	defer root.Close()
	cur, err := scan(root)
	if err != nil {
		return res, err
	}
	afterScan()
	next := Index{} // media gone from src stays mirrored until the next archive
	maps.Copy(next, old)
	for _, rel := range slices.Sorted(maps.Keys(cur)) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		e := cur[rel]
		if old[rel] == e && exists(mirrored(dir, rel)) {
			res.Unchanged++
			continue
		}
		err := copyIn(a, root, dir, rel)
		if errors.Is(err, fs.ErrNotExist) {
			delete(cur, rel) // deleted since the scan
			continue
		}
		if err != nil {
			return res, fmt.Errorf("media: %s: %w", rel, err)
		}
		next[rel] = e
		res.Copied++
	}
	if err := writeIndex(a, indexPath, next); err != nil {
		return res, err
	}
	name := now.UTC().Format(archiveFmt)
	if exists(filepath.Join(dir, name)) {
		return res, nil
	}
	if err := writeArchive(dir, name, next); err != nil {
		return res, err
	}
	res.Archive = name
	// The archive holds what the mirror held; now the mirror drops media deleted from src.
	// Index first, so it never lists a file that is gone.
	if err := writeIndex(a, indexPath, cur); err != nil {
		return res, err
	}
	for rel := range next {
		if _, kept := cur[rel]; !kept {
			if err := os.Remove(mirrored(dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			res.Pruned++
		}
	}
	return res, pruneArchives(dir, keep)
}

func mirrored(dir, rel string) string { return filepath.Join(dir, mirrorDir, filepath.FromSlash(rel)) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// scan lists the regular files under Sources. Reading through root means a symlink planted in
// the media store cannot pull a file from outside it; symlinks are skipped either way.
func scan(root *os.Root) (Index, error) {
	idx := Index{}
	for _, top := range Sources {
		err := fs.WalkDir(root.FS(), top, func(rel string, d fs.DirEntry, err error) error {
			if rel == top && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir // nothing uploaded yet
			}
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			idx[rel] = Entry{Size: info.Size(), MTime: info.ModTime().UnixNano()}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func copyIn(a cipher.AEAD, root *os.Root, dir, rel string) error {
	plain, err := root.ReadFile(rel)
	if err != nil {
		return err
	}
	dst := mirrored(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return writeAtomic(dst, seal(a, plain, fileAAD+rel))
}

// writeAtomic writes b beside path under a temporary name and renames it into place, so a full
// disk or a crash never leaves a half file under a real name.
func writeAtomic(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+tmpMarker+"*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// sweepTemp removes temporary files a killed run left; the lock is held, so none is live.
func sweepTemp(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") && strings.Contains(d.Name(), tmpMarker) {
			return os.Remove(p)
		}
		return nil
	})
}

func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("media: key is %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(a cipher.AEAD, plain []byte, aad string) []byte {
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	return a.Seal(nonce, nonce, plain, []byte(aad))
}

func unseal(a cipher.AEAD, sealed []byte, aad string) ([]byte, error) {
	n := a.NonceSize()
	if len(sealed) < n+a.Overhead() {
		return nil, errors.New("too short")
	}
	return a.Open(nil, sealed[:n], sealed[n:], []byte(aad))
}

// validRel accepts only clean slash paths inside Sources, so no index or archive entry can
// name a path outside the kept subtrees of the media store.
func validRel(rel string) bool {
	if !fs.ValidPath(rel) || strings.Contains(rel, `\`) {
		return false
	}
	top, _, found := strings.Cut(rel, "/")
	return found && slices.Contains(Sources, top)
}

func readIndex(a cipher.AEAD, p string) (Index, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeIndex(a, b)
}

func decodeIndex(a cipher.AEAD, sealed []byte) (Index, error) {
	plain, err := unseal(a, sealed, indexAAD)
	if err != nil {
		return nil, errors.New("media: the index does not open under the media key")
	}
	var idx Index
	if err := json.Unmarshal(plain, &idx); err != nil {
		return nil, fmt.Errorf("media: index: %w", err)
	}
	for rel := range idx {
		if !validRel(rel) {
			return nil, fmt.Errorf("media: index names %q", rel)
		}
	}
	return idx, nil
}

func writeIndex(a cipher.AEAD, p string, idx Index) error {
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return writeAtomic(p, seal(a, b, indexAAD))
}

// writeArchive tars the sealed index, then the sealed mirror files it lists, into dir/name.
// Nothing is decrypted: the archive is exactly as sealed as the mirror.
func writeArchive(dir, name string, idx Index) error {
	f, err := os.CreateTemp(dir, "."+name+tmpMarker+"*")
	if err != nil {
		return err
	}
	err = func() error {
		tw := tar.NewWriter(f)
		if err := addFile(tw, filepath.Join(dir, mirrorDir, indexName), indexName); err != nil {
			return err
		}
		for _, rel := range slices.Sorted(maps.Keys(idx)) {
			if err := addFile(tw, mirrored(dir, rel), mirrorDir+"/"+rel); err != nil {
				return err
			}
		}
		return tw.Close()
	}()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func addFile(tw *tar.Writer, src, member string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// archives lists dir's monthly archives, newest first (YYYY-MM sorts by name).
func archives(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if _, err := time.Parse(archiveFmt, e.Name()); err == nil && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names, nil
}

func pruneArchives(dir string, keep int) error {
	names, err := archives(dir)
	if err != nil {
		return err
	}
	for _, n := range names[min(keep, len(names)):] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

// Restore writes the newest monthly archive, then the mirror, into dst, owned by uid:gid
// (Synapse's user). With write false it only proves that every file opens under key at its own
// path, so a caller can check everything before changing anything. It returns the file count.
func Restore(ctx context.Context, dir string, key []byte, dst string, uid, gid int, write bool) (int, error) {
	a, err := newAEAD(key)
	if err != nil {
		return 0, err
	}
	var out *os.Root
	if write {
		if out, err = os.OpenRoot(dst); err != nil {
			return 0, err
		}
		defer out.Close()
	}
	put := func(rel string, sealed []byte) error {
		plain, err := unseal(a, sealed, fileAAD+rel)
		if err != nil {
			return fmt.Errorf("media: %s does not open at its path", rel)
		}
		if !write {
			return nil
		}
		return place(out, rel, plain, uid, gid)
	}
	names, err := archives(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	indexPath := filepath.Join(dir, mirrorDir, indexName)
	if len(names) == 0 && !exists(indexPath) {
		return 0, ErrNoBackup
	}
	n := 0
	if len(names) > 0 {
		c, err := fromArchive(ctx, filepath.Join(dir, names[0]), a, put)
		n += c
		if err != nil {
			return n, fmt.Errorf("media: %s: %w", names[0], err)
		}
	}
	idx, err := readIndex(a, indexPath)
	if err != nil {
		return n, err
	}
	for _, rel := range slices.Sorted(maps.Keys(idx)) {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		sealed, err := os.ReadFile(mirrored(dir, rel))
		if err != nil {
			return n, fmt.Errorf("media: the mirror lacks %s: %w", rel, err)
		}
		if err := put(rel, sealed); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func fromArchive(ctx context.Context, p string, a cipher.AEAD, put func(string, []byte) error) (int, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != indexName {
		return 0, errors.New("the archive does not start with its index")
	}
	sealed, err := io.ReadAll(tr)
	if err != nil {
		return 0, err
	}
	idx, err := decodeIndex(a, sealed)
	if err != nil {
		return 0, err
	}
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		rel, ok := strings.CutPrefix(hdr.Name, mirrorDir+"/")
		if _, listed := idx[rel]; !ok || !listed || hdr.Typeflag != tar.TypeReg {
			return n, fmt.Errorf("unexpected member %q", hdr.Name)
		}
		sealed, err := io.ReadAll(tr)
		if err != nil {
			return n, err
		}
		if err := put(rel, sealed); err != nil {
			return n, err
		}
		n++
	}
}

// place writes one file through out, the media store's root, so no path escapes it. The file
// and its directories belong to uid:gid; it lands under a temporary name and is renamed.
func place(out *os.Root, rel string, plain []byte, uid, gid int) error {
	dir := path.Dir(rel)
	if err := out.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for d := dir; d != "."; d = path.Dir(d) {
		if err := out.Chown(d, uid, gid); err != nil {
			return err
		}
	}
	tmp := rel + tmpMarker + "restore"
	f, err := out.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(plain)
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = out.Rename(tmp, rel)
	}
	if err != nil {
		out.Remove(tmp)
	}
	return err
}
```

- [ ] **Step 4: Run the tests and confirm they pass.** Run `go test -race ./internal/backup/media/ -v`. Expected: PASS. Then run `go vet ./internal/backup/...`. Expected: clean.

- [ ] **Step 5: DOX.** Create `internal/backup/media/AGENTS.md` with these sections:
- **Purpose:** the encrypted local media mirror and monthly archives.
- **Ownership:** `media.go`.
- **Local Contracts:**
  - The AES-256-GCM file format: nonce prefix, AAD `kymessages-media/v1\x00<path>`, index AAD `kymessages-media-index/v1`.
  - The layout: `mirror/`, `mirror/index`, `full-YYYY-MM.tar` (index first) and `.lock`.
  - Only `local_content`/`local_thumbnails`; symlinks and other subtrees are skipped.
  - The monthly decision is the archive file's existence (UTC month).
  - Pruning happens only after a new archive, with the index first.
  - Temp-then-rename for every write.
  - A run under another key refuses.
  - `Restore` with `write=false` checks everything; with `write=true` it writes through `os.Root` as `uid:gid`.
- **Verification:** `go test -race ./internal/backup/media/`.

In `internal/backup/AGENTS.md`, set its Child DOX Index to `- [media/AGENTS.md](media/AGENTS.md): encrypted media mirror and monthly archives.`.

- [ ] **Step 6: Commit.** Run `git add internal/backup/media internal/backup/AGENTS.md`, then commit with the message `backup/media: encrypted incremental mirror and monthly archives of Synapse media`.

---

### Task 5: Scheduler, `deposit` and status run and report media and capsule size

**Files:**
- Create: `internal/backup/media_run.go`
- Modify: `cmd/server/main.go:62-71` (timeouts), `:203-274` (`runBackup`, `backupTick`, `recordRun`), `:297-324` (`runDeposit`)
- Modify: `docker-compose.yml:9-10`
- Modify: `internal/api/backup_handlers.go:285-362` (`handleRunBackup`), `:479-576` (status)
- Test: `cmd/server/backuploop_test.go`, `internal/api/backup_test.go`, `internal/backup/size_test.go`

**Interfaces:**
- Consumes: `backup.CollectForRun`, `backup.LastSize`, `backup.SizeError`, `backup.DumpTimeout` (Task 3); `media.Run` and `media.Result` (Task 4); `config.BackupConfig.MediaFullKeep` and `config.MatrixConfig.MediaDir` (Task 2).
- Produces:
  - `backup.RunMedia(ctx context.Context, cfg *config.Config, now time.Time) (media.Result, error)` and `backup.ErrNoMediaDir`.
  - The audit action `admin.backup_media` (details: `outcome`, `copied`, `unchanged`, `pruned`, `archive`, `error`).
  - Status JSON gains `capsule_size` (`backup.SizeStatus`) and, with Matrix, `media_last_run` `{outcome, trigger, recorded_at, archive}`.

- [ ] **Step 1: Write the failing tests.** In `cmd/server/backuploop_test.go`, add:

```go
func stubMedia(t *testing.T, fn func() error) *int {
	t.Helper()
	var ran int
	old := runMedia
	t.Cleanup(func() { runMedia = old })
	runMedia = func(context.Context, *config.Config) (media.Result, error) {
		ran++
		return media.Result{Copied: 1}, fn()
	}
	return &ran
}

func latest(t *testing.T, st store.Store, action string) *store.AuditRecord {
	t.Helper()
	rec, err := st.Audit().LatestAuditRecord(context.Background(), action)
	if err != nil {
		t.Fatalf("no %s row: %v", action, err)
	}
	return rec
}

// Media runs after the capsule whatever the capsule's outcome, and is audited on its own row.
func TestBackupTickRunsMediaAfterTheCapsule(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	cfg.Matrix.ServerName = "example.com"
	stubRun(t, func() error { return errors.New("capsule failed") })
	ran := stubMedia(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 1 {
		t.Fatalf("media ran %d times", *ran)
	}
	if !strings.HasPrefix(latest(t, st, mediaRunAction).Details, `outcome="success"`) ||
		!strings.HasPrefix(latest(t, st, backupRunAction).Details, `outcome="failure"`) {
		t.Fatal("capsule and media outcomes not recorded separately")
	}
}

func TestBackupTickMediaFailureLeavesTheCapsuleSuccess(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	cfg.Matrix.ServerName = "example.com"
	stubRun(t, func() error { return nil })
	stubMedia(t, func() error { return errors.New("disk full") })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if d := latest(t, st, mediaRunAction).Details; !strings.HasPrefix(d, `outcome="failure"`) || !strings.Contains(d, "disk full") {
		t.Errorf("media row %q", d)
	}
	if !strings.HasPrefix(latest(t, st, backupRunAction).Details, `outcome="success"`) {
		t.Error("a media failure changed the capsule outcome")
	}
}

func TestBackupTickSkipsMediaWithoutMatrixOrWhenBusy(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	stubRun(t, func() error { return nil })
	ran := stubMedia(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	cfg.Matrix.ServerName = "example.com"
	stubRun(t, func() error { return recoveryclient.ErrInProgress })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 0 {
		t.Fatalf("media ran %d times without Matrix or beside a running backup", *ran)
	}
}
```

Add the imports `errors`, `strings` and `github.com/Busnes-app/ky_server_base/internal/backup/media`.

In `internal/api/backup_test.go`, add:

```go
func TestStatusReportsCapsuleSizeAndMediaRun(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	session := loginAs(t, srv, st, "size-admin", "admin")
	status := statusOf(t, srv, session)
	if _, ok := status["capsule_size"]; ok {
		t.Fatal("size before any run")
	}
	if _, ok := status["media_last_run"]; ok {
		t.Fatal("media result without Matrix")
	}
	ctx := context.Background()
	if err := st.Settings().SetSetting(ctx, backup.LastSizeSetting, strconv.FormatInt(capsule.MaxExpandedBytes*4/5, 10)); err != nil {
		t.Fatal(err)
	}
	cfg.Matrix.ServerName = "example.com"
	if err := st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: "system", Action: "admin.backup_media", Resource: "full-2026-10.tar",
		Details: api.AuditDetails(map[string]any{"outcome": "failure", "error": "private-test-detail"})}); err != nil {
		t.Fatal(err)
	}
	status = statusOf(t, srv, session)
	size, _ := status["capsule_size"].(map[string]any)
	if size["percent"] != float64(80) || size["warning"] != true {
		t.Errorf("capsule_size %v", size)
	}
	m, _ := status["media_last_run"].(map[string]any)
	if m["outcome"] != "failure" || m["trigger"] != "scheduled" || m["archive"] != "full-2026-10.tar" {
		t.Errorf("media_last_run %v", m)
	}
	if raw, _ := json.Marshal(status); bytes.Contains(raw, []byte("private-test-detail")) {
		t.Fatal("status exposed raw media error")
	}
}
```

Add the imports `strconv`, `github.com/Busnes-app/ky-primitives/capsule` and `github.com/Busnes-app/ky_server_base/internal/backup` if they are missing.

In `internal/backup/size_test.go`, add:

```go
func TestRunMediaNeedsADirAndTheKey(t *testing.T) {
	cfg, _ := payloadConfig(t)
	cfg.Matrix = config.MatrixConfig{ServerName: "example.com", MediaDir: t.TempDir()}
	cfg.Backup.MediaFullKeep = 3
	if _, err := backup.RunMedia(context.Background(), cfg, time.Now()); !errors.Is(err, backup.ErrNoMediaDir) {
		t.Fatalf("no KY_BACKUP_DIR: %v", err)
	}
	cfg.Backup.Dir = t.TempDir()
	if _, err := backup.RunMedia(context.Background(), cfg, time.Now()); err == nil || !strings.Contains(err.Error(), "media key") {
		t.Fatalf("no key yet: %v", err)
	}
	if _, err := keyfile.LoadOrCreate(filepath.Join(cfg.Database.DataDir, "media.key"), 32); err != nil {
		t.Fatal(err)
	}
	if res, err := backup.RunMedia(context.Background(), cfg, time.Now()); err != nil || res.Archive == "" {
		t.Fatalf("with key: %+v %v", res, err)
	}
}
```

`size_test.go` then needs the imports `errors`, `path/filepath`, `strings`, `time`, `github.com/Busnes-app/ky-primitives/keyfile` and `github.com/Busnes-app/ky_server_base/internal/config`.

- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./cmd/server/ ./internal/api/ ./internal/backup/ -run 'Media|CapsuleSize|BackupTick' -v`. Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/backup/media_run.go`.**

```go
package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// ErrNoMediaDir is a Matrix deployment with nowhere to mirror media.
var ErrNoMediaDir = errors.New("backup: Matrix media needs KY_BACKUP_DIR")

// RunMedia mirrors Synapse's media into <KY_BACKUP_DIR>/media under the media key, which
// Collect creates and seals: the mirror opens once a capsule holding the key exists.
func RunMedia(ctx context.Context, cfg *config.Config, now time.Time) (media.Result, error) {
	if cfg.Backup.Dir == "" {
		return media.Result{}, ErrNoMediaDir
	}
	key, err := keyfile.Load(mediaKeyFile(cfg), 32)
	if err != nil {
		return media.Result{}, fmt.Errorf("backup: media key (a capsule run creates it): %w", err)
	}
	defer clear(key)
	return media.Run(ctx, cfg.Matrix.MediaDir, filepath.Join(cfg.Backup.Dir, "media"), key, cfg.Backup.MediaFullKeep, now)
}
```

- [ ] **Step 4: Wire `cmd/server/main.go`.** Import `github.com/Busnes-app/ky_server_base/internal/backup/media`. Replace the `backupWaitTimeout` block with:

```go
// backupWaitTimeout bounds the wait for detached backup work. recoveryclient caps one deposit
// at 15 minutes (its uploadTimeout, for a container of at most capsule.MaxContainerBytes,
// 384 MiB); backup.DumpTimeout (3m) covers the Matrix dumps before sealing, and two more
// minutes cover sealing and the local copy. Media mirroring honours shutdown and is not
// counted. docker-compose.yml's stop_grace_period must exceed shutdownTimeout +
// backupWaitTimeout, and TestComposeGracePeriodCoversTheShutdownBudget holds them in step.
const backupWaitTimeout = 20 * time.Minute
```

In `docker-compose.yml`, set `stop_grace_period: 21m` with the comment `# Covers the 5s HTTP drain and the 20m in-flight backup shutdown budget.`.

Replace `runBackup` and its comment with:

```go
// runBackup is recoveryclient.Run for the server capsule; tests replace it.
var runBackup = func(ctx context.Context, cfg *config.Config, rc recoveryclient.RunConfig, s recoveryclient.Settings, client recoveryclient.Depositor) (recoveryclient.Result, error) {
	return recoveryclient.Run(ctx, rc, s, func() (recoveryclient.Payload, error) { return backup.CollectForRun(ctx, cfg, s, appVersion) }, client)
}

// runMedia mirrors Matrix media after a capsule run; tests replace it.
var runMedia = func(ctx context.Context, cfg *config.Config) (media.Result, error) {
	return backup.RunMedia(ctx, cfg, time.Now())
}
```

In `backupTick`, change the comment to `// backupTick runs the server capsule if due, then mirrors Matrix media.`. After `recordRun(runCtx, st, "system", backupRunAction, res, err)`, add:

```go
	if cfg.Matrix.Enabled() {
		// Incremental, so unlike the capsule it stops for shutdown and resumes next run.
		mres, merr := runMedia(ctx, cfg)
		if !errors.Is(merr, context.Canceled) {
			recordMedia(runCtx, st, "system", mres, merr)
		}
	}
```

After `recordRun`, add:

```go
const mediaRunAction = "admin.backup_media"

// recordMedia audits one media run; the status route reads the latest row.
func recordMedia(ctx context.Context, st store.Store, actor string, res media.Result, err error) {
	details := map[string]any{"outcome": "success", "copied": res.Copied, "unchanged": res.Unchanged, "pruned": res.Pruned}
	if res.Archive != "" {
		details["archive"] = res.Archive
	}
	if err != nil {
		details["outcome"] = "failure"
		details["error"] = recoveryclient.AuditSafe(err.Error())
	}
	_ = st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: actor, Action: mediaRunAction, Resource: res.Archive, Details: api.AuditDetails(details)})
	if err != nil {
		log.Printf("[BACKUP] media %s: %s", actor, recoveryclient.AuditSafe(err.Error()))
		return
	}
	log.Printf("[BACKUP] media %s: %d copied, %d unchanged, %d pruned, archive %q", actor, res.Copied, res.Unchanged, res.Pruned, res.Archive)
}
```

In `runDeposit`, replace everything from `res, err := runBackup(...)` to the end with:

```go
	res, err := runBackup(ctx, cfg, rc, backup.Settings(ctx, st.Settings()), client)
	recordRun(ctx, st, "cli", backupRunAction, res, err)
	var merr error
	if cfg.Matrix.Enabled() {
		var mres media.Result
		mres, merr = runMedia(ctx, cfg)
		recordMedia(ctx, st, "cli", mres, merr)
	}
	if err != nil {
		log.Fatalf("Backup: %v", err)
	}
	if res.Receipt != nil {
		log.Printf("✓ Capsule %s deposited at %s; digest %s", res.Manifest.CapsuleID, res.Receipt.DepositedAt.Format(time.RFC3339), res.Receipt.Digest)
	}
	if merr != nil {
		log.Fatalf("Media backup: %v", merr)
	}
```

- [ ] **Step 5: Wire the API.** In `handleRunBackup`, change the collect closure to `return backup.CollectForRun(ctx, s.config, settings, appVersion)`. Change the `ErrCapsuleTooLarge` case to:

```go
		case errors.Is(err, capsule.ErrCapsuleTooLarge):
			// A SizeError names the measured size and the member; nothing secret.
			var se *backup.SizeError
			if errors.As(err, &se) {
				s.writeError(w, http.StatusRequestEntityTooLarge, se.Error())
			} else {
				s.writeError(w, http.StatusRequestEntityTooLarge, recoveryclient.TooLargeMessage)
			}
```

At the end of `scheduleStatus`, add:

```go
	if size, ok, err := backup.LastSize(settings); err == nil && ok {
		out["capsule_size"] = size
	}
	if s.config.Matrix.Enabled() {
		// Media has its own result: its failure never fails the capsule run.
		if last, err := s.store.Audit().LatestAuditRecord(ctx, "admin.backup_media"); err == nil {
			out["media_last_run"] = map[string]any{"outcome": auditOutcome(last.Details), "trigger": auditTrigger(last.UserID),
				"recorded_at": last.CreatedAt, "archive": last.Resource}
		} else if !errors.Is(err, store.ErrNotFound) {
			out["media_last_run_error"] = "Could not read the latest media backup result"
		}
	}
```

Add the helpers and reuse `auditTrigger` in the existing `last_run` block, replacing its inline trigger switch:

```go
// auditOutcome reads the outcome AuditDetails always writes first.
func auditOutcome(details string) string {
	switch {
	case strings.HasPrefix(details, `outcome="success"`):
		return "success"
	case strings.HasPrefix(details, `outcome="failure"`):
		return "failure"
	}
	return "unknown"
}

func auditTrigger(actor string) string {
	switch actor {
	case "system":
		return "scheduled"
	case "cli":
		return "cli"
	}
	return "admin"
}
```

In `cmd/server/main.go`, `internal/backup/payload.go` and the API comments, replace the remaining "people capsule" wording with "server capsule".

- [ ] **Step 6: Run the tests and confirm they pass.** Run `go test -race ./cmd/server/ ./internal/api/ ./internal/backup/... -v`. Expected: PASS, including `TestComposeGracePeriodCoversTheShutdownBudget` (21m > 20m5s) and `TestStatusShowsLatestBackupAttemptWithoutExposingAuditDetails`. Then run `make ci`. Expected: PASS. Finally run `grep -rn "people capsule" --include='*.go' .`. Expected: no output.

- [ ] **Step 7: Commit.** Run `git add cmd/server internal/api internal/backup docker-compose.yml`, then commit with the message `backup: scheduler and deposit mirror Matrix media; status reports capsule size and media result`.

---

### Task 6: `restore` brings back `matrix/`; `restore-matrix` loads a fresh stack

**Files:**
- Modify: `cmd/server/restore.go:70-72` (call), new `prepareRestoredMatrix`
- Create: `internal/backup/matrix_restore.go`, `internal/backup/matrix_restore_test.go`, `cmd/server/restorematrix.go`, `cmd/server/restorematrix_test.go`
- Modify: `cmd/server/main.go:30-57` (subcommand), `docker-compose.matrix.yml` (service), `scripts/check-compose-matrix.sh`
- Test: `cmd/server/restore_test.go`

**Interfaces:**
- Consumes: `openFiles`, `restoreTOC`, `tail` and `mediaKeyFile` (Task 3); `media.Restore` (Task 4).
- Produces:
  - `type backup.MatrixRestore struct{ MatrixDir, MediaDir, BackupDir, DataDir, DBHost string; SkipMedia bool; Relations func(ctx context.Context, host, db, owner, password string) (int, error) }`
  - `(MatrixRestore) Run(ctx context.Context, out io.Writer) error` and `backup.CountRelations(ctx context.Context, host, db, owner, password string) (int, error)`
  - `kymessages restore-matrix [-matrix /matrix] [-media /media] [-backups /app/backups] [-data /app/data] [-db-host postgres] [-skip-media]`
  - The Compose service `restore-matrix` (profile `restore`).

- [ ] **Step 1: Write the failing restore test.** In `cmd/server/restore_test.go`, add:

```go
// restore extracts matrix/ with the rest; Element's config gets back the 0644 its nginx needs,
// everything else stays owner-only.
func TestRestoreBringsBackMatrix(t *testing.T) {
	key, shares := testKit(t)
	dbPath := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	db, _ := os.ReadFile(dbPath)
	path := sealTo(t, key, recoveryclient.Payload{ServiceName: "busnes_app", AppVersion: "1.0.0", Files: []recoveryclient.File{
		{Path: "data/ky_server.db", Data: db, Mode: 0600},
		{Path: "data/encryption.key", Data: []byte(strings.Repeat("01", 32)), Mode: 0600},
		{Path: "matrix/synapse/signing.key", Data: []byte("ed25519 a_abcd seed\n"), Mode: 0600},
		{Path: "matrix/element/config.json", Data: []byte("{}"), Mode: 0600},
		{Path: "matrix/dumps/mas.dump.000", Data: []byte("PGDMP"), Mode: 0600},
	}})
	target := filepath.Join(t.TempDir(), "restored")
	if err := restore(path, target, "busnes_app", shares, io.Discard); err != nil {
		t.Fatal(err)
	}
	for rel, mode := range map[string]os.FileMode{"matrix/element/config.json": 0o644, "matrix/synapse/signing.key": 0o600, "matrix/dumps/mas.dump.000": 0o600} {
		fi, err := os.Stat(filepath.Join(target, rel))
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %v", rel, fi, err, mode)
		}
	}
}
```

Add `io` to the imports if it is missing.

- [ ] **Step 2: Implement it.** In `restore.go`, after the `prepareRestoredData` block, add:

```go
	if err := prepareRestoredMatrix(root); err != nil {
		return errors.Join(fmt.Errorf("restore failed, restored files were removed: %w", err), removeExtracted(root, target, created))
	}
```

Then add the function:

```go
// prepareRestoredMatrix gives Element's config back the 0644 matrix-init wrote: capsules clamp
// every member to owner-only, and Element's nginx reads the file as another user.
func prepareRestoredMatrix(root *os.Root) error {
	if err := root.Chmod("matrix/element/config.json", 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
```

Import `io/fs`. Run `go test ./cmd/server/ -run Restore -v`. Expected: PASS.

- [ ] **Step 3: Write the failing `MatrixRestore` tests.** Create `internal/backup/matrix_restore_test.go`:

```go
package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
)

// restoreFixture is a restored tree with dumps for both databases, a media backup of one file,
// an empty media store, and a fake pg_restore that answers --list and logs every restore.
func restoreFixture(t *testing.T, toc string) (backup.MatrixRestore, string) {
	t.Helper()
	mdir, data, backups, mediaDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for rel, body := range map[string]string{
		"secrets/mas_db_password": "maspw\n", "secrets/synapse_db_password": "synpw\n",
		"dumps/mas.dump.000": "PGDMP mas\n" + toc, "dumps/synapse.dump.000": "PGDMP syn part 0\n",
		"dumps/synapse.dump.001": "; TABLE public events synapse\n",
	} {
		p := filepath.Join(mdir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := keyfile.LoadOrCreate(filepath.Join(data, "media.key"), 32)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "local_content", "ab", "cd"), 0o700)
	_ = os.WriteFile(filepath.Join(src, "local_content", "ab", "cd", "img"), []byte("image bytes"), 0o600)
	if _, err := media.Run(context.Background(), src, filepath.Join(backups, "media"), key, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	fakeTool(t, "pg_restore", fmt.Sprintf(`if [ "$1" = --list ]; then
  IFS= read -r first || [ -n "$first" ] || exit 1
  case $first in PGDMP*) ;; *) echo 'pg_restore: error: not an archive' >&2; exit 1 ;; esac
  while IFS= read -r line; do echo "$line"; done
  exit 0
fi
echo "$* env=$PGPASSWORD" >> %s
`, log))
	r := backup.MatrixRestore{MatrixDir: mdir, MediaDir: mediaDir, BackupDir: backups, DataDir: data, DBHost: "postgres",
		Relations: func(context.Context, string, string, string, string) (int, error) { return 0, nil }}
	return r, log
}

func TestMatrixRestoreRestoresDumpsThenMedia(t *testing.T) {
	r, log := restoreFixture(t, "; TABLE public users mas\n")
	var out bytes.Buffer
	if err := r.Run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--dbname=mas") || !strings.Contains(lines[1], "--dbname=synapse") {
		t.Fatalf("pg_restore calls %q, want mas then synapse", lines)
	}
	for _, want := range []string{"--username=", "--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("pg_restore lacks %s: %q", want, lines[0])
		}
	}
	if args, env, _ := strings.Cut(lines[0], " env="); env != "maspw" || strings.Contains(args, "maspw") || !strings.HasSuffix(lines[1], " env=synpw") {
		t.Errorf("owner passwords must reach pg_restore by PGPASSWORD only: %q", lines)
	}
	if b, err := os.ReadFile(filepath.Join(r.MediaDir, "local_content", "ab", "cd", "img")); err != nil || string(b) != "image bytes" {
		t.Fatalf("media not restored: %q %v", b, err)
	}
	if !strings.Contains(out.String(), "Restored database synapse") || !strings.Contains(out.String(), "media files") {
		t.Errorf("output %q", out.String())
	}
}

func TestMatrixRestoreRefusesANonEmptyStack(t *testing.T) {
	r, log := restoreFixture(t, "")
	r.Relations = func(_ context.Context, _, db, _, _ string) (int, error) {
		if db == "synapse" {
			return 7, nil
		}
		return 0, nil
	}
	err := r.Run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already holds 7 relations") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, r, log, 0)
}

// assertUntouched: no pg_restore ran and the media store still holds only its seeded entries.
func assertUntouched(t *testing.T, r backup.MatrixRestore, log string, seeded int) {
	t.Helper()
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("pg_restore ran after a failed check")
	}
	if entries, _ := os.ReadDir(r.MediaDir); len(entries) != seeded {
		t.Errorf("media store written after a failed check: %v", entries)
	}
}

func TestMatrixRestoreRefusesBeforeWriting(t *testing.T) {
	for name, spoil := range map[string]func(r backup.MatrixRestore) int{
		"stray part": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MatrixDir, "dumps", "mas.dump.002"), []byte("x"), 0o600)
			return 0
		},
		"missing dump": func(r backup.MatrixRestore) int {
			_ = os.Remove(filepath.Join(r.MatrixDir, "dumps", "mas.dump.000"))
			return 0
		},
		"corrupt dump": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MatrixDir, "dumps", "synapse.dump.000"), []byte("garbage\n"), 0o600)
			return 0
		},
		"media store not empty": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MediaDir, "already-here"), nil, 0o600)
			return 1
		},
		"swapped media": func(r backup.MatrixRestore) int {
			m := filepath.Join(r.BackupDir, "media")
			full, _ := filepath.Glob(filepath.Join(m, "full-*.tar"))
			for _, f := range full {
				_ = os.Remove(f)
			}
			_ = os.WriteFile(filepath.Join(m, "mirror", "local_content", "ab", "cd", "img"), []byte("0123456789012345678901234567890123"), 0o600)
			return 0
		},
		"no media key": func(r backup.MatrixRestore) int {
			_ = os.Remove(filepath.Join(r.DataDir, "media.key"))
			return 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, log := restoreFixture(t, "")
			seeded := spoil(r)
			if err := r.Run(context.Background(), io.Discard); err == nil || !strings.HasPrefix(err.Error(), "refused: ") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			assertUntouched(t, r, log, seeded)
		})
	}
}

func TestMatrixRestoreRefusesAnExtension(t *testing.T) {
	r, log := restoreFixture(t, "; 3; 3079 16385 EXTENSION - pg_trgm\n")
	if err := r.Run(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "extension") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, r, log, 0)
}

func TestMatrixRestoreSkipMedia(t *testing.T) {
	r, log := restoreFixture(t, "")
	r.SkipMedia = true
	_ = os.Remove(filepath.Join(r.DataDir, "media.key"))
	var out bytes.Buffer
	if err := r.Run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 2 || !strings.Contains(out.String(), "Media skipped") {
		t.Fatalf("calls %q, out %q", calls, out.String())
	}
}

// CountRelations sees a table created in any user schema. Needs KY_TEST_POSTGRES_DSN.
func TestCountRelationsSeesUserTables(t *testing.T) {
	dsn := os.Getenv("KY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KY_TEST_POSTGRES_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	ctx := context.Background()
	before, err := backup.CountRelations(ctx, u.Hostname(), db, u.User.Username(), pw)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema := fmt.Sprintf("countrel_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+schema+"; CREATE TABLE "+schema+".t (id int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	if after, err := backup.CountRelations(ctx, u.Hostname(), db, u.User.Username(), pw); err != nil || after != before+1 {
		t.Fatalf("before %d, after %d (%v)", before, after, err)
	}
}
```

- [ ] **Step 4: Run them and confirm they fail.** Run `go test ./internal/backup/ -run 'MatrixRestore|CountRelations' -v`. Expected: FAIL to compile.

- [ ] **Step 5: Implement `internal/backup/matrix_restore.go`.**

```go
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// MatrixRestore loads a server capsule's Matrix half into a fresh stack: the dumps from the
// restored matrix/, then media from the local backup directory. Run checks everything first and
// changes nothing unless every check passes.
type MatrixRestore struct {
	MatrixDir string // restored matrix-init directory, holding dumps/ and secrets/
	MediaDir  string // Synapse's media store; must be empty
	BackupDir string // KY_BACKUP_DIR, holding media/
	DataDir   string // restored data directory, holding media.key
	DBHost    string
	SkipMedia bool
	// Relations counts a database's user relations; nil means CountRelations. Tests replace it.
	Relations func(ctx context.Context, host, db, owner, password string) (int, error)
}

// restoreDBs run in dump order, each as its owner (matrix-init's init.sql).
var restoreDBs = []struct{ db, secret string }{{"mas", "mas_db_password"}, {"synapse", "synapse_db_password"}}

func (r MatrixRestore) Run(ctx context.Context, out io.Writer) error {
	relations := r.Relations
	if relations == nil {
		relations = CountRelations
	}
	type plan struct {
		db, password string
		parts        []string
	}
	var plans []plan
	for _, d := range restoreDBs {
		pw, err := os.ReadFile(filepath.Join(r.MatrixDir, "secrets", d.secret))
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		parts, err := dumpFiles(filepath.Join(r.MatrixDir, "dumps"), d.db)
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		toc, err := listFiles(ctx, parts)
		if err != nil {
			return fmt.Errorf("refused: the %s dump: %w", d.db, err)
		}
		if strings.Contains(toc, " EXTENSION ") {
			return fmt.Errorf("refused: the %s dump creates an extension, which its owner cannot restore", d.db)
		}
		password := strings.TrimSpace(string(pw))
		n, err := relations(ctx, r.DBHost, d.db, d.db, password)
		if err != nil {
			return fmt.Errorf("refused: database %s: %w", d.db, err)
		}
		if n != 0 {
			return fmt.Errorf("refused: database %s already holds %d relations; restore into a fresh stack (down -v, then up -d postgres only)", d.db, n)
		}
		plans = append(plans, plan{d.db, password, parts})
	}
	var key []byte
	var uid, gid int
	if !r.SkipMedia {
		entries, err := os.ReadDir(r.MediaDir)
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		if len(entries) != 0 {
			return fmt.Errorf("refused: media store %s is not empty; restore into a fresh stack", r.MediaDir)
		}
		if key, err = keyfile.Load(filepath.Join(r.DataDir, "media.key"), 32); err != nil {
			return fmt.Errorf("refused: media key: %w", err)
		}
		defer clear(key)
		fi, err := os.Stat(r.MatrixDir)
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		// Synapse runs as the owner of ./matrix (KY_MATRIX_UID), so restored media is theirs.
		st := fi.Sys().(*syscall.Stat_t)
		uid, gid = int(st.Uid), int(st.Gid)
		n, err := media.Restore(ctx, filepath.Join(r.BackupDir, "media"), key, r.MediaDir, uid, gid, false)
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		fmt.Fprintf(out, "Checked %d media files\n", n)
	}
	for _, p := range plans {
		if err := pgRestore(ctx, r.DBHost, p.db, p.password, p.parts); err != nil {
			return fmt.Errorf("restoring %s failed; its transaction rolled back, but earlier databases are restored: start again from a fresh stack: %w", p.db, err)
		}
		fmt.Fprintf(out, "Restored database %s from %d parts\n", p.db, len(p.parts))
	}
	if r.SkipMedia {
		fmt.Fprintln(out, "Media skipped (-skip-media): uploads from before the backup are missing")
		return nil
	}
	n, err := media.Restore(ctx, filepath.Join(r.BackupDir, "media"), key, r.MediaDir, uid, gid, true)
	if err != nil {
		return fmt.Errorf("media restore failed after the databases were restored; start again from a fresh stack: %w", err)
	}
	fmt.Fprintf(out, "Restored %d media files\n", n)
	return nil
}

// dumpFiles returns db's parts in dir, <db>.dump.000 upwards, refusing a gap or a stray part.
func dumpFiles(dir, db string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, db+".dump.*"))
	if err != nil {
		return nil, err
	}
	var parts []string
	for i := 0; ; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s.dump.%03d", db, i))
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			break
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 || len(parts) != len(matches) {
		return nil, fmt.Errorf("the %s dump parts in %s are missing or not contiguous from .000", db, dir)
	}
	return parts, nil
}

func listFiles(ctx context.Context, parts []string) (string, error) {
	rd, closeAll, err := openFiles(parts)
	if err != nil {
		return "", err
	}
	defer closeAll()
	return restoreTOC(ctx, rd)
}

// pgRestore restores one database as its owner in one transaction. The parts are joined into a
// private temporary file first: pg_restore can seek in a file, not in a pipe.
func pgRestore(ctx context.Context, host, db, password string, parts []string) error {
	tmp, err := os.CreateTemp("", "kymessages-"+db+"-*.dump")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	rd, closeAll, err := openFiles(parts)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, rd)
	closeAll()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "pg_restore", "--host="+host, "--username="+db, "--dbname="+db, "--no-password",
		"--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error", tmp.Name())
	cmd.Env = []string{"PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=10"}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore %s: %w: %s", db, err, stderr.String())
	}
	return nil
}

// CountRelations counts relations outside the system schemas, connecting as owner.
func CountRelations(ctx context.Context, host, db, owner, password string) (int, error) {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(owner, password), Host: net.JoinHostPort(host, "5432"),
		Path: "/" + db, RawQuery: "connect_timeout=10"}
	conn, err := sql.Open("pgx", u.String())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var n int
	err = conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`).Scan(&n)
	return n, err
}
```

Check that a pgx connect error never includes the password: run `TestCountRelationsSeesUserTables` with a wrong password, as a temporary local edit, and read the error. If it does include the password, wrap it as `fmt.Errorf("cannot connect to %s as %s", db, owner)`.

- [ ] **Step 6: Run the tests and confirm they pass.** Run `go test -race ./internal/backup/ -v`. Expected: PASS, with `TestCountRelationsSeesUserTables` SKIPPED without a DSN. Then run `make test-postgres` if a local Postgres exists. Expected: PASS.

- [ ] **Step 7: The CLI command.** Create `cmd/server/restorematrix.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Busnes-app/ky_server_base/internal/backup"
)

func runRestoreMatrix(args []string) {
	r, err := parseRestoreMatrix(args, os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if err := r.Run(context.Background(), os.Stdout); err != nil {
		log.Fatalf("restore-matrix: %v", err)
	}
}

// parseRestoreMatrix takes the defaults the restore-matrix Compose service mounts.
func parseRestoreMatrix(args []string, out io.Writer) (backup.MatrixRestore, error) {
	var r backup.MatrixRestore
	fs := flag.NewFlagSet("restore-matrix", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&r.MatrixDir, "matrix", "/matrix", "restored matrix-init directory (holds dumps/ and secrets/)")
	fs.StringVar(&r.MediaDir, "media", "/media", "Synapse's media store; must be empty")
	fs.StringVar(&r.BackupDir, "backups", "/app/backups", "local backup directory holding media/")
	fs.StringVar(&r.DataDir, "data", "/app/data", "restored data directory holding media.key")
	fs.StringVar(&r.DBHost, "db-host", "postgres", "Postgres host")
	fs.BoolVar(&r.SkipMedia, "skip-media", false, "restore the databases only (no media backup survived)")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: docker compose run --rm restore-matrix [-skip-media]\n\nRun once, after `kymessages restore`, against a fresh stack with only postgres up.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return r, err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return r, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return r, nil
}
```

In `main.go`'s switch, add `case "restore-matrix": runRestoreMatrix(os.Args[2:]); return`. Create `cmd/server/restorematrix_test.go`:

```go
package main

import (
	"io"
	"testing"
)

func TestRestoreMatrixFlags(t *testing.T) {
	r, err := parseRestoreMatrix(nil, io.Discard)
	if err != nil || r.MatrixDir != "/matrix" || r.MediaDir != "/media" || r.BackupDir != "/app/backups" || r.DataDir != "/app/data" || r.DBHost != "postgres" || r.SkipMedia {
		t.Fatalf("defaults %+v %v", r, err)
	}
	for _, args := range [][]string{{"extra"}, {"-bogus"}} {
		if _, err := parseRestoreMatrix(args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
```

- [ ] **Step 8: The Compose service and its check.** Write the failing check first. In `scripts/check-compose-matrix.sh`, before `exit $fail`, add:

```bash
# restore-matrix: a one-shot under the restore profile, the only writer of the media volume
# besides Synapse; everything else it mounts is read-only.
jq -e '.services | has("restore-matrix") | not' <<<"$out" >/dev/null || bad "restore-matrix runs without --profile restore"
rs=$(render "${stack[@]}" --profile restore) || { echo "restore profile does not compose"; exit 1; }
r=$(jq -c '.services["restore-matrix"]' <<<"$rs")
[ "$(jq -c '[.entrypoint, (.networks | keys), .cap_drop, (.cap_add | sort), .security_opt, .profiles, .pull_policy, .depends_on.postgres.condition]' <<<"$r")" \
  = '[["/app/kymessages","restore-matrix"],["matrix-db"],["ALL"],["CHOWN","DAC_OVERRIDE"],["no-new-privileges:true"],["restore"],"never","service_healthy"]' ] \
  || bad "restore-matrix is not locked down: $r"
want=$(printf '%s\n' "/app/backups	$root/backups	true" "/app/data	$root/data	true" "/matrix	$root/matrix	true" "/media	matrix-media	false")
[ "$(jq -r '.volumes[] | [.target, .source, (.read_only // false)] | @tsv' <<<"$r" | sort)" = "$want" ] \
  || bad "restore-matrix mounts: $(jq -c .volumes <<<"$r")"
```

Run `bash scripts/check-compose-matrix.sh`. Expected: `restore profile does not compose` or `restore-matrix is not locked down`, and exit 1. Then add the service to `docker-compose.matrix.yml` after `element`:

```yaml
  # One-shot, after `kymessages restore`, against a fresh stack with only postgres up:
  #   docker compose run --rm restore-matrix
  # Loads the restored dumps as their owners and writes media back as the owner of ./matrix.
  # The only writer of the media volume besides Synapse; everything else is read-only.
  restore-matrix:
    profiles: [restore]
    image: ${KY_IMAGE:-kymessages:local}
    pull_policy: never
    entrypoint: ["/app/kymessages", "restore-matrix"]
    security_opt: ["no-new-privileges:true"]
    cap_drop: [ALL]
    # Root writes into the KY_MATRIX_UID-owned volume and hands each file to that user.
    cap_add: [CHOWN, DAC_OVERRIDE]
    networks: [matrix-db]
    volumes:
      - ./data:/app/data:ro
      - ./backups:/app/backups:ro
      - ./matrix:/matrix:ro
      - matrix-media:/media
    depends_on:
      postgres: {condition: service_healthy}
```

Run `bash scripts/check-compose-matrix.sh && shellcheck scripts/*.sh`. Expected: pass. Confirm that the existing per-service loops did not start iterating `restore-matrix`; they render without the profile.

- [ ] **Step 9: Run everything.** Run `go test -race ./cmd/server/ ./internal/backup/... -v` and then `make ci`. Expected: PASS. `TestNothingInTheServerDecrypts` must still pass: media AES-GCM is not a forbidden selector.

- [ ] **Step 10: Commit.** Run `git add cmd/server internal/backup docker-compose.matrix.yml scripts/check-compose-matrix.sh`, then commit with the message `restore: matrix/ comes back with the capsule; restore-matrix loads dumps and media into a fresh stack`.

---

### Task 7: Acceptance: backup, lose the host, restore, read history

**Files:**
- Create: `scripts/matrix-acceptance/suitekey/main.go` (harness only)
- Modify: `scripts/matrix-acceptance.sh` (new steps after `admin-isolation`, before `reproduce`; header comment), `scripts/matrix-acceptance/e2e.mjs`, `scripts/matrix-acceptance/overrides/app.yml`
- Modify: `.github/workflows/ci.yml:160` (`timeout-minutes`)

**Interfaces:**
- Consumes: everything above; the KyMessages admin API (`/api/auth/login`, `/api/auth/change-password`, `/api/backup/schedule`, `/api/backup/pin-key`, `/api/backup/status`); `kymessages deposit`, `backup-drill`, `restore` and `restore-matrix`.

Read first: the whole of `scripts/matrix-acceptance.sh` and `e2e.mjs`, and `scripts/backup-acceptance.py:58-97` for the CSRF, password-change and pin flow.

- [ ] **Step 1: Throwaway suite key.** Create `scripts/matrix-acceptance/suitekey/main.go`:

```go
// Command suitekey makes the acceptance harness's throwaway suite recovery key. It prints the
// public key in base64 (what the ceremony page shows) and writes two of three custodian shares,
// one per line, 0600, to the file it is given. Harness only; never part of a deployment.
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/Busnes-app/ky-primitives/recoverykey"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: suitekey <shares-file>")
		os.Exit(2)
	}
	priv, err := recoverykey.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Non-consecutive shares, as the restore tests use.
	if err := os.WriteFile(os.Args[1], []byte(shares[0].String()+"\n"+shares[2].String()+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().Bytes()))
}
```

Confirm `go vet ./...` stays clean and `TestNothingInTheServerDecrypts` still passes (`Generate` and `Split` are not forbidden).

- [ ] **Step 2: Harness app override.** In `overrides/app.yml`:
- Add `environment: {KY_CAPTCHA_PROVIDER: none}`, with the comment `# Harness login without proof-of-work; never in a deployment.`.
- Change `volumes: !override` to list `app-data:/app/data`, `app-backups:/app/backups`, `./matrix:/matrix:ro` and `matrix-media:/matrix-media:ro`. `!override` replaces the overlay's mounts too, so all four must be listed.
- Declare `app-backups:` under `volumes:`.
- Add a `restore-matrix:` service override with only `image: ${KYMATRIX_ACCEPT_APP_IMAGE:?}`. Its shipped `./data`, `./backups` and `./matrix` binds then resolve under the scratch project directory, which this harness fills with user-owned files.

- [ ] **Step 3: e2e changes.**
- In `prove`, keep both recovery keys: `fs.writeFileSync(\`${dir}/state/alice.recovery\`, await setUpRecovery(alice), { mode: 0o600 })`, and the same for bob.
- Generalise `aliceSession` into `resume(name, profile)` (the same take-over logic) and keep `aliceSession = () => resume('alice', 'prove-alice')`.
- Give `backedUp` a `who` parameter used only in its message, defaulting to `user`.
- Add the scenarios below and register them in `scenarios`:

```js
// A 1x1 PNG: Element uploads it encrypted, so Synapse's local_content holds ciphertext.
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

async function decryptedImage(page) {
  const img = timeline(page).locator('.mx_MImageBody img').last();
  await img.waitFor({ timeout: 60000 });
  await page.waitForFunction((el) => el.complete && el.naturalWidth > 0, await img.elementHandle(), { timeout: 60000 });
  console.log('  ok: image decrypted and shown');
}

// Bob sends an image into the DM; alice sees it; alice's key backup then holds the DM's keys.
async function media() {
  const { rooms: { dm } } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  const bob = await resume('bob', 'prove-bob');
  await openRoom(bob, dm, false);
  await bob.locator('input[type="file"]').first().setInputFiles({ name: `kymatrix-${tag}.png`, mimeType: 'image/png', buffer: Buffer.from(PNG, 'base64') });
  await bob.getByRole('dialog').getByRole('button', { name: 'Upload' }).click();
  await bob.locator('.mx_EventTile[data-event-id^="$"] .mx_MImageBody').last().waitFor();
  const alice = await aliceSession();
  await openRoom(alice, dm, false);
  await decryptedImage(alice);
  await backedUp(alice, dm, 'alice');
  out({ dm });
}

// After the restore: alice on a new device confirms her identity with her recovery key, then
// reads bob's earlier message and image from key backup, and sends one message.
async function restored() {
  const { rooms: { dm }, messages } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  const page = await launch('alice', 'restored-alice');
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await kyidentityLogin(page, 'Alice.Q@Ky');
  await page.getByRole('button', { name: 'Use recovery key', exact: true }).click();
  const dlg = page.getByRole('dialog');
  await dlg.locator('input, textarea').first().fill(fs.readFileSync(`${dir}/state/alice.recovery`, 'utf8'));
  await dlg.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  const mxid = await signedIn(page, `@alice.q_ky:${SERVER}`);
  await openRoom(page, dm, false);
  await sees(page, messages[1]); // prove's dm2, bob's DM message
  await decryptedImage(page);
  const after = text('after-restore');
  await send(page, after);
  // The reproduction step's native-session control needs a live token.
  fs.writeFileSync(`${dir}/state/alice.token`, await page.evaluate(() => window.mxMatrixClientPeg.get().getAccessToken()), { mode: 0o600 });
  out({ mxid, after });
}
```

Verify the upload selectors against Element v1.12.30 the first time this runs. If `input[type="file"]` or the `Upload` dialog differ, use the trace in the artifacts and adjust the selectors only; do not weaken the `naturalWidth > 0` check.

- [ ] **Step 4: The `backup` step.** Insert it after `admin-isolation`. Helpers:

```bash
jar=$state/app.cookies
# app_api METHOD PATH [JSON]: the KyMessages admin API as the browser calls it (CSRF header from the cookie).
app_api() {
	local csrf body=()
	csrf=$(awk '$6 == "ky_csrf" { print $7 }' "$jar" 2>/dev/null || true)
	[[ $# -ge 3 ]] && body=(-d "$3")
	hcurl -fsS -b "$jar" -c "$jar" -X "$1" -H "Origin: $KY_APP_URL" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" "${body[@]}" "$KY_APP_URL$2"
}
kyb() { dc exec -T -e PGPASSWORD="$(cat "$scratch/matrix/secrets/kybackup_db_password")" postgres psql -h 127.0.0.1 -U kybackup -v ON_ERROR_STOP=1 "$@"; }
```

Step body, in order:
1. `e2e media`.
2. The upgrade path is idempotent: `dc exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql >/dev/null`, then `ok "kybackup-role.sql re-applied on a running stack"`.
3. Role limits:
   - `expect "$(kyb -d synapse -Atc 'SELECT count(*) > 0 FROM users')" t "kybackup reads synapse"`.
   - `kyb -d postgres -c 'SELECT 1'` must fail.
   - `kyb -d synapse -c 'CREATE TABLE kyb_probe ()'` must fail.
   - `kyb -d mas -c "UPDATE users SET locked_at = now()"` must fail.
   - Write each refusal as `if kyb …; then echo "  FAILED: …" >&2; false; fi; ok "…"`.
4. Admin setup:
   - `app_api POST /api/auth/login "$(jq -n --arg p "$KY_ADMIN_PASSWORD" '{username: "admin", password: $p}')"`.
   - Change the password to a new `openssl rand -hex 16` value, then log in again with it.
   - `app_api PUT /api/backup/schedule '{"interval_sec": 0}'`, so the scheduler cannot race the CLI.
   - `pub=$(go run -C "$repo" ./scripts/matrix-acceptance/suitekey "$state/shares")`.
   - `app_api POST /api/backup/pin-key "$(jq -n --arg k "$pub" '{public_key: $k, threshold: 2, total_shares: 3}')"`.
5. `dc exec -T app /app/kymessages deposit` (exit 0). Then run `dc exec -T app /app/kymessages backup-drill | tee "$state/drill.out"`, and grep `Status:   PASSED`, `Postgres Dump: matrix/dumps/mas.dump` and `Postgres Dump: matrix/dumps/synapse.dump`.
6. Status: `app_api GET /api/backup/status >"$state/status.json"` and `jq -e '.capsule_size.bytes > 0 and .capsule_size.warning == false and .media_last_run.outcome == "success"' "$state/status.json"`.
7. The media mirror is ciphertext:
   - `mid=$(sql synapse "SELECT media_id FROM local_media_repository WHERE user_id = '@bob:$KY_MATRIX_SERVER_NAME' ORDER BY created_ts DESC LIMIT 1")` and `rel="local_content/${mid:0:2}/${mid:2:2}/${mid:4}"`.
   - `plain=$(dc exec -T synapse stat -c %s "/media/$rel")`, then `expect "$(dc exec -T app stat -c %s "/app/backups/media/mirror/$rel")" $((plain + 28)) "bob's image mirrored as AES-GCM ciphertext"`.
   - `media_sum=$(dc exec -T synapse sha256sum "/media/$rel" | cut -d' ' -f1)`. Assert the mirror's sha256 differs from it.
   - `dc exec -T app test -f "/app/backups/media/full-$(date -u +%Y-%m).tar"` (the month's archive, made by the first run).
8. Evidence for later: `cp "$scratch/matrix/synapse/signing.key" "$state/signing.key"`; `keyid=$(awk '{print $2}' "$state/signing.key")`; the `dm` and `group` room IDs come from `prove.json`.
9. `pass`.

- [ ] **Step 5: The `restore` step.**
1. Copy the backups off the host: `dc cp app:/app/backups "$state/backups"`. Assert exactly one `*.kycap`: `caps=("$state"/backups/*.kycap); expect "${#caps[@]}" 1 "one sealed capsule"`.
2. Lose the host (KyIdentity survives):

```bash
dc rm -sfv app element synapse mas postgres synapse-media-owner >/dev/null
for v in matrix-postgres matrix-media app-data app-backups; do docker volume rm "${project}_$v" >/dev/null; done
expect "$(docker volume ls -q --filter "label=com.docker.compose.project=$project" | grep -cE '_(matrix-postgres|matrix-media|app-data|app-backups)$' || true)" 0 "Matrix and app volumes gone"
mv "$scratch/matrix" "$state/matrix.before"
```

3. Restore: `"$scratch/kymessages" restore -capsule "${caps[0]}" -to "$scratch/restored" -service KyMessages <"$state/shares" | tee "$state/restore.out"`. Then:
   - `mv "$scratch/restored/matrix" "$scratch/matrix"`, `mv "$scratch/restored/data" "$scratch/data"` and `mv "$state/backups" "$scratch/backups"`.
   - Assert `cmp "$state/signing.key" "$scratch/matrix/synapse/signing.key"`.
   - Assert `diff -r "$state/matrix.before/secrets" "$scratch/matrix/secrets"`.
   - Assert `stat -c %a "$scratch/matrix/element/config.json"` is `644`.
4. Fresh stack, database only: `dc up -d --wait postgres`. Then `dc create app` and `dc cp "$scratch/data/." app:/app/data/`.
5. `dc run --rm -T restore-matrix | tee "$state/restore-matrix.out"`. Grep `Restored database mas`, `Restored database synapse` and `Restored [1-9][0-9]* media files`.
6. One-time keys excluded: `expect "$(sql synapse 'SELECT count(*) FROM e2e_one_time_keys_json')" 0 "no one-time keys restored"`.
7. Media bytes back: `expect "$(dc run --rm --no-deps -T --entrypoint sha256sum synapse "/media/$rel" | cut -d' ' -f1)" "$media_sum" "bob's image restored byte for byte"`.
8. A second run refuses and changes nothing: `if dc run --rm -T restore-matrix >"$state/again.out" 2>&1; then echo "  FAILED: restore-matrix ran twice" >&2; false; fi; grep -q 'refused: database mas already holds' "$state/again.out"`. Then compare the sha256 again.
9. Start the stack: `dc up -d --quiet-pull --wait --wait-timeout 300 element`, `dc up -d app`, `ready "$KY_APP_URL/.well-known/matrix/client"` and `ready https://matrix.kymatrix.test/_matrix/client/versions`.
10. Users: `e2e restored`. Then:
    - Bob is intact: `expect "$(mas_user bob 'locked_at IS NULL AND deactivated_at IS NULL')" t "bob's MAS account intact"`.
    - For each of `dm` and `group`: `expect "$(sql synapse "SELECT membership FROM local_current_membership WHERE user_id = '@bob:$KY_MATRIX_SERVER_NAME' AND room_id = '$r'")" join "bob still in $r"`.
11. Server name and signing key:
    - `expect "$(hcurl -fsS -H "Authorization: Bearer $(cat "$state/alice.token")" https://matrix.kymatrix.test/_matrix/client/v3/account/whoami | jq -r .user_id)" "@alice.q_ky:$KY_MATRIX_SERVER_NAME" "server name unchanged"`.
    - `sql synapse "SELECT json FROM event_json j JOIN events e USING (event_id) WHERE e.sender = '@alice.q_ky:$KY_MATRIX_SERVER_NAME' ORDER BY e.stream_ordering DESC LIMIT 1" | grep -qF "\"ed25519:$keyid\""`, then `ok "new events signed with the restored key $keyid"`.
12. `pass`.

Update the header comment (lines 2-7) with one sentence: the run also takes a server backup, loses the host, and restores the whole stack from custodian shares, proving history, media, accounts, the server name and the signing key come back.

- [ ] **Step 6: Run it.** Run `make matrix-acceptance`. Expected: every step PASS, including the earlier ones; `reproduce` (in CI) and `no-username` now run on the restored stack. If `restore-matrix` fails in `pg_restore` with a permission or extension error, stop. Record the exact error and the dump's `pg_restore --list` lines that need privileges, and report BLOCKED (open question 3). Do not switch to the superuser.

- [ ] **Step 7: CI and lint.** Raise `matrix-acceptance`'s `timeout-minutes` from 30 to 45, with a comment giving the measured local duration. Run `shellcheck scripts/*.sh scripts/matrix-acceptance/*.sh`. Expected: clean.

- [ ] **Step 8: Commit.** Run `git add scripts .github/workflows/ci.yml`, then commit with the message `matrix acceptance: back up, lose the host, restore from custodian shares, read history`.

---

### Task 8: Docs and DOX pass

**Files:**
- Modify: `docs/RESTORE.md`, `README.md` (Matrix section, upgrade, settings table), `docs/CHAT-PLATFORM-OPTIONS.md:89,100`
- Modify: `AGENTS.md` (root), `internal/backup/AGENTS.md`, `internal/config/AGENTS.md`, `internal/matrixinit/AGENTS.md`, `internal/api/AGENTS.md`

- [ ] **Step 1: `docs/RESTORE.md`.**
- Rewrite "What recovery can restore" as the server capsule:
  - The existing four files.
  - `data/media.key`.
  - `matrix/` (secrets, signing key, Synapse/MAS/Element configs, Postgres init SQL).
  - `matrix/dumps/{mas,synapse}.dump.NNN` (MAS first, seconds apart; one-time keys excluded per Synapse's guide).
  - Media lives outside the capsule, in `KY_BACKUP_DIR/media`: an encrypted mirror plus `full-YYYY-MM.tar`. Copy that directory off the host with the capsules; only the capsule's `data/media.key` opens it.
- Replace the 64 MiB sentence with the 256 MiB expanded limit, the 75% warning, and "allow about 2 GiB of memory for the app near the limit (estimate)".
- Add a "Restore the Matrix stack" section after "Restore offline":
  1. Run the restore as the unprivileged user that will own `./matrix`.
  2. Move `restored/data` to `./data`, `restored/matrix` to `./matrix`, and the backup directory copy to `./backups`.
  3. Set `.env` as before (`KY_MATRIX_UID/GID` = that user).
  4. Build the image (build overlay).
  5. `docker compose up -d postgres` only.
  6. `docker compose run --rm restore-matrix` (`-skip-media` only if no media backup survived).
  7. `docker compose up -d`.
  8. Delete `./matrix/dumps`.
  9. Members sign in on a new device and restore keys from key backup with their own recovery key.
- Refusals: a non-empty database or media store, a gap in the dump parts, a dump with an extension, or a media file that does not open at its path. All are refused before anything is written. A failure after the checks means `docker compose down -v` and start again.
- Update Verification: `go test` covers the split, limits, media and restore refusals; `make matrix-acceptance` proves the full cycle.

- [ ] **Step 2: README.**
- In the Matrix section, replace "Not built yet: Matrix backups and…" with a short "Backups" paragraph:
  - The scheduled capsule includes Matrix.
  - Media is mirrored to `KY_BACKUP_DIR`; copy it off the host.
  - Status shows capsule size, the 75% warning and the last media run.
  - "Run now" seals the capsule only; media follows the schedule or `kymessages deposit`.
  - Restore: see RESTORE.md.
- Add the upgrade path from the offboarding stack:
  1. Re-run `./kymessages matrix-init` (adds `kybackup_db_password` and `postgres/kybackup-role.sql`).
  2. Run `docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql`.
  3. Rebuild the image.
  4. Optionally set `KY_BACKUP_MEDIA_FULL_KEEP` and keep `KY_BACKUP_DIR` set.
  5. `docker compose up -d`.
- Add `KY_BACKUP_MEDIA_FULL_KEEP` to the settings table.
- Replace "The backup is one people capsule (accounts and settings)" with the server capsule wording.

- [ ] **Step 3: `docs/CHAT-PLATFORM-OPTIONS.md`.**
- Line 89, KyMessages column: "Server capsule: KyMessages database, Synapse and MAS dumps (one-time keys excluded), Matrix config, secrets and signing key. Media in an encrypted local mirror with monthly archives. Restore proven by `make matrix-acceptance`. [RESTORE](RESTORE.md)". This removes the retired messages-capsule text.
- Line 100: "`Collect` adds the Synapse and MAS dumps (one-time keys excluded), Matrix config and secrets; media is mirrored outside the capsule".

- [ ] **Step 4: AGENTS.md files.**
- Root `AGENTS.md`:
  - The Matrix compose bullet: the app joins `matrix-db` and mounts `./matrix` and `matrix-media` read-only; the `kybackup_db_password` secret; the `restore-matrix` service under profile `restore`; the compose check enforces the pg_dump major.
  - The `cmd/server` paragraph:
    - "server capsule".
    - `backupTick` runs media after the capsule (honours shutdown; `admin.backup_media`).
    - `deposit` also runs media.
    - `backupWaitTimeout` is 20m (15m deposit + 3m dumps + 2m).
    - `stop_grace_period` is 21m.
  - The restore paragraph: `restore` also extracts `matrix/` (Element config back to 0644), and `restore-matrix` refuses a non-empty stack.
  - The "Open:" list drops Matrix backups.
  - The Verification `matrix-acceptance` bullet adds the backup and restore cycle.
- `internal/backup/AGENTS.md`:
  - Matrix members, dumps, `DumpTimeout`, `memberBytes` and the 1 MiB reserve (the v0.8.0 Seal/Open mismatch), `SizeError`, `CollectForRun`, `LastSize`, `RunMedia`, `MatrixRestore` and the drill's `pg_dumps` checks.
  - Delete the "people capsule" sentence.
- `internal/config/AGENTS.md`: the four new Matrix variables, `KY_MATRIX_DB_HOST` validation and `KY_BACKUP_MEDIA_FULL_KEEP`.
- `internal/matrixinit/AGENTS.md`: the layout gains `postgres/kybackup-role.sql` (sorts after `init.sql`, idempotent, the upgrade file) and `secrets/kybackup_db_password`; the container contract gains the app's read-only mounts.
- `internal/api/AGENTS.md:26`: "Backup handlers seal the server capsule; status adds `capsule_size` and, with Matrix, `media_last_run` from `admin.backup_media`; a `SizeError` answers 413 with its message."

- [ ] **Step 5: Verify.**
- `make ci`. Expected: PASS.
- `grep -rni "people capsule\|messages capsule\|Not built yet: Matrix backups" --include='*.md' --include='*.go' . | grep -v docs/superpowers`. Expected: no output.
- `bash scripts/check-compose-matrix.sh`. Expected: pass.

- [ ] **Step 6: Commit.** Run `git add docs README.md AGENTS.md internal/*/AGENTS.md`, then commit with the message `docs: Matrix server backups, restore runbook and upgrade path`.
