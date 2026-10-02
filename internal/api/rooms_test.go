package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

const (
	rGroup = "!grp:example.com"
	rDM    = "!dm:example.com"
)

func roomPath(id string) string { return "/api/admin/matrix/rooms/" + url.PathEscape(id) }

// fakeRooms stands in for Synapse's admin API: an encrypted group room "Team chat" and an
// unnamed DM, both with alice and bob; the group room's avatar is local media.
type fakeRooms struct {
	mu        sync.Mutex
	rooms     map[string]*synapseadmin.Room
	order     []string
	members   map[string][]string
	blocked   map[string]bool
	jobs      map[string][]synapseadmin.DeleteJob
	media     map[string][]synapseadmin.Media
	err       error // every call
	changeErr error // Close and Delete
	mediaErr  error // DeleteMedia
	tokens    []string
	changes   []string // "media <id>", "close <room>", "delete <room>", in order
}

func newFakeRooms() *fakeRooms {
	enc := "m.megolm.v1.aes-sha2"
	both := func() []string { return []string{"@alice:example.com", "@bob:example.com"} }
	return &fakeRooms{
		rooms: map[string]*synapseadmin.Room{
			rGroup: {ID: rGroup, Name: "Team chat", Creator: "@alice:example.com", Members: 2, LocalMembers: 2, Encryption: enc, JoinRule: "invite", StateEvents: 12},
			rDM:    {ID: rDM, Creator: "@alice:example.com", Members: 2, LocalMembers: 2, Encryption: enc, JoinRule: "invite", StateEvents: 9},
		},
		order:   []string{rGroup, rDM},
		members: map[string][]string{rGroup: both(), rDM: both()},
		blocked: map[string]bool{},
		jobs:    map[string][]synapseadmin.DeleteJob{},
		media:   map[string][]synapseadmin.Media{rGroup: {{Server: "example.com", ID: "AVATAR1"}}},
	}
}

// use records the token and returns the injected failure; the caller holds mu.
func (f *fakeRooms) use(tok string) error { f.tokens = append(f.tokens, tok); return f.err }

func roomNotFound(id string) error {
	return &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms/" + id, Status: http.StatusNotFound, Errcode: "M_NOT_FOUND", Message: "Room not found"}
}

func (f *fakeRooms) Rooms(_ context.Context, tok string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return synapseadmin.RoomPage{}, err
	}
	var all []synapseadmin.Room
	for _, id := range f.order {
		r, ok := f.rooms[id]
		if ok && (q.Search == "" || r.ID == q.Search || strings.Contains(strings.ToLower(r.Name), strings.ToLower(q.Search))) {
			all = append(all, *r)
		}
	}
	n := len(all)
	return synapseadmin.RoomPage{Rooms: all[min(q.Offset, n):min(q.Offset+q.Limit, n)], Total: n}, nil
}

func (f *fakeRooms) Room(_ context.Context, tok, id string) (synapseadmin.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return synapseadmin.Room{}, err
	}
	r, ok := f.rooms[id]
	if !ok {
		return synapseadmin.Room{}, roomNotFound(id)
	}
	return *r, nil
}

func (f *fakeRooms) Members(_ context.Context, tok, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return nil, err
	}
	if _, ok := f.rooms[id]; !ok {
		return nil, roomNotFound(id)
	}
	return f.members[id], nil
}

func (f *fakeRooms) Blocked(_ context.Context, tok, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[id], f.use(tok)
}

func (f *fakeRooms) change(tok, kind, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return "", err
	}
	if f.changeErr != nil {
		return "", f.changeErr
	}
	f.changes = append(f.changes, kind+" "+id)
	job := fmt.Sprintf("%s-%d", kind, len(f.changes))
	f.jobs[id] = append(f.jobs[id], synapseadmin.DeleteJob{ID: job, Status: "complete"})
	f.blocked[id] = true
	if kind == "delete" {
		delete(f.rooms, id)
	} else {
		f.members[id] = nil
		f.rooms[id].Members, f.rooms[id].LocalMembers = 0, 0
	}
	return job, nil
}

