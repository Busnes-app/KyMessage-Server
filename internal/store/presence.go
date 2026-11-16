package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type presenceStore struct {
	store *SQLStore
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

func joinBusy(b []MeetingWindow) string {
	lines := make([]string, len(b))
	for i, w := range b {
		lines[i] = fmt.Sprintf("%d-%d", w.Start.Unix(), w.End.Unix())
	}
	return strings.Join(lines, "\n")
}

func splitBusy(s string) []MeetingWindow {
	var out []MeetingWindow
	for line := range strings.SplitSeq(s, "\n") {
		var a, b int64
		if _, err := fmt.Sscanf(line, "%d-%d", &a, &b); err == nil {
			out = append(out, MeetingWindow{fromUnix(a), fromUnix(b)})
		}
	}
	return out
}

func (s *presenceStore) Presence(ctx context.Context, subjects []string) (map[string]Presence, error) {
	out := map[string]Presence{}
	if len(subjects) == 0 {
		return out, nil
	}
	q := `SELECT subject, share_off, manual_state, manual_until, ooo_revision, ooo_start, ooo_end, cal_revision, cal_at, busy FROM presence WHERE subject IN (?` + strings.Repeat(", ?", len(subjects)-1) + `)`
	args := make([]any, len(subjects))
	for i, sub := range subjects {
		args[i] = sub
	}
	rows, err := s.store.db.QueryContext(ctx, s.store.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sub, busy string
		var p Presence
		var off int
		var mu, ooS, ooE, ca int64
		if err := rows.Scan(&sub, &off, &p.ManualState, &mu, &p.OOORev, &ooS, &ooE, &p.CalendarRev, &ca, &busy); err != nil {
			return nil, err
		}
		p.ShareOff = off != 0
		p.ManualUntil, p.OOOStart, p.OOOEnd, p.CalendarAt = fromUnix(mu), fromUnix(ooS), fromUnix(ooE), fromUnix(ca)
		p.Busy = splitBusy(busy)
		out[sub] = p
	}
	return out, rows.Err()
}

// ensure makes subject's row, sharing on, if it has none.
func (s *presenceStore) ensure(ctx context.Context, subject string) error {
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO presence (subject) VALUES (?) ON CONFLICT (subject) DO NOTHING`), subject)
	return err
}

func (s *presenceStore) SetSharing(ctx context.Context, subject string, on bool) error {
	if err := s.ensure(ctx, subject); err != nil {
		return err
	}
	if on {
		_, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE presence SET share_off = 0 WHERE subject = ?`), subject)
		return err
	}
	// Revisions stay, so a push older than the one cleared stays stale after sharing is back.
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE presence SET share_off = 1, manual_state = '', manual_until = 0, ooo_start = 0, ooo_end = 0, cal_at = 0, busy = '' WHERE subject = ?`), subject)
	return err
}

func (s *presenceStore) SetManual(ctx context.Context, subject, state string, until time.Time) error {
	if err := s.ensure(ctx, subject); err != nil {
		return err
	}
	if state == "" {
		until = time.Time{}
	}
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE presence SET manual_state = ?, manual_until = ? WHERE subject = ? AND share_off = 0`), state, unix(until), subject)
	if err = oneRow(res, err); err == ErrNotFound {
		return ErrSharingOff
	}
	return err
}

// push applies set (columns after the revision column) when sharing is on and revision is not
// older than the stored one, else says why not.
func (s *presenceStore) push(ctx context.Context, subject, revCol string, revision int64, set string, args ...any) (PresenceOutcome, error) {
	if err := s.ensure(ctx, subject); err != nil {
		return "", err
	}
	args = append([]any{revision}, args...)
	args = append(args, subject, revision)
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE presence SET `+revCol+` = ?, `+set+` WHERE subject = ? AND share_off = 0 AND `+revCol+` <= ?`), args...)
	switch err = oneRow(res, err); err {
	case nil:
		return PresenceApplied, nil
	case ErrNotFound:
	default:
		return "", err
	}
	var off int
	err = s.store.db.QueryRowContext(ctx, s.store.rebind(`SELECT share_off FROM presence WHERE subject = ?`), subject).Scan(&off)
	switch {
	case err == sql.ErrNoRows:
		return "", ErrNotFound
	case err != nil:
		return "", err
	case off != 0:
		return PresenceSharingOff, nil
	}
	return PresenceStale, nil
}

func (s *presenceStore) SetCalendar(ctx context.Context, subject string, revision int64, busy []MeetingWindow) (PresenceOutcome, error) {
	return s.push(ctx, subject, "cal_revision", revision, `cal_at = ?, busy = ?`, time.Now().Unix(), joinBusy(busy))
}

func (s *presenceStore) SetOutOfOffice(ctx context.Context, subject string, revision int64, start, end time.Time) (PresenceOutcome, error) {
	return s.push(ctx, subject, "ooo_revision", revision, `ooo_start = ?, ooo_end = ?`, unix(start), unix(end))
}
