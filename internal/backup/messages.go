package backup

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// MessagesDir holds the messages capsule's members; MessagesAccounts marks the kind.
const (
	MessagesDir      = "data/messages"
	MessagesAccounts = MessagesDir + "/accounts.db"
)

// Variables so tests can prove the split and total rules with megabytes. The part budget
// counts row bytes and leaves headroom under the file cap for SQLite pages.
var (
	messagesPartBudget  = recoveryclient.MaxCapsuleFileBytes - 4<<20
	messagesTotalBudget = recoveryclient.MaxCapsuleTotalBytes
	messagesFileCap     = int64(recoveryclient.MaxCapsuleFileBytes)
)

// messagesAccountTables travel in accounts.db; events and Welcomes travel in event parts.
// KeyPackages, recovery-auth requests and reset receipts are never restored.
var messagesAccountTables = []string{"messaging_identities", "messaging_devices", "messaging_rooms", "messaging_members", "messaging_epoch_devices"}

type eventRange struct {
	room     string
	from, to int64
}

// CollectMessages assembles the opt-in messages capsule from one consistent snapshot:
// accounts.db plus events-NNN.db parts, each a contiguous run of sequences per room that
// fits the part budget. Only approved and revoked devices are exported; pending and
// unverified enrollments are session-bound and would be stale on restore.
func CollectMessages(ctx context.Context, cfg *config.Config, appVersion string) (recoveryclient.Payload, error) {
	if strings.ToLower(cfg.Database.Driver) != "sqlite" {
		return recoveryclient.Payload{}, fmt.Errorf("%w: %s", ErrNoDatabaseSnapshot, cfg.Database.Driver)
	}
	dbPath, cleanup, err := snapshotFile(ctx, cfg.Database.DSN, cfg.Database.DataDir)
	if err != nil {
		return recoveryclient.Payload{}, err
	}
	defer cleanup()
	snapshot, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return recoveryclient.Payload{}, err
	}
	defer snapshot.Close()
	// ATTACH is per connection, so every export runs on this one.
	conn, err := snapshot.Conn(ctx)
	if err != nil {
		return recoveryclient.Payload{}, err
	}
	defer conn.Close()

	parts, roomBytes, err := planEventParts(ctx, conn)
	if err != nil {
		return recoveryclient.Payload{}, err
	}
	largest := strings.Join(largestRooms(roomBytes, 3), ", ")
	if len(parts) > maxEventParts {
		return recoveryclient.Payload{}, fmt.Errorf("%w: messages need %d event parts, restore-messages accepts %d; largest rooms: %s", capsule.ErrCapsuleTooLarge, len(parts), maxEventParts, largest)
	}
	dir := filepath.Dir(dbPath)
	var files []recoveryclient.File
	var total int64
	add := func(name string, statements [][]any) error {
		data, err := exportDB(ctx, conn, filepath.Join(dir, filepath.Base(name)), statements)
		if err != nil && name != MessagesAccounts && errors.Is(err, capsule.ErrCapsuleTooLarge) {
			return fmt.Errorf("export %s: %w; largest rooms: %s", name, err, largest)
		}
		if err != nil {
			return fmt.Errorf("export %s: %w", name, err)
		}
		if total += int64(len(data)); total > messagesTotalBudget {
			return fmt.Errorf("%w: messages exceed %d MiB; largest rooms: %s", capsule.ErrCapsuleTooLarge, messagesTotalBudget>>20, largest)
		}
		files = append(files, recoveryclient.File{Path: name, Data: data, Mode: 0600})
		return nil
	}

	var accounts [][]any
	for _, table := range messagesAccountTables {
		query := "CREATE TABLE part." + table + " AS SELECT * FROM main." + table
		if table == "messaging_devices" {
			// Bearer-token hashes and enrollment state stay out of a custodian-openable capsule.
			query = `CREATE TABLE part.messaging_devices AS SELECT id, user_id, name, public_key, status,
				'' AS challenge, '' AS enrollment_session, expires_at, NULL AS token_hash, approved_by,
				created_at, verified_at, identity_generation
				FROM main.messaging_devices WHERE status IN ('approved', 'revoked')`
		}
		accounts = append(accounts, []any{query})
	}
	if err := add(MessagesAccounts, accounts); err != nil {
		return recoveryclient.Payload{}, err
	}
	for i, ranges := range parts {
		statements := [][]any{
			{"CREATE TABLE part.messaging_events AS SELECT * FROM main.messaging_events WHERE 0"},
			{"CREATE TABLE part.messaging_welcomes AS SELECT * FROM main.messaging_welcomes WHERE 0"},
		}
		for _, r := range ranges {
			statements = append(statements,
				[]any{"INSERT INTO part.messaging_events SELECT * FROM main.messaging_events WHERE room_id = ? AND sequence BETWEEN ? AND ?", r.room, r.from, r.to},
				[]any{"INSERT INTO part.messaging_welcomes SELECT * FROM main.messaging_welcomes WHERE room_id = ? AND sequence BETWEEN ? AND ?", r.room, r.from, r.to})
		}
		if err := add(fmt.Sprintf("%s/events-%03d.db", MessagesDir, i+1), statements); err != nil {
			return recoveryclient.Payload{}, err
		}
	}

	paths := requiredFiles(files)
	return recoveryclient.Payload{
		ServiceName: cfg.Server.AppName,
		AppVersion:  appVersion,
		Files:       files,
		VerificationRecipe: map[string]any{
			"kind":                   "messages",
			"check_sqlite_integrity": true,
			"sqlite_paths":           paths,
			"required_files":         paths,
		},
	}, nil
}

