# People and Message Backups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- The existing capsule protects **people**: accounts, access and settings, and nothing else.
- A separate, opt-in **messages** capsule holds threads, members, devices and retained ciphertext.
- A `restore-messages` step brings threads back after a people restore.
- Restored devices stay suspended until their owner re-proves the device key.

**Architecture:**
- **People capsule.** The existing `Collect` empties every messaging table, and every `messaging.*` audit row, in its private snapshot.
- **Messages capsule.** A new `CollectMessages` exports the messaging tables into `data/messages/accounts.db` plus event part files of at most 64 MiB each, with a total of at most 256 MiB.
- **Library reuse.** Both kinds reuse `recoveryclient.Run`:
  - The messages kind sees prefixed settings keys, so it has its own schedule, last attempt and receipt.
  - Its local copies go in `<KY_BACKUP_DIR>/messages/`.
  - It is audited as `admin.backup_run_messages`.
- **Suspended devices.** A suspended device is stored as `status='approved'` with `token_hash IS NULL`. Every device-scoped operation already requires a matching credential, so a suspended device can do nothing, but it keeps its roster place so that unchanged threads resume.
- **Resume.** Resume reuses the Ed25519 enrollment challenge under a new domain string.

**Tech Stack:** Go, SQLite (`VACUUM INTO`, `ATTACH`), `ky-primitives/recoveryclient` v0.8.0, React/TypeScript operator console, the isolated `mls-proof` client, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-29-thread-purge-and-split-backups-design.md`, section 2.

**Depends on:** Plan A (`feat/thread-auto-purge`, PR #4). Branch from it, or from `master` once it has merged.

## Design decisions made in this plan (review these)

| Decision | Why | Cost if wrong |
|---|---|---|
| Suspended = `status='approved' AND token_hash IS NULL`, exposed as `"suspended"` | Adding a status value needs a SQLite table rebuild that cascade-deletes `messaging_recovery_auth` rows. Every device gate already matches on `token_hash`, and the roster stays intact, so unchanged threads resume | A future code path that uses `status='approved'` without a credential check would treat suspended devices as live. The roster is the intended exception. |
| Messages kind uses a prefixed `Settings` adapter (`messages_` + `backup_interval_sec`, `backup_last_attempt`, `kyrecovery_last_deposit`); pairing and pin keys pass through | The library's keys are fixed, and sharing them would make each kind overwrite the other's schedule and receipt | None known |
| Messages local copies go in `<KY_BACKUP_DIR>/messages/` | The library prunes by `<app>.` prefix with one keep count, so messages copies would prune people copies | None known |
| Messages capsule = `accounts.db` plus event parts of at most 64 MiB, split by sequence ranges; fails beyond 256 MiB | Library limits: 64 MiB per file, 256 MiB expanded. Room quotas are 512 MiB, so rooms must be splittable | A very large deployment cannot take a messages backup until purge windows shrink |
| Only `approved` devices are imported (suspended). `revoked` devices are imported as tombstones. `pending`/`unverified` are dropped | Tombstones keep first-device bootstrap safe; pending enrollments are session-bound and stale | A device that was pending at snapshot time must re-enroll |
| A room whose owner is missing from the people restore is dropped (counted as retired) | `owner_id` has a foreign key to `users`, so an ownerless room row cannot exist | That thread is lost and must be recreated |
| `restore-messages` refuses a target that has no `data/ky_server.db` or has messaging rows already | It is a second step onto a freshly people-restored target, never a merge | None known |
| Resume needs a sign-in from the last 10 minutes (same window as backup step-up) | The spec asks for fresh sign-in; this reuses the proven rule | None known |

## Global Constraints

- The people capsule must contain no row from any `messaging_*` table and no `audit_records` row whose action starts with `messaging.`.
- Messages capsule limits: at most 64 MiB per file and at most 256 MiB in total (`recoveryclient.MaxCapsuleFileBytes`, `recoveryclient.MaxCapsuleTotalBytes`). Over the limit, fail with `capsule.ErrCapsuleTooLarge`, naming the largest rooms.
- The messages capsule is marked with recipe `"kind": "messages"` and `data/messages/accounts.db`. `restore-messages` refuses a capsule without `data/messages/accounts.db` or with `data/ky_server.db`.
- The messages schedule defaults to off (interval 0). It shares the pairing, token and key pin.
- `restore-messages` runs offline, reads custodian shares from stdin only, and imports in one transaction.
- Resume requires a live suite session no older than 10 minutes and an Ed25519 signature by the device's stored public key. Revoked devices cannot resume.
- The backup mutation routes for the messages kind use `requireFreshAdmin`, except drill, as for people.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Follow the DOX chain: read `AGENTS.md` at the root and in `internal/backup`, `internal/store`, `internal/api`, `cmd/server` (covered by the root), `web` and `mls-proof` before editing. Update them after.

## Review Focus

1. **Messages capsule sealed while a room is mid-write.** The export must come from one consistent snapshot, so `VACUUM INTO` runs first and every part is read from that snapshot, never from the live database. Pin it in Task 2 by exporting from the snapshot file only.
2. **`restore-messages` run twice, or onto a live data directory.** It must refuse the second run instead of duplicating rows. Pin it in Task 5.
3. **A people capsule older than the messages capsule.** Messages reference users who are missing from the people restore. Their rows must be dropped and reported, not left as dangling foreign keys. Pin it in Task 5.
4. **A device revoked after the messages snapshot.** It is restored suspended. The admin must be able to see and revoke it before users return. Pin it in Task 4, where suspended devices appear in the device list with status `suspended` and revoke works.
5. **Both kinds scheduled for the same minute.** The library's process lock refuses the second run with `ErrInProgress`. The scheduler must run the two kinds one after the other, never silently skip one. Pin it in Task 3.

---

### Task 1: People capsule excludes all messaging data

**Files:**
- Modify: `internal/backup/payload.go:113-120` (the snapshot statements).
- Test: `internal/backup/payload_test.go`.
- Docs: `internal/backup/AGENTS.md` (`Collect` bullet), `docs/RESTORE.md` (the "What recovery can restore" table and the restore effects).

**Interfaces:**
- Produces: `backup.Collect` output unchanged in shape. The snapshot copy now has empty messaging tables and no `messaging.*` audit rows.
- Produces: `backup.MessagingTables = []string{"messaging_welcomes", "messaging_events", "messaging_epoch_devices", "messaging_members", "messaging_rooms", "messaging_key_packages", "messaging_recovery_auth", "messaging_reset_receipts", "messaging_devices", "messaging_identities"}`. The order is child-first, so deletes never violate foreign keys. Tasks 2 and 5 reuse it.

- [ ] **Step 1: Write the failing test.** In `payload_test.go`, reusing the file's existing collect fixture (it seeds a room and events; read the file to find it), add:

```go
func TestPeopleCapsuleHasNoMessagingData(t *testing.T) {
	cfg, st := collectFixture(t) // the file's existing helper that seeds users, a room and events
	payload, err := backup.Collect(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	db := openPayloadDB(t, payload) // write data/ky_server.db to a temp file and sql.Open it
	for _, table := range backup.MessagingTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows in the people capsule", table, n)
		}
	}
	var audit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_records WHERE action LIKE 'messaging.%'`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit != 0 {
		t.Fatalf("%d messaging audit rows in the people capsule", audit)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users == 0 {
		t.Fatalf("people missing from the people capsule: %d %v", users, err)
	}
	_ = st
}
```

