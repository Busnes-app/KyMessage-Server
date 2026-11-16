package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type meetingStore struct {
	store *SQLStore
}

// Members are stored newline-separated: a Matrix user ID holds no newline.
func joinMembers(m []string) string { return strings.Join(m, "\n") }

func splitMembers(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (s *meetingStore) GetMeeting(ctx context.Context, id string) (*Meeting, error) {
	var m Meeting
	var members string
	var created, updated int64
	err := s.store.db.QueryRowContext(ctx, s.store.rebind(`SELECT id, room_id, organizer_subject, members, state, created_at, updated_at FROM meetings WHERE id = ?`), id).
		Scan(&m.ID, &m.RoomID, &m.OrganizerSubject, &members, &m.State, &created, &updated)
	if errorsIs(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Members = splitMembers(members)
	m.CreatedAt, m.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return &m, nil
}

func (s *meetingStore) CreateMeeting(ctx context.Context, m *Meeting) error {
	now := time.Now().UTC().Truncate(time.Second)
	m.CreatedAt, m.UpdatedAt, m.State = now, now, MeetingOpen
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO meetings (id, room_id, organizer_subject, members, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		m.ID, m.RoomID, m.OrganizerSubject, joinMembers(m.Members), m.State, now.Unix(), now.Unix())
	return uniqueError(err)
}

func (s *meetingStore) UpdateMeeting(ctx context.Context, id, organizer string, members []string) error {
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE meetings SET organizer_subject = ?, members = ?, updated_at = ? WHERE id = ? AND state = ?`),
		organizer, joinMembers(members), time.Now().Unix(), id, MeetingOpen)
	return oneRow(res, err)
}

func (s *meetingStore) CloseMeeting(ctx context.Context, id string) error {
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE meetings SET state = ?, updated_at = ? WHERE id = ?`),
		MeetingClosed, time.Now().Unix(), id)
	return oneRow(res, err)
}

// RememberMeetingEvent drops expired IDs, then inserts this one; the primary key refuses one
// still held.
func (s *meetingStore) RememberMeetingEvent(ctx context.Context, eventID string, expires time.Time) error {
	if _, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM meeting_events WHERE expires_at < ?`), time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO meeting_events (event_id, expires_at) VALUES (?, ?)`), eventID, expires.Unix())
	return uniqueError(err)
}

func (s *meetingStore) ForgetMeetingEvent(ctx context.Context, eventID string) error {
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM meeting_events WHERE event_id = ?`), eventID)
	return err
}

func (s *meetingStore) ReplaceWindows(ctx context.Context, id string, windows []MeetingWindow) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.store.rebind(`DELETE FROM meeting_windows WHERE meeting_id = ?`), id); err != nil {
		return err
	}
	for _, w := range windows {
		if _, err := tx.ExecContext(ctx, s.store.rebind(`INSERT INTO meeting_windows (meeting_id, start_at, end_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`), id, w.Start.Unix(), w.End.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *meetingStore) Windows(ctx context.Context, id string) ([]MeetingWindow, error) {
	rows, err := s.store.db.QueryContext(ctx, s.store.rebind(`SELECT start_at, end_at FROM meeting_windows WHERE meeting_id = ? ORDER BY start_at, end_at`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingWindow
	for rows.Next() {
		var start, end int64
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		out = append(out, MeetingWindow{time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()})
	}
	return out, rows.Err()
}

const guestColumns = `meeting_id, key_hash, link_hash, revoked, mxid, mas_user_id, session_id, session_end, membership, created_at`

func (s *meetingStore) guests(ctx context.Context, where string, args ...any) ([]MeetingGuest, error) {
	rows, err := s.store.db.QueryContext(ctx, s.store.rebind(`SELECT `+guestColumns+` FROM meeting_guests WHERE `+where+` ORDER BY meeting_id, key_hash`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingGuest
	for rows.Next() {
		var g MeetingGuest
		var revoked int
		var end, created int64
		if err := rows.Scan(&g.MeetingID, &g.KeyHash, &g.LinkHash, &revoked, &g.MXID, &g.MASUserID, &g.SessionID, &end, &g.Membership, &created); err != nil {
			return nil, err
		}
		g.Revoked = revoked != 0
		g.SessionEnd, g.CreatedAt = unixOrZero(end), time.Unix(created, 0).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

func unixOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func zeroOrUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *meetingStore) one(ctx context.Context, where string, args ...any) (*MeetingGuest, error) {
	gs, err := s.guests(ctx, where, args...)
	if err != nil {
		return nil, err
	}
	if len(gs) != 1 {
		return nil, ErrNotFound
	}
	return &gs[0], nil
}

func (s *meetingStore) Guests(ctx context.Context, id string) ([]MeetingGuest, error) {
	return s.guests(ctx, `meeting_id = ?`, id)
}

func (s *meetingStore) AddGuest(ctx context.Context, g *MeetingGuest) error {
	g.CreatedAt = time.Now().UTC().Truncate(time.Second)
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO meeting_guests (meeting_id, key_hash, link_hash, created_at) VALUES (?, ?, ?, ?)`),
		g.MeetingID, g.KeyHash, g.LinkHash, g.CreatedAt.Unix())
	return uniqueError(err)
}

