package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
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

	st, err := store.Open(ctx, cfg)
	must(err)
	defer st.Close()

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
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
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