If the file has no helpers named like this, write `collectFixture` and `openPayloadDB` in the test file. Base them on how existing tests there build a config, open a store, seed a kysignon user with an approved device and a room with one event, and decode the payload's `data/ky_server.db` into a temp file.

- [ ] **Step 2: Run it and see it fail.** Run `go test ./internal/backup/ -run TestPeopleCapsuleHasNoMessagingData -v`. Expected: FAIL (`messaging_rooms has 1 rows`).

- [ ] **Step 3: Implement.** In `payload.go`, add the exported slice above. Replace the statement list in `snapshotSQLite` with:

```go
	// The people capsule restores accounts, access and settings. Threads are the opt-in
	// messages capsule's job, so nothing messaging-related is sealed here.
	statements := []string{}
	for _, table := range MessagingTables {
		statements = append(statements, "DELETE FROM "+table)
	}
	statements = append(statements, "DELETE FROM audit_records WHERE action LIKE 'messaging.%'", "VACUUM")
	for _, query := range statements {
```

Keep the loop body.

- [ ] **Step 4: Run it and see it pass,** then run the package and the restore tests: `go test ./internal/backup/ ./cmd/server/`. Existing restore tests that expected retired rooms after a restore may now find no rooms. Update their assertions to the new contract: after a people restore there are no messaging rows at all.

- [ ] **Step 5: Update the docs.**
  - `internal/backup/AGENTS.md`: `Collect` now seals people only, empties every messaging table and drops `messaging.*` audit rows.
  - `docs/RESTORE.md`: the table row for `data/ky_server.db` now reads "accounts, MFA state, settings, non-messaging audits and sealed recovery token; no messaging data". A people restore leaves no threads or messaging devices, and threads come back only through the messages capsule (Task 5).
- [ ] **Step 6: Commit** `backup: the people capsule seals no messaging data`.

---

### Task 2: Messages capsule collection and drill checks

**Files:**
- Create: `internal/backup/messages.go`.
- Create: `internal/backup/messages_test.go`.
- Modify: `internal/backup/run_drill.go` (`RunDrill` takes the checks function).
- Modify: its callers in `internal/api/backup_handlers.go` and `cmd/server/main.go`.

**Interfaces:**
- Consumes: `MessagingTables` (Task 1).
- Produces:
  - `func CollectMessages(ctx context.Context, cfg *config.Config, appVersion string) (recoveryclient.Payload, error)`
  - `func MessagesChecks(dir string, opened capsule.Manifest) []recoveryclient.Check`
  - `const MessagesDir = "data/messages"` and `const MessagesAccounts = "data/messages/accounts.db"`
  - `func RunDrill(ctx context.Context, cfg *config.Config, payload recoveryclient.Payload, checks func(string, capsule.Manifest) []recoveryclient.Check) (*recoveryclient.DrillResult, error)`. The existing callers pass `backup.Checks`.

- [ ] **Step 1: Write the failing tests** in `messages_test.go`:

