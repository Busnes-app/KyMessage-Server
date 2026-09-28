package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestRestoreInvalidatesGrantsAndPermanentlyRetiresRooms(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	actor, first, request := recoverySetup(t, st)
	old := actor
	old.DeviceTokenHash = first.TokenHash
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 100 {
		must(st.Messaging().CreateRoom(ctx, old, store.MessagingRoom{ID: fmt.Sprint("old-", i), Name: "Old room"}))
	}
	appendDelivery(t, st, old, "old-0", deliveryInput(t, st, old, "old-0", "commit"))
	must(st.Messaging().BeginRecoveryAuthentication(ctx, actor, request))
	must(st.InvalidateRestoredGrants(ctx))
	must(st.InvalidateRestoredGrants(ctx)) // Repeating offline preparation cannot restore authority.
	if _, err := st.Sessions().GetSession(ctx, actor.SessionHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old session: %v", err)
	}
	if _, err := st.Messaging().ListDevices(ctx, old); err == nil {
		t.Fatal("restored session still works")
	}
	actor.SessionHash = crypto.RandomHex(32)
	must(st.Sessions().CreateSession(ctx, &store.Session{TokenHash: actor.SessionHash, UserID: actor.UserID, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""))
	old.SessionHash = actor.SessionHash
	if _, err := st.Messaging().ReadEvents(ctx, old, "old-0", 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("old credential: %v", err)
	}
	devices, err := st.Messaging().ListDevices(ctx, actor)
	must(err)
	for _, device := range devices {
		if device.Status != "revoked" {
			t.Fatal("restored approval", device)
		}
	}
	if _, err := st.Messaging().RecoveryAuthentication(ctx, actor, request.StateHash); err == nil {
		t.Fatal("restored recovery request survived")
	}
	fresh, sig := messagingEnrollment(t, actor)
	must(st.Messaging().EnrollDevice(ctx, actor, fresh))
	device, err := st.Messaging().VerifyDevice(ctx, actor, fresh.Device.ID, sig)
	must(err)
	if device.Status != "pending" {
		t.Fatal("restore bypassed device recovery", device)
	}
	actor.DeviceTokenHash = fresh.TokenHash
	request = store.MessagingRecoveryAuthentication{StateHash: crypto.RandomHex(32), DeviceID: fresh.Device.ID, SealedRequest: "opaque", CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(4 * time.Minute).Unix(), ResetConfirmed: true}
	must(st.Messaging().BeginRecoveryAuthentication(ctx, actor, request))
	_, err = st.Messaging().ResetIdentity(ctx, actor, request.StateHash, "alice")
	must(err)
	rooms, err := st.Messaging().ListRooms(ctx, actor, 0)
	must(err)
	if len(rooms) != 0 {
		t.Fatal("restored memberships revived", rooms)
	}
	if err := st.Messaging().InviteMember(ctx, actor, "old-0", "alice"); err == nil {
		t.Fatal("restored ownership revived")
	}
	must(st.Messaging().CreateRoom(ctx, actor, store.MessagingRoom{ID: "fresh-room", Name: "Fresh room"}))
	appendDelivery(t, st, actor, "fresh-room", deliveryInput(t, st, actor, "fresh-room", "commit"))
	appendDelivery(t, st, actor, "fresh-room", deliveryInput(t, st, actor, "fresh-room", "application"))
}

func TestRestoreGrantInvalidationRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restore.db")
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	actor, _ := deliveryDevice(t, st, messagingActor(t, st, "alice"))
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
	if _, err := st.Sessions().GetSession(ctx, actor.SessionHash); err != nil {
		t.Fatal("partial session deletion", err)
	}
	devices, err := st.Messaging().ListDevices(ctx, actor)
	if err != nil || len(devices) != 1 || devices[0].Status != "approved" {
		t.Fatal("partial device revocation", devices, err)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_restore_audit`); err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateRestoredGrants(ctx); err != nil {
		t.Fatal(err)
	}
}
