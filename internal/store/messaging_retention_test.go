package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func TestMessagingRetentionPreservesReceiptsAndRequiresRejoin(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, _ := deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	must(st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "retained", Name: "Retained", RetentionDays: 1}))
	appendDelivery(t, st, a, "retained", deliveryInput(t, st, a, "retained", "commit"))
	must(st.Messaging().InviteMember(ctx, a, "retained", b.UserID))
	must(st.Messaging().AcceptInvite(ctx, b, "retained"))
	join := deliveryInput(t, st, a, "retained", "commit")
	join.Welcomes[bid] = "d2VsY29tZQ=="
	receipt := appendDelivery(t, st, a, "retained", join)
	page, err := st.Messaging().ReadEvents(ctx, b, "retained", 0)
	must(err)
	if len(page.Events) != 1 || page.Events[0].ExpiresAt-page.Events[0].CreatedAt != 86400 {
		t.Fatal(page)
	}
	// Age actual stored ciphertext. No production clock override or test-only API.
	_, err = db.ExecContext(ctx, `UPDATE messaging_events SET expires_at = 1`)
	must(err)
	if _, err := st.Messaging().ReadEvents(ctx, b, "retained", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal("expired Welcome was offered", err)
	}
	must(st.Messaging().ExpireMessages(ctx))
	var payloads, welcomes, bytes, floor int64
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_events WHERE payload <> ''`).Scan(&payloads))
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_welcomes`).Scan(&welcomes))
	must(db.QueryRowContext(ctx, `SELECT retained_bytes, retained_from FROM messaging_rooms WHERE id = 'retained'`).Scan(&bytes, &floor))
	if payloads != 0 || welcomes != 0 || bytes != 0 || floor != receipt.Sequence+1 {
		t.Fatalf("cleanup: %d %d %d %d", payloads, welcomes, bytes, floor)
	}
	if retry := appendDelivery(t, st, a, "retained", join); retry != receipt {
		t.Fatal("expired retry appended again", retry)
	}
	changed := join
	changed.Payload = "dGFtcGVy"
	if _, err := st.Messaging().AppendEvent(ctx, a, "retained", changed); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("changed retry accepted", err)
	}
	// A caught-up ratchet can continue. An offline one cannot skip the missing prefix.
	next := appendDelivery(t, st, a, "retained", deliveryInput(t, st, a, "retained", "application"))
	page, err = st.Messaging().ReadEvents(ctx, a, "retained", receipt.Sequence)
	must(err)
	if len(page.Events) != 1 || page.Events[0].Sequence != next.Sequence {
		t.Fatal(page)
	}
	if _, err := st.Messaging().ReadEvents(ctx, b, "retained", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal(err)
	}
	// Ordinary explicit remove/reinvite establishes a new generation and Welcome floor.
	must(st.Messaging().RemoveMember(ctx, a, "retained", b.UserID))
	appendDelivery(t, st, a, "retained", deliveryInput(t, st, a, "retained", "commit"))
	must(st.Messaging().InviteMember(ctx, a, "retained", b.UserID))
	must(st.Messaging().AcceptInvite(ctx, b, "retained"))
	rejoin := deliveryInput(t, st, a, "retained", "commit")
	rejoin.Welcomes[bid] = "bmV3LXdlbGNvbWU="
	fresh := appendDelivery(t, st, a, "retained", rejoin)
	page, err = st.Messaging().ReadEvents(ctx, b, "retained", 0)
	must(err)
	if len(page.Events) != 1 || page.StartSequence != fresh.Sequence || page.Events[0].Welcome != "bmV3LXdlbGNvbWU=" {
		t.Fatal(page)
	}
	// Opening an older database clears expired ciphertext before it can be served.
	_, err = db.ExecContext(ctx, `UPDATE messaging_events SET expires_at = 1`)
	must(err)
	restored, err := store.Open(ctx, cfg)
	must(err)
	defer restored.Close()
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_events WHERE payload <> ''`).Scan(&payloads))
	if payloads != 0 {
		t.Fatal("startup retained expired ciphertext")
	}
	must(restored.Messaging().ExpireMessages(ctx)) // Idempotent sweeps do not subtract bytes twice.
	must(db.QueryRowContext(ctx, `SELECT retained_bytes FROM messaging_rooms WHERE id = 'retained'`).Scan(&bytes))
	if bytes != 0 {
		t.Fatal(bytes)
	}
}

func TestMessagingRetentionKeepsExpiryOrdered(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := deliveryDevice(t, st, messagingActor(t, st, "clock"))
	if err := st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "clock", Name: "Clock", RetentionDays: 7}); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, "clock", deliveryInput(t, st, a, "clock", "commit"))
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Simulate a prior append made while the server clock was ahead.
	future := time.Now().Add(8 * 24 * time.Hour).Unix()
	query := `UPDATE messaging_events SET expires_at = ?`
	if cfg.Driver == "postgres" {
		query = `UPDATE messaging_events SET expires_at = $1`
	}
	if _, err := db.ExecContext(ctx, query, future); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, "clock", deliveryInput(t, st, a, "clock", "application"))
	page, err := st.Messaging().ReadEvents(ctx, a, "clock", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].ExpiresAt != future {
		t.Fatal(page)
	}
}

func TestMessagingRetentionRaceAcrossConnections(t *testing.T) {
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
	a, _ := deliveryDevice(t, first, messagingActor(t, first, "racing"))
	if err := first.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "race", Name: "Race"}); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, first, a, "race", deliveryInput(t, first, a, "race", "commit"))
	input := deliveryInput(t, first, a, "race", "application")
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE messaging_events SET expires_at = 1`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := first.Messaging().AppendEvent(ctx, a, "race", input); results <- err }()
	go func() { <-start; results <- second.Messaging().ExpireMessages(ctx) }()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var bytes, sequence, floor int64
	if err := db.QueryRowContext(ctx, `SELECT retained_bytes, sequence, retained_from FROM messaging_rooms WHERE id = 'race'`).Scan(&bytes, &sequence, &floor); err != nil {
		t.Fatal(err)
	}
	if bytes != int64(len(input.Payload)) || sequence != 2 || floor != 2 {
		t.Fatalf("race accounting: %d %d %d", bytes, sequence, floor)
	}
}