```go
func TestCollectMessagesSplitsAndMarksKind(t *testing.T) {
	cfg, _ := collectFixture(t) // from Task 1
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range payload.Files {
		paths[f.Path] = true
		if int64(len(f.Data)) > recoveryclient.MaxCapsuleFileBytes {
			t.Fatalf("%s is %d bytes", f.Path, len(f.Data))
		}
	}
	if !paths[backup.MessagesAccounts] || paths["data/ky_server.db"] || !paths["data/encryption.key"] {
		t.Fatalf("unexpected members: %v", paths)
	}
	if payload.VerificationRecipe["kind"] != "messages" {
		t.Fatalf("recipe kind: %v", payload.VerificationRecipe["kind"])
	}
	// accounts.db holds rooms, members, devices and identities; event parts hold events and Welcomes.
	db := openMember(t, payload, backup.MessagesAccounts)
	for _, table := range []string{"messaging_rooms", "messaging_members", "messaging_devices", "messaging_identities", "messaging_epoch_devices"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n == 0 {
			t.Fatalf("%s: %d %v", table, n, err)
		}
	}
	events := 0
	for path := range paths {
		if strings.HasPrefix(path, backup.MessagesDir+"/events-") {
			var n int
			if err := openMember(t, payload, path).QueryRow("SELECT COUNT(*) FROM messaging_events").Scan(&n); err != nil {
				t.Fatal(err)
			}
			events += n
		}
	}
	if events == 0 {
		t.Fatal("no events exported")
	}
}

func TestCollectMessagesSplitsLargeRoomAcrossParts(t *testing.T) {
	// Seed one room with events totalling about 150 MiB of payload (for example 2,400 events
	// of 64 KiB each, inserted directly into messaging_events via a raw handle so no API rate
	// limit applies). Then CollectMessages must return at least 3 event parts, each at most
	// MaxCapsuleFileBytes, whose event counts sum to 2,400, with each part's sequences
	// contiguous and non-overlapping.
}

func TestCollectMessagesRefusesOverTotalLimit(t *testing.T) {
	// Seed more than MaxCapsuleTotalBytes of payload across rooms (raw inserts), then
	// CollectMessages returns an error wrapping capsule.ErrCapsuleTooLarge whose message
	// names the largest room ID.
}

func TestMessagesDrillPasses(t *testing.T) {
	cfg, _ := collectFixture(t)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := backup.RunDrill(context.Background(), cfg, payload, backup.MessagesChecks)
	if err != nil || !result.Passed {
		t.Fatalf("drill: %+v %v", result, err)
	}
}
```

Write the two described tests in full before running, with raw inserts through a `*sql.DB` on `cfg.Database.DSN`. Also write `openMember`, which writes one payload member to a temp file and opens it read-only.

- [ ] **Step 2: Run them and see them fail.** Run `go test ./internal/backup/ -run 'Messages' -v`. Expected: FAIL (undefined `CollectMessages`).

- [ ] **Step 3: Implement `messages.go`:**
  - `CollectMessages` refuses non-SQLite with `ErrNoDatabaseSnapshot`, like `Collect`. It takes a consistent snapshot with the same `recoveryclient.SQLiteSnapshot` call that `snapshotSQLite` uses, factored into a shared `snapshotFile(ctx, dsn, dataDir) (path string, cleanup func(), err error)` so the code is not duplicated.
  - It opens the snapshot and builds `accounts.db` in a temp dir:

```go
	// One consistent snapshot, then plain copies of the messaging tables: no constraints are
	// needed in transport; the importer re-inserts with explicit columns and the live schema.
	accounts := []string{"messaging_identities", "messaging_devices", "messaging_rooms", "messaging_members", "messaging_epoch_devices"}
```

  - For each table: `ATTACH DATABASE '<partPath>' AS part; CREATE TABLE part.<t> AS SELECT * FROM main.<t>; DETACH DATABASE part`. Do this on one `*sql.Conn` from the snapshot handle, because `ATTACH` is per connection. Exclude device rows whose status is `unverified` or `pending` (see the design decisions).
  - Event parts: read `SELECT room_id, sequence, LENGTH(payload) + COALESCE((SELECT SUM(LENGTH(payload)) FROM messaging_welcomes w WHERE w.room_id = e.room_id AND w.sequence = e.sequence), 0) FROM messaging_events e ORDER BY room_id, sequence` and cut contiguous `(room, fromSeq, toSeq)` ranges.
    - Start a new part when adding a row would exceed `partBudget = recoveryclient.MaxCapsuleFileBytes - 4<<20` (headroom for SQLite pages). Build each part with `CREATE TABLE part.messaging_events AS SELECT * FROM main.messaging_events WHERE 0`, then one `INSERT INTO part.messaging_events SELECT * FROM main.messaging_events WHERE room_id = ? AND sequence BETWEEN ? AND ?` per range, and the same for `messaging_welcomes`.
    - Name parts `events-001.db`, `events-002.db`, and so on.
    - `VACUUM` each part, then check its size is at most `MaxCapsuleFileBytes`.
  - Keep a running total across all members, including `data/encryption.key`. If it exceeds `recoveryclient.MaxCapsuleTotalBytes`, return `fmt.Errorf("%w: messages exceed %d MiB; largest rooms: %s", capsule.ErrCapsuleTooLarge, recoveryclient.MaxCapsuleTotalBytes>>20, strings.Join(top3RoomsByBytes, ", "))`.
  - Payload: `ServiceName: cfg.Server.AppName`, the same `AppVersion`, files `accounts.db`, the event parts and `data/encryption.key` (so a drill can prove the pairing key is intact, and so the kind is self-contained). Recipe: `{"kind": "messages", "check_sqlite_integrity": true, "sqlite_paths": [all member .db paths], "required_files": [...]}`.
  - `MessagesChecks`: required `accounts.db` present and non-empty; recipe kind is `messages`; SQLite integrity for every `.db` member (read-only), mirroring the per-file checks in `drill.go`.
  - `RunDrill`: add the `checks` parameter and pass it to the library drill. Update the callers in `backup_handlers.go` (`handleBackupDrill` passes `backup.Checks`) and `cmd/server/main.go` (`runBackupDrill`).

