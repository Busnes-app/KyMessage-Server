package store_test

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const suspensionLifetime = 30 * 24 * time.Hour

// suspendedFor backdates a device's suspension.
func suspendedFor(t *testing.T, db *sql.DB, id string, age time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE messaging_devices SET suspended_at = $1 WHERE id = $2`, time.Now().Add(-age).Unix(), id); err != nil {
		t.Fatal(err)
	}
}

func rosterHas(t *testing.T, st store.Store, actor store.MessagingActor, room, id string) bool {
	t.Helper()
	state, err := st.Messaging().DeliveryState(context.Background(), actor, room)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range state.Devices {
		if d.ID == id {
			return true
		}
	}
	return false
}

func expiryAudits(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.device_suspension_expired'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMessagingSuspendedDevicesExpireAfter30Days(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	alice, aliceID := deliveryDevice(t, st, messagingActor(t, st, "alice"))
	bob, bobID := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	room := "team"
	if err := st.Messaging().CreateRoom(ctx, alice, store.MessagingRoom{ID: room, Name: "Team"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().InviteMember(ctx, alice, room, bob.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().AcceptInvite(ctx, bob, room); err != nil {
		t.Fatal(err)
	}
	// Bob's device is restored suspended and he starts a resume he never finishes.
	if _, err := db.ExecContext(ctx, `UPDATE messaging_devices SET token_hash = NULL, suspended_at = $1 WHERE id = $2`, time.Now().Unix(), bobID); err != nil {
		t.Fatal(err)
	}
	bob.DeviceTokenHash = ""
	challenge := "resume " + crypto.RandomHex(8)
	if err := st.Messaging().StartDeviceResume(ctx, bob, bobID, crypto.RandomHex(32), challenge, time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	suspendedFor(t, db, bobID, suspensionLifetime+time.Second)

	young, youngID, _, _ := suspendedDevice(t, st, db, messagingActor(t, st, "carol"))
	suspendedFor(t, db, youngID, 29*24*time.Hour)

	// A resumed device carries no suspension clock.
	dave, daveID, _, daveKey := suspendedDevice(t, st, db, messagingActor(t, st, "dave"))
	resumeChallenge := "resume " + crypto.RandomHex(8)
	if err := st.Messaging().StartDeviceResume(ctx, dave, daveID, crypto.RandomHex(32), resumeChallenge, time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ResumeDevice(ctx, dave, daveID, ed25519.Sign(daveKey, []byte(resumeChallenge))); err != nil {
		t.Fatal(err)
	}
	var daveSuspendedAt int64
	if err := db.QueryRowContext(ctx, `SELECT suspended_at FROM messaging_devices WHERE id = $1`, daveID).Scan(&daveSuspendedAt); err != nil || daveSuspendedAt != 0 {
		t.Fatalf("resume left suspended_at=%d: %v", daveSuspendedAt, err)
	}

	// Past its 30 days but not yet swept: resume is already refused.
	if _, err := st.Messaging().ResumeDevice(ctx, bob, bobID, []byte("any signature")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired resume verify: %v", err)
	}
	if err := st.Messaging().StartDeviceResume(ctx, bob, bobID, crypto.RandomHex(32), "x", time.Now().Add(5*time.Minute).Unix()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired resume start: %v", err)
	}
	if !rosterHas(t, st, alice, room, bobID) {
		t.Fatal("suspended device missing from roster before the sweep")
	}

	n, err := st.Messaging().ExpireSuspendedDevices(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep revoked %d: %v", n, err)
	}
	if got := deviceStatus(t, st, bob, bobID); got != "revoked" {
		t.Fatal(got)
	}
	var resumeHash string
	var suspendedAt int64
	if err := db.QueryRowContext(ctx, `SELECT resume_token_hash, suspended_at FROM messaging_devices WHERE id = $1`, bobID).Scan(&resumeHash, &suspendedAt); err != nil || resumeHash != "" || suspendedAt != 0 {
		t.Fatalf("revoke left resume=%q suspended_at=%d: %v", resumeHash, suspendedAt, err)
	}
	if rosterHas(t, st, alice, room, bobID) {
		t.Fatal("expired device still in the delivery roster")
	}
	rec, err := st.Audit().LatestAuditRecord(ctx, "messaging.device_suspension_expired")
	if err != nil || rec.Resource != bobID || rec.UserID != "system" || rec.Details != "user_id=bob" {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := st.Messaging().StartDeviceResume(ctx, bob, bobID, crypto.RandomHex(32), "x", time.Now().Add(5*time.Minute).Unix()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked resume: %v", err)
	}

	if got := deviceStatus(t, st, young, youngID); got != "suspended" {
		t.Fatalf("29-day device %s", got)
	}
	if got := deviceStatus(t, st, dave, daveID); got != "approved" {
		t.Fatalf("resumed device %s", got)
	}
	if got := deviceStatus(t, st, alice, aliceID); got != "approved" {
		t.Fatalf("live device %s", got)
	}
	if _, err := st.Messaging().ListRooms(ctx, alice, 0); err != nil {
		t.Fatal(err)
	}

	before := expiryAudits(t, db)
	if n, err := st.Messaging().ExpireSuspendedDevices(ctx); err != nil || n != 0 {
		t.Fatalf("second sweep revoked %d: %v", n, err)
	}
	if after := expiryAudits(t, db); after != before || after != 1 {
		t.Fatalf("audit rows %d then %d", before, after)
	}
}

// Expiry is at 30 days: a device a minute short of it still resumes.
func TestMessagingSuspendedDeviceResumesBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	owner, id, _, key := suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	suspendedFor(t, db, id, suspensionLifetime-time.Minute)
	if n, err := st.Messaging().ExpireSuspendedDevices(ctx); err != nil || n != 0 {
		t.Fatalf("sweep revoked %d: %v", n, err)
	}
	challenge := "resume " + crypto.RandomHex(8)
	if err := st.Messaging().StartDeviceResume(ctx, owner, id, crypto.RandomHex(32), challenge, time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if d, err := st.Messaging().ResumeDevice(ctx, owner, id, ed25519.Sign(key, []byte(challenge))); err != nil || d.Status != "approved" {
		t.Fatalf("%v %v", d, err)
	}
}

// The admin list and the owner's list say when each suspended device will be revoked.
func TestMessagingSuspendedDeviceExpiryIsListed(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	owner, id, _, _ := suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	at := time.Now().Add(-time.Hour).Unix()
	if _, err := db.ExecContext(ctx, `UPDATE messaging_devices SET suspended_at = $1 WHERE id = $2`, at, id); err != nil {
		t.Fatal(err)
	}
	want := at + int64(suspensionLifetime/time.Second)
	devices, _, err := st.Messaging().SuspendedDevices(ctx, 10)
	if err != nil || len(devices) != 1 || devices[0].ExpiresAt != want {
		t.Fatalf("%+v %v", devices, err)
	}
	listed, err := st.Messaging().ListDevices(ctx, owner)
	if err != nil || len(listed) != 1 || listed[0].SuspensionExpiresAt != want {
		t.Fatalf("%+v %v", listed, err)
	}
	live, _ := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	listed, err = st.Messaging().ListDevices(ctx, live)
	if err != nil || len(listed) != 1 || listed[0].SuspensionExpiresAt != 0 {
		t.Fatalf("live device lists an expiry: %+v %v", listed, err)
	}
}
