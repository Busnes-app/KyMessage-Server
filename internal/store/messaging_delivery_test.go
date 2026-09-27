package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/google/uuid"
)

func deliveryDevice(t *testing.T, st store.Store, actor store.MessagingActor) (store.MessagingActor, string) {
	t.Helper()
	ctx := context.Background()
	e, sig := messagingEnrollment(t, actor)
	if err := st.Messaging().EnrollDevice(ctx, actor, e); err != nil {
		t.Fatal(err)
	}
	d, err := st.Messaging().VerifyDevice(ctx, actor, e.Device.ID, sig)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status == "pending" {
		if err := st.Messaging().ApproveDevice(ctx, actor, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	actor.DeviceTokenHash = e.TokenHash
	return actor, d.ID
}

func deliveryInput(t *testing.T, st store.Store, actor store.MessagingActor, room, kind string) store.MessagingEventInput {
	t.Helper()
	state, err := st.Messaging().DeliveryState(context.Background(), actor, room)
	if err != nil {
		t.Fatal(err)
	}
	return store.MessagingEventInput{ID: uuid.NewString(), Kind: kind, Epoch: state.Epoch, RosterHash: state.RosterHash, Payload: "b3BhcXVl", Welcomes: map[string]string{}}
}

func appendDelivery(t *testing.T, st store.Store, actor store.MessagingActor, room string, input store.MessagingEventInput) store.MessagingReceipt {
	t.Helper()
	receipt, err := st.Messaging().AppendEvent(context.Background(), actor, room, input)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestMessagingDeliveryLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a, _ := deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	room := "team"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: room, Name: "Team"}))
	// No application traffic before the initializing commit.
	if _, err := st.Messaging().AppendEvent(ctx, a, room, deliveryInput(t, st, a, room, "application")); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	first := deliveryInput(t, st, a, room, "commit")
	receipt := appendDelivery(t, st, a, room, first)
	if receipt.Sequence != 1 || receipt.Epoch != 1 {
		t.Fatal(receipt)
	}
	app := deliveryInput(t, st, a, room, "application")
	sent := appendDelivery(t, st, a, room, app)
	retry := appendDelivery(t, st, a, room, app)
	if sent != retry {
		t.Fatalf("duplicate assigned a different sequence: %v %v", sent, retry)
	}
	app.Payload = "dGFtcGVy"
	if _, err := st.Messaging().AppendEvent(ctx, a, room, app); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal(err)
	}
	must(st.Messaging().InviteMember(ctx, a, room, b.UserID))
	if _, err := st.Messaging().ReadEvents(ctx, b, room, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	must(st.Messaging().AcceptInvite(ctx, b, room))
	stale := deliveryInput(t, st, a, room, "application")
	if _, err := st.Messaging().AppendEvent(ctx, a, room, stale); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal(err)
	}
	if _, err := st.Messaging().ReadEvents(ctx, b, room, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	commit := deliveryInput(t, st, a, room, "commit")
	if _, err := st.Messaging().AppendEvent(ctx, a, room, commit); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("missing Welcome accepted", err)
	}
	commit.Welcomes[bid] = "Ym9iLW9ubHk="
	joined := appendDelivery(t, st, a, room, commit)
	page, err := st.Messaging().ReadEvents(ctx, b, room, 0)
	must(err)
	if page.StartSequence != joined.Sequence || len(page.Events) != 1 || page.Events[0].Welcome != commit.Welcomes[bid] {
		t.Fatal(page)
	}
	apage, err := st.Messaging().ReadEvents(ctx, a, room, 0)
	must(err)
	for _, event := range apage.Events {
		if event.Welcome != "" {
			t.Fatal("another device's Welcome leaked")
		}
	}
	// Old exact commit retry returns its original receipt after a later epoch.
	if got := appendDelivery(t, st, a, room, first); got != receipt {
		t.Fatal(got)
	}
	// Removal/rejoin without an intervening commit must not restore old access.
	must(st.Messaging().RemoveMember(ctx, a, room, b.UserID))
	if _, err := st.Messaging().ReadEvents(ctx, b, room, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	must(st.Messaging().InviteMember(ctx, a, room, b.UserID))
	must(st.Messaging().AcceptInvite(ctx, b, room))
	if _, err := st.Messaging().ReadEvents(ctx, b, room, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal("rejoin inherited old access", err)
	}
	rejoin := deliveryInput(t, st, a, room, "commit")
	rejoin.Welcomes[bid] = "bmV3LWJvYg=="
	rejoined := appendDelivery(t, st, a, room, rejoin)
	page, err = st.Messaging().ReadEvents(ctx, b, room, 0)
	must(err)
	if page.StartSequence != rejoined.Sequence || len(page.Events) != 1 {
		t.Fatal(page)
	}
	// New approved device pauses sends until included by an existing epoch device.
	b2, b2id := deliveryDevice(t, st, b)
	if _, err := st.Messaging().AppendEvent(ctx, b2, room, deliveryInput(t, st, b2, room, "commit")); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal("new device committed itself", err)
	}
	add := deliveryInput(t, st, a, room, "commit")
	add.Welcomes[b2id] = "c2Vjb25k"
	appendDelivery(t, st, a, room, add)
	must(st.Messaging().RevokeDevice(ctx, b, bid))
	if _, err := st.Messaging().ReadEvents(ctx, b, room, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	if _, err := st.Messaging().AppendEvent(ctx, a, room, deliveryInput(t, st, a, room, "application")); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("revocation did not pause sends", err)
	}
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "commit"))
	last := appendDelivery(t, st, b2, room, deliveryInput(t, st, b2, room, "application"))
	page, err = st.Messaging().ReadEvents(ctx, a, room, last.Sequence)
	must(err)
	if len(page.Events) != 0 || page.Next != last.Sequence {
		t.Fatal(page)
	}
	if _, err := st.Messaging().ReadEvents(ctx, a, room, last.Sequence+1); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal(err)
	}
	// Directory deactivation also changes the eligible roster, even without a
	// messaging-device mutation. Reactivation after removal needs a fresh Welcome.
	u, err := st.Users().GetUserByID(ctx, b.UserID)
	must(err)
	u.Status = "disabled"
	must(st.Users().UpdateUser(ctx, u))
	if _, err := st.Messaging().ReadEvents(ctx, b2, room, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal(err)
	}
	if _, err := st.Messaging().AppendEvent(ctx, a, room, deliveryInput(t, st, a, room, "application")); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("deactivation did not pause sends", err)
	}
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "commit"))
	u.Status = "active"
	must(st.Users().UpdateUser(ctx, u))
	if _, err := st.Messaging().ReadEvents(ctx, b2, room, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatal("reactivation inherited old epoch access", err)
	}
}

func TestMessagingConcurrentCommitsAcrossConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, _ := deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	if err := st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "r", Name: "Room"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().InviteMember(ctx, a, "r", b.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().AcceptInvite(ctx, b, "r"); err != nil {
		t.Fatal(err)
	}
	init := deliveryInput(t, st, a, "r", "commit")
	init.Welcomes[bid] = "d2VsY29tZQ=="
	appendDelivery(t, st, a, "r", init)
	x := deliveryInput(t, st, a, "r", "commit")
	y := deliveryInput(t, st, b, "r", "commit")
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := st.Messaging().AppendEvent(ctx, a, "r", x); results <- err }()
	go func() { <-start; _, err := other.Messaging().AppendEvent(ctx, b, "r", y); results <- err }()
	close(start)
	wins, conflicts := 0, 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				wins++
			} else if errors.Is(err, store.ErrMessagingConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("commit deadlock")
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	page, err := other.Messaging().ReadEvents(ctx, a, "r", 0)
	if err != nil || len(page.Events) != 2 || page.Next != 2 {
		t.Fatalf("%+v %v", page, err)
	}
}