- [ ] **Step 4: Run them and see them pass,** then run `go test ./internal/backup/ ./internal/api/ ./cmd/server/`.
- [ ] **Step 5: Docs.** In `internal/backup/AGENTS.md`, add a `CollectMessages` bullet covering members, split rule, limits, kind marker and excluded device statuses.
- [ ] **Step 6: Commit** `backup: opt-in messages capsule with split event parts and drill checks`.

---

### Task 3: Separate schedule, receipts, local copies and CLI for the messages kind

**Files:**
- Modify: `internal/backup/settings.go`: add `MessagesSettings` and `MessagesRunConfig`.
- Modify: `cmd/server/main.go`:
  - `backupLoop` runs both kinds in sequence.
  - `recordRun` takes an action.
  - `deposit` and `backup-drill` accept `-messages`.
- Test: `internal/backup/settings_test.go` (new if absent) and `cmd/server` tests.

**Interfaces:**
- Consumes: `CollectMessages`, `MessagesChecks` (Task 2).
- Produces:
  - `func MessagesSettings(ctx context.Context, s store.SettingsStore) recoveryclient.Settings`
  - `func MessagesRunConfig(cfg *config.Config, appVersion string) (recoveryclient.RunConfig, error)`. It sets `BackupDir` to `filepath.Join(cfg.Backup.Dir, "messages")` when `cfg.Backup.Dir != ""`.
  - `const MessagesRunAction = "admin.backup_run_messages"`.

- [ ] **Step 1: Write failing tests.**

```go
func TestMessagesSettingsKeepOwnScheduleAndReceipt(t *testing.T) {
	ctx, st := settingsStore(t) // open a testdb store
	people := backup.Settings(ctx, st.Settings())
	messages := backup.MessagesSettings(ctx, st.Settings())
	if err := recoveryclient.SetInterval(people, 3600); err != nil {
		t.Fatal(err)
	}
	if err := recoveryclient.SetInterval(messages, 7200); err != nil {
		t.Fatal(err)
	}
	p, _ := recoveryclient.Interval(0, people)
	m, _ := recoveryclient.Interval(0, messages)
	if p != time.Hour || m != 2*time.Hour {
		t.Fatalf("schedules shared: people %v messages %v", p, m)
	}
	// Pairing and key pin are shared: a key pinned through one is visible through the other.
	if err := people.Set("kyrecovery_key_id", "k1"); err != nil {
		t.Fatal(err)
	}
	if v, err := messages.Get("kyrecovery_key_id"); err != nil || v != "k1" {
		t.Fatalf("key pin not shared: %q %v", v, err)
	}
	if v, err := messages.Get("backup_interval_sec"); err != nil || v != "7200" {
		t.Fatalf("messages interval: %q %v", v, err)
	}
	if v, _ := st.Settings().GetSetting(ctx, "messages_backup_interval_sec"); v != "7200" {
		t.Fatalf("stored key: %q", v)
	}
}

func TestMessagesLocalCopiesDoNotPrunePeople(t *testing.T) {
	// cfg.Backup.Dir = t.TempDir(), Keep = 1. Pin a throwaway recovery key (the api tests'
	// pinBody/recoverykey.Generate pattern). Run recoveryclient.Run once for people
	// (backup.RunConfig + backup.Settings + backup.Collect) and twice for messages
	// (backup.MessagesRunConfig + backup.MessagesSettings + backup.CollectMessages).
	// Assert recoveryclient.ListLocalCopies(cfg.Backup.Dir, app) still lists 1 people copy and
	// ListLocalCopies(filepath.Join(cfg.Backup.Dir, "messages"), app) lists 1 messages copy.
}
```

Write the second test in full, and a `cmd/server` test that `backupLoop`'s per-tick helper runs messages after people when both are due. Extract the tick body as `func backupTick(ctx context.Context, cfg *config.Config, st store.Store, kinds []backupKind, client recoveryclient.Depositor)`, so that it is testable without the ticker.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** In `settings.go`:

