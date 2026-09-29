package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrMessagingHistoryGone = errors.New("required encrypted history expired")

// purgeRoom deletes the room's events older than its retention window: rows, Welcomes and
// their per-event audit rows. created_at is monotonic per room, so this is a sequence prefix
// and retained_from stays an exact floor. The caller holds the room row lock.
func (m *messagingStore) purgeRoom(ctx context.Context, tx *sql.Tx, room string, now int64) error {
	var days int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT retention_days FROM messaging_rooms WHERE id = ?`), room).Scan(&days); err != nil || days == 0 {
		return err
	}
	cutoff := now - days*86400
	var last, bytes int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(MAX(sequence), 0) FROM messaging_events WHERE room_id = ? AND created_at <= ?`), room, cutoff).Scan(&last); err != nil || last == 0 {
		return err
	}
	// Same bound as the deletes: pre-migration rows may have non-monotonic created_at.
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_events WHERE room_id = ? AND sequence <= ?`), room, last).Scan(&bytes); err != nil {
		return err
	}
	var welcomes int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_welcomes WHERE room_id = ? AND sequence <= ?`), room, last).Scan(&welcomes); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM messaging_welcomes WHERE room_id = ? AND sequence <= ?`,
		`DELETE FROM messaging_events WHERE room_id = ? AND sequence <= ?`,
	} {
		if _, err := tx.ExecContext(ctx, m.store.rebind(q), room, last); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM audit_records WHERE action = 'messaging.event_accepted' AND resource = ? AND created_at <= ?`), room, time.Unix(cutoff, 0).UTC()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET retained_bytes = retained_bytes - ?, retained_from = CASE WHEN retained_from < ? THEN ? ELSE retained_from END WHERE id = ?`), bytes+welcomes, last+1, last+1, room)
	return err
}

// ExpireMessages purges every room with events past its window, in short per-room
// transactions. Room operations also purge under their own lock.
func (m *messagingStore) ExpireMessages(ctx context.Context) error {
	for {
		now := time.Now().Unix()
		rows, err := m.store.db.QueryContext(ctx, m.store.rebind(`SELECT r.id FROM messaging_rooms r WHERE r.retention_days > 0 AND EXISTS (SELECT 1 FROM messaging_events e WHERE e.room_id = r.id AND e.created_at <= ? - r.retention_days * 86400) LIMIT 100`), now)
		if err != nil {
			return err
		}
		rooms := []string{}
		for rows.Next() {
			var room string
			if err = rows.Scan(&room); err != nil {
				break
			}
			rooms = append(rooms, room)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil || len(rooms) == 0 {
			return err
		}
		for _, room := range rooms {
			if err := m.purgeOne(ctx, room, now); err != nil {
				return err
			}
		}
	}
}

func (m *messagingStore) purgeOne(ctx context.Context, room string, now int64) error {
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
		// Room deletion can win after discovery.
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if err := m.purgeRoom(ctx, tx, room, now); err != nil {
		return err
	}
	return tx.Commit()
}
