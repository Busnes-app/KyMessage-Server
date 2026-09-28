package store

import "context"

const (
	MessagingActiveEventLimit  = 4096
	MessagingRetainedByteLimit = 32 * 1024 * 1024
	MessagingReceiptLimit      = 1_000_000
)

type MessagingRoomUsage struct {
	ID, Name                              string
	Retired                               bool
	ActiveEvents, RetainedBytes, Receipts int64
}

type MessagingUsage struct {
	Rooms, ActiveEvents, RetainedBytes, Receipts int64
	LargestRooms                                 []MessagingRoomUsage
}

// Usage is operator metadata only. One statement gives a consistent snapshot and
// global totals even when the bounded room list is truncated. Expiry clears an
// ordered prefix, so the retained floor counts active events without scanning
// ciphertext or lifetime receipt rows. It never triggers cleanup or reads bodies.
func (m *messagingStore) Usage(ctx context.Context) (MessagingUsage, error) {
	usage := MessagingUsage{LargestRooms: []MessagingRoomUsage{}}
	rows, err := m.store.db.QueryContext(ctx, m.store.rebind(`SELECT id, name,
		owner_identity_generation = 0, sequence - retained_from + 1, retained_bytes, sequence,
		COUNT(*) OVER (), SUM(sequence - retained_from + 1) OVER (),
		SUM(retained_bytes) OVER (), SUM(sequence) OVER ()
		FROM messaging_rooms
		ORDER BY CASE WHEN sequence - retained_from + 1 >= ? OR retained_bytes >= ? OR sequence >= ? THEN 1 ELSE 0 END DESC,
		retained_bytes DESC, id LIMIT 100`), (MessagingActiveEventLimit*4+4)/5, (MessagingRetainedByteLimit*4+4)/5, (MessagingReceiptLimit*4+4)/5)
	if err != nil {
		return usage, err
	}
	defer rows.Close()
	for rows.Next() {
		var room MessagingRoomUsage
		if err := rows.Scan(&room.ID, &room.Name, &room.Retired, &room.ActiveEvents, &room.RetainedBytes, &room.Receipts,
			&usage.Rooms, &usage.ActiveEvents, &usage.RetainedBytes, &usage.Receipts); err != nil {
			return usage, err
		}
		usage.LargestRooms = append(usage.LargestRooms, room)
	}
	return usage, rows.Err()
}
