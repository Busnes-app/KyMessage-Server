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
