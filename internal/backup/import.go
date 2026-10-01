package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ImportCounts reports what ImportMessages brought back and what it dropped because the
// people it references are not in the restored database.
type ImportCounts struct {
	Rooms, DroppedRooms, Members, DroppedMembers, Devices, DroppedDevices, Events int
}

// The 256 MiB capsule cap over the ~60 MiB part budget allows 5 parts; SQLite attaches at
// most 10 databases, accounts.db included.
const maxEventParts = 8

var eventPartName = regexp.MustCompile(`^events-[0-9]{3}\.db$`)

// ErrMessagingDataPresent refuses an import into a database that already has messaging rows.
var ErrMessagingDataPresent = errors.New("target already has messaging data; restore-messages runs once, into a fresh people restore")

const messagingRows = `SELECT (SELECT COUNT(*) FROM main.messaging_rooms) + (SELECT COUNT(*) FROM main.messaging_devices) + (SELECT COUNT(*) FROM main.messaging_identities)`

// CheckMessagesTarget refuses a database that already has messaging rows, without writing,
// so a repeated restore-messages stops before decrypting anything.
func CheckMessagesTarget(ctx context.Context, dbPath string) error {
	db, err := sql.Open("sqlite", sqliteURI(dbPath, "mode=ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	return refuseMessagingData(db.QueryRowContext(ctx, messagingRows))
}

func refuseMessagingData(row *sql.Row) error {
	var n int
	if err := row.Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return ErrMessagingDataPresent
	}
	return nil
}

func sqliteURI(path, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}

// messagesMembers returns accounts.db and the sorted event parts of an opened messages
// capsule. Every one must be a regular file directly inside openedDir's messages directory:
// the capsule is custodian-verified, but it still crosses a boundary here.
func messagesMembers(openedDir string) (string, []string, error) {
	root, err := filepath.EvalSymlinks(openedDir)
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(root, filepath.FromSlash(MessagesDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var parts []string
	for _, e := range entries {
		if e.Name() != filepath.Base(MessagesAccounts) && !eventPartName.MatchString(e.Name()) {
			return "", nil, fmt.Errorf("unexpected messages capsule member %q", e.Name())
		}
		if e.Name() != filepath.Base(MessagesAccounts) {
			parts = append(parts, filepath.Join(dir, e.Name()))
		}
	}
	if len(parts) > maxEventParts {
		return "", nil, fmt.Errorf("messages capsule has %d event parts, more than %d", len(parts), maxEventParts)
	}
	accounts := filepath.Join(dir, filepath.Base(MessagesAccounts))
	for _, path := range append([]string{accounts}, parts...) {
		info, err := os.Lstat(path)
		if err != nil {
			return "", nil, err
		}
		if resolved, err := filepath.EvalSymlinks(path); err != nil || resolved != path || !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("%s is not a regular file inside the opened capsule", filepath.Base(path))
		}
	}
	return accounts, parts, nil
}

// ImportMessages imports an opened messages capsule into a people-restored SQLite database,
// offline and in one transaction. The result is what deleting every person missing from the
// database would leave under the schema's ON DELETE CASCADE rules: their identities, devices,
// memberships and owned rooms (with those rooms' events) go; rows without a foreign key to
// them, such as epoch devices and Welcomes, stay. Imported devices keep their status but carry
// no bearer token, so approved ones stay suspended until their owners resume them, for at most
// 30 days from the import (store.ExpireSuspendedDevices). KeyPackages,
// recovery-auth requests and reset receipts are not in the capsule and are not imported.
func ImportMessages(ctx context.Context, dbPath string, openedDir string) (ImportCounts, error) {
	var counts ImportCounts
	accounts, parts, err := messagesMembers(openedDir)
	if err != nil {
		return counts, err
	}
	db, err := sql.Open("sqlite", sqliteURI(dbPath, "mode=rw&_pragma=busy_timeout(5000)"))
	if err != nil {
		return counts, err
	}
	defer db.Close()
	// ATTACH is per connection, so the whole import runs on this one.
	conn, err := db.Conn(ctx)
	if err != nil {
		return counts, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return counts, err
	}
	if err := refuseMessagingData(conn.QueryRowContext(ctx, messagingRows)); err != nil {
		return counts, err
	}
	// SQLite refuses ATTACH inside a transaction.
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS acc", sqliteURI(accounts, "mode=ro")); err != nil {
		return counts, err
	}
	for i, part := range parts {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE ? AS p%d", i+1), sqliteURI(part, "mode=ro")); err != nil {
			return counts, err
		}
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return counts, err
	}
	if counts, err = importRows(ctx, conn, len(parts)); err != nil {
		_, rollback := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return ImportCounts{}, errors.Join(err, rollback)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, rollback := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return ImportCounts{}, errors.Join(err, rollback)
	}
	return counts, nil
}

