package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrMessagingHistoryGone = errors.New("required encrypted history expired")

// The caller holds the room row lock. Keep receipt hashes and sequence metadata:
// an expired accepted event must never become a new append on an exact retry.
func (m *messagingStore) expireRoom(ctx context.Context, tx *sql.Tx, room string, now int64) error {
	var bytes, last int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(SUM(LENGTH(payload)), 0), COALESCE(MAX(sequence), 0) FROM messaging_events WHERE room_id = ? AND expires_at <= ? AND payload <> ''`), room, now).Scan(&bytes, &last); err != nil {
		return err
	}
	if last == 0 {
		return nil
	}
	var welcomes int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(SUM(LENGTH(w.payload)), 0) FROM messaging_welcomes w JOIN messaging_events e ON e.room_id = w.room_id AND e.sequence = w.sequence WHERE e.room_id = ? AND e.expires_at <= ?`), room, now).Scan(&welcomes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_welcomes WHERE room_id = ? AND sequence IN (SELECT sequence FROM messaging_events WHERE room_id = ? AND expires_at <= ?)`), room, room, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_events SET payload = '' WHERE room_id = ? AND expires_at <= ? AND payload <> ''`), room, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET retained_bytes = retained_bytes - ?, retained_from = CASE WHEN retained_from < ? THEN ? ELSE retained_from END WHERE id = ?`), bytes+welcomes, last+1, last+1, room)
	return err
}

// ExpireMessages clears expired ciphertext and Welcomes in short per-room
// transactions. Active fetches enforce expiry independently of this sweep.
func (m *messagingStore) ExpireMessages(ctx context.Context) error {
	now := time.Now().Unix()
	for {
		rows, err := m.store.db.QueryContext(ctx, m.store.rebind(`SELECT DISTINCT room_id FROM messaging_events WHERE expires_at <= ? AND payload <> '' LIMIT 100`), now)
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
		if err != nil {
			return err
		}
		if len(rooms) == 0 {
			return nil
		}
		for _, room := range rooms {
			if err := m.expireRoomTransaction(ctx, room, now); err != nil {
				return err
			}
		}
	}
}
func (m *messagingStore) expireRoomTransaction(ctx context.Context, room string, now int64) error {
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
	if err := m.expireRoom(ctx, tx, room, now); err != nil {
		return err
	}
	return tx.Commit()
}
