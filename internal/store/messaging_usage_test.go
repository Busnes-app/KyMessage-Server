package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func assertMessagingUsageMatchesRows(t *testing.T, st store.Store, db *sql.DB) {
	t.Helper()
	usage, err := st.Messaging().Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var active, receipts, events, welcomes int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_events WHERE payload <> ''`).Scan(&active, &events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messaging_events`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_welcomes`).Scan(&welcomes); err != nil {
		t.Fatal(err)
	}
	if usage.ActiveEvents != active || usage.RetainedBytes != events+welcomes || usage.Receipts != receipts {
		t.Fatalf("usage %+v differs from stored events=%d bytes=%d receipts=%d", usage, active, events+welcomes, receipts)
	}
}

func TestMessagingUsageBoundsAndPrioritizesRoomLimits(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	usage, err := st.Messaging().Usage(ctx)
	if err != nil || usage.Rooms != 0 || usage.ActiveEvents != 0 || usage.RetainedBytes != 0 || usage.Receipts != 0 || len(usage.LargestRooms) != 0 {
		t.Fatal(usage, err)
	}
	first, _ := deliveryDevice(t, st, messagingActor(t, st, "usage-a"))
	second, _ := deliveryDevice(t, st, messagingActor(t, st, "usage-b"))
	actors := []store.MessagingActor{first, second}
	for i := range 102 {
		if err := st.Messaging().CreateRoom(ctx, actors[i%2], store.MessagingRoom{ID: fmt.Sprintf("room-%03d", i), Name: "Synthetic usage"}); err != nil {
			t.Fatal(err)
		}
	}
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Synthetic metadata exercises ranking without generating a million receipts.
	// The retention lifecycle separately compares real rows with metadata totals.
	for _, statement := range []string{
		`UPDATE messaging_rooms SET sequence = 900000, retained_from = 900001, owner_identity_generation = 0 WHERE id = 'room-101'`,
		`UPDATE messaging_rooms SET sequence = 3500, retained_bytes = 3500 WHERE id = 'room-100'`,
		`UPDATE messaging_rooms SET sequence = 1, retained_bytes = 28000000 WHERE id = 'room-099'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	usage, err = st.Messaging().Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Rooms != 102 || len(usage.LargestRooms) != 100 || usage.ActiveEvents != 3501 || usage.Receipts != 903501 || usage.RetainedBytes != 28003500 {
		t.Fatal(usage)
	}
	for i, id := range []string{"room-099", "room-100", "room-101"} {
		if usage.LargestRooms[i].ID != id {
			t.Fatalf("near-limit room %s not prioritized: %+v", id, usage.LargestRooms[:3])
		}
	}
	if !usage.LargestRooms[2].Retired {
		t.Fatal("retired room hidden")
	}
}