const (
	inUsers    = ` IN (SELECT id FROM main.users)`
	inRooms    = ` IN (SELECT id FROM main.messaging_rooms)`
	partEvents = `INSERT INTO main.messaging_events (room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at)
		SELECT room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at
		FROM p%d.messaging_events WHERE room_id` + inRooms
	partWelcomes = `INSERT INTO main.messaging_welcomes (room_id, sequence, device_id, payload)
		SELECT room_id, sequence, device_id, payload FROM p%d.messaging_welcomes WHERE room_id` + inRooms
)

func importRows(ctx context.Context, conn *sql.Conn, parts int) (ImportCounts, error) {
	var c ImportCounts
	// Again under the write lock: another import may have committed since the first check.
	if err := refuseMessagingData(conn.QueryRowContext(ctx, messagingRows)); err != nil {
		return c, err
	}
	type step struct {
		n     *int
		query string
	}
	steps := []step{
		{nil, `INSERT INTO main.messaging_identities (user_id, generation)
			SELECT user_id, generation FROM acc.messaging_identities WHERE user_id` + inUsers},
		// An approved device's 30 days to resume start now.
		{&c.Devices, fmt.Sprintf(`INSERT INTO main.messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, token_hash, approved_by, created_at, verified_at, identity_generation, suspended_at)
			SELECT id, user_id, name, public_key, status, '', '', expires_at, NULL, approved_by, created_at, verified_at, identity_generation, CASE WHEN status = 'approved' THEN %d ELSE 0 END
			FROM acc.messaging_devices WHERE status IN ('approved', 'revoked') AND user_id`+inUsers, time.Now().Unix())},
		{&c.Rooms, `INSERT INTO main.messaging_rooms (id, name, owner_id, created_at, epoch, sequence, roster_hash, retained_bytes, owner_identity_generation, direct_peer_id, retained_from, retention_days)
			SELECT id, name, owner_id, created_at, epoch, sequence, roster_hash, 0, owner_identity_generation, direct_peer_id, retained_from, retention_days
			FROM acc.messaging_rooms WHERE owner_id` + inUsers},
		{&c.Members, `INSERT INTO main.messaging_members (room_id, user_id, status, generation, identity_generation)
			SELECT room_id, user_id, status, generation, identity_generation FROM acc.messaging_members
			WHERE room_id` + inRooms + ` AND user_id` + inUsers},
		{nil, `INSERT INTO main.messaging_epoch_devices (room_id, device_id, generation, joined_sequence)
			SELECT room_id, device_id, generation, joined_sequence FROM acc.messaging_epoch_devices WHERE room_id` + inRooms},
	}
	// Every part's events before any Welcome: a Welcome's foreign key is its event.
	for i := 1; i <= parts; i++ {
		steps = append(steps, step{&c.Events, fmt.Sprintf(partEvents, i)})
	}
	for i := 1; i <= parts; i++ {
		steps = append(steps, step{nil, fmt.Sprintf(partWelcomes, i)})
	}
	steps = append(steps, step{nil, `UPDATE main.messaging_rooms SET retained_bytes =
		(SELECT COALESCE(SUM(LENGTH(CAST(payload AS BLOB))), 0) FROM main.messaging_events e WHERE e.room_id = messaging_rooms.id) +
		(SELECT COALESCE(SUM(LENGTH(CAST(payload AS BLOB))), 0) FROM main.messaging_welcomes w WHERE w.room_id = messaging_rooms.id)`})
	for _, s := range steps {
		res, err := conn.ExecContext(ctx, s.query)
		if err != nil {
			return c, err
		}
		if s.n != nil {
			n, err := res.RowsAffected()
			if err != nil {
				return c, err
			}
			*s.n += int(n)
		}
	}

	for _, d := range []struct {
		dropped  *int
		imported int
		table    string
	}{
		{&c.DroppedRooms, c.Rooms, "messaging_rooms"},
		{&c.DroppedMembers, c.Members, "messaging_members"},
		{&c.DroppedDevices, c.Devices, "messaging_devices"},
	} {
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM acc."+d.table).Scan(d.dropped); err != nil {
			return c, err
		}
		*d.dropped -= d.imported
	}

	rows, err := conn.QueryContext(ctx, "PRAGMA main.foreign_key_check")
	if err != nil {
		return c, err
	}
	violation := rows.Next()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return c, err
	}
	if violation {
		return c, errors.New("imported messages violate a foreign key")
	}

	details := fmt.Sprintf("rooms=%d dropped_rooms=%d members=%d dropped_members=%d devices=%d dropped_devices=%d events=%d",
		c.Rooms, c.DroppedRooms, c.Members, c.DroppedMembers, c.Devices, c.DroppedDevices, c.Events)
	_, err = conn.ExecContext(ctx, `INSERT INTO main.audit_records (action, details, created_at) VALUES (?, ?, ?)`,
		"restore.messages_imported", details, time.Now().UTC())
	return c, err
}
