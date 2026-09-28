package store_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/google/uuid"
)

func TestMessagingKeyPackageClaimsAcrossRooms(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, _ := deliveryDevice(t, first, messagingActor(t, first, "alice"))
	c, _ := deliveryDevice(t, first, messagingActor(t, first, "charlie"))
	b, bid := deliveryDevice(t, first, messagingActor(t, first, "bob"))
	for _, actor := range []store.MessagingActor{a, c} {
		must(first.Messaging().CreateRoom(ctx, actor, store.MessagingRoom{ID: actor.UserID, Name: "Room"}))
		must(first.Messaging().InviteMember(ctx, actor, actor.UserID, b.UserID))
		must(first.Messaging().AcceptInvite(ctx, b, actor.UserID))
	}
	raw := []byte("opaque synthetic KeyPackage")
	kp := store.MessagingKeyPackage{ID: crypto.SHA256Hex(raw), Payload: base64.StdEncoding.EncodeToString(raw), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	must(first.Messaging().PublishKeyPackage(ctx, b, kp))
	must(first.Messaging().PublishKeyPackage(ctx, b, kp))
	if err := first.Messaging().PublishKeyPackage(ctx, a, kp); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal(err)
	}
	if _, err := first.Messaging().ClaimKeyPackage(ctx, b, a.UserID, bid, uuid.NewString()); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal("new member drained package", err)
	}
	type result struct {
		actor   store.MessagingActor
		request string
		kp      store.MessagingKeyPackage
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i, actor := range []store.MessagingActor{a, c} {
		st := []store.Store{first, second}[i]
		go func() {
			<-start
			request := uuid.NewString()
			got, err := st.Messaging().ClaimKeyPackage(ctx, actor, actor.UserID, bid, request)
			results <- result{actor, request, got, err}
		}()
	}
	close(start)
	var winner result
	won, lost := 0, 0
	for range 2 {
		select {
		case r := <-results:
			if r.err == nil {
				winner = r
				won++
			} else if errors.Is(r.err, store.ErrNotFound) {
				lost++
			} else {
				t.Fatal(r.err)
			}
		case <-ctx.Done():
			t.Fatal("claim deadlock")
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("won=%d lost=%d", won, lost)
	}
	retry, err := second.Messaging().ClaimKeyPackage(ctx, winner.actor, winner.actor.UserID, bid, winner.request)
	must(err)
	if retry != winner.kp {
		t.Fatal("lost-ack retry differs")
	}
	if err := first.Messaging().PublishKeyPackage(ctx, b, kp); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("claimed package was republished", err)
	}
	must(first.Messaging().RemoveMember(ctx, winner.actor, winner.actor.UserID, b.UserID))
	if _, err := first.Messaging().ClaimKeyPackage(ctx, winner.actor, winner.actor.UserID, bid, winner.request); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("removed target still accessible", err)
	}
	must(first.Messaging().InviteMember(ctx, winner.actor, winner.actor.UserID, b.UserID))
	must(first.Messaging().AcceptInvite(ctx, b, winner.actor.UserID))
	if _, err := first.Messaging().ClaimKeyPackage(ctx, winner.actor, winner.actor.UserID, bid, winner.request); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("rejoin reused claim", err)
	}
	expired := store.MessagingKeyPackage{ID: crypto.SHA256Hex([]byte("expired")), Payload: "ZXhwaXJlZA==", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	must(first.Messaging().PublishKeyPackage(ctx, b, expired))
	if _, err := first.Messaging().ClaimKeyPackage(ctx, winner.actor, winner.actor.UserID, bid, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired package returned", err)
	}
}

func TestMessagingKeyPackageCapacityAndRevocation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a, _ := deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "r", Name: "Room"}))
	must(st.Messaging().InviteMember(ctx, a, "r", b.UserID))
	must(st.Messaging().AcceptInvite(ctx, b, "r"))
	for i := 0; i < 17; i++ {
		raw := []byte{byte(i)}
		err := st.Messaging().PublishKeyPackage(ctx, b, store.MessagingKeyPackage{ID: crypto.SHA256Hex(raw), Payload: base64.StdEncoding.EncodeToString(raw), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if i == 16 {
			if !errors.Is(err, store.ErrMessagingLimit) {
				t.Fatal(err)
			}
		} else {
			must(err)
		}
	}
	must(st.Messaging().RevokeDevice(ctx, b, bid))
	if _, err := st.Messaging().ClaimKeyPackage(ctx, a, "r", bid, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("revoked device package returned", err)
	}
	if err := st.Messaging().PublishKeyPackage(ctx, b, store.MessagingKeyPackage{}); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	must(st.Users().DeleteUser(ctx, b.UserID))
	used := []byte{0}
	if err := st.Messaging().PublishKeyPackage(ctx, a, store.MessagingKeyPackage{ID: crypto.SHA256Hex(used), Payload: base64.StdEncoding.EncodeToString(used), ExpiresAt: time.Now().Add(time.Hour).Unix()}); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("account deletion reset package publication history", err)
	}
}

func TestMessagingKeyPackagesStayInTheirPublishedRoom(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := deliveryDevice(t, st, messagingActor(t, st, "owner"))
	joiner, target := deliveryDevice(t, st, messagingActor(t, st, "joiner"))
	for _, room := range []string{"one", "two"} {
		must(st.Messaging().CreateRoom(ctx, owner, store.MessagingRoom{ID: room, Name: room}))
		must(st.Messaging().InviteMember(ctx, owner, room, joiner.UserID))
	}
	raw := []byte("room one join material")
	kp := store.MessagingKeyPackage{ID: crypto.SHA256Hex(raw), Payload: base64.StdEncoding.EncodeToString(raw), ExpiresAt: time.Now().Add(time.Hour).Unix(), RoomID: "one"}
	if err := st.Messaging().PublishKeyPackage(ctx, joiner, kp); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invitation alone allowed publication: %v", err)
	}
	must(st.Messaging().AcceptInvite(ctx, joiner, "one"))
	must(st.Messaging().AcceptInvite(ctx, joiner, "two"))
	must(st.Messaging().PublishKeyPackage(ctx, joiner, kp))
	must(st.Messaging().PublishKeyPackage(ctx, joiner, kp))
	moved := kp
	moved.RoomID = "two"
	if err := st.Messaging().PublishKeyPackage(ctx, joiner, moved); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatalf("publication moved rooms: %v", err)
	}
	if _, err := st.Messaging().ClaimKeyPackage(ctx, owner, "two", target, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong room consumed package: %v", err)
	}
	request := uuid.NewString()
	claimed, err := st.Messaging().ClaimKeyPackage(ctx, owner, "one", target, request)
	must(err)
	if claimed.ID != kp.ID || claimed.RoomID != "one" {
		t.Fatal(claimed)
	}
	retry, err := st.Messaging().ClaimKeyPackage(ctx, owner, "one", target, request)
	must(err)
	if retry != claimed {
		t.Fatal("room-scoped retry changed package")
	}
	if _, err := st.Messaging().ClaimKeyPackage(ctx, owner, "two", target, request); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatalf("claim replay crossed rooms: %v", err)
	}
}