func (f *fakeRooms) Close(_ context.Context, tok, id string) (string, error) {
	return f.change(tok, "close", id)
}
func (f *fakeRooms) Delete(_ context.Context, tok, id string) (string, error) {
	return f.change(tok, "delete", id)
}

func (f *fakeRooms) DeleteJobs(_ context.Context, tok, id string) ([]synapseadmin.DeleteJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]synapseadmin.DeleteJob{}, f.jobs[id]...), f.use(tok)
}

func (f *fakeRooms) RoomMedia(_ context.Context, tok, id string) ([]synapseadmin.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.media[id], f.use(tok)
}

func (f *fakeRooms) DeleteMedia(_ context.Context, tok string, m synapseadmin.Media) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return err
	}
	if f.mediaErr != nil {
		return f.mediaErr
	}
	f.changes = append(f.changes, "media "+m.ID)
	return nil
}

// setupRoomsServer is setupMatrixServer with its Synapse admin fake in hand.
func setupRoomsServer(t *testing.T) (*api.Server, store.Store, *fakeMAS, *fakeRooms) {
	t.Helper()
	srv, st, _, f := setupMatrixServer(t)
	rooms := newFakeRooms()
	api.SetRoomAdminForTest(srv, rooms)
	return srv, st, f, rooms
}

// synapseAdminHealth is the synapse-admin component of one Health request.
func synapseAdminHealth(t *testing.T, srv *api.Server, admin *http.Cookie) health.Component {
	t.Helper()
	h := decode[componentsBody](t, adminDo(t, srv, admin, "GET", "/api/admin/health", nil))
	for _, c := range h.Components {
		if c.Name == "synapse-admin" {
			return c
		}
	}
	t.Fatalf("no synapse-admin in %+v", h.Components)
	return health.Component{}
}

func TestHealthProvesSynapseAdminAccess(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	get := func() health.Component { return synapseAdminHealth(t, srv, admin) }
	if c := get(); c.Status != "up" || c.Error != "" || len(rooms.tokens) != 1 || rooms.tokens[0] != "console-token" || f.consoleRuns != 1 {
		t.Fatalf("working: %+v tokens %v runs %d", c, rooms.tokens, f.consoleRuns)
	}
	rooms.err = &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms", Status: 401, Errcode: "M_UNKNOWN_TOKEN", Message: "Token is not active"}
	if c := get(); c.Status != "down" || !strings.Contains(c.Error, "Token is not active") {
		t.Errorf("token refused: %+v", c)
	}
	f.consoleErr = errors.New("console account is locked in MAS")
	if c := get(); c.Status != "down" || c.Error != "console account is locked in MAS" {
		t.Errorf("locked: %+v", c)
	}
}

// A MAS that never answers (and ignores the context) cannot hold Health past its deadline.
func TestHealthSynapseAdminProbeCannotHang(t *testing.T) {
	srv, st, f, _ := setupRoomsServer(t)
	f.consoleHang = make(chan struct{})
	t.Cleanup(func() { close(f.consoleHang) })
	admin := loginAs(t, srv, st, "root", "admin")
	start := time.Now()
	c := synapseAdminHealth(t, srv, admin)
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("health took %s", d)
	}
	if c.Status != "down" || !strings.Contains(c.Error, "timed out") {
		t.Fatalf("%+v", c)
	}
}

type roomRow struct {
	ID, Name, Alias, Creator  string
	Members                   int
	Encrypted, Public, Closed bool
	JoinRule                  string `json:"join_rule"`
	StateEvents               int    `json:"state_events"`
}

type roomsPage struct {
	Rooms                []roomRow
	Total, Offset, Limit int
}

