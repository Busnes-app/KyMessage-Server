package store_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/google/uuid"
)

func messagingActor(t *testing.T, st store.Store, id string) store.MessagingActor {
	t.Helper()
	ctx := context.Background()
	if err := st.Users().CreateUser(ctx, &store.User{ID: id, Username: id, Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: id}); err != nil {
		t.Fatal(err)
	}
	actor := store.MessagingActor{UserID: id, SessionHash: crypto.RandomHex(32)}
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: actor.SessionHash, UserID: id, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	return actor
}

func messagingEnrollment(t *testing.T, actor store.MessagingActor) (store.MessagingEnrollment, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := store.MessagingEnrollment{
		Device:    store.MessagingDevice{ID: uuid.NewString(), UserID: actor.UserID, Name: "Browser", PublicKey: base64.StdEncoding.EncodeToString(pub), CreatedAt: time.Now().Unix()},
		Challenge: "test-only enrollment challenge " + uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute).Unix(), TokenHash: crypto.RandomHex(32),
	}
	return enrollment, ed25519.Sign(key, []byte(enrollment.Challenge))
}

func TestMessagingFirstDeviceRaceAcrossConnections(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	first, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	actor := messagingActor(t, first, "alice")
	a, asig := messagingEnrollment(t, actor)
	b, bsig := messagingEnrollment(t, actor)
	for _, enrollment := range []store.MessagingEnrollment{a, b} {
		if err := first.Messaging().EnrollDevice(ctx, actor, enrollment); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan string, 2)
	errorsCh := make(chan error, 2)
	for _, attempt := range []struct {
		st  store.Store
		id  string
		sig []byte
	}{{first, a.Device.ID, asig}, {second, b.Device.ID, bsig}} {
		go func() {
			<-start
			d, err := attempt.st.Messaging().VerifyDevice(ctx, actor, attempt.id, attempt.sig)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- d.Status
		}()
	}
	close(start)
	statuses := map[string]int{}
	for range 2 {
		select {
		case status := <-results:
			statuses[status]++
		case err := <-errorsCh:
			t.Fatal(err)
		case <-time.After(10 * time.Second):
			t.Fatal("verification deadlock")
		}
	}
	if statuses["approved"] != 1 || statuses["pending"] != 1 {
		t.Fatal(statuses)
	}
}

