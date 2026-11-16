package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestPresenceStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Presence()
	got, err := st.Presence(ctx, []string{"sub-a"})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	busy := []store.MeetingWindow{{Start: now, End: now.Add(time.Hour)}, {Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)}}
	if o, err := st.SetCalendar(ctx, "sub-a", 10, busy); o != store.PresenceApplied || err != nil {
		t.Fatalf("calendar: %v %v", o, err)
	}
	if o, _ := st.SetCalendar(ctx, "sub-a", 9, nil); o != store.PresenceStale {
		t.Fatalf("older revision: %v", o)
	}
	if o, _ := st.SetCalendar(ctx, "sub-a", 10, busy); o != store.PresenceApplied {
		t.Fatalf("same revision is applied again: %v", o)
	}
	if o, _ := st.SetOutOfOffice(ctx, "sub-a", 5, now, now.Add(24*time.Hour)); o != store.PresenceApplied {
		t.Fatalf("ooo: %v", o)
	}
	if err := st.SetManual(ctx, "sub-a", "busy", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p := mustPresence(t, st, "sub-a")
	if p.ShareOff || len(p.Busy) != 2 || !p.Busy[1].End.Equal(now.Add(3*time.Hour)) || p.CalendarAt.IsZero() || p.CalendarRev != 10 ||
		!p.OOOEnd.Equal(now.Add(24*time.Hour)) || p.ManualState != "busy" || !p.ManualUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("stored: %+v", p)
	}

	// Off clears everything and refuses later inputs.
	if err := st.SetSharing(ctx, "sub-a", false); err != nil {
		t.Fatal(err)
	}
	p = mustPresence(t, st, "sub-a")
	if !p.ShareOff || p.Busy != nil || !p.CalendarAt.IsZero() || !p.OOOEnd.IsZero() || p.ManualState != "" {
		t.Fatalf("after off: %+v", p)
	}
	if o, _ := st.SetCalendar(ctx, "sub-a", 11, busy); o != store.PresenceSharingOff {
		t.Fatalf("calendar while off: %v", o)
	}
	if o, _ := st.SetOutOfOffice(ctx, "sub-a", 6, now, now.Add(time.Hour)); o != store.PresenceSharingOff {
		t.Fatalf("ooo while off: %v", o)
	}
	if err := st.SetManual(ctx, "sub-a", "away", now.Add(time.Hour)); !errors.Is(err, store.ErrSharingOff) {
		t.Fatalf("manual while off: %v", err)
	}
	if p := mustPresence(t, st, "sub-a"); p.Busy != nil || p.ManualState != "" {
		t.Fatalf("stored while off: %+v", p)
	}
	// Back on: a push older than the last stored one stays stale.
	if err := st.SetSharing(ctx, "sub-a", true); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.SetCalendar(ctx, "sub-a", 9, busy); o != store.PresenceStale {
		t.Fatalf("old push after on: %v", o)
	}
	if o, _ := st.SetOutOfOffice(ctx, "sub-a", 7, time.Time{}, time.Time{}); o != store.PresenceApplied {
		t.Fatalf("ooo off: %v", o)
	}
	if err := st.SetManual(ctx, "sub-a", "", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p := mustPresence(t, st, "sub-a"); p.ShareOff || !p.OOOEnd.IsZero() || p.ManualState != "" || !p.ManualUntil.IsZero() {
		t.Fatalf("cleared: %+v", p)
	}
	all, err := st.Presence(ctx, []string{"sub-a", "sub-none"})
	if err != nil || len(all) != 1 {
		t.Fatalf("batch: %v %v", all, err)
	}
}

func mustPresence(t *testing.T, st store.PresenceStore, sub string) store.Presence {
	t.Helper()
	m, err := st.Presence(context.Background(), []string{sub})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := m[sub]
	if !ok {
		t.Fatalf("no row for %s", sub)
	}
	return p
}