// rowBytes counts every text column's bytes plus an allowance for integers and SQLite's
// record and cell headers, so small events with long ids and hashes are not undercounted.
const rowBytes = `SELECT room_id, sequence,
	64 + LENGTH(CAST(room_id || device_id || event_id || kind || roster_hash || payload || request_hash AS BLOB))
	+ COALESCE((SELECT SUM(32 + LENGTH(CAST(w.room_id || w.device_id || w.payload AS BLOB))) FROM messaging_welcomes w WHERE w.room_id = e.room_id AND w.sequence = e.sequence), 0)
FROM messaging_events e ORDER BY room_id, sequence`

// planEventParts cuts events, ordered by room and sequence, into parts whose row bytes
// stay within the part budget. A Welcome always travels with its event.
func planEventParts(ctx context.Context, conn *sql.Conn) ([][]eventRange, map[string]int64, error) {
	rows, err := conn.QueryContext(ctx, rowBytes)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var parts [][]eventRange
	var partBytes int64
	roomBytes := map[string]int64{}
	for rows.Next() {
		var r eventRange
		var size int64
		if err := rows.Scan(&r.room, &r.from, &size); err != nil {
			return nil, nil, err
		}
		roomBytes[r.room] += size
		if len(parts) == 0 || (partBytes > 0 && partBytes+size > messagesPartBudget) {
			parts, partBytes = append(parts, nil), 0
		}
		partBytes += size
		last := &parts[len(parts)-1]
		// Rows arrive in order, so extending the range can only take in this part's rows.
		if n := len(*last); n > 0 && (*last)[n-1].room == r.room {
			(*last)[n-1].to = r.from
			continue
		}
		r.to = r.from
		*last = append(*last, r)
	}
	return parts, roomBytes, rows.Err()
}

// exportDB builds a standalone SQLite file from the snapshot, compacts it and refuses it
// above the capsule's per-file cap before reading it into memory.
func exportDB(ctx context.Context, conn *sql.Conn, path string, statements [][]any) ([]byte, error) {
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS part", path); err != nil {
		return nil, err
	}
	for _, s := range statements {
		if _, err := conn.ExecContext(ctx, s[0].(string), s[1:]...); err != nil {
			return nil, err
		}
	}
	for _, query := range []string{"VACUUM part", "DETACH DATABASE part"} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return nil, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > messagesFileCap {
		return nil, fmt.Errorf("%w: %d bytes", capsule.ErrCapsuleTooLarge, info.Size())
	}
	return os.ReadFile(path)
}

func largestRooms(roomBytes map[string]int64, n int) []string {
	rooms := slices.SortedFunc(maps.Keys(roomBytes), func(a, b string) int {
		return cmp.Or(cmp.Compare(roomBytes[b], roomBytes[a]), cmp.Compare(a, b))
	})
	return rooms[:min(n, len(rooms))]
}

// MessagesChecks validates a messages capsule's recipe and members: the kind marker,
// accounts.db, at most maxEventParts event parts, and SQLite integrity on every .db member.
func MessagesChecks(dir string, opened capsule.Manifest) []recoveryclient.Check {
	recipe, ok := opened.VerificationRecipe.(map[string]any)
	if !ok {
		return recipeFailure("Expected a recipe object")
	}
	if recipe["kind"] != "messages" {
		return recipeFailure("kind must be messages")
	}
	required, err := recipeStrings(recipe["required_files"])
	if err != nil {
		return recipeFailure("required_files: " + err.Error())
	}
	sqlitePaths, err := recipeStrings(recipe["sqlite_paths"])
	if err != nil {
		return recipeFailure("sqlite_paths: " + err.Error())
	}
	if enabled, ok := recipe["check_sqlite_integrity"].(bool); !ok || !enabled {
		return recipeFailure("check_sqlite_integrity must be true")
	}
	if !slices.Contains(required, MessagesAccounts) {
		return recipeFailure("required_files omits " + MessagesAccounts)
	}
	parts := 0
	for _, file := range opened.Files {
		if strings.HasSuffix(file.Path, ".db") && !slices.Contains(sqlitePaths, file.Path) {
			return recipeFailure("sqlite_paths omits " + file.Path)
		}
		if path.Dir(file.Path) == MessagesDir && eventPartName.MatchString(path.Base(file.Path)) {
			parts++
		}
	}
	if parts > maxEventParts {
		return recipeFailure(fmt.Sprintf("%d event parts, restore-messages accepts %d", parts, maxEventParts))
	}
	if message := memberFailure(dir, opened, required, sqlitePaths); message != "" {
		return recipeFailure(message)
	}
	return fileChecks(dir, required, sqlitePaths)
}
