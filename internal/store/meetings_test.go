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
