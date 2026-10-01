package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// A v16 database with 1/7/30-day rooms holding events blanked by the old expiry.
func TestThreadAutoPurgeMigratesV16(t *testing.T) {
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
	if err := migrations.RunThrough(ctx, db, cfg.Driver, 16); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		if driver == "pgx" {
			var b strings.Builder
			n := 0
			for _, r := range q {
				if r == '?' {
					n++
					fmt.Fprintf(&b, "$%d", n)
				} else {
					b.WriteRune(r)
				}
			}
			q = b.String()
		}
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	must(exec(`INSERT INTO users (id, username, created_at, updated_at) VALUES ('owner', 'owner', ?, ?)`, now, now))
	const live = "b3BhcXVl"
	for _, days := range []int64{1, 7, 30} {
		room := fmt.Sprintf("r%d", days)
		// The old sweep blanked sequences 1 and 2 and raised the floor and bytes past them.
		must(exec(`INSERT INTO messaging_rooms (id, name, owner_id, created_at, retention_days, sequence, retained_from, retained_bytes) VALUES (?, ?, 'owner', ?, ?, 3, 3, ?)`, room, room, now.Unix(), days, len(live)))
		old := now.Add(-time.Duration(days+5) * 24 * time.Hour)
		for seq, at := range map[int64]time.Time{1: old, 2: old.Add(time.Second), 3: now} {
			payload := ""
			if seq == 3 {
				payload = live
			}
			must(exec(`INSERT INTO messaging_events (room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at, expires_at) VALUES (?, ?, 'dev', ?, 'application', 1, 'h', ?, 'rh', ?, ?)`,
				room, seq, fmt.Sprintf("e%d", seq), payload, at.Unix(), at.Unix()+days*86400))
			must(exec(`INSERT INTO audit_records (user_id, action, resource, details, created_at) VALUES ('owner', 'messaging.event_accepted', ?, ?, ?)`,
				room, fmt.Sprintf("device_id=dev sequence=%d kind=application", seq), at))
		}
		must(exec(`INSERT INTO audit_records (user_id, action, resource, details, created_at) VALUES ('owner', 'messaging.room_created', ?, '', ?)`, room, old))
	}

	// Through 17 only: migration 21 drops these tables.
	must(migrations.RunThrough(ctx, db, cfg.Driver, 17))

	scalar := func(q, room string) int64 {
		t.Helper()
		if driver == "pgx" {
			q = strings.Replace(q, "?", "$1", 1)
		}
		var n int64
		must(db.QueryRowContext(ctx, q, room).Scan(&n))
		return n
	}
	for _, days := range []int64{1, 7, 30} {
		room := fmt.Sprintf("r%d", days)
		for q, want := range map[string]int64{
			`SELECT retention_days FROM messaging_rooms WHERE id = ?`:                                                         days,
			`SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`:                                                         1,
			`SELECT MIN(sequence) FROM messaging_events WHERE room_id = ?`:                                                    3,
			`SELECT retained_from FROM messaging_rooms WHERE id = ?`:                                                          3,
			`SELECT sequence FROM messaging_rooms WHERE id = ?`:                                                               3,
			`SELECT retained_bytes FROM messaging_rooms WHERE id = ?`:                                                         int64(len(live)),
			`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_events WHERE room_id = ?`:                                int64(len(live)),
			`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.event_accepted' AND resource = ?`:                   1,
			`SELECT COUNT(*) FROM audit_records WHERE details = 'device_id=dev sequence=3 kind=application' AND resource = ?`: 1,
			`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.room_created' AND resource = ?`:                     1,
		} {
			if got := scalar(q, room); got != want {
				t.Errorf("%s [%s] = %d, want %d", q, room, got, want)
			}
		}
	}
	if err := exec(`UPDATE messaging_rooms SET retention_days = 5 WHERE id = 'r1'`); err == nil {
		t.Error("CHECK accepted retention 5")
	}
	must(exec(`UPDATE messaging_rooms SET retention_days = 0 WHERE id = 'r1'`))
	if err := exec(`UPDATE messaging_events SET expires_at = 0`); err == nil {
		t.Error("messaging_events.expires_at still exists")
	}
}