func TestMessagingEnrollmentExpirySessionAndRevocation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	actor := messagingActor(t, st, "alice")
	expired, sig := messagingEnrollment(t, actor)
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := st.Messaging().EnrollDevice(ctx, actor, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().VerifyDevice(ctx, actor, expired.Device.ID, sig); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	fresh, signature := messagingEnrollment(t, actor)
	if err := st.Messaging().EnrollDevice(ctx, actor, fresh); err != nil {
		t.Fatal(err)
	}
	otherSession := actor
	otherSession.SessionHash = crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: otherSession.SessionHash, UserID: actor.UserID, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().VerifyDevice(ctx, otherSession, fresh.Device.ID, signature); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	device, err := st.Messaging().VerifyDevice(ctx, actor, fresh.Device.ID, signature)
	if err != nil || device.Status != "approved" {
		t.Fatalf("%v %v", device, err)
	}
	actor.DeviceTokenHash = fresh.TokenHash
	if err := st.Messaging().RevokeDevice(ctx, actor, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ListRooms(ctx, actor, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	if err := st.Sessions().DeleteSession(ctx, actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ListDevices(ctx, actor); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
}

func TestMessagingConcurrentCrossInvitations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := newTestStore(t)
	a := messagingActor(t, st, "alice")
	b := messagingActor(t, st, "bob")
	for _, actor := range []*store.MessagingActor{&a, &b} {
		enrollment, sig := messagingEnrollment(t, *actor)
		if err := st.Messaging().EnrollDevice(ctx, *actor, enrollment); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Messaging().VerifyDevice(ctx, *actor, enrollment.Device.ID, sig); err != nil {
			t.Fatal(err)
		}
		actor.DeviceTokenHash = enrollment.TokenHash
		if err := st.Messaging().CreateRoom(ctx, *actor, store.MessagingRoom{ID: actor.UserID + "-room", Name: "Room", CreatedAt: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, pair := range [][2]store.MessagingActor{{a, b}, {b, a}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- st.Messaging().InviteMember(ctx, pair[0], pair[0].UserID+"-room", pair[1].UserID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// suspendedDevice enrolls an approved device, then clears its credential as
// restore-messages does. It returns the owner (without a device token), the
// device ID, the device's old token hash and its signing key.
func suspendedDevice(t *testing.T, st store.Store, db *sql.DB, actor store.MessagingActor) (store.MessagingActor, string, string, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := store.MessagingEnrollment{
		Device:    store.MessagingDevice{ID: uuid.NewString(), UserID: actor.UserID, Name: "Browser", PublicKey: base64.StdEncoding.EncodeToString(pub), CreatedAt: time.Now().Unix()},
		Challenge: "test-only enrollment " + uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute).Unix(), TokenHash: crypto.RandomHex(32),
	}
	if err := st.Messaging().EnrollDevice(ctx, actor, e); err != nil {
		t.Fatal(err)
	}
	d, err := st.Messaging().VerifyDevice(ctx, actor, e.Device.ID, ed25519.Sign(key, []byte(e.Challenge)))
	if err != nil || d.Status != "approved" {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messaging_devices SET token_hash = NULL, suspended_at = $1 WHERE id = $2`, time.Now().Unix(), d.ID); err != nil {
		t.Fatal(err)
	}
	return actor, d.ID, e.TokenHash, key
}

func rawMessagingDB(t *testing.T) (store.Store, *sql.DB) {
	t.Helper()
	cfg := testdb.Config(t)
	st, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return st, db
}

func deviceStatus(t *testing.T, st store.Store, actor store.MessagingActor, id string) string {
	t.Helper()
	devices, err := st.Messaging().ListDevices(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.ID == id {
			return d.Status
		}
	}
	t.Fatalf("device %s missing", id)
	return ""
}

func TestMessagingSuspendedDeviceResume(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	owner, id, oldToken, key := suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	if got := deviceStatus(t, st, owner, id); got != "suspended" {
		t.Fatal(got)
	}
	old := owner
	old.DeviceTokenHash = oldToken
	if _, err := st.Messaging().DeliveryState(ctx, old, "any-room"); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("old token read: %v", err)
	}
	if err := st.Messaging().PublishKeyPackage(ctx, old, store.MessagingKeyPackage{ID: uuid.NewString(), Payload: "a2V5", ExpiresAt: time.Now().Add(time.Hour).Unix()}); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("old token publish: %v", err)
	}
	pending, psig := messagingEnrollment(t, owner)
	if err := st.Messaging().EnrollDevice(ctx, owner, pending); err != nil {
		t.Fatal(err)
	}
	if d, err := st.Messaging().VerifyDevice(ctx, owner, pending.Device.ID, psig); err != nil || d.Status != "pending" {
		t.Fatalf("%v %v", d, err)
	}
	if err := st.Messaging().ApproveDevice(ctx, old, pending.Device.ID); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("suspended device approved another: %v", err)
	}

	challenge := "resume challenge " + uuid.NewString()
	newToken := crypto.RandomHex(32)
	if err := st.Messaging().StartDeviceResume(ctx, owner, id, newToken, challenge, time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	// The pending credential is not usable before the key is proven.
	early := owner
	early.DeviceTokenHash = newToken
	if _, err := st.Messaging().ListRooms(ctx, early, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("unproven token accepted: %v", err)
	}
	// Another session of the same account cannot finish this resume.
	other := owner
	other.SessionHash = crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: other.SessionHash, UserID: owner.UserID, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ResumeDevice(ctx, other, id, ed25519.Sign(key, []byte(challenge))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other session: %v", err)
	}
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := st.Messaging().ResumeDevice(ctx, owner, id, ed25519.Sign(wrong, []byte(challenge))); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("wrong key: %v", err)
	}
	if got := deviceStatus(t, st, owner, id); got != "suspended" {
		t.Fatal(got)
	}
	if rec, err := st.Audit().LatestAuditRecord(ctx, "messaging.device_resume_failed"); err != nil || rec.Resource != id {
		t.Fatalf("failed verify not audited: %v %v", rec, err)
	}
	// The failure left the challenge in place: the right signature still works.
	d, err := st.Messaging().ResumeDevice(ctx, owner, id, ed25519.Sign(key, []byte(challenge)))
	if err != nil || d.Status != "approved" {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := st.Messaging().DeliveryState(ctx, early, "any-room"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resumed token not accepted: %v", err)
	}
	if _, err := st.Messaging().ResumeDevice(ctx, owner, id, ed25519.Sign(key, []byte(challenge))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replay: %v", err)
	}
	if rec, err := st.Audit().LatestAuditRecord(ctx, "messaging.device_resumed"); err != nil || rec.Resource != id || !strings.Contains(rec.Details, "fingerprint=") {
		t.Fatalf("resume not audited: %v %v", rec, err)
	}
}

func TestMessagingSuspendedDeviceResumeRefusals(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	owner, id, _, key := suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	expires := time.Now().Add(5 * time.Minute).Unix()

	// Expired challenge.
	if err := st.Messaging().StartDeviceResume(ctx, owner, id, crypto.RandomHex(32), "stale", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ResumeDevice(ctx, owner, id, ed25519.Sign(key, []byte("stale"))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}

	// Another account's device.
	bob := messagingActor(t, st, "bob")
	if err := st.Messaging().StartDeviceResume(ctx, bob, id, crypto.RandomHex(32), "x", expires); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-account: %v", err)
	}

	// A device from before an identity reset.
	if _, err := db.ExecContext(ctx, `UPDATE messaging_identities SET generation = generation + 1 WHERE user_id = $1`, owner.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().StartDeviceResume(ctx, owner, id, crypto.RandomHex(32), "x", expires); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("stale generation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messaging_identities SET generation = generation - 1 WHERE user_id = $1`, owner.UserID); err != nil {
		t.Fatal(err)
	}

	// Revoking a suspended device leaves it revoked and unable to resume.
	if err := st.Messaging().RevokeDevice(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if got := deviceStatus(t, st, owner, id); got != "revoked" {
		t.Fatal(got)
	}
	if err := st.Messaging().StartDeviceResume(ctx, owner, id, crypto.RandomHex(32), "x", expires); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestMessagingAdminSuspendedDevices(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	owner, id, _, _ := suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	live, liveID := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	devices, truncated, err := st.Messaging().SuspendedDevices(ctx, 1000)
	if err != nil || truncated {
		t.Fatal(truncated, err)
	}
	if len(devices) != 1 || devices[0].ID != id || devices[0].UserID != owner.UserID || devices[0].Username != "alice" || devices[0].PublicKey == "" || devices[0].IdentityGeneration != 1 {
		t.Fatalf("%+v", devices)
	}
	// Admin power stops at suspended devices.
	if err := st.Messaging().RevokeSuspendedDevice(ctx, "admin", "", liveID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("live device: %v", err)
	}
	if _, err := st.Messaging().ListRooms(ctx, live, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().RevokeSuspendedDevice(ctx, "admin", "", id); err != nil {
		t.Fatal(err)
	}
	if got := deviceStatus(t, st, owner, id); got != "revoked" {
		t.Fatal(got)
	}
	if err := st.Messaging().RevokeSuspendedDevice(ctx, "admin", "", id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	rec, err := st.Audit().LatestAuditRecord(ctx, "messaging.device_revoked_by_admin")
	if err != nil || rec.Resource != id || rec.UserID != "admin" || !strings.Contains(rec.Details, "user_id=alice") {
		t.Fatalf("%+v %v", rec, err)
	}
	if devices, _, err := st.Messaging().SuspendedDevices(ctx, 1000); err != nil || len(devices) != 0 {
		t.Fatalf("%+v %v", devices, err)
	}
}

func TestMessagingSuspendedDevicesReportTruncation(t *testing.T) {
	ctx := context.Background()
	st, db := rawMessagingDB(t)
	suspendedDevice(t, st, db, messagingActor(t, st, "alice"))
	suspendedDevice(t, st, db, messagingActor(t, st, "bob"))
	if devices, truncated, err := st.Messaging().SuspendedDevices(ctx, 1); err != nil || len(devices) != 1 || !truncated {
		t.Fatalf("%d %v %v", len(devices), truncated, err)
	}
	if devices, truncated, err := st.Messaging().SuspendedDevices(ctx, 2); err != nil || len(devices) != 2 || truncated {
		t.Fatalf("%d %v %v", len(devices), truncated, err)
	}
}
