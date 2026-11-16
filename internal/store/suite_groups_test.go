package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestSuiteGroupRooms(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Meetings()
	for _, id := range []string{"grp_1", "grp_2"} { // two pending rows: no room ID clash
		if err := st.CreateSuiteGroupRoom(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateSuiteGroupRoom(ctx, "grp_1"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	rows, err := st.SuiteGroupRooms(ctx)
	if g := rows["grp_1"]; err != nil || len(rows) != 2 || g.State != store.SuiteGroupPending || g.RoomID != "" || g.Members != nil || g.CreatedAt.IsZero() {
		t.Fatalf("pending: %+v %v", rows, err)
	}
	g := &store.SuiteGroupRoom{GroupID: "grp_1", RoomID: "!r:x", Members: []string{"@a:x", "@b:x"}, State: store.SuiteGroupActive}
	if err := st.UpdateSuiteGroupRoom(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSuiteGroupRoom(ctx, &store.SuiteGroupRoom{GroupID: "grp_2", RoomID: "!r:x", State: store.SuiteGroupActive}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("room taken: %v", err)
	}
	if err := st.UpdateSuiteGroupRoom(ctx, &store.SuiteGroupRoom{GroupID: "grp_9", State: store.SuiteGroupActive}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := st.UpdateSuiteGroupRoom(ctx, &store.SuiteGroupRoom{GroupID: "grp_2", State: "closed"}); err == nil {
		t.Fatal("state outside the CHECK accepted")
	}
	rows, _ = st.SuiteGroupRooms(ctx)
	if got := rows["grp_1"]; got.RoomID != "!r:x" || got.State != store.SuiteGroupActive || !slices.Equal(got.Members, g.Members) {
		t.Fatalf("updated: %+v", got)
	}
}
