package backup_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// openedMessages collects the fixture's messages capsule and writes its members to a
// directory, standing in for capsule.Open. A Welcome for the pending bob-new is added so the
// import can be seen keeping rows that point at devices it does not import.
func openedMessages(t *testing.T) string {
	t.Helper()
	cfg, _ := seedMessagingFixture(t)
	if _, err := rawDB(t, cfg).Exec(`INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES ('room',1,'bob-new','w2')`); err != nil {
		t.Fatal(err)
	}
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return writeOpened(t, payload)
}

func writeOpened(t *testing.T, payload recoveryclient.Payload) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range payload.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// importTarget is a fresh database at the current schema holding only the named users, as
// a people restore leaves it.
func importTarget(t *testing.T, users ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ky_server.db")
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, u := range users {
		if _, err := db.Exec(`INSERT INTO users (id,username,created_at,updated_at) VALUES (?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, u, u); err != nil {
			t.Fatal(err)
		}
	}
	return path, db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// messagingSnapshot is every messaging row count plus the audit count, to prove a refusal
// changed nothing.
func messagingSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []string
	for _, table := range []string{"messaging_identities", "messaging_devices", "messaging_rooms", "messaging_members", "messaging_epoch_devices", "messaging_events", "messaging_welcomes", "audit_records"} {
		out = append(out, fmt.Sprintf("%s=%d", table, count(t, db, "SELECT COUNT(*) FROM "+table)))
	}
	return strings.Join(out, " ")
}

func TestImportMessagesDropsMissingPeopleAndSuspendsDevices(t *testing.T) {
	opened := openedMessages(t)
	path, db := importTarget(t, "alice", "bob")
	counts, err := backup.ImportMessages(context.Background(), path, opened)
	if err != nil {
		t.Fatal(err)
	}
	want := backup.ImportCounts{Rooms: 1, Members: 2, DroppedMembers: 1, Devices: 3, DroppedDevices: 1, Events: 3}
	if counts != want {
		t.Fatalf("counts %+v, want %+v", counts, want)
	}
	var epoch, sequence, retainedFrom, retainedBytes int64
	if err := db.QueryRow(`SELECT epoch, sequence, retained_from, retained_bytes FROM messaging_rooms WHERE id = 'room'`).Scan(&epoch, &sequence, &retainedFrom, &retainedBytes); err != nil {
		t.Fatal(err)
	}
	// Three 4-byte events plus the 7- and 2-byte Welcomes.
	if epoch != 1 || sequence != 3 || retainedFrom != 1 || retainedBytes != 3*4+7+2 {
		t.Fatalf("room epoch=%d sequence=%d retained_from=%d retained_bytes=%d", epoch, sequence, retainedFrom, retainedBytes)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_members WHERE user_id IN ('alice','bob')`); n != 2 || count(t, db, `SELECT COUNT(*) FROM messaging_members`) != 2 {
		t.Fatalf("members: %d", n)
	}
	for _, id := range []string{"alice-phone", "bob-laptop"} {
		if count(t, db, `SELECT COUNT(*) FROM messaging_devices WHERE id = ? AND status = 'approved' AND token_hash IS NULL AND challenge = '' AND enrollment_session = ''`, id) != 1 {
			t.Errorf("%s not imported suspended", id)
		}
	}
	if count(t, db, `SELECT COUNT(*) FROM messaging_devices WHERE id = 'alice-old' AND status = 'revoked'`) != 1 {
		t.Error("revoked device not imported as revoked")
	}
	if count(t, db, `SELECT COUNT(*) FROM messaging_devices WHERE id IN ('bob-new','carol-tablet')`) != 0 {
		t.Error("pending or missing user's device imported")
	}
	if count(t, db, `SELECT COUNT(*) FROM messaging_identities`) != 2 {
		t.Error("identities of missing people imported")
	}
	// As if carol had been deleted: her epoch-device row and the Welcome for the pending
	// device have no foreign key to devices, so deletion would have kept them.
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_epoch_devices`); n != 3 {
		t.Errorf("epoch devices %d, want 3", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_events`); n != 3 {
		t.Errorf("events %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_welcomes`); n != 2 {
		t.Errorf("welcomes %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_records WHERE action = 'restore.messages_imported' AND details LIKE '%dropped_members=1%'`); n != 1 {
		t.Errorf("audit rows %d", n)
	}

	before := messagingSnapshot(t, db)
	if _, err := backup.ImportMessages(context.Background(), path, opened); err == nil || !strings.Contains(err.Error(), "already has messaging data") {
		t.Fatalf("second run: %v", err)
	}
	if after := messagingSnapshot(t, db); after != before {
		t.Fatalf("second run changed rows:\n%s\n%s", before, after)
	}
}

func TestImportMessagesRetiresRoomWithoutOwner(t *testing.T) {
	opened := openedMessages(t)
	path, db := importTarget(t, "bob", "carol")
	counts, err := backup.ImportMessages(context.Background(), path, opened)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Rooms != 0 || counts.RetiredRooms != 1 || counts.Members != 0 || counts.Events != 0 {
		t.Fatalf("counts %+v", counts)
	}
	for _, table := range []string{"messaging_rooms", "messaging_members", "messaging_epoch_devices", "messaging_events", "messaging_welcomes"} {
		if n := count(t, db, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Errorf("%s has %d rows for a room whose owner is gone", table, n)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_devices`); n != 2 {
		t.Errorf("bob's and carol's approved devices: %d", n)
	}
}

func TestImportMessagesRefusesBadInputBeforeWriting(t *testing.T) {
	cases := map[string]func(t *testing.T, messages string){
		"too many parts": func(t *testing.T, messages string) {
			part := filepath.Join(messages, "events-001.db")
			data, err := os.ReadFile(part)
			if err != nil {
				t.Fatal(err)
			}
			for i := 2; i <= 9; i++ {
				if err := os.WriteFile(filepath.Join(messages, fmt.Sprintf("events-%03d.db", i)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		},
		"bad part name": func(t *testing.T, messages string) {
			if err := os.Rename(filepath.Join(messages, "events-001.db"), filepath.Join(messages, "events-1.db")); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked part": func(t *testing.T, messages string) {
			part := filepath.Join(messages, "events-001.db")
			outside := filepath.Join(t.TempDir(), "outside.db")
			if err := os.Rename(part, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, part); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked directory": func(t *testing.T, messages string) {
			outside := filepath.Join(t.TempDir(), "messages")
			if err := os.Rename(messages, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, messages); err != nil {
				t.Fatal(err)
			}
		},
		"missing accounts": func(t *testing.T, messages string) {
			if err := os.Remove(filepath.Join(messages, "accounts.db")); err != nil {
				t.Fatal(err)
			}
		},
		// A Welcome without its event fails the foreign key inside the transaction.
		"orphan welcome": func(t *testing.T, messages string) {
			db, err := sql.Open("sqlite", filepath.Join(messages, "events-001.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`DELETE FROM messaging_events WHERE sequence = 1`); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			opened := openedMessages(t)
			corrupt(t, filepath.Join(opened, filepath.FromSlash(backup.MessagesDir)))
			path, db := importTarget(t, "alice", "bob")
			before := messagingSnapshot(t, db)
			_, err := backup.ImportMessages(context.Background(), path, opened)
			if err == nil {
				t.Fatal("accepted")
			}
			t.Logf("refused: %v", err)
			if after := messagingSnapshot(t, db); after != before {
				t.Fatalf("refusal wrote rows:\n%s\n%s", before, after)
			}
		})
	}
}

// One room spans two parts; the Welcome at sequence 5 travels with its event in the first.
func TestImportMessagesAcrossParts(t *testing.T) {
	const events, size = 10, 32 << 10
	backup.SetMessagesBudgets(t, 200<<10, recoveryclient.MaxCapsuleTotalBytes)
	cfg, _ := sqliteInstance(t)
	seedRoom(t, rawDB(t, cfg), "big", events, size)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	parts := eventParts(payload)
	if len(parts) != 2 {
		t.Fatalf("parts: %v", parts)
	}
	first := openMember(t, payload, parts[0])
	if count(t, first, `SELECT COUNT(*) FROM messaging_welcomes WHERE sequence = 5`) != 1 || count(t, first, `SELECT COUNT(*) FROM messaging_events WHERE sequence = 5`) != 1 {
		t.Fatal("the Welcome at sequence 5 is not in the first part with its event")
	}
	path, db := importTarget(t, "owner-big")
	counts, err := backup.ImportMessages(context.Background(), path, writeOpened(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Rooms != 1 || counts.Events != events {
		t.Fatalf("counts %+v", counts)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_events WHERE room_id = 'big'`); n != events {
		t.Fatalf("events %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messaging_welcomes WHERE room_id = 'big'`); n != 2 {
		t.Fatalf("welcomes %d", n)
	}
}
