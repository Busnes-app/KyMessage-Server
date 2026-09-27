package store_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
