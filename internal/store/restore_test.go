package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// seedGrants gives one user a session, an MFA challenge and a device pairing.
func seedGrants(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	for _, err := range []error{
		st.Users().CreateUser(ctx, &store.User{ID: "u", Username: "u", PasswordHash: "h", Role: "user", Status: "active", SSOProvider: "local"}),
		st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "session", UserID: "u", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, "h"),
		st.Sessions().CreateMFAChallenge(ctx, &store.MFAChallenge{TokenHash: "challenge", UserID: "u", ExpiresAt: now.Add(time.Hour)}, "h"),
		st.Devices().CreatePairing(ctx, &store.DevicePairing{Secret: "pair", UserID: "u", Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestoreInvalidatesGrants(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seedGrants(t, st)
	for range 2 { // Repeating offline preparation is harmless.
		if err := st.InvalidateRestoredGrants(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Sessions().GetSession(ctx, "session"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session survived", err)
	}
	if _, _, err := st.Sessions().ConsumeMFAChallenge(ctx, "challenge"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("challenge survived", err)
	}
	if _, err := st.Devices().GetPairingBySecret(ctx, "pair"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("pairing survived", err)
	}
	if _, err := st.Audit().LatestAuditRecord(ctx, "restore.grants_invalidated"); err != nil {
		t.Fatal("no audit row", err)
	}
}

func TestRestoreGrantInvalidationRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restore.db")
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedGrants(t, st)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse_restore_audit BEFORE INSERT ON audit_records WHEN NEW.action = 'restore.grants_invalidated' BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateRestoredGrants(ctx); err == nil {
		t.Fatal("failed audit was ignored")
	}
	if _, err := st.Sessions().GetSession(ctx, "session"); err != nil {
		t.Fatal("partial session deletion", err)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_restore_audit`); err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateRestoredGrants(ctx); err != nil {
		t.Fatal(err)
	}
}
