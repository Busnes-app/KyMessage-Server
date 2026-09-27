package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func TestMessagingIdentityReset(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	replacement, first, request := recoverySetup(t, st)
	old := replacement
	old.DeviceTokenHash = first.TokenHash
	bob, _ := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Messaging().CreateRoom(ctx, old, store.MessagingRoom{ID: "old-owned", Name: "Old owned"}))
	must(st.Messaging().InviteMember(ctx, old, "old-owned", bob.UserID))
	must(st.Messaging().AcceptInvite(ctx, bob, "old-owned"))
	must(st.Messaging().CreateRoom(ctx, bob, store.MessagingRoom{ID: "team", Name: "Team"}))
	appendDelivery(t, st, bob, "team", deliveryInput(t, st, bob, "team", "commit"))
	must(st.Messaging().InviteMember(ctx, bob, "team", old.UserID))
	must(st.Messaging().AcceptInvite(ctx, old, "team"))
	addition := deliveryInput(t, st, bob, "team", "commit")
	addition.Welcomes[first.Device.ID] = "b3BhcXVl"
	appendDelivery(t, st, bob, "team", addition)
	pendingBefore := deliveryInput(t, st, bob, "team", "application")
	must(st.Messaging().BeginRecoveryAuthentication(ctx, replacement, request))
	if _, err := st.Messaging().ResetIdentity(ctx, replacement, request.StateHash, "alice"); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("unconfirmed reset: %v", err)
	}
	request.StateHash = crypto.RandomHex(32)
	request.ResetConfirmed = true
	must(st.Messaging().BeginRecoveryAuthentication(ctx, replacement, request))
	if err := st.Messaging().CompleteRecoveryAuthentication(ctx, replacement, request.StateHash, "alice"); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("confirmed reset consumed as ordinary authentication: %v", err)
	}
	if _, err := st.Messaging().ResetIdentity(ctx, replacement, request.StateHash, "bob"); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("wrong subject: %v", err)
	}
	receipt, err := st.Messaging().ResetIdentity(ctx, replacement, request.StateHash, "alice")
	must(err)
	if receipt.IdentityGeneration != 2 || receipt.DeviceID != request.DeviceID {
		t.Fatal(receipt)
	}
	retry, err := st.Messaging().ResetIdentity(ctx, replacement, request.StateHash, "alice")
	must(err)
	if retry != receipt {
		t.Fatal("reset retry changed identity")
	}
	devices, err := st.Messaging().ListDevices(ctx, replacement)
	must(err)
	for _, d := range devices {
		if d.ID == request.DeviceID {
			if d.Status != "approved" || d.IdentityGeneration != 2 {
				t.Fatal(d)
			}
		} else if d.Status != "revoked" {
			t.Fatal(d)
		}
	}
	if _, err := st.Messaging().ListRooms(ctx, old, 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("old device access: %v", err)
	}
	rooms, err := st.Messaging().ListRooms(ctx, replacement, 0)
	must(err)
	if len(rooms) != 0 {
		t.Fatal("inherited old room access")
	}
	if err := st.Messaging().InviteMember(ctx, replacement, "old-owned", bob.UserID); err == nil {
		t.Fatal("inherited ownership")
	}
	if _, err := st.Messaging().AppendEvent(ctx, bob, "team", pendingBefore); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatalf("room did not pause: %v", err)
	}
	removal := deliveryInput(t, st, bob, "team", "commit")
	appendDelivery(t, st, bob, "team", removal)
	must(st.Messaging().InviteMember(ctx, bob, "team", replacement.UserID))
	must(st.Messaging().AcceptInvite(ctx, replacement, "team"))
	if _, err := st.Messaging().ReadEvents(ctx, replacement, "team", 0); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("history before Welcome: %v", err)
	}
	readd := deliveryInput(t, st, bob, "team", "commit")
	readd.Welcomes[request.DeviceID] = "bmV3LXdlbGNvbWU="
	joined := appendDelivery(t, st, bob, "team", readd)
	page, err := st.Messaging().ReadEvents(ctx, replacement, "team", 0)
	must(err)
	if page.StartSequence != joined.Sequence {
		t.Fatalf("old history leaked: %+v", page)
	}
	must(st.Messaging().CreateRoom(ctx, replacement, store.MessagingRoom{ID: "new-owned", Name: "New owned"}))
	must(st.Messaging().InviteMember(ctx, replacement, "new-owned", bob.UserID))
	state, err := st.Messaging().DeliveryState(ctx, replacement, "new-owned")
	must(err)
	if len(state.Devices) != 1 || state.Devices[0].IdentityGeneration != 2 {
		t.Fatal(state)
	}
}

func TestMessagingIdentityResetConcurrent(t *testing.T) {
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
	actor, _, request := recoverySetup(t, first)
	request.ResetConfirmed = true
	if err := first.Messaging().BeginRecoveryAuthentication(ctx, actor, request); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan store.MessagingResetReceipt, 2)
	for _, st := range []store.Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.Messaging().ResetIdentity(ctx, actor, request.StateHash, "alice")
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	var saved store.MessagingResetReceipt
	for r := range results {
		if r.IdentityGeneration != 2 {
			t.Fatal(r)
		}
		if saved.DeviceID != "" && r != saved {
			t.Fatal("concurrent retry changed receipt")
		}
		saved = r
	}
}
