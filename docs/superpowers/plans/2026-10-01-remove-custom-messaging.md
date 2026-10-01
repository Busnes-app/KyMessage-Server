# Remove the Custom Messaging Stack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete the superseded custom MLS chat stack so KyMessages is a lean identity/console/backup server ready for the Matrix stack.

**Architecture:** Pure removal in dependency order — web UI first, then the out-of-tree client packages and CI, then the backup "messages" kind, then the server messaging API/store with a migration that drops the tables, then docs. Every task leaves `make ci` green.

**Tech Stack:** Go (net/http, database/sql on SQLite/Postgres), React 19 + vitest + Playwright, Docker Compose, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-01-remove-custom-messaging-design.md` (parent: `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`)

## Global Constraints

- Base: master **after PR #9 (KyIdentity rename, migration 20) is merged**. The new migration is **21**.
- Nothing is deployed; no compatibility shims, aliases or deprecation paths.
- Migrations are append-only: do not delete migrations 5–12 or 17–19; migration 21 drops the tables.
- Drop order (child-first): `messaging_welcomes`, `messaging_events`, `messaging_epoch_devices`, `messaging_members`, `messaging_rooms`, `messaging_key_packages`, `messaging_recovery_auth`, `messaging_reset_receipts`, `messaging_devices`, `messaging_identities`.
- Keep: KyIdentity sign-in and directory webhook, QR device pairing (`internal/devices`), people capsule, KyRecovery pairing, proxy overlay, network self-check, `WaitDetached`/`tracked`.
- Non-admin console text, verbatim: `Chat isn't available yet.`
- `docs/superpowers/` history is never edited.
- Commits end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. DOX: read every AGENTS.md on the path before editing; update after.

## Review Focus

1. A database created before this change (with rows in every messaging table, messaging audit rows and messages settings) must migrate cleanly and twice — Task 4 `TestDropMessagingTables`.
2. The people backup must still seal after the tables are gone (its old code emptied them) — Task 3 keeps `TestCollect*` passing and adds no messaging statement.
3. Restore must still invalidate sessions, MFA challenges and device pairings after the messaging statements go — Task 4 keeps `TestInvalidateRestoredGrants*` with a non-messaging assertion.
4. Deleting a user through the KyIdentity webhook must still work with no messaging tables — Task 4 runs the directory webhook tests.
5. A non-admin who signs in must see `Chat isn't available yet.` and no admin page — Task 1 vitest.

---

### Task 1: Web — remove messaging UI, replace My account

**Files:**
- Delete: `web/src/pages/MessagingUsage.tsx`, `MessagingUsage.test.tsx`, `SuspendedDevices.tsx`, `SuspendedDevices.test.tsx`, `MyAccount.tsx`, `MyAccount.test.tsx`, `web/src/browserSupport.ts`, `browserSupport.test.ts`
- Create: `web/src/pages/MemberHome.tsx`, `web/src/pages/MemberHome.test.tsx`
- Modify: `web/src/pages/Dashboard.tsx` (imports, `<MessagingUsage /><SuspendedDevices />`), `web/src/pages/Backup.tsx` (`MessagesStatus`, `messagesStatus()`, `messages?:`, `MessagesBackup`, its render), `web/src/pages/Backup.test.tsx` (messages cases), `web/src/App.tsx`, `web/src/components/AppHeader.tsx`, `web/src/components/AppHeader.test.tsx`, `web/browser/ui.spec.mjs`, `web/AGENTS.md`, `web/browser/AGENTS.md`

**Interfaces:**
- Produces: `MemberHome({user, onLogout})`; `navItemsFor(role)` returns admin items only for `admin`, `[]` otherwise.

- [ ] **Step 1: Failing tests.** `web/src/pages/MemberHome.test.tsx`:

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemberHome } from './MemberHome';

afterEach(cleanup);

it('shows the account, the chat notice and sign out', () => {
  const onLogout = vi.fn();
  render(<MemberHome user={{display_name: 'Alice', username: 'alice'}} onLogout={onLogout} />);
  expect(screen.getByText('Alice')).toBeTruthy();
  expect(screen.getByText("Chat isn't available yet.")).toBeTruthy();
  fireEvent.click(screen.getByRole('button', {name: 'Sign out'}));
  expect(onLogout).toHaveBeenCalledOnce();
});
```

Update `web/src/components/AppHeader.test.tsx`:

```tsx
import { expect, it } from 'vitest';
import { navItemsFor } from './AppHeader';

