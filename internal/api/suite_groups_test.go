package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky-primitives/suitedir"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// fakeGroupDirectory is KyIdentity's GET /api/suite/groups for client kymessage.
type fakeGroupDirectory struct {
	mu     sync.Mutex
	status int
	groups []suitedir.Group
}

func (d *fakeGroupDirectory) set(status int, groups ...suitedir.Group) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status, d.groups = status, groups
}

// countingMeetings counts suite group row writes.
type countingMeetings struct {
	store.MeetingStore
	mu     sync.Mutex
	writes int
}

func (c *countingMeetings) CreateSuiteGroupRoom(ctx context.Context, id string) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.MeetingStore.CreateSuiteGroupRoom(ctx, id)
}

func (c *countingMeetings) UpdateSuiteGroupRoom(ctx context.Context, g *store.SuiteGroupRoom) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.MeetingStore.UpdateSuiteGroupRoom(ctx, g)
}

func (c *countingMeetings) take() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.writes
	c.writes = 0
	return n
}

type countingStore struct {
	store.Store
	meetings *countingMeetings
}

func (c countingStore) Meetings() store.MeetingStore { return c.meetings }

type groupEnv struct {
	meetingEnv
	dir    *fakeGroupDirectory
	writes *countingMeetings
}

// setupGroups: setupMeetings' people, plus owner (sub-o), a dedicated administrator the
// directory reports inactive, with a chat account.
func setupGroups(t *testing.T) groupEnv {
	t.Helper()
	d := &fakeGroupDirectory{status: 200}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := r.BasicAuth(); !ok || id != "kymessage" || secret != "client-secret" || r.URL.Path != "/api/suite/groups" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		w.WriteHeader(d.status)
		if d.status == 200 {
			groups := d.groups
			if groups == nil {
				groups = []suitedir.Group{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
		}
	}))
	t.Cleanup(hs.Close)
	e := setupMeetingsAt(t, false, hs.URL)
	e.mas.users = append(e.mas.users, matrixsync.User{ID: "01J9ZK8V6N3W4X5Y6Z7A8BOWNR", Username: "owner", Subject: "sub-o"})
	if err := e.st.Users().CreateUser(context.Background(), &store.User{ID: "usr_o", Username: "owner-ky", Role: "admin", Status: "inactive", SSOProvider: "kyidentity", SSOSubject: "sub-o"}); err != nil {
		t.Fatal(err)
	}
	c := &countingMeetings{MeetingStore: e.st.Meetings()}
	api.SetStoreForTest(e.srv, countingStore{e.st, c})
	return groupEnv{e, d, c}
}

func active(id, name string, subs ...string) suitedir.Group {
	if subs == nil {
		subs = []string{}
	}
	return suitedir.Group{ID: id, Name: name, Slug: strings.ReplaceAll(id, "_", "-"), State: suitedir.GroupActive, Members: subs}
}

func archived(id, name string) suitedir.Group {
	return suitedir.Group{ID: id, Name: name, Slug: strings.ReplaceAll(id, "_", "-"), State: suitedir.GroupArchived, Members: []string{}}
}

func (e groupEnv) reconcile(t *testing.T) error {
	t.Helper()
	return e.srv.ReconcileSuiteGroups(context.Background())
}

func (e groupEnv) row(t *testing.T, id string) store.SuiteGroupRoom {
	t.Helper()
	rows, err := e.st.Meetings().SuiteGroupRooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows[id]
}

// settled: a second pass makes no Matrix change and no write.
func (e groupEnv) settled(t *testing.T) {
	t.Helper()
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if calls, writes := e.rooms.take(), e.writes.take(); len(calls) != 0 || writes != 0 {
		t.Fatalf("second pass changed things: %v, %d writes", calls, writes)
	}
}

func (e groupEnv) member(room, mxid string) string {
	e.rooms.mu.Lock()
	defer e.rooms.mu.Unlock()
	return e.rooms.member[room][mxid]
}