```go
// The messages capsule has its own schedule, last attempt and receipt; the pairing, token
// and key pin are shared with the people capsule.
var messagesOwnKeys = map[string]bool{"backup_interval_sec": true, "backup_last_attempt": true, "kyrecovery_last_deposit": true}

type messagesSettings struct{ recoveryclient.Settings }

func (m messagesSettings) key(k string) string {
	if messagesOwnKeys[k] {
		return "messages_" + k
	}
	return k
}
func (m messagesSettings) Get(k string) (string, error) { return m.Settings.Get(m.key(k)) }
func (m messagesSettings) Set(k, v string) error         { return m.Settings.Set(m.key(k), v) }
func (m messagesSettings) Delete(k string) error         { return m.Settings.Delete(m.key(k)) }

func MessagesSettings(ctx context.Context, s store.SettingsStore) recoveryclient.Settings {
	return messagesSettings{Settings(ctx, s)}
}

func MessagesRunConfig(cfg *config.Config, appVersion string) (recoveryclient.RunConfig, error) {
	rc, err := RunConfig(cfg, appVersion)
	if err == nil && rc.BackupDir != "" {
		// Its own directory: the library prunes local copies by app prefix with one keep count.
		rc.BackupDir = filepath.Join(rc.BackupDir, "messages")
	}
	return rc, err
}
```

In `cmd/server/main.go`, define:

```go
type backupKind struct {
	name, action string
	rc           recoveryclient.RunConfig
	settings     func(context.Context) recoveryclient.Settings
	defaultEvery time.Duration
	collect      func(context.Context) (recoveryclient.Payload, error)
}
```

Build two kinds in `backupLoop`:
- **people:** action `"admin.backup_run"`, default `cfg.Backup.DepositInterval`.
- **messages:** action `backup.MessagesRunAction`, default `0`, so it is off until the admin sets it.

`backupTick` runs the kinds that are due, sequentially. The library lock makes concurrent runs return `ErrInProgress`, and sequential calls never hit it. `recordRun` gains an `action string` parameter that overrides the library's action. `runDeposit` and `runBackupDrill` parse `-messages` with a `flag.FlagSet` and pick the messages kind (collector, `MessagesRunConfig`, `MessagesSettings`, `MessagesChecks`).

- [ ] **Step 4: Run them and see them pass,** then run `go test ./internal/backup/ ./cmd/server/ ./internal/api/`.
- [ ] **Step 5: Docs.**
  - Root `AGENTS.md`: the `cmd/server` scheduler paragraph now says the loop runs the people and messages kinds in sequence.
  - `internal/backup/AGENTS.md`: settings prefixing and the `messages/` subdirectory.
- [ ] **Step 6: Commit** `backup: messages capsule has its own schedule, receipts, local copies and CLI flag`.

---

### Task 4: Admin API and backup screen for the messages kind

**Files:**
- Modify: `internal/api/server.go`: routes.
- Modify: `internal/api/backup_handlers.go`: status gains `messages`, plus three handlers.
- Modify: `web/src/pages/Backup.tsx` and `web/src/pages/Backup.test.tsx`.
- Modify: `scripts/backup-acceptance.py` (independence check).
- Rebuild: `web/dist`.
- Test: `internal/api/backup_test.go`.

**Interfaces:**
- Consumes: Task 2 and Task 3 functions.
- Produces:
  - Routes:
    - `PUT /api/backup/messages/schedule` taking `{interval_sec}`, with `requireFreshAdmin`.
    - `POST /api/backup/messages/deposit`, with `tracked(requireFreshAdmin)`.
    - `POST /api/backup/messages/drill`, with `requireAdmin`.
  - Status JSON adds `"messages": {"interval_sec", "next_run_at", "last_run", "last_run_error", "last_deposit", "local_copies", "local_error"}`, with the same field semantics as the people fields. `last_run` reads the latest `admin.backup_run_messages` audit row.

- [ ] **Step 1: Write failing API tests** in `backup_test.go`, reusing `setupSQLiteServer`, `loginAs`, `adminDo` and `pinBody`:
  - `PUT /api/backup/messages/schedule {"interval_sec":3600}` returns 200. Status then shows `messages.interval_sec == 3600` while the people `interval_sec` is unchanged.
  - With a pinned key and `cfg.Backup.Dir` set, `POST /api/backup/messages/deposit` returns 200. `status.messages.local_copies` has 1 entry, people `local_copies` has 0, and `status.messages.last_run.outcome == "success"`.
  - `POST /api/backup/messages/drill` returns 200 with `passed: true`.
  - With a session older than 10 minutes, the schedule and deposit routes return 403 `reauthentication_required`. Reuse the stale-session setup from `TestBackupMutationsRequireRecentSignIn`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement the handlers.** Mirror `handleSetSchedule`, `handleRunBackup` and `handleBackupDrill`, swapping in `MessagesSettings`, `MessagesRunConfig`, `CollectMessages`, `MessagesChecks` and `MessagesRunAction`. Factor the shared bodies into helpers that take a kind struct, rather than copying them. Add the `messages` object to `handleBackupStatus`, reusing the helper that builds the people `last_run`, with the action as a parameter.
- [ ] **Step 4: UI.** In `Backup.tsx`:
  - Add a "Message backups" section below "Schedule". It has an explanation: "Opt-in. Holds threads, members, devices and messages still inside each thread's retention. Restored devices must be resumed by their owners."
  - It has the schedule select (off by default), "Back up messages now", "Run message drill", last run, last receipt and local copies. Validate `status.messages` at the boundary like the existing DTO validation.
  - Add a vitest case in `Backup.test.tsx` that renders the section from a stubbed status with `messages.interval_sec: 0` and shows "Off".
  - Run `cd web && npm test && npm run build`.