func (s *meetingStore) GuestByLink(ctx context.Context, linkHash string) (*MeetingGuest, error) {
	return s.one(ctx, `link_hash = ?`, linkHash)
}

func (s *meetingStore) UpdateGuest(ctx context.Context, g *MeetingGuest) error {
	revoked := 0
	if g.Revoked {
		revoked = 1
	}
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE meeting_guests SET revoked = ?, mxid = ?, mas_user_id = ?, session_id = ?, session_end = ?, membership = ? WHERE meeting_id = ? AND key_hash = ?`),
		revoked, g.MXID, g.MASUserID, g.SessionID, zeroOrUnix(g.SessionEnd), g.Membership, g.MeetingID, g.KeyHash)
	return oneRow(res, err)
}

func (s *meetingStore) DeleteGuest(ctx context.Context, meetingID, keyHash string) error {
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM meeting_guests WHERE meeting_id = ? AND key_hash = ?`), meetingID, keyHash)
	return err
}

func (s *meetingStore) ActiveGuests(ctx context.Context) ([]MeetingGuest, error) {
	return s.guests(ctx, `mxid <> '' OR revoked <> 0`)
}

func (s *meetingStore) SuiteGroupRooms(ctx context.Context) (map[string]SuiteGroupRoom, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT group_id, room_id, members, state, created_at, updated_at FROM suite_group_rooms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SuiteGroupRoom{}
	for rows.Next() {
		var g SuiteGroupRoom
		var room sql.NullString
		var members string
		var created, updated int64
		if err := rows.Scan(&g.GroupID, &room, &members, &g.State, &created, &updated); err != nil {
			return nil, err
		}
		g.RoomID, g.Members = room.String, splitMembers(members)
		g.CreatedAt, g.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out[g.GroupID] = g
	}
	return out, rows.Err()
}

func (s *meetingStore) CreateSuiteGroupRoom(ctx context.Context, groupID string) error {
	now := time.Now().Unix()
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO suite_group_rooms (group_id, room_id, members, state, created_at, updated_at) VALUES (?, NULL, '', ?, ?, ?)`),
		groupID, SuiteGroupPending, now, now)
	return uniqueError(err)
}

func (s *meetingStore) UpdateSuiteGroupRoom(ctx context.Context, g *SuiteGroupRoom) error {
	room := sql.NullString{String: g.RoomID, Valid: g.RoomID != ""}
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE suite_group_rooms SET room_id = ?, members = ?, state = ?, updated_at = ? WHERE group_id = ?`),
		room, joinMembers(g.Members), g.State, time.Now().Unix(), g.GroupID)
	return uniqueError(oneRow(res, err))
}