func TestSuiteGroupLifecycle(t *testing.T) {
	e := setupGroups(t)
	// sub-b is locked, sub-x has no account, sub-o is a dedicated administrator.
	e.dir.set(200, active("grp_sales", "Sales", "sub-a", "sub-b", "sub-x", "sub-o"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, "grp_sales")
	if calls := e.rooms.take(); !slices.Equal(calls, []string{"create ", "invite " + aliceMX}) {
		t.Fatalf("create: %v", calls)
	}
	room := e.rooms.rooms[r.RoomID]
	if r.State != store.SuiteGroupActive || !slices.Equal(r.Members, []string{aliceMX}) || room.Name != "Sales" ||
		room.JoinRule != "invite" || room.Encryption == "" || e.rooms.orgs[r.RoomID] != "" {
		t.Fatalf("created %+v %+v", r, room)
	}
	e.writes.take()
	e.settled(t)

	// Rename, add erin, remove alice.
	e.dir.set(200, active("grp_sales", "Sales EMEA", "sub-e"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if calls := e.rooms.take(); !slices.Equal(calls, []string{"name", "kick " + aliceMX, "invite " + erinMX}) || e.rooms.rooms[r.RoomID].Name != "Sales EMEA" {
		t.Fatalf("update: %v", calls)
	}
	if got := e.row(t, "grp_sales"); !slices.Equal(got.Members, []string{erinMX}) {
		t.Fatalf("members %v", got.Members)
	}
	e.writes.take()
	e.settled(t)

	// Absence never archives.
	e.dir.set(200)
	e.settled(t)

	// Turned off: read-only, everyone out, room kept.
	e.dir.set(200, archived("grp_sales", "Sales EMEA"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if calls := e.rooms.take(); !slices.Equal(calls, []string{"readonly true", "kick " + erinMX}) {
		t.Fatalf("archive: %v", calls)
	}
	if got := e.row(t, "grp_sales"); got.State != store.SuiteGroupArchived || got.RoomID != r.RoomID || got.Members != nil || e.rooms.rooms[r.RoomID] == nil {
		t.Fatalf("archived %+v", got)
	}
	e.writes.take()
	e.settled(t)

	// Back on: the same room, levels restored, members invited again (erin had been kicked).
	e.dir.set(200, active("grp_sales", "Sales EMEA", "sub-e", "sub-a"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if calls := e.rooms.take(); !slices.Equal(calls, []string{"readonly false", "invite " + aliceMX, "invite " + erinMX}) {
		t.Fatalf("restore: %v", calls)
	}
	if got := e.row(t, "grp_sales"); got.State != store.SuiteGroupActive || got.RoomID != r.RoomID {
		t.Fatalf("restored %+v", got)
	}
	e.writes.take()
	e.settled(t)

	// Deleted, then a namesake with a new ID: a new room; the old one stays archived.
	e.dir.set(200, archived("grp_sales", "Sales EMEA"), active("grp_sales2", "Sales EMEA", "sub-a"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if n := e.row(t, "grp_sales2"); n.RoomID == "" || n.RoomID == r.RoomID || !slices.Equal(n.Members, []string{aliceMX}) || e.row(t, "grp_sales").State != store.SuiteGroupArchived {
		t.Fatalf("namesake %+v", n)
	}
	e.rooms.take()
	e.writes.take()
	e.settled(t)

	if e.member(r.RoomID, "@owner:example.com") != "" {
		t.Fatal("a dedicated administrator was invited")
	}
	e.rooms.mu.Lock()
	changes := e.rooms.changes
	e.rooms.mu.Unlock()
	if len(changes) != 0 {
		t.Fatalf("a room was closed or deleted: %v", changes)
	}
	rows, _, _ := e.st.Audit().ListAuditRecords(context.Background(), 0, 100, "suite_group.")
	for _, a := range rows {
		if strings.Contains(a.Details, "Sales") || strings.Contains(a.Details, "@alice") || strings.Contains(a.Details, "sub-") {
			t.Fatalf("audit names a group or person: %s", a.Details)
		}
	}
	if len(rows) != 6 {
		t.Fatalf("audits: %d", len(rows))
	}
}

// Someone who left the room is not invited back every pass.
func TestSuiteGroupLeaverStaysOut(t *testing.T) {
	e := setupGroups(t)
	e.dir.set(200, active("grp_1", "Ops", "sub-a"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	room := e.row(t, "grp_1").RoomID
	e.rooms.mu.Lock()
	e.rooms.member[room][aliceMX] = "leave"
	e.rooms.mu.Unlock()
	e.rooms.take()
	e.writes.take()
	e.settled(t)
}

func TestSuiteGroupCrashHalfWayHeals(t *testing.T) {
	ctx := context.Background()
	e := setupGroups(t)
	// A pending row with no room: the room is created once.
	if err := e.st.Meetings().CreateSuiteGroupRoom(ctx, "grp_p"); err != nil {
		t.Fatal(err)
	}
	// A recorded room whose invites never went out.
	recorded, _ := e.rooms.CreateMeetingRoom(ctx, "tok", "Support", "")
	if err := e.st.Meetings().CreateSuiteGroupRoom(ctx, "grp_r"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Meetings().UpdateSuiteGroupRoom(ctx, &store.SuiteGroupRoom{GroupID: "grp_r", RoomID: recorded, Members: []string{aliceMX, erinMX}, State: store.SuiteGroupActive}); err != nil {
		t.Fatal(err)
	}
	e.rooms.take()
	e.dir.set(200, active("grp_p", "Pending", "sub-a"), active("grp_r", "Support", "sub-a", "sub-e"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, c := range e.rooms.take() {
		if strings.HasPrefix(c, "create") {
			creates++
		}
	}
	if p := e.row(t, "grp_p"); creates != 1 || p.RoomID == "" || p.RoomID == recorded || e.member(p.RoomID, aliceMX) != "invite" {
		t.Fatalf("pending: %d creates, %+v", creates, p)
	}
	if e.member(recorded, aliceMX) != "invite" || e.member(recorded, erinMX) != "invite" || e.row(t, "grp_r").RoomID != recorded {
		t.Fatal("recorded room's invites not sent")
	}
	e.writes.take()
	e.settled(t)
}

func TestSuiteGroupDirectoryErrorsChangeNothing(t *testing.T) {
	e := setupGroups(t)
	e.dir.set(200, active("grp_1", "Ops", "sub-a"))
	if err := e.reconcile(t); err != nil {
		t.Fatal(err)
	}
	e.rooms.take()
	e.writes.take()
	for _, status := range []int{500, 401} {
		e.dir.set(status, archived("grp_1", "Ops"))
		err := e.reconcile(t)
		if calls, writes := e.rooms.take(), e.writes.take(); err == nil || len(calls) != 0 || writes != 0 {
			t.Fatalf("%d: %v %v %d", status, err, calls, writes)
		}
		if status == 401 && !strings.Contains(err.Error(), suitedir.ErrUnauthorized.Error()) {
			t.Fatalf("401: %v", err)
		}
		if strings.Contains(err.Error(), "client-secret") {
			t.Fatal("secret in the error")
		}
	}
	if r := e.row(t, "grp_1"); r.State != store.SuiteGroupActive || e.member(r.RoomID, aliceMX) != "invite" {
		t.Fatalf("changed: %+v", r)
	}
}