func TestRoomsListPagedSearchedWithClosedFlag(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	rooms.blocked[rDM] = true
	admin := loginAs(t, srv, st, "root", "admin")
	w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms", nil)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", w.Header())
	}
	all := decode[roomsPage](t, w)
	if all.Total != 2 || all.Limit != 50 || len(all.Rooms) != 2 {
		t.Fatalf("%+v", all)
	}
	g, d := all.Rooms[0], all.Rooms[1]
	if g.ID != rGroup || g.Name != "Team chat" || g.Members != 2 || !g.Encrypted || g.Public || g.JoinRule != "invite" ||
		g.StateEvents != 12 || g.Closed || g.Creator != "@alice:example.com" {
		t.Errorf("group %+v", g)
	}
	if d.ID != rDM || d.Name != "" || !d.Closed {
		t.Errorf("dm %+v", d)
	}
	if p := decode[roomsPage](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms?search=team&limit=1", nil)); p.Total != 1 || p.Rooms[0].ID != rGroup {
		t.Errorf("search %+v", p)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms?search=zzz", nil).Body.String(); !strings.Contains(body, `"rooms":[]`) {
		t.Errorf("empty page is not an empty array: %s", body)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?offset=-1", "?search=" + strings.Repeat("x", 256)} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	for _, tok := range rooms.tokens {
		if tok != "console-token" {
			t.Fatalf("a Synapse call carried %q", tok)
		}
	}
	if f.consoleRuns == 0 {
		t.Fatal("no console session was used")
	}
}