- [ ] **Step 5: Acceptance.** Extend `scripts/backup-acceptance.py`: after the people schedule checks, set the messages interval to 900, back-date `messages_backup_last_attempt`, wait for one scheduled messages copy in `<dir>/messages/`, and assert the people copy count is unchanged. Run `python3 scripts/backup-acceptance.py`.
- [ ] **Step 6: Docs.** Add the three routes to the route table and contracts in `internal/api/AGENTS.md`, and the section to `web/AGENTS.md`.
- [ ] **Step 7: Commit** `backup: admin API and screen for opt-in message backups`.

---

### Task 5: `restore-messages` import

**Files:**
- Create: `internal/backup/import.go` and `internal/backup/import_test.go`.
- Modify: `cmd/server/main.go` (`restore-messages` subcommand) and `cmd/server/restore.go` (`restoreMessages`).
- Test: `cmd/server/restore_test.go`.
- Docs: `docs/RESTORE.md`.

**Interfaces:**
- Consumes: `MessagesAccounts` and `MessagesDir` (Task 2).
- Produces:
  - `type ImportCounts struct { Rooms, RetiredRooms, Members, DroppedMembers, Devices, DroppedDevices, Events int }`
  - `func ImportMessages(ctx context.Context, dbPath string, openedDir string) (ImportCounts, error)`
  - CLI: `kymessages restore-messages -capsule <path> -into <people-restored dir> [-service <name>]`, with shares on stdin.

- [ ] **Step 1: Write the failing round-trip test** in `import_test.go`:
  1. Seed a source database with users alice (owner), bob (member) and carol (member). Give them approved devices, a revoked device of alice's and a pending device of bob's. Add a room with 3 events and a Welcome.
  2. Call `CollectMessages`, and write the members to a temp "opened" dir, standing in for `capsule.Open`.
  3. Build a target database with a fresh store at the current schema, containing users alice and bob only (carol missing).
  4. Call `ImportMessages(target, openedDir)` and assert:
     - The room exists with its epoch, sequence and `retained_from` unchanged.
     - The members are alice and bob; carol is dropped and `DroppedMembers == 1`.
     - alice's and bob's approved devices exist with `status='approved' AND token_hash IS NULL`.
     - alice's revoked device exists as revoked.
     - bob's pending device is absent.
     - 3 events and 1 Welcome exist.
     - An audit row `restore.messages_imported` exists.
  5. Owner-missing case: remove alice from the target instead. The room, its members and its events are not imported, and `RetiredRooms == 1`. `messaging_rooms.owner_id` has a foreign key to `users`, so a room without its owner cannot exist; dropping it is the faithful form of the spec's "retired".
  6. Second run: calling `ImportMessages` again returns an error containing `already has messaging data`, and nothing changes.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Implement `ImportMessages`:**
  - Open `dbPath` with `sql.Open("sqlite", ...)` using the same file-URI style as `prepareRestoredData`.
  - Take one `*sql.Conn` and refuse if `SELECT COUNT(*) FROM messaging_rooms` or `messaging_devices` is non-zero.
  - Before `BEGIN IMMEDIATE`, attach `accounts.db` as `acc` and each event part as `p1`, `p2` and so on. SQLite forbids `ATTACH` inside a transaction and allows 10 attached databases by default. The 256 MiB capsule cap divided by the 60 MiB part budget gives at most 5 parts, so refuse more than 8 parts as malformed. Then run every insert below inside the one transaction, so the import is atomic.
  - Inserts, all with explicit column lists matching the current schema:

```sql
INSERT INTO messaging_identities (user_id, generation)
  SELECT user_id, generation FROM acc.messaging_identities WHERE user_id IN (SELECT id FROM main.users);
INSERT INTO messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, token_hash, approved_by, created_at, verified_at, identity_generation)
  SELECT id, user_id, name, public_key, status, '', '', expires_at, NULL, approved_by, created_at, verified_at, identity_generation
  FROM acc.messaging_devices WHERE status IN ('approved', 'revoked') AND user_id IN (SELECT id FROM main.users);
INSERT INTO messaging_rooms (id, name, owner_id, created_at, epoch, sequence, roster_hash, retained_bytes, owner_identity_generation, direct_peer_id, retained_from, retention_days)
  SELECT id, name, owner_id, created_at, epoch, sequence, roster_hash, retained_bytes, owner_identity_generation, direct_peer_id, retained_from, retention_days
  FROM acc.messaging_rooms WHERE owner_id IN (SELECT id FROM main.users);
```

    - A room whose owner is missing is skipped and counted in `RetiredRooms`, and so are its members, epoch devices, events and Welcomes (see Step 1, item 5).
    - Members and epoch devices come from `acc` where the room was imported and the user exists.
    - Events and Welcomes come from each part where the room was imported.
    - Set `retained_bytes` from the actual imported sums.
    - Write the audit row with the counts as details.
  - `restoreMessages(capsulePath, targetDir, expectService, shares, stdout)` in `restore.go`:
    - Require `targetDir/data/ky_server.db` to exist; otherwise the people restore has not run.
    - Call `recoveryclient.Restore` into a fresh `os.MkdirTemp(targetDir, "messages-*")`.
    - Refuse if the opened dir lacks `MessagesAccounts` or contains `data/ky_server.db`.
    - Run `store.Open` on the target (migrations), then `ImportMessages`, then remove the temp dir.
    - Print the manifest, the counts and: "Restored devices are suspended until their owners sign in and resume them in the messaging client. Review and revoke unknown devices first."
  - `main.go`: add a `restore-messages` subcommand mirroring `runRestore`'s flags (`-capsule`, `-into`, `-service`) and reading shares with `recoveryclient.ReadShares(os.Stdin)`.
