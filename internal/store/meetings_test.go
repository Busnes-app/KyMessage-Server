package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestMeetingStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Meetings()
	if _, err := st.GetMeeting(ctx, "mtg_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	m := &store.Meeting{ID: "mtg_1", RoomID: "!r:example.com", OrganizerSubject: "sub-a"}
	if err := st.CreateMeeting(ctx, m); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []store.Meeting{{ID: "mtg_1", RoomID: "!s:example.com"}, {ID: "mtg_2", RoomID: "!r:example.com"}} {
		if err := st.CreateMeeting(ctx, &dup); !errors.Is(err, store.ErrAlreadyExists) {
			t.Fatalf("duplicate %+v: %v", dup, err)
		}
	}
	got, err := st.GetMeeting(ctx, "mtg_1")
	if err != nil || got.State != store.MeetingOpen || got.Members != nil || got.CreatedAt.IsZero() {
		t.Fatalf("created: %+v %v", got, err)
	}
	if err := st.UpdateMeeting(ctx, "mtg_1", "sub-e", []string{"@a:x", "@e:x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetMeeting(ctx, "mtg_1"); got.OrganizerSubject != "sub-e" || !slices.Equal(got.Members, []string{"@a:x", "@e:x"}) {
		t.Fatalf("updated: %+v", got)
	}
	if err := st.CloseMeeting(ctx, "mtg_1"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateMeeting(ctx, "mtg_1", "sub-a", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of a closed meeting: %v", err)
	}
	if err := st.CloseMeeting(ctx, "mtg_none"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("close unknown: %v", err)
	}

	later := time.Now().Add(time.Minute)
	if err := st.RememberMeetingEvent(ctx, "e1", later); err != nil {
		t.Fatal(err)
	}
	if err := st.RememberMeetingEvent(ctx, "e1", later); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("held: %v", err)
	}
	if err := st.ForgetMeetingEvent(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if err := st.RememberMeetingEvent(ctx, "e1", time.Now().Add(-2*time.Second)); err != nil {
		t.Fatalf("after forget: %v", err)
	}
	if err := st.RememberMeetingEvent(ctx, "e1", later); err != nil {
		t.Fatalf("an expired ID is dropped: %v", err)
	}
}

func TestMeetingGuestStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Meetings()
	if err := st.CreateMeeting(ctx, &store.Meeting{ID: "mtg_g", RoomID: "!g:example.com", OrganizerSubject: "s"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	w := []store.MeetingWindow{{at.Add(time.Hour), at.Add(2 * time.Hour)}, {at, at.Add(time.Hour)}, {at, at.Add(time.Hour)}}
	if err := st.ReplaceWindows(ctx, "mtg_g", w); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Windows(ctx, "mtg_g"); err != nil || len(got) != 2 || !got[0].Start.Equal(at) || !got[1].End.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("windows: %v %v", got, err)
	}
	if err := st.ReplaceWindows(ctx, "mtg_g", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Windows(ctx, "mtg_g"); len(got) != 0 {
		t.Fatalf("replaced by none: %v", got)
	}

	g := &store.MeetingGuest{MeetingID: "mtg_g", KeyHash: "k1", LinkHash: "l1"}
	if err := st.AddGuest(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []store.MeetingGuest{{MeetingID: "mtg_g", KeyHash: "k1", LinkHash: "l2"}, {MeetingID: "mtg_g", KeyHash: "k2", LinkHash: "l1"}} {
		if err := st.AddGuest(ctx, &dup); !errors.Is(err, store.ErrAlreadyExists) {
			t.Fatalf("duplicate %+v: %v", dup, err)
		}
	}
	if active, _ := st.ActiveGuests(ctx); len(active) != 0 {
		t.Fatalf("a link alone is not active: %v", active)
	}
	got, err := st.GuestByLink(ctx, "l1")
	if err != nil || got.KeyHash != "k1" || got.MXID != "" || !got.SessionEnd.IsZero() {
		t.Fatalf("by link: %+v %v", got, err)
	}
	got.MXID, got.MASUserID, got.SessionID, got.SessionEnd = "@guest-a:x", "01MAS", "01SES", at
	got.Membership = "knock"
	if err := st.UpdateGuest(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, err := st.GuestByLink(ctx, "l1"); err != nil || again.MXID != "@guest-a:x" || !again.SessionEnd.Equal(at) || again.Membership != "knock" {
		t.Fatalf("updated: %+v %v", again, err)
	}
	if active, _ := st.ActiveGuests(ctx); len(active) != 1 {
		t.Fatalf("live: %v", active)
	}
	got.MXID, got.Revoked = "", true
	if err := st.UpdateGuest(ctx, got); err != nil {
		t.Fatal(err)
	}
	if active, _ := st.ActiveGuests(ctx); len(active) != 1 || !active[0].Revoked {
		t.Fatalf("a revocation to finish is active: %v", active)
	}
	if err := st.DeleteGuest(ctx, "mtg_g", "k1"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := st.Guests(ctx, "mtg_g"); len(gs) != 0 {
		t.Fatalf("deleted: %v", gs)
	}
	if err := st.UpdateGuest(ctx, got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of a deleted row: %v", err)
	}
}
