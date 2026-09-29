package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	assertMessagingUsageMatchesRows(t, st, db)
	// Age actual stored ciphertext. No production clock override or test-only API.
	age(t, db, driver, "retained", 400*86400)
	assertMessagingUsageMatchesRows(t, st, db) // Usage is read-only even when rows await expiry.
	if _, err := st.Messaging().ReadEvents(ctx, b, "retained", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal("expired Welcome was offered", err)
	}
	must(st.Messaging().ExpireMessages(ctx))
	assertMessagingUsageMatchesRows(t, st, db)
	var payloads, welcomes, bytes, floor int64
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_events`).Scan(&payloads))
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_welcomes`).Scan(&welcomes))
	must(db.QueryRowContext(ctx, `SELECT retained_bytes, retained_from FROM messaging_rooms WHERE id = 'retained'`).Scan(&bytes, &floor))
	if payloads != 0 || welcomes != 0 || bytes != 0 || floor != receipt.Sequence+1 {
		t.Fatalf("cleanup: %d %d %d %d", payloads, welcomes, bytes, floor)
	}
	if _, err := st.Messaging().AppendEvent(ctx, a, "retained", join); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("a purged stale-epoch retry must be refused", err)
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
	age(t, db, driver, "retained", 400*86400)
	restored, err := store.Open(ctx, cfg)
	must(err)
	defer restored.Close()
	must(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messaging_events`).Scan(&payloads))
	if payloads != 0 {
		t.Fatal("startup retained expired ciphertext")
	}
	must(restored.Messaging().ExpireMessages(ctx)) // Idempotent sweeps do not subtract bytes twice.
	must(db.QueryRowContext(ctx, `SELECT retained_bytes FROM messaging_rooms WHERE id = 'retained'`).Scan(&bytes))
	if bytes != 0 {
		t.Fatal(bytes)
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
	if err := first.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "race", Name: "Race", RetentionDays: 1}); err != nil {
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
	age(t, db, driver, "race", 400*86400)
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

// retentionDB opens the store and a raw handle on the same database.
func retentionDB(t *testing.T) (context.Context, store.Store, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
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
	return ctx, st, db, driver
}

// age moves every event in room back by seconds, as if written that long ago.
func age(t *testing.T, db *sql.DB, driver, room string, seconds int64) {
	t.Helper()
	q := `UPDATE messaging_events SET created_at = created_at - ? WHERE room_id = ?`
	if driver == "pgx" {
		q = `UPDATE messaging_events SET created_at = created_at - $1 WHERE room_id = $2`
	}
	if _, err := db.Exec(q, seconds, room); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, driver, query, room string) int64 {
	t.Helper()
	if driver == "pgx" {
		query = strings.ReplaceAll(query, "?", "$1")
	}
	var n int64
	if err := db.QueryRow(query, room).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// purgeRoomFixture creates a room with the given retention, a member bob with a Welcome,
// and three events (join commit, then two applications).
func purgeRoomFixture(t *testing.T, st store.Store, room string, days int64) (a, b store.MessagingActor) {
	t.Helper()
	ctx := context.Background()
	a, _ = deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	if err := st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: room, Name: room, RetentionDays: days}); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "commit"))
	if err := st.Messaging().InviteMember(ctx, a, room, b.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().AcceptInvite(ctx, b, room); err != nil {
		t.Fatal(err)
	}
	join := deliveryInput(t, st, a, room, "commit")
	join.Welcomes[bid] = "d2VsY29tZQ=="
	appendDelivery(t, st, a, room, join)
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "application"))
	return a, b
}

func TestPurgeDeletesEventsWelcomesAndAuditRows(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	_, b := purgeRoomFixture(t, st, "purged", 1)
	age(t, db, driver, "purged", 2*86400)
	backdateAudit := `UPDATE audit_records SET created_at = ? WHERE resource = ?`
	if driver == "pgx" {
		backdateAudit = `UPDATE audit_records SET created_at = $1 WHERE resource = $2`
	}
	if _, err := db.Exec(backdateAudit, time.Now().UTC().Add(-48*time.Hour), "purged"); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().ExpireMessages(ctx); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int64{
		`SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`:                                       0,
		`SELECT COUNT(*) FROM messaging_welcomes WHERE room_id = ?`:                                     0,
		`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.event_accepted' AND resource = ?`: 0,
		`SELECT retained_bytes FROM messaging_rooms WHERE id = ?`:                                       0,
		`SELECT retained_from FROM messaging_rooms WHERE id = ?`:                                        4,
		`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.room_created' AND resource = ?`:   1,
	} {
		if got := count(t, db, driver, query, "purged"); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	if _, err := st.Messaging().ReadEvents(ctx, b, "purged", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal("a reader behind the purged prefix must see the gap", err)
	}
}

func TestPurgeOffKeepsEverything(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	purgeRoomFixture(t, st, "kept", 0)
	age(t, db, driver, "kept", 400*86400)
	if err := st.Messaging().ExpireMessages(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, driver, `SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`, "kept"); got != 3 {
		t.Fatalf("Off purged events: %d left", got)
	}
}

func TestCreatedAtStaysMonotonicWhenClockMovesBack(t *testing.T) {
	_, st, db, driver := retentionDB(t)
	a, _ := purgeRoomFixture(t, st, "clock", 1)
	future := `UPDATE messaging_events SET created_at = ? WHERE room_id = ? AND sequence = 3`
	if driver == "pgx" {
		future = `UPDATE messaging_events SET created_at = $1 WHERE room_id = $2 AND sequence = 3`
	}
	ahead := time.Now().Unix() + 3600
	if _, err := db.Exec(future, ahead, "clock"); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, "clock", deliveryInput(t, st, a, "clock", "application"))
	if got := count(t, db, driver, `SELECT created_at FROM messaging_events WHERE room_id = ? AND sequence = 4`, "clock"); got < ahead {
		t.Fatalf("created_at went backwards: %d < %d", got, ahead)
	}
}

func TestPurgeBytesMatchDeletedRowsWhenCreatedAtNonMonotonic(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	purgeRoomFixture(t, st, "skew", 1)
	set := `UPDATE messaging_events SET created_at = ? WHERE room_id = ? AND sequence = ?`
	if driver == "pgx" {
		set = `UPDATE messaging_events SET created_at = $1 WHERE room_id = $2 AND sequence = $3`
	}
	now := time.Now().Unix()
	// Sequence 1 is recent, sequence 3 is past the window: pre-migration skew.
	for seq, at := range map[int]int64{1: now, 2: now, 3: now - 2*86400} {
		if _, err := db.Exec(set, at, "skew", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Messaging().ExpireMessages(ctx); err != nil {
		t.Fatal(err)
	}
	var bytes, payloads, welcomes int64
	if err := db.QueryRow(`SELECT retained_bytes FROM messaging_rooms WHERE id = 'skew'`).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_events WHERE room_id = 'skew'`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_welcomes WHERE room_id = 'skew'`).Scan(&welcomes); err != nil {
		t.Fatal(err)
	}
	if bytes != payloads+welcomes {
		t.Fatalf("retained_bytes %d, rows hold %d", bytes, payloads+welcomes)
	}
}