- [ ] **Step 4: Run and pass,** plus a `cmd/server` test: people-restore a capsule, then `restoreMessages` a messages capsule sealed to a throwaway key with test shares. Mirror the existing restore test's key and share setup. Assert the imported counts.
- [ ] **Step 5: Docs.** Rewrite `docs/RESTORE.md` for the two-step restore: the people capsule first, then optionally `restore-messages`. Cover what is dropped, suspended devices, reviewing and revoking devices before return, resuming, and threads that advanced after the snapshot staying paused. Update the restore policy paragraph in root `AGENTS.md` (it currently says restored rooms are permanently retired). Update `docs/PRODUCT.md`'s server-backup paragraphs.
- [ ] **Step 6: Commit** `restore: optional restore-messages brings threads back with suspended devices`.

---

### Task 6: Suspended devices and the resume endpoint

**Files:**
- Modify: `internal/store/messaging.go`:
  - `deviceColumns` reports `suspended`.
  - Add `StartDeviceResume` and `ResumeDevice`.
  - `RevokeDevice` must also revoke suspended devices; check its predicate.
- Modify: `internal/api/messaging_handlers.go`: routes and handlers.
- Tests: `internal/store/messaging_test.go`, `internal/api/messaging_test.go`.
- Docs: `docs/MESSAGING-API.md` (device status list, resume routes).

**Interfaces:**
- Produces:
  - `func (m *messagingStore) StartDeviceResume(ctx context.Context, actor MessagingActor, id, tokenHash, challenge string, expiresAt int64) error`. The device must be the actor's own, `status='approved' AND token_hash IS NULL`. It stores `challenge`, `enrollment_session = actor.SessionHash` and `expires_at`, plus the pending token hash in a new column.
  - `func (m *messagingStore) ResumeDevice(ctx context.Context, actor MessagingActor, id string, signature []byte) (*MessagingDevice, error)`
  - Routes: `POST /api/messaging/devices/{device}/resume` taking `{token_hash}` and returning `{signing_input, expires_at}`; and `POST /api/messaging/devices/{device}/resume/verify` taking `{signature}` and returning `{device}`.
- Migration 18: `ALTER TABLE messaging_devices ADD COLUMN resume_token_hash TEXT NOT NULL DEFAULT '';`. This holds the proposed token until the signature verifies, so the credential cannot be set before the key is proven.

- [ ] **Step 1: Write failing store tests.** Create an approved device, then use a raw SQL `UPDATE ... SET token_hash = NULL` to simulate an import. Then check:
  - `ListDevices` shows `status == "suspended"`.
  - A device-scoped call with the old token fails with `ErrMessagingDenied`.
  - `StartDeviceResume` works with a fresh session.
  - `ResumeDevice` with a wrong signature returns `ErrMessagingDenied` and the device stays suspended.
  - With the correct Ed25519 signature over the stored challenge, the device is `approved` and the new token hash works for `DeliveryState`.
  - A revoked device cannot start a resume (`ErrNotFound`).
  - A second `ResumeDevice` replay fails.
  - `RevokeDevice` on a suspended device leaves it revoked.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
  - `deviceColumns`: `id, user_id, name, public_key, CASE WHEN status = 'approved' AND token_hash IS NULL THEN 'suspended' ELSE status END, approved_by, created_at, identity_generation`.
  - `StartDeviceResume`: inside `m.transaction(ctx, actor, false, ...)`, run `UPDATE messaging_devices SET challenge = ?, enrollment_session = ?, expires_at = ?, resume_token_hash = ? WHERE id = ? AND user_id = ? AND status = 'approved' AND token_hash IS NULL`, then `messagingChanged`, then audit `messaging.device_resume_started`.
  - `ResumeDevice`: select `challenge, public_key, resume_token_hash` where id and user match, `enrollment_session = actor.SessionHash`, `status='approved' AND token_hash IS NULL AND expires_at > now AND resume_token_hash <> ''`. Verify Ed25519 as `VerifyDevice` does. Then set `token_hash = resume_token_hash, resume_token_hash = '', challenge = '', enrollment_session = ''` and audit `messaging.device_resumed` with the fingerprint.
  - Handlers:
    - `resume`: validate `token_hash` hex (as enroll does). Require `time.Since(session.CreatedAt) <= 10*time.Minute`, otherwise return 403 `{"code":"reauthentication_required","reauth_url":"/api/sso/kysignon/login?fresh=1"}` (reuse the step-up constant from `server.go`). This needs the session creation time: extend `MessagingActor` with `SessionCreatedAt int64`, populated in `requireMessaging`. Build the challenge JSON like enroll with `Domain: "KyMessages resume v1"`, plus `DeviceID`, `PublicKey`, `TokenHash`, `Nonce` and `ExpiresAt`, 5 minutes.
    - `verify`: the same as `handleMessagingVerify`, calling `ResumeDevice`, then `wakeMessaging("")`.