it('member gets no admin navigation', () => {
  expect(navItemsFor('user')).toEqual([]);
  expect(navItemsFor('manager')).toEqual([]);
});
it('admin sees the admin pages', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'scim', 'backup', 'settings']);
});
```

- [ ] **Step 2: Run and see them fail.** `cd web && npx vitest run src/pages/MemberHome.test.tsx src/components/AppHeader.test.tsx` → FAIL (module missing; account entry still present).

- [ ] **Step 3: Implement.** `web/src/pages/MemberHome.tsx`:

```tsx
// Members chat in Element (Matrix sub-project 2); the console is for administrators.
export function MemberHome({user, onLogout}: {user: {display_name?: string; username: string}; onLogout: () => void}) {
  return <section className="card" aria-labelledby="member-home">
    <h2 id="member-home">{user.display_name || user.username}</h2>
    <p>Signed in as {user.username}.</p>
    <p>Chat isn't available yet.</p>
    <button type="button" className="btn-secondary" onClick={onLogout}>Sign out</button>
  </section>;
}
```

In `AppHeader.tsx`: delete `accountItem` and the `UserCircle` import; `navItemsFor = (role) => role === 'admin' ? adminItems : []`. In `App.tsx`: replace the `MyAccount` import/render with: non-admins render `<MemberHome user={user} onLogout={handleLogout} />` instead of the shell's pages (keep the header for sign-out styling consistent with today, or render `MemberHome` alone — match whichever keeps `AppHeader`'s existing sign-out button working; the test only pins `MemberHome`). Admins: initial tab `dashboard`, no `account` tab. Remove the deleted pages from `Dashboard.tsx` and the messages section from `Backup.tsx`/`Backup.test.tsx`.

In `web/browser/ui.spec.mjs` remove the "Messaging storage" region assertions and screenshot, and the My account block (navigation click, notice, `Messaging needs a KyIdentity account.`, browser-support lines). Keep everything else.

- [ ] **Step 4: Run.** `cd web && npm test && npx tsc -b && npm run build`, then `go build -o .browser/server ./cmd/server && cd web && npx playwright test`. Expected: all pass, 6 browser projects.
- [ ] **Step 5: DOX.** `web/AGENTS.md`: delete the MessagingUsage, SuspendedDevices, MyAccount, browserSupport and message-backup bullets; add one `MemberHome` bullet (non-admin page: account, notice, sign out; tested by `MemberHome.test.tsx`). `web/browser/AGENTS.md`: drop messaging/My account assertions text.
- [ ] **Step 6: Commit** (include rebuilt `web/dist`): `web: drop the messaging UI; members see a chat-coming page`

---

### Task 2: Remove chat-core, mls-proof, gate, rehearsal and their CI

**Files:**
- Delete: `chat-core/`, `mls-proof/`, `scripts/check-chat-gate.sh`, `scripts/restore-messages-rehearsal.sh`, `scripts/rehearsal/`
- Modify: `.github/workflows/ci.yml` (the `messaging-proof` job; the rehearsal `go vet`/`go test` and chat-gate steps; the comment near line 247), `Makefile` (rehearsal vet/test and gate lines in `lint`), `.dockerignore` (`/mls-proof/`, `/chat-core/` and comment)

- [ ] **Step 1: Delete** with `git rm -r chat-core mls-proof scripts/check-chat-gate.sh scripts/restore-messages-rehearsal.sh scripts/rehearsal`.
- [ ] **Step 2: Edit CI, Makefile, .dockerignore** as listed. Confirm no other job `needs:` the removed job: `grep -n 'messaging-proof' .github/workflows/ci.yml` → no output.
- [ ] **Step 3: Verify.** `make ci` → `==> Local CI checks passed`; `grep -rn 'chat-core\|mls-proof\|check-chat-gate\|rehearsal' .github Makefile .dockerignore scripts` → no output; `docker build -t kymessages:check .` succeeds (build context no longer references the directories).
- [ ] **Step 4: Commit:** `remove the isolated MLS client, chat-core package and their CI`

---

### Task 3: Remove the messages backup kind

**Files:**
- Delete: `internal/backup/messages.go`, `messages_test.go`, `import.go`, `import_test.go`
- Modify: `internal/backup/settings.go` (`MessagesRunAction`, `messagesSettings`, `MessagesSettings`, `MessagesRunConfig`), `settings_test.go`, `export_test.go` (`SetMessagesBudgets`, `SetMessagesFileCap`), `nodecrypt_test.go` (the `restoreMessages` allowlist entry), `payload.go` (`MessagingTables` and the emptying loop), `payload_test.go` (messaging cases incl. `TestMessagingTablesListsEveryMessagingTable`), `run_drill.go` (comment), `internal/api/backup_handlers.go` (`messagesBackup`, `handleMessagesDrill`, `handleRunMessagesBackup`, `handleSetMessagesSchedule`, status `messages` block), `internal/api/server.go` (`/api/backup/messages/*` routes), `internal/api/backup_test.go` (messages tests), `internal/api/authz_test.go` (messages routes), `cmd/server/main.go` (`restore-messages` case, `messagesKind`, `kinds` slice, `-messages` flag, `runRestoreMessages`), `cmd/server/restore.go` (messages-capsule refusal, `CheckMessagesTarget` hint, `restoreMessages`), `cmd/server/restore_test.go`, `cmd/server/backuploop_test.go`, `scripts/backup-acceptance.py` (messages checks), `internal/backup/AGENTS.md`

**Interfaces:**
- Produces: `backup.Collect` seals the people capsule without touching any messaging table; `cmd/server` has a single backup kind. Keep the `backupKind` type only if it still has two users; otherwise inline it (YAGNI — sub-project 4 introduces the server capsule).

- [ ] **Step 1: Failing guard.** In `internal/backup/payload_test.go` add:

```go
// The people capsule must not depend on messaging tables: Task 4 drops them.
func TestCollectNeedsNoMessagingTables(t *testing.T) {
	src, err := os.ReadFile("payload.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(src, []byte("messaging")) {
		t.Fatal("payload.go still references messaging tables")
	}
}
```

Run `go test ./internal/backup/ -run TestCollectNeedsNoMessagingTables` → FAIL.
- [ ] **Step 2: Implement.** In `payload.go` delete `MessagingTables` and replace the statement loop with a single `VACUUM`:

```go
	if _, err := snapshot.ExecContext(ctx, "VACUUM"); err != nil {
		return nil, fmt.Errorf("prepare recovery snapshot: %w", err)
	}
```

(Keep the surrounding open/close logic unchanged; update its comment to "The people capsule is the whole application database.") Then remove every other file/symbol listed above.
- [ ] **Step 3: Verify.** `go build ./... && go vet ./...`; `go test ./internal/backup/... ./internal/api/... ./cmd/... -count=1` → ok; `python3 scripts/backup-acceptance.py` → passes; `grep -rn 'Messages\|messages_\|restore-messages\|-messages' internal/backup cmd/server internal/api/backup*` → no output except unrelated words (inspect each hit).
- [ ] **Step 4: DOX.** `internal/backup/AGENTS.md` and root `AGENTS.md`'s `cmd/server` scheduler and restore paragraphs: drop the messages kind, `restore-messages`, suspended devices and resume text.
- [ ] **Step 5: Commit:** `backup: retire the messages capsule and restore-messages`

---

### Task 4: Remove the messaging API and store; drop the tables

**Files:**
- Delete: `internal/api/messaging_{delivery,handlers,key_packages,live,recovery,usage}.go` and every `internal/api/messaging_*_test.go`; `internal/store/messaging*.go` and their tests; `internal/store/migrations/messaging.go` **only if** no migration constant it defines is still referenced by the migrations list (migrations 5–12, 17–19 must keep their SQL — move needed constants into `migrations.go` rather than deleting them); `cmd/server/messaging_maintenance.go`, `messaging_maintenance_test.go`
- Modify: `internal/api/server.go` (`live` registry field, `s.messagingRoutes()`, `/api/admin/messaging/*`, `X-KyMessages-Device` CORS header, `StopMessaging`, `WakeMessaging`), `internal/api/authz_test.go`, `internal/api/export_test.go`, `cmd/server/main.go` (maintenance loop, `StopMessaging`, shutdown log text), `internal/store/store.go` (`Messaging()`), `internal/store/sqlstore.go` (startup `ExpireMessages`, `Messaging()`), `internal/store/restore.go`, `internal/store/restore_test.go`, `internal/store/migrations/migrations.go` (+ migration 21), `migrations_test.go`, `internal/config/config.go` (`Messaging`, `KY_MESSAGING_IDENTITY_RESET_ENABLED`), `config_test.go`, `docker-compose.yml`, `go.mod`/`go.sum`, `internal/api/AGENTS.md`, `internal/store/AGENTS.md`, `internal/config/AGENTS.md`, `internal/sso/AGENTS.md`

**Interfaces:**
- Consumes: Task 3 (no backup code references the store's messaging methods).
- Produces: migration `{Version: 21, Name: "drop_messaging", SQLite: dropMessaging, Postgres: dropMessaging}`.

- [ ] **Step 1: Failing migration test** in `internal/store/migrations/migrations_test.go`:

```go
// Migration 21 removes the retired messaging stack; rerunning it is harmless.
func TestDropMessagingTables(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	driver := "sqlite"
	if cfg.Driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.RunThrough(ctx, db, cfg.Driver, 20); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(q, err)
		}
	}
	exec(`INSERT INTO users (id, username, sso_provider, sso_subject, created_at, updated_at) VALUES ('u1', 'u1', 'kyidentity', 'u1', $1, $2)`, now, now)
	exec(`INSERT INTO audit_records (action, details, created_at) VALUES ('messaging.device_resumed', '', $1)`, now)
	exec(`INSERT INTO audit_records (action, details, created_at) VALUES ('admin.login', '', $1)`, now)
	exec(`INSERT INTO server_settings (key, value, updated_at) VALUES ('messages_backup_interval_sec', '3600', $1)`, now)
	exec(`INSERT INTO server_settings (key, value, updated_at) VALUES ('backup_interval_sec', '86400', $1)`, now)
	// Seed at least one row in messaging_devices and messaging_rooms using the columns
	// migration 20's schema requires (read internal/store/migrations for the NOT NULL set).

	check := func(run string) {
		t.Helper()
		for _, table := range []string{"messaging_welcomes", "messaging_events", "messaging_epoch_devices", "messaging_members", "messaging_rooms", "messaging_key_packages", "messaging_recovery_auth", "messaging_reset_receipts", "messaging_devices", "messaging_identities"} {
			if _, err := db.ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err == nil {
				t.Errorf("%s: %s still exists", run, table)
			}
		}
		for q, want := range map[string]int{
			`SELECT COUNT(*) FROM audit_records WHERE action LIKE 'messaging.%'`:                   0,
			`SELECT COUNT(*) FROM audit_records WHERE action = 'admin.login'`:                      1,
			`SELECT COUNT(*) FROM server_settings WHERE key = 'messages_backup_interval_sec'`:      0,
			`SELECT COUNT(*) FROM server_settings WHERE key = 'backup_interval_sec'`:               1,
			`SELECT COUNT(*) FROM users WHERE id = 'u1'`:                                           1,
		} {
			var n int
			if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Errorf("%s: %s = %d, want %d", run, q, n, want)
			}
		}
	}
	if err := migrations.Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	check("first run")
	if _, err := db.ExecContext(ctx, dropMessagingForTest); err != nil {
		t.Fatal(err)
	}
	check("rerun")
}
```

Export the SQL for the rerun in `internal/store/migrations/export_test.go`: `var dropMessagingForTest = dropMessaging` (adjust the test to reference `migrations.DropMessagingForTest` if the test is in the external `_test` package — match how `RunThrough` is exported). Before writing the seed rows, read the real column lists; the server settings keys must match what `internal/backup/settings.go` actually wrote (confirm the three `messages_*` keys with git history: `git show HEAD~:internal/backup/settings.go`).

Run `go test ./internal/store/migrations/ -run TestDropMessagingTables -count=1` → FAIL (no migration 21).
- [ ] **Step 2: Add migration 21** to `migrations.go`:

```go
	{Version: 21, Name: "drop_messaging", SQLite: dropMessaging, Postgres: dropMessaging},
```

```go
// The custom MLS messaging stack was retired for Matrix; its tables, audit rows and backup
// settings go. Child tables first so foreign keys never block a drop.
const dropMessaging = `
DROP TABLE IF EXISTS messaging_welcomes;
DROP TABLE IF EXISTS messaging_events;
DROP TABLE IF EXISTS messaging_epoch_devices;
DROP TABLE IF EXISTS messaging_members;
DROP TABLE IF EXISTS messaging_rooms;
DROP TABLE IF EXISTS messaging_key_packages;
DROP TABLE IF EXISTS messaging_recovery_auth;
DROP TABLE IF EXISTS messaging_reset_receipts;
DROP TABLE IF EXISTS messaging_devices;
DROP TABLE IF EXISTS messaging_identities;
DELETE FROM audit_records WHERE action LIKE 'messaging.%';
DELETE FROM server_settings WHERE key IN ('messages_backup_interval_sec', 'messages_backup_last_attempt', 'messages_kyrecovery_last_deposit');
`
```

If a drop fails on a remaining foreign key, reorder per the actual `REFERENCES` clauses and record the order change in the commit message.
- [ ] **Step 3: Remove the API, store, loop and config** listed above. `InvalidateRestoredGrants` keeps only:

```go
	for _, query := range []string{
		`DELETE FROM sessions`,
		`DELETE FROM mfa_challenges`,
		`DELETE FROM device_pairings`,
	} {
```

with audit details `"Sessions, challenges and pairings cleared. Users sign in again."` and the comment `// A snapshot cannot prove which sessions or pairings were later revoked.` Keep a restore test asserting a session, an MFA challenge and a device pairing are gone and the audit row exists.
Run `go mod tidy` to drop `github.com/coder/websocket`.
- [ ] **Step 4: Verify.** `go build ./... && go vet ./...`; `make ci` → passes; `go test ./...` on a disposable Postgres 17 (own uniquely named container on a free loopback port, stopped by name) → all ok; `go test ./internal/api/ -run 'Directory|SSO' -count=1` → ok; `grep -rn -i 'messaging' --include='*.go' .` → only `migrations.go` (migrations 5–12, 17–21 SQL) and `migrations_test.go`.
- [ ] **Step 5: DOX.** `internal/api`, `internal/store`, `internal/config`, `internal/sso` AGENTS.md: delete messaging text; root `AGENTS.md`: delete the `messagingMaintenanceLoop`, `StopMessaging`/WebSocket drain and messaging route text from the `cmd/server` paragraph (keep the backup scheduler and `WaitDetached` text).
- [ ] **Step 6: Commit:** `server: remove the messaging API and store; migration 21 drops its tables`

---

### Task 5: Docs and final verification

**Files:**
- Delete: `docs/MESSAGING-API.md`, `docs/MLS-INTEROP-RESEARCH.md`, `docs/MLS-LIBRARY-RESEARCH.md`, `docs/MLS-PROOF-HANDOFF.md`, `docs/MESSAGING-BACKEND-HANDOFF.md`, `docs/MESSAGING-LOAD.md`, `docs/BROWSER-EVIDENCE.md`, `docs/BROWSER-SUPPORT.md`, `docs/FIRST-RELEASE-PLAN.md`
- Modify: `docs/KYMESSAGES-PROTOCOL-RESEARCH.md` (drop "MLS text and device lifecycle" and "Required proofs"; keep "OCM and Nextcloud Talk interoperability" and "Calling, SFrame and connectivity"), `docs/RESTORE.md` (drop "Restore messages (optional)" and messaging mentions), `docs/PRODUCT.md` (drop "Keys, devices, and history", "Retention and recovery", "Architecture on this base", "Delivery requirements"; add one line under the title: `The chat platform is Matrix; see docs/superpowers/specs/2026-10-01-matrix-platform-design.md. Product-level sections below are reworded in Matrix sub-project 2.`), `README.md`, root `AGENTS.md` (intro chat/MLS bullets, review-policy preference text that names chat-core, Verification lines for the rehearsal/proof/gate, the chat-core and mls-proof child index entries), `docs/CHAT-PLATFORM-OPTIONS.md` only if it links a deleted doc (replace the link with "removed; see git history").

- [ ] **Step 1: Delete and edit** as listed. Fix every link to a deleted doc: `grep -rn 'MESSAGING-API\|MLS-INTEROP\|MLS-LIBRARY\|MLS-PROOF\|MESSAGING-BACKEND\|MESSAGING-LOAD\|BROWSER-EVIDENCE\|BROWSER-SUPPORT\|FIRST-RELEASE-PLAN' --include='*.md' . | grep -v docs/superpowers` → no output.
- [ ] **Step 2: Final verification.**
  - `grep -rni 'messaging\|chat-core\|mls-proof' . --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=dist | grep -v docs/superpowers | grep -v migrations` → only `kymessages-net`/product naming; inspect each hit.
  - `make ci` → passes; Postgres `go test ./...` → all ok; console browser suite → 6 passed; `python3 scripts/backup-acceptance.py` → passes; `bash scripts/check-compose-proxy.sh` → exit 0; `docker build -t kymessages:check .` → succeeds.
- [ ] **Step 3: Commit:** `docs: remove the custom messaging docs and point to the Matrix design`