func TestRoomDetailShowsMembersAndConfirmText(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type detail struct {
		Room         struct{ ID, Name string }
		Members      []string
		MembersTotal int    `json:"members_total"`
		ConfirmText  string `json:"confirm_text"`
		Jobs         []struct {
			DeleteID string `json:"delete_id"`
			Status   string `json:"status"`
		}
	}
	get := func(id string) detail { return decode[detail](t, adminDo(t, srv, admin, "GET", roomPath(id), nil)) }
	g := get(rGroup)
	if g.Room.ID != rGroup || strings.Join(g.Members, ",") != "@alice:example.com,@bob:example.com" || g.MembersTotal != 2 ||
		g.ConfirmText != "Team chat" || g.Jobs == nil || len(g.Jobs) != 0 {
		t.Fatalf("group %+v", g)
	}
	if d := get(rDM); d.ConfirmText != rDM {
		t.Errorf("an unnamed room confirms with its ID: %q", d.ConfirmText)
	}
	// Names nobody can type fall back to the room ID; a long name confirms as shown (clipped).
	for name, want := range map[string]string{
		"Evil\u202egnp.exe": rGroup,
		"zero\u200bwidth":   rGroup,
		"tab\tname":         rGroup,
	} {
		rooms.rooms[rGroup].Name = name
		if got := get(rGroup).ConfirmText; got != want {
			t.Errorf("%q: confirm %q, want %q", name, got, want)
		}
	}
	rooms.rooms[rGroup].Name = strings.Repeat("é", 300)
	long := get(rGroup)
	if len(long.Room.Name) > 200 || !utf8.ValidString(long.Room.Name) || long.ConfirmText != long.Room.Name {
		t.Errorf("long name %d bytes, confirm %d bytes", len(long.Room.Name), len(long.ConfirmText))
	}
	if w := adminDo(t, srv, admin, "GET", roomPath("!nope:example.com"), nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown room: %d %s", w.Code, w.Body.String())
	}
	rooms.tokens = nil
	for _, id := range []string{"nope", "!", "!a/b:example.com", "!" + strings.Repeat("a", 255), "!a:b:c:d",
		"!../x:example.com", "!a:example.com/..", "!a\u202e:example.com", "!a\x00:example.com", "%2e%2e"} {
		for _, p := range []string{"", "/delete-status", "/close", "/delete"} {
			method := "GET"
			if p == "/close" || p == "/delete" {
				method = "POST"
			}
			if w := adminDo(t, srv, admin, method, roomPath(id)+p, map[string]string{"confirm": id}); w.Code != http.StatusBadRequest {
				t.Errorf("%s id %q%s: %d", method, id, p, w.Code)
			}
		}
	}
	// Dot segments never reach a handler: the mux redirects to the cleaned path.
	for _, p := range []string{"/..", "/.", "/%2e%2e", "/%2E%2E/close", "/../delete"} {
		if w := adminDo(t, srv, admin, "POST", "/api/admin/matrix/rooms"+p, nil); w.Code < 300 {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if len(rooms.tokens) != 0 || len(rooms.changes) != 0 {
		t.Fatalf("an invalid ID reached Synapse: %v %v", rooms.tokens, rooms.changes)
	}
}

func TestRoomCloseNeedsFreshAdminIsAuditedAndIdempotent(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	path := roomPath(rGroup) + "/close"
	if w := adminDo(t, srv, staleAdmin(t, st), "POST", path, nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale: %d %s", w.Code, w.Body.String())
	}
	if len(rooms.changes) != 0 || len(rooms.tokens) != 0 {
		t.Fatalf("a stale session reached Synapse: %v", rooms.changes)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, want := range []string{`{"delete_id":"close-1","outcome":"started"}`, `{"delete_id":"","outcome":"already_closed"}`} {
		if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("want %s: %d %s", want, w.Code, w.Body.String())
		}
	}
	if strings.Join(rooms.changes, ",") != "close "+rGroup {
		t.Fatalf("changes %v", rooms.changes)
	}
	rooms.err = &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms/x", Status: 500, Message: "db down"}
	if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Synapse admin access is not working") {
		t.Fatalf("Synapse failure: %d %s", w.Code, w.Body.String())
	}
	rows := auditRows(t, st, "matrix.room_close") // newest first
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, want := range []string{`outcome="error: Synapse GET`, `outcome="already_closed"`, `outcome="started" delete_id="close-1"`} {
		r := rows[i]
		if r.Resource != rGroup || r.UserID != "usr_root" || r.IPAddress == "" || !strings.HasPrefix(r.Details, want) {
			t.Errorf("row %d: %+v", i, r)
		}
	}
}

// A double click or a second admin: refused while a job runs, by our check or Synapse's.
func TestRoomChangeRefusedWhileAJobRuns(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	rooms.jobs[rGroup] = []synapseadmin.DeleteJob{{ID: "d0", Status: "active"}}
	for _, p := range []string{"/close", "/delete"} {
		w := adminDo(t, srv, admin, "POST", roomPath(rGroup)+p, map[string]string{"confirm": "Team chat"})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "still running") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if len(rooms.changes) != 0 {
		t.Fatalf("changes %v", rooms.changes)
	}
	// Refused before any media is counted, so the row carries the outcome alone.
	if r := auditRows(t, st, "matrix.room_delete")[0]; r.Details != `outcome="refused: a close or delete is running"` {
		t.Errorf("delete row %q", r.Details)
	}
	rooms.jobs[rGroup] = nil
	rooms.changeErr = &synapseadmin.Error{Method: "DELETE", Path: "/_synapse/admin/v2/rooms/x", Status: 400, Errcode: "M_UNKNOWN", Message: "Purge already in progress for " + rGroup}
	if w := adminDo(t, srv, admin, "POST", roomPath(rGroup)+"/close", nil); w.Code != http.StatusConflict {
		t.Errorf("Synapse's own refusal: %d %s", w.Code, w.Body.String())
	}
	if r := auditRows(t, st, "matrix.room_close")[0]; r.Details != `outcome="refused: a close or delete is running"` {
		t.Errorf("Synapse's refusal row %q", r.Details)
	}
}

func TestRoomDeleteChecksConfirmationServerSide(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	path := roomPath(rGroup) + "/delete"
	for _, body := range []any{map[string]string{"confirm": "team chat"}, map[string]string{"confirm": ""}, map[string]string{}} {
		if w := adminDo(t, srv, admin, "POST", path, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "exactly") {
			t.Fatalf("%v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(rooms.changes) != 0 {
		t.Fatalf("a refused delete changed something: %v", rooms.changes)
	}
	if w := adminDo(t, srv, admin, "POST", path, "not an object"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: %d", w.Code)
	}
	if n := len(auditRows(t, st, "matrix.room_delete")); n != 3 {
		t.Errorf("%d delete rows; the malformed body must not be audited", n)
	}
	w := adminDo(t, srv, admin, "POST", path, map[string]string{"confirm": "Team chat"})
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"delete_id":"delete-2","outcome":"started"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Join(rooms.changes, ",") != "media AVATAR1,delete "+rGroup {
		t.Fatalf("media first, then the purge: %v", rooms.changes)
	}
	if r := auditRows(t, st, "matrix.room_delete")[0]; r.Details != `outcome="started" delete_id="delete-2" media="1"` || r.Resource != rGroup {
		t.Errorf("row %+v", r)
	}
	if w := adminDo(t, srv, admin, "POST", roomPath(rDM)+"/delete", map[string]string{"confirm": rDM}); w.Code != http.StatusOK {
		t.Errorf("unnamed room by ID: %d %s", w.Code, w.Body.String())
	}
	if w := adminDo(t, srv, admin, "POST", path, map[string]string{"confirm": "Team chat"}); w.Code != http.StatusNotFound {
		t.Errorf("deleting a deleted room: %d", w.Code)
	}
}

func TestRoomDeleteStopsWhenMediaDeletionFails(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	rooms.mediaErr = errors.New("Synapse DELETE /_synapse/admin/v1/media/example.com/AVATAR1: " + strings.Repeat("\U0001F4A5", 120))
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "POST", roomPath(rGroup)+"/delete", map[string]string{"confirm": "Team chat"})
	if w.Code != http.StatusBadGateway || len(rooms.changes) != 0 {
		t.Fatalf("%d %v", w.Code, rooms.changes)
	}
	r := auditRows(t, st, "matrix.room_delete")[0]
	if len(r.Details) > 200 || !strings.HasSuffix(r.Details, `media="0"`) || !strings.HasPrefix(api.DetailOutcomeForTest(r.Details), "error: Synapse DELETE") {
		t.Fatalf("details %q", r.Details)
	}
}

func TestRoomDeleteStatusListsJobs(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	rooms.jobs["!purged:example.com"] = []synapseadmin.DeleteJob{{ID: "d1", Status: "active"}, {ID: "d2", Status: "shiny"}}
	body := adminDo(t, srv, admin, "GET", roomPath("!purged:example.com")+"/delete-status", nil).Body.String()
	if strings.TrimSpace(body) != `{"jobs":[{"delete_id":"d1","status":"active"},{"delete_id":"d2","status":"unknown"}]}` {
		t.Errorf("%s", body)
	}
	if body := adminDo(t, srv, admin, "GET", roomPath(rGroup)+"/delete-status", nil).Body.String(); strings.TrimSpace(body) != `{"jobs":[]}` {
		t.Errorf("no jobs: %s", body)
	}
}

func TestBrokenConsoleAccountReachesRooms(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	f.consoleErr = errors.New("console account is locked in MAS")
	admin := loginAs(t, srv, st, "root", "admin")
	for _, rt := range []struct{ method, path string }{{"GET", "/api/admin/matrix/rooms"}, {"GET", roomPath(rGroup)}, {"POST", roomPath(rGroup) + "/close"}} {
		w := adminDo(t, srv, admin, rt.method, rt.path, nil)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Synapse admin access is not working: console account is locked in MAS") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	if len(rooms.tokens) != 0 {
		t.Fatal("Synapse was called without a console session")
	}
	if r := auditRows(t, st, "matrix.room_close"); len(r) != 1 || r[0].Details != `outcome="error: console account is locked in MAS"` {
		t.Errorf("rows %+v", r)
	}
}