- [ ] **Step 4: API tests** mirroring `TestMessagingEnrollmentAfterVerifiedOIDCCallback`'s signing helpers:
  - A stale session gets 403 with `reauth_url`.
  - A fresh session gets `signing_input`; signing it and verifying returns `approved`.
  - A wrong key gets 403.
- [ ] **Step 5: Run the store and API tests on SQLite and Postgres** (disposable container as in Plan A).
- [ ] **Step 6: Docs and commit.** In `docs/MESSAGING-API.md` and `internal/store/AGENTS.md`, document the suspended representation, resume and migration 18. Commit `messaging: suspended restored devices resume by re-proving their key`.

---

### Task 7: Isolated client handles suspended devices

**Files:**
- Modify: `mls-proof/src/delivery.ts`:
  - `accountDevices` status whitelist (~234).
  - `enroll` reuse (~266).
  - A new `resumeDevice()`.
- Modify: `mls-proof/src/chat.ts` (~99, 130, 178, 249-265, 284-286, 307-309): a suspended state and a "Resume this device" button.
- Modify: `mls-proof/server/main.go`: a fixture `POST /proof-fixture/suspend-devices/{user}` that runs `UPDATE messaging_devices SET token_hash = NULL WHERE user_id = ? AND status = 'approved'`.
- Test: `mls-proof/tests/oidc.spec.ts`.

**Interfaces:**
- Consumes: the resume routes (Task 6), and the `?fresh=1` login from the earlier SSO step-up work.
- Produces: `delivery.resumeDevice(): Promise<'resumed' | 'reauth'>`.

- [ ] **Step 1: Write the failing Playwright case** in `oidc.spec.ts`:
  1. Two OIDC users share a room and exchange a message.
  2. Call the fixture to suspend alice's devices.
  3. Alice's page shows "This device is suspended after a server restore" and a "Resume this device" button.
  4. Alice clicks it. The fixture issuer's session is fresh in the test, so this goes straight to signing. If the flow needs a reauth redirect, follow it.
  5. The state returns to normal, and alice sends a message that bob receives, proving the unchanged thread resumed.
  6. A second case: suspend alice, have bob commit a membership change so the epoch advances, then resume alice. She sees the existing history-gap or paused outcome, not an error loop.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Implement.**
  - Add `'suspended'` to the `accountDevices` whitelist and to `enroll`'s reuse list, so a suspended key is never re-enrolled.
  - `resumeDevice`:
    1. Generate a new 32-byte token (as `enroll` does).
    2. POST `/devices/{id}/resume` with its SHA-256 hash.
    3. On 403 with `reauth_url` starting `/api/sso/`, return `'reauth'`. The caller navigates there.
    4. Otherwise sign `signing_input` with the stored device Ed25519 key (the same key and helper `enroll` uses), POST `/resume/verify`, and replace the stored token in the vault transaction only after verify succeeds. Return `'resumed'`.
  - In `chat.ts`, treat `suspended` like "not approved" for polling. Show the suspended message and button in the room-state area (~307-309). On `'reauth'`, `location.assign` the URL.
- [ ] **Step 4: Run** `npm run build`, the OIDC config, the default config and the delivery config on Chromium and Firefox. Run `go vet -tags mlsproof ./mls-proof/server/`.
- [ ] **Step 5: Docs and commit.** In `mls-proof/AGENTS.md`, document the suspended state and resume. Commit `mls-proof: resume a suspended device after a message restore`.

---

### Task 8: Full verification and DOX pass

- [ ] **Step 1:** `make ci`, expecting `==> Local CI checks passed`. Commit a rebuilt `web/dist` if it changed.
- [ ] **Step 2:** The full Go suite on a disposable Postgres 17. Expected: all `ok`. Message backups are SQLite-only; the Postgres run proves the migration and store changes.
- [ ] **Step 3:** `python3 scripts/backup-acceptance.py` passes, including the messages independence check.
- [ ] **Step 4:** An end-to-end rehearsal with the built binary against scratch directories:
  1. Seed via the API.
  2. `export-capsule` for people.
  3. `deposit -messages` with a pinned test key and a local dir.
  4. `restore` then `restore-messages` into an empty dir using the test shares.
  5. Start the restored server and check `/api/messaging/devices` reports `suspended`.
  Put this in `scripts/restore-messages-rehearsal.sh` so it can be re-run.
- [ ] **Step 5:** The DOX pass.
  - Root `AGENTS.md`: the restore policy and scheduler paragraphs.
  - `internal/backup/AGENTS.md`, `internal/store/AGENTS.md`, `internal/api/AGENTS.md`, `web/AGENTS.md` and `mls-proof/AGENTS.md`.
  - `docs/RESTORE.md` and `docs/PRODUCT.md`.
  - Commit `docs: people and message backups`.
