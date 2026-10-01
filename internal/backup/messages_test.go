package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// seedMessagingFixture is a live SQLite instance with users alice (room owner), bob and
// carol; approved devices alice-phone, bob-laptop and carol-tablet, a revoked alice-old and
// a pending bob-new; room "room" with all three as active members and epoch devices; events
// 1-3 from alice-phone; and a Welcome for bob-laptop at sequence 1.
func seedMessagingFixture(t *testing.T) (*config.Config, store.Store) {
	t.Helper()
	cfg, st := sqliteInstance(t)
	db := rawDB(t, cfg)
	statements := []string{}
	for _, user := range []string{"alice", "bob", "carol"} {
		statements = append(statements,
			fmt.Sprintf(`INSERT INTO users (id,username,created_at,updated_at) VALUES ('%[1]s','%[1]s',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, user),
			fmt.Sprintf(`INSERT INTO messaging_identities (user_id) VALUES ('%s')`, user))
	}
	for _, d := range [][3]string{{"alice-phone", "alice", "approved"}, {"alice-old", "alice", "revoked"}, {"bob-laptop", "bob", "approved"}, {"bob-new", "bob", "pending"}, {"carol-tablet", "carol", "approved"}} {
		token := "NULL"
		if d[2] == "approved" {
			token = "'tok-" + d[0] + "'"
		}
		statements = append(statements, fmt.Sprintf(`INSERT INTO messaging_devices (id,user_id,name,public_key,status,challenge,enrollment_session,expires_at,token_hash,created_at,verified_at) VALUES ('%[1]s','%[2]s','%[1]s','pk-%[1]s','%[3]s','ch-%[1]s','sess-%[1]s',1,%[4]s,1,1)`, d[0], d[1], d[2], token))
	}
	statements = append(statements, `INSERT INTO messaging_rooms (id,name,owner_id,created_at,epoch,sequence,retained_bytes) VALUES ('room','r','alice',1,1,3,12)`)
	for _, m := range [][2]string{{"alice", "alice-phone"}, {"bob", "bob-laptop"}, {"carol", "carol-tablet"}} {
		statements = append(statements,
			fmt.Sprintf(`INSERT INTO messaging_members (room_id,user_id,status,generation) VALUES ('room','%s','active',1)`, m[0]),
			fmt.Sprintf(`INSERT INTO messaging_epoch_devices (room_id,device_id,generation,joined_sequence) VALUES ('room','%s',1,1)`, m[1]))
	}
	for seq := 1; seq <= 3; seq++ {
		statements = append(statements, fmt.Sprintf(`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES ('room',%[1]d,'alice-phone','ev-%[1]d','application',1,'h','b2xk','h',1)`, seq))
	}
	statements = append(statements, `INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES ('room',1,'bob-laptop','welcome')`)
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return cfg, st
}

func rawDB(t *testing.T, cfg *config.Config) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedRoom inserts one room owned by a fresh user and n events of size payload bytes each,
// with a Welcome on every fifth event, in one transaction.
func seedRoom(t *testing.T, db *sql.DB, room string, n, size int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	owner := "owner-" + room
	exec(`INSERT INTO users (id,username,created_at,updated_at) VALUES (?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, owner, owner)
	exec(`INSERT INTO messaging_rooms (id,name,owner_id,created_at,epoch,sequence,retained_bytes) VALUES (?,?,?,1,1,?,?)`, room, room, owner, n, n*size)
	payload := strings.Repeat("A", size)
	for seq := 1; seq <= n; seq++ {
		exec(`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES (?,?,'dev',?,'application',1,'h',?,'h',1)`, room, seq, fmt.Sprint(seq), payload)
		if seq%5 == 0 {
			exec(`INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES (?,?,'dev','w')`, room, seq)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// openMember writes one payload member to a temp file and opens it read-only.
func openMember(t *testing.T, payload recoveryclient.Payload, path string) *sql.DB {
	t.Helper()
	f := findFile(payload.Files, path)
	if f == nil {
		t.Fatalf("payload has no %s", path)
	}
	file := filepath.Join(t.TempDir(), "member.db")
	if err := os.WriteFile(file, f.Data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func eventParts(payload recoveryclient.Payload) []string {
	var parts []string
	for _, f := range payload.Files {
		if strings.HasPrefix(f.Path, backup.MessagesDir+"/events-") {
			parts = append(parts, f.Path)
		}
	}
	slices.Sort(parts)
	return parts
}

func TestCollectMessagesSplitsAndMarksKind(t *testing.T) {
	cfg, _ := seedMessagingFixture(t)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range payload.Files {
		paths[f.Path] = true
		if int64(len(f.Data)) > recoveryclient.MaxCapsuleFileBytes {
			t.Fatalf("%s is %d bytes", f.Path, len(f.Data))
		}
		if f.Mode != 0600 {
			t.Errorf("%s mode %o", f.Path, f.Mode)
		}
	}
	// The messages capsule is the messaging tables only: the deployment key stays in the
	// people capsule.
	if !paths[backup.MessagesAccounts] || paths["data/ky_server.db"] || paths["data/encryption.key"] {
		t.Fatalf("unexpected members: %v", paths)
	}
	if payload.VerificationRecipe["kind"] != "messages" {
		t.Fatalf("recipe kind: %v", payload.VerificationRecipe["kind"])
	}
	if payload.ServiceName != cfg.Server.AppName || payload.AppVersion != "test" {
		t.Fatalf("identity: %q %q", payload.ServiceName, payload.AppVersion)
	}
	db := openMember(t, payload, backup.MessagesAccounts)
	for _, table := range []string{"messaging_rooms", "messaging_members", "messaging_devices", "messaging_identities", "messaging_epoch_devices"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n == 0 {
			t.Fatalf("%s: %d %v", table, n, err)
		}
	}
	rows, err := db.Query("SELECT id FROM messaging_devices ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var devices []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		devices = append(devices, id)
	}
	rows.Close()
	if want := []string{"alice-old", "alice-phone", "bob-laptop", "carol-tablet"}; !slices.Equal(devices, want) {
		t.Fatalf("devices %v, want %v (pending and unverified dropped)", devices, want)
	}
	// Bearer-token hashes and enrollment state never reach a capsule custodians can open.
	var credentials int
	if err := db.QueryRow("SELECT COUNT(*) FROM messaging_devices WHERE token_hash IS NOT NULL OR challenge <> '' OR enrollment_session <> ''").Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("%d exported devices carry credentials (%v)", credentials, err)
	}
	parts := eventParts(payload)
	if len(parts) != 1 || parts[0] != backup.MessagesDir+"/events-001.db" {
		t.Fatalf("parts: %v", parts)
	}
	var events, welcomes int
	part := openMember(t, payload, parts[0])
	if err := part.QueryRow("SELECT COUNT(*) FROM messaging_events").Scan(&events); err != nil || events != 3 {
		t.Fatalf("events: %d %v", events, err)
	}
	if err := part.QueryRow("SELECT COUNT(*) FROM messaging_welcomes").Scan(&welcomes); err != nil || welcomes != 1 {
		t.Fatalf("welcomes: %d %v", welcomes, err)
	}
	sqlitePaths, _ := payload.VerificationRecipe["sqlite_paths"].([]string)
	required, _ := payload.VerificationRecipe["required_files"].([]string)
	for path := range paths {
		if !slices.Contains(sqlitePaths, path) || !slices.Contains(required, path) {
			t.Errorf("%s missing from recipe: %v %v", path, sqlitePaths, required)
		}
	}
}

func TestCollectMessagesSplitsLargeRoomAcrossParts(t *testing.T) {
	const budget, count, size = 300 << 10, 40, 32 << 10
	backup.SetMessagesBudgets(t, budget, recoveryclient.MaxCapsuleTotalBytes)
	cfg, _ := sqliteInstance(t)
	seedRoom(t, rawDB(t, cfg), "big", count, size)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	parts := eventParts(payload)
	if len(parts) < 3 {
		t.Fatalf("parts: %v", parts)
	}
	total, welcomes, next := 0, 0, int64(1)
	for i, path := range parts {
		if want := fmt.Sprintf("%s/events-%03d.db", backup.MessagesDir, i+1); path != want {
			t.Fatalf("part %d named %s, want %s", i, path, want)
		}
		if n := int64(len(findFile(payload.Files, path).Data)); n > recoveryclient.MaxCapsuleFileBytes {
			t.Fatalf("%s is %d bytes", path, n)
		}
		db := openMember(t, payload, path)
		var n, w int
		var lo, hi, bytes int64
		if err := db.QueryRow("SELECT COUNT(*), MIN(sequence), MAX(sequence), SUM(LENGTH(payload)) FROM messaging_events WHERE room_id='big'").Scan(&n, &lo, &hi, &bytes); err != nil {
			t.Fatal(err)
		}
		if lo != next || hi-lo+1 != int64(n) {
			t.Fatalf("%s holds %d events %d..%d, want contiguous from %d", path, n, lo, hi, next)
		}
		if bytes > budget {
			t.Fatalf("%s carries %d payload bytes over the %d budget", path, bytes, budget)
		}
		// A Welcome travels with its event.
		if err := db.QueryRow("SELECT COUNT(*) FROM messaging_welcomes WHERE sequence BETWEEN ? AND ?", lo, hi).Scan(&w); err != nil {
			t.Fatal(err)
		}
		next = hi + 1
		total += n
		welcomes += w
	}
	if total != count || welcomes != count/5 {
		t.Fatalf("exported %d events and %d welcomes, want %d and %d", total, welcomes, count, count/5)
	}
}

// Small MLS messages carry as many bytes of ids and hashes as of payload. The budget must
// count them, or a part of small events outgrows the file cap its headroom was sized for.
func TestCollectMessagesBudgetCountsRowMetadata(t *testing.T) {
	const budget, count = 1 << 20, 8000
	backup.SetMessagesBudgets(t, budget, recoveryclient.MaxCapsuleTotalBytes)
	cfg, _ := sqliteInstance(t)
	db := rawDB(t, cfg)
	seedRoom(t, db, "small-events", 0, 0)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	hash, payload := strings.Repeat("f", 64), strings.Repeat("A", 200)
	for seq := 1; seq <= count; seq++ {
		if _, err := tx.Exec(`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES ('small-events',?,?,?,'application',1,?,?,?,1)`,
			seq, "device-"+hash[:29], fmt.Sprintf("%036d", seq), hash, payload, hash); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	collected, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	// The shipped budget leaves 4 MiB of 64 for SQLite pages; hold parts to the same ratio.
	limit := budget * recoveryclient.MaxCapsuleFileBytes / (recoveryclient.MaxCapsuleFileBytes - 4<<20)
	for _, path := range eventParts(collected) {
		if n := int64(len(findFile(collected.Files, path).Data)); n > limit {
			t.Errorf("%s is %d bytes for a %d budget; limit %d", path, n, budget, limit)
		}
	}
}

func TestCollectMessagesPartOverflowNamesLargestRooms(t *testing.T) {
	backup.SetMessagesFileCap(t, 256<<10)
	cfg, _ := sqliteInstance(t)
	db := rawDB(t, cfg)
	seedRoom(t, db, "room-big", 12, 32<<10)
	seedRoom(t, db, "room-small", 2, 32<<10)
	_, err := backup.CollectMessages(context.Background(), cfg, "test")
	if !errors.Is(err, capsule.ErrCapsuleTooLarge) {
		t.Fatalf("got %v, want ErrCapsuleTooLarge", err)
	}
	if !strings.Contains(err.Error(), "largest rooms: room-big, room-small") {
		t.Fatalf("part overflow does not name the largest rooms: %v", err)
	}
}

func TestCollectMessagesRefusesOverTotalLimit(t *testing.T) {
	backup.SetMessagesBudgets(t, 300<<10, 1<<20)
	cfg, _ := sqliteInstance(t)
	db := rawDB(t, cfg)
	seedRoom(t, db, "room-big", 30, 32<<10)
	seedRoom(t, db, "room-mid", 15, 32<<10)
	seedRoom(t, db, "room-small", 5, 32<<10)
	_, err := backup.CollectMessages(context.Background(), cfg, "test")
	if !errors.Is(err, capsule.ErrCapsuleTooLarge) {
		t.Fatalf("got %v, want ErrCapsuleTooLarge", err)
	}
	if !strings.Contains(err.Error(), "largest rooms: room-big, room-mid, room-small") {
		t.Fatalf("error does not name the largest rooms in order: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(cfg.Database.DataDir, "snapshot-*"))
	if len(leftovers) != 0 {
		t.Fatalf("scratch left behind: %v", leftovers)
	}
}

func TestCollectMessagesRefusesADriverItCannotSnapshot(t *testing.T) {
	cfg, _ := sqliteInstance(t)
	cfg.Database.Driver = "postgres"
	if _, err := backup.CollectMessages(context.Background(), cfg, "test"); !errors.Is(err, backup.ErrNoDatabaseSnapshot) {
		t.Fatalf("got %v, want ErrNoDatabaseSnapshot", err)
	}
}

func TestMessagesDrillPasses(t *testing.T) {
	cfg, _ := seedMessagingFixture(t)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := backup.RunDrill(context.Background(), cfg, payload, backup.MessagesChecks)
	if err != nil || !result.Passed {
		t.Fatalf("drill: %+v %v", result, err)
	}
}

// Each kind's checks refuse the other kind's capsule.
func TestMessagesChecksRefuseOtherKinds(t *testing.T) {
	cfg, _ := seedMessagingFixture(t)
	ctx := context.Background()
	people, err := backup.Collect(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backup.RunDrill(ctx, cfg, people, backup.MessagesChecks); err != nil || result.Passed {
		t.Fatalf("people capsule passed the messages drill: %+v %v", result, err)
	}
	messages, err := backup.CollectMessages(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backup.RunDrill(ctx, cfg, messages, backup.Checks); err != nil || result.Passed {
		t.Fatalf("messages capsule passed the people drill: %+v %v", result, err)
	}
	messages.VerificationRecipe["kind"] = "people"
	if result, err := backup.RunDrill(ctx, cfg, messages, backup.MessagesChecks); err != nil || result.Passed {
		t.Fatalf("a capsule without the messages kind passed: %+v %v", result, err)
	}
	messages.VerificationRecipe["kind"] = "messages"
	messages.VerificationRecipe["sqlite_paths"] = []string{backup.MessagesAccounts}
	if result, err := backup.RunDrill(ctx, cfg, messages, backup.MessagesChecks); err != nil || result.Passed {
		t.Fatalf("an event part escaped the integrity check: %+v %v", result, err)
	}
}

// A capsule restore-messages would refuse must never be sealed.
func TestCollectMessagesRefusesTooManyParts(t *testing.T) {
	backup.SetMessagesBudgets(t, 64<<10, recoveryclient.MaxCapsuleTotalBytes)
	cfg, _ := sqliteInstance(t)
	seedRoom(t, rawDB(t, cfg), "many-parts", backup.MaxEventParts+2, 40<<10)
	_, err := backup.CollectMessages(context.Background(), cfg, "test")
	if !errors.Is(err, capsule.ErrCapsuleTooLarge) || !strings.Contains(err.Error(), "event parts") {
		t.Fatalf("got %v, want a part-count refusal", err)
	}
}

// ...nor pass a drill: the drill accepts exactly the part counts the import accepts.
func TestMessagesChecksRefuseTooManyParts(t *testing.T) {
	cfg, _ := seedMessagingFixture(t)
	payload, err := backup.CollectMessages(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	part := *findFile(payload.Files, backup.MessagesDir+"/events-001.db")
	withParts := func(n int) recoveryclient.Payload {
		p := payload
		p.Files = slices.Clone(payload.Files)
		for i := 2; i <= n; i++ {
			extra := part
			extra.Path = fmt.Sprintf("%s/events-%03d.db", backup.MessagesDir, i)
			p.Files = append(p.Files, extra)
		}
		var paths []string
		for _, f := range p.Files {
			paths = append(paths, f.Path)
		}
		p.VerificationRecipe = maps.Clone(payload.VerificationRecipe)
		p.VerificationRecipe["sqlite_paths"], p.VerificationRecipe["required_files"] = paths, paths
		return p
	}
	if result, err := backup.RunDrill(context.Background(), cfg, withParts(backup.MaxEventParts), backup.MessagesChecks); err != nil || !result.Passed {
		t.Fatalf("%d parts failed the drill: %+v %v", backup.MaxEventParts, result, err)
	}
	if result, err := backup.RunDrill(context.Background(), cfg, withParts(backup.MaxEventParts+1), backup.MessagesChecks); err != nil || result.Passed {
		t.Fatalf("%d parts passed the drill: %+v %v", backup.MaxEventParts+1, result, err)
	}
}
