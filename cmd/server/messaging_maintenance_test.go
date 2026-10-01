package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// A maintenance tick revokes a device left suspended past 30 days and wakes live streams so
// other members see the new roster; a tick that revokes nothing wakes nobody.
func TestMaintenanceExpiresSuspendedDevicesAndWakes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := st.Users().CreateUser(ctx, &store.User{ID: "alice", Username: "alice", Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30*24*time.Hour - time.Second).Unix()
	if _, err := db.Exec(`INSERT INTO messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, token_hash, created_at, suspended_at) VALUES ('d', 'alice', 'n', 'k', 'approved', '', '', 0, NULL, 0, ?)`, old); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	wake := func() { wakes++ }
	maintainMessaging(ctx, st, wake)
	var status string
	if err := db.QueryRow(`SELECT status FROM messaging_devices WHERE id = 'd'`).Scan(&status); err != nil || status != "revoked" || wakes != 1 {
		t.Fatalf("status %q wakes %d: %v", status, wakes, err)
	}
	maintainMessaging(ctx, st, wake)
	if wakes != 1 {
		t.Fatalf("idle tick woke streams: %d", wakes)
	}
}