// Devices suspended before migration 19 start their 30 days at upgrade instead of never.
func TestSuspendedAtBackfill(t *testing.T) {
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
	if err := migrations.RunThrough(ctx, db, cfg.Driver, 18); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, created_at, updated_at) VALUES ('owner', 'owner', $1, $2)`, now, now); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		id, status string
		token      any
	}{{"suspended", "approved", nil}, {"live", "approved", "hash"}, {"revoked", "revoked", nil}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, token_hash, created_at) VALUES ($1, 'owner', 'n', $2, $3, '', '', 0, $4, 0)`, d.id, d.id, d.status, d.token); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().Unix()
	if err := migrations.RunThrough(ctx, db, cfg.Driver, 19); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()
	for id, wantSet := range map[string]bool{"suspended": true, "live": false, "revoked": false} {
		var at int64
		if err := db.QueryRowContext(ctx, `SELECT suspended_at FROM messaging_devices WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if wantSet && (at < before || at > after) || !wantSet && at != 0 {
			t.Errorf("%s suspended_at = %d, migration ran in [%d, %d]", id, at, before, after)
		}
	}
}

// Migration 20 renames the stored KySignOn provider; rerunning its SQL changes nothing more.
func TestKyIdentityProviderRename(t *testing.T) {
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
	must := func(_ sql.Result, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := migrations.RunThrough(ctx, db, cfg.Driver, 19); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, u := range [][2]string{{"suite", "kysignon"}, {"local", "local"}, {"generic", "oidc"}} {
		must(db.ExecContext(ctx, `INSERT INTO users (id, username, sso_provider, sso_subject, created_at, updated_at) VALUES ($1, $1, $2, $1, $3, $4)`, u[0], u[1], now, now))
	}
	must(db.ExecContext(ctx, `INSERT INTO directory_sync_state (provider, subject, revision) VALUES ('kysignon', 'suite', 7)`))
	must(db.ExecContext(ctx, `INSERT INTO directory_sync_events (provider, event_id) VALUES ('kysignon', 'evt-1')`))

	check := func(run string) {
		t.Helper()
		for id, want := range map[string]string{"suite": "kyidentity", "local": "local", "generic": "oidc"} {
			var got string
			if err := db.QueryRowContext(ctx, `SELECT sso_provider FROM users WHERE id = $1`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s: user %s sso_provider = %q, want %q", run, id, got, want)
			}
		}
		for _, q := range []string{
			`SELECT COUNT(*) FROM directory_sync_state WHERE provider = 'kyidentity' AND subject = 'suite' AND revision = 7`,
			`SELECT COUNT(*) FROM directory_sync_events WHERE provider = 'kyidentity' AND event_id = 'evt-1'`,
		} {
			var n int
			if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("%s: %s = %d, want 1", run, q, n)
			}
		}
		var stale int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE sso_provider = 'kysignon') + (SELECT COUNT(*) FROM directory_sync_state WHERE provider = 'kysignon') + (SELECT COUNT(*) FROM directory_sync_events WHERE provider = 'kysignon')`).Scan(&stale); err != nil {
			t.Fatal(err)
		}
		if stale != 0 {
			t.Errorf("%s: %d kysignon rows remain", run, stale)
		}
	}
	if err := migrations.Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	check("first run")
	// Forget that v20 ran so the second Run executes its SQL again.
	must(db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 20`))
	if err := migrations.Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	check("second run")
}

// Migration 21 removes the retired messaging stack; rerunning it is harmless.
func TestDropMessagingTables(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	driver := "sqlite"
	if cfg.Driver == "postgres" {
		driver = "pgx"
	}
	dsn := cfg.DSN
	if driver == "sqlite" {
		dsn += "?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open(driver, dsn)
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
	exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES ('s1', 'u1', $1, $2)`, now, now.Add(time.Hour))
	exec(`INSERT INTO audit_records (action, details, created_at) VALUES ('messaging.device_resumed', '', $1)`, now)
	exec(`INSERT INTO audit_records (action, details, created_at) VALUES ('admin.login', '', $1)`, now)
	for _, key := range []string{"messages_backup_interval_sec", "messages_backup_last_attempt", "messages_kyrecovery_last_deposit", "backup_interval_sec"} {
		exec(`INSERT INTO server_settings (key, value, updated_at) VALUES ($1, '1', $2)`, key, now)
	}
	// A row in every messaging table, so a drop blocked by a foreign key fails the migration.
	exec(`INSERT INTO messaging_identities (user_id) VALUES ('u1')`)
	exec(`INSERT INTO messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, token_hash, created_at) VALUES ('d1', 'u1', 'n', 'pk', 'approved', '', '', 0, 'th', 0)`)
	exec(`INSERT INTO messaging_rooms (id, name, owner_id, created_at) VALUES ('r1', 'r', 'u1', 0)`)
	exec(`INSERT INTO messaging_members (room_id, user_id, status) VALUES ('r1', 'u1', 'active')`)
	exec(`INSERT INTO messaging_events (room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at) VALUES ('r1', 1, 'd1', 'e1', 'commit', 1, 'h', 'p', 'rh', 0)`)
	exec(`INSERT INTO messaging_welcomes (room_id, sequence, device_id, payload) VALUES ('r1', 1, 'd1', 'w')`)
	exec(`INSERT INTO messaging_epoch_devices (room_id, device_id, generation, joined_sequence) VALUES ('r1', 'd1', 1, 1)`)
	exec(`INSERT INTO messaging_key_packages (id, device_id, payload, expires_at) VALUES ('k1', 'd1', 'p', 0)`)
	exec(`INSERT INTO messaging_recovery_auth (state_hash, user_id, session_hash, device_id, public_key, subject, registry_hash, sealed_request, created_at, expires_at) VALUES ('st', 'u1', 's1', 'd1', 'pk', 'u1', 'rh', 'sr', 0, 0)`)
	exec(`INSERT INTO messaging_reset_receipts (state_hash, user_id, session_hash, device_id, identity_generation, completed_at) VALUES ('st', 'u1', 's1', 'd1', 1, 0)`)

	check := func(run string) {
		t.Helper()
		for _, table := range []string{"messaging_welcomes", "messaging_events", "messaging_epoch_devices", "messaging_members", "messaging_rooms", "messaging_key_packages", "messaging_recovery_auth", "messaging_reset_receipts", "messaging_devices", "messaging_identities"} {
			if _, err := db.ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err == nil {
				t.Errorf("%s: %s still exists", run, table)
			}
		}
		idxQ := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'audit_action_resource_created'`
		if cfg.Driver == "postgres" {
			idxQ = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'audit_action_resource_created'`
		}
		for q, want := range map[string]int{
			idxQ: 0,
			`SELECT COUNT(*) FROM audit_records WHERE action LIKE 'messaging.%'`:     0,
			`SELECT COUNT(*) FROM audit_records WHERE action = 'admin.login'`:        1,
			`SELECT COUNT(*) FROM server_settings WHERE key LIKE 'messages_%'`:       0,
			`SELECT COUNT(*) FROM server_settings WHERE key = 'backup_interval_sec'`: 1,
			`SELECT COUNT(*) FROM users WHERE id = 'u1'`:                             1,
			`SELECT COUNT(*) FROM sessions WHERE token_hash = 's1'`:                  1,
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
	// Forget that v21 ran so the second Run executes its SQL again.
	exec(`DELETE FROM schema_migrations WHERE version = 21`)
	if err := migrations.Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	check("rerun")
}
