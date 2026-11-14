package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

const (
	calSecret  = "kycalendar-pair-secret-0123456789abcdef!"
	meetID     = "mtg_0123456789abcdef"
	calIssuer  = "https://id.example.com"
	consoleMX  = "@kymessages-console:example.com"
	aliceMX    = "@alice:example.com"
	erinMX     = "@erin:example.com"
	meetRoomID = "!meet1:example.com"
)

// fakeMeetingRooms is Synapse's client API for meeting rooms over fakeRooms' admin view.
type fakeMeetingRooms struct {
	*fakeRooms
	member  map[string]map[string]string // room → user → membership
	calls   []string
	banned  map[string]bool
	callErr error
}

func (f *fakeMeetingRooms) record(call string) error {
	f.calls = append(f.calls, call)
	return f.callErr
}

func (f *fakeMeetingRooms) CreateMeetingRoom(_ context.Context, tok, name, organizer string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("create " + organizer); err != nil {
		return "", err
	}
	f.rooms[meetRoomID] = &synapseadmin.Room{ID: meetRoomID, Name: name, Creator: consoleMX, Members: 1, LocalMembers: 1, Encryption: "m.megolm.v1.aes-sha2", JoinRule: "invite"}
	f.member[meetRoomID] = map[string]string{consoleMX: "join"}
	return meetRoomID, nil
}

func (f *fakeMeetingRooms) Memberships(_ context.Context, tok, room string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.member[room] {
		out[k] = v
	}
	return out, f.callErr
}

func (f *fakeMeetingRooms) Invite(_ context.Context, tok, room, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.banned[user] {
		return &synapseadmin.Error{Method: "POST", Path: "/_matrix/client/v3/rooms/x/invite", Status: 403, Errcode: "M_FORBIDDEN"}
	}
	if err := f.record("invite " + user); err != nil {
		return err
	}
	f.member[room][user] = "invite"
	return nil
}

func (f *fakeMeetingRooms) Kick(_ context.Context, tok, room, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("kick " + user); err != nil {
		return err
	}
	f.member[room][user] = "leave"
	return nil
}

func (f *fakeMeetingRooms) SetRoomName(_ context.Context, tok, room, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms[room].Name = name
	return f.record("name")
}

func (f *fakeMeetingRooms) SetOrganizer(_ context.Context, tok, room, organizer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("organizer " + organizer)
}

func (f *fakeMeetingRooms) ExemptFromRatelimit(_ context.Context, tok, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if user != consoleMX {
		return fmt.Errorf("exempted %s", user)
	}
	return nil
}

func (f *fakeMeetingRooms) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

type meetingEnv struct {
	srv   *api.Server
	st    store.Store
	cfg   *config.Config
	mas   *fakeMAS
	rooms *fakeMeetingRooms
}

// setupMeetings: KyCalendar paired from the environment; alice (sub-a) and erin (sub-e) are
// members who chat, bob's (sub-b) MAS account is locked, root (sub-r) is an administrator
// with a chat account, and nobody has subject sub-x.
func setupMeetings(t *testing.T, paired bool) meetingEnv {
	t.Helper()
	_, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	cfg.SSO.Enabled = true
	cfg.SSO.KyIdentityIssuer = calIssuer
	if paired {
		cfg.KyCalendar = config.KyCalendarConfig{URL: "https://cal.example.com", Secret: []byte(calSecret)}
	}
	srv := api.NewServer(cfg, st)
	mas := &fakeMAS{finished: map[string]bool{}, users: []matrixsync.User{
		{ID: uAlice, Username: "alice", Subject: "sub-a"},
		{ID: uBob, Username: "bob", Subject: "sub-b", Locked: true},
		{ID: uCarol, Username: "erin", Subject: "sub-e"},
		{ID: uDave, Username: "root", Subject: "sub-r"},
	}}
	srv.SetMatrixAdmin(mas)
	rooms := &fakeMeetingRooms{fakeRooms: newFakeRooms(), member: map[string]map[string]string{}, banned: map[string]bool{}}
	api.SetRoomAdminForTest(srv, rooms.fakeRooms)
	api.SetMeetingRoomsForTest(srv, rooms)
	for _, u := range []store.User{
		{ID: "usr_a", Username: "alice-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-a"},
		{ID: "usr_b", Username: "bob-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-b"},
		{ID: "usr_e", Username: "erin-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-e"},
		{ID: "usr_r", Username: "root-ky", Role: "admin", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-r"},
	} {
		if err := st.Users().CreateUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	return meetingEnv{srv, st, cfg, mas, rooms}
}

type signOpts struct {
	secret, event, eventID string
	at                     time.Time
}

// call sends a signed request; zero options sign correctly for the method's event type.
func (e meetingEnv) call(t *testing.T, method, id, body string, o signOpts) *httptest.ResponseRecorder {
	t.Helper()
	if o.secret == "" {
		o.secret = calSecret
	}
	if o.event == "" {
		o.event = kycalendar.EventPut
		if method == http.MethodDelete {
			o.event = kycalendar.EventClose
		}
	}
	if o.eventID == "" {
		o.eventID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	if o.at.IsZero() {
		o.at = time.Now()
	}
	h, err := syncauth.Sign([]byte(o.secret), o.at, o.event, o.eventID, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/api/meetings/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Apply(req)
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	return w
}

func putBody(sub, title string, members ...string) string {
	b, _ := json.Marshal(map[string]any{"sub": sub, "issuer": calIssuer, "title": title, "member_subs": members})
	return string(b)
}

func wantCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != status || out["code"] != code {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, status, code)
	}
}

func (e meetingEnv) audits(t *testing.T) []string {
	t.Helper()
	rows, _, err := e.st.Audit().ListAuditRecords(context.Background(), 0, 100, "meeting.")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].Action+" "+rows[i].Resource+" "+rows[i].Details)
	}
	return out
}

func TestMeetingLifecycle(t *testing.T) {
	e := setupMeetings(t, true)
	w := e.call(t, "PUT", meetID, putBody("sub-a", "Weekly sync", "sub-e", "sub-b", "sub-x", "sub-a", "sub-e"), signOpts{eventID: "put-1"})
	out := decode[struct {
		MemberURL string   `json:"member_url"`
		Skipped   []string `json:"skipped_subs"`
	}](t, w)
	if out.MemberURL != "https://chat.example.com/#/room/"+meetRoomID || !slices.Equal(out.Skipped, []string{"sub-b", "sub-x"}) {
		t.Fatalf("created: %+v", out)
	}
	if got := e.rooms.take(); !slices.Equal(got, []string{"create " + aliceMX, "invite " + aliceMX, "invite " + erinMX}) {
		t.Fatalf("calls: %v", got)
	}

	// A replay is acknowledged and not acted on.
	w = e.call(t, "PUT", meetID, putBody("sub-a", "Weekly sync", "sub-e"), signOpts{eventID: "put-1"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"duplicate"`) || len(e.rooms.take()) != 0 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}

	// Somebody the organizer invited by hand is never removed; a listed member who left is
	// invited again.
	e.rooms.member[meetRoomID]["@notes:example.com"] = "join"
	e.rooms.member[meetRoomID][erinMX] = "leave"
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-a", "Weekly sync", "sub-e"), signOpts{}))
	if got := e.rooms.take(); !slices.Equal(got, []string{"invite " + erinMX}) {
		t.Fatalf("rejoin: %v", got)
	}

	// Title change and erin removed.
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-a", "Weekly sync (moved)"), signOpts{}))
	if got := e.rooms.take(); !slices.Equal(got, []string{"name", "kick " + erinMX}) || e.rooms.rooms[meetRoomID].Name != "Weekly sync (moved)" {
		t.Fatalf("reconcile: %v", got)
	}

	// A new organizer gets the organizer's level; alice, no longer listed, goes.
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-e", "Weekly sync (moved)"), signOpts{}))
	if got := e.rooms.take(); !slices.Equal(got, []string{"organizer " + erinMX, "kick " + aliceMX, "invite " + erinMX}) {
		t.Fatalf("organizer change: %v", got)
	}

	w = e.call(t, "DELETE", meetID, "", signOpts{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"closed"`) || !slices.Equal(e.rooms.changes, []string{"close " + meetRoomID}) {
		t.Fatalf("close: %d %s %v", w.Code, w.Body, e.rooms.changes)
	}
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-e", "Again"), signOpts{}), 409, "closed")
	for _, id := range []string{meetID, "mtg_unknown_0123456789"} {
		if w := e.call(t, "DELETE", id, "", signOpts{}); w.Code != 200 || !strings.Contains(w.Body.String(), `"closed"`) {
			t.Fatalf("close again %s: %d %s", id, w.Code, w.Body)
		}
	}
	if len(e.rooms.changes) != 1 {
		t.Fatalf("closed twice: %v", e.rooms.changes)
	}

	audits := strings.Join(e.audits(t), "\n")
	for _, want := range []string{
		`meeting.put ` + meetID + ` outcome="created" room="` + meetRoomID + `" invited="2" removed="0" skipped="2"`,
		`meeting.put ` + meetID + ` outcome="updated"`,
		`meeting.close ` + meetID + ` outcome="started" room="` + meetRoomID + `" delete_id=`,
		`meeting.put ` + meetID + ` outcome="refused: closed"`,
		`meeting.close ` + meetID + ` outcome="already_closed"`,
		`meeting.close mtg_unknown_0123456789 outcome="unknown"`,
	} {
		if !strings.Contains(audits, want) {
			t.Errorf("audit lacks %q:\n%s", want, audits)
		}
	}
	for _, secretish := range []string{"Weekly", "sub-a", "sub-e", calSecret} {
		if strings.Contains(audits, secretish) {
			t.Errorf("audit carries %q:\n%s", secretish, audits)
		}
	}
}

func TestMeetingRefusals(t *testing.T) {
	e := setupMeetings(t, true)
	ok := putBody("sub-a", "Sync")
	stale := time.Now().Add(-10 * time.Minute)
	for _, c := range []struct {
		name, method, id, body string
		o                      signOpts
		status                 int
		code                   string
	}{
		{"wrong secret", "PUT", meetID, ok, signOpts{secret: "some-other-secret-0123456789abcdef"}, 401, "bad_signature"},
		{"close event on PUT", "PUT", meetID, ok, signOpts{event: kycalendar.EventClose}, 401, "bad_signature"},
		{"put event on DELETE", "DELETE", meetID, "", signOpts{event: kycalendar.EventPut}, 401, "bad_signature"},
		{"directory event", "PUT", meetID, ok, signOpts{event: "user.updated"}, 401, "bad_signature"},
		{"stale", "PUT", meetID, ok, signOpts{at: stale}, 401, "bad_signature"},
		{"oversize", "PUT", meetID, putBody("sub-a", "Sync", slices.Repeat([]string{strings.Repeat("s", 255)}, 300)...), signOpts{}, 401, "bad_signature"},
		{"short id", "PUT", "mtg_short", ok, signOpts{}, 400, "bad_body"},
		{"id with dot", "PUT", "mtg_0123456789abc.ef", ok, signOpts{}, 400, "bad_body"},
		{"bad id on DELETE", "DELETE", "x", "", signOpts{}, 400, "bad_body"},
		{"not JSON", "PUT", meetID, "{", signOpts{}, 400, "bad_body"},
		{"empty title", "PUT", meetID, putBody("sub-a", ""), signOpts{}, 400, "bad_body"},
		{"control in title", "PUT", meetID, putBody("sub-a", "a\u0007b"), signOpts{}, 400, "bad_body"},
		{"long title", "PUT", meetID, putBody("sub-a", strings.Repeat("é", 128)), signOpts{}, 400, "bad_body"},
		{"101 members", "PUT", meetID, putBody("sub-a", "Sync", slices.Repeat([]string{"sub-e"}, 101)...), signOpts{}, 400, "bad_body"},
		{"guests", "PUT", meetID, `{"sub":"sub-a","issuer":"` + calIssuer + `","title":"Sync","guests":[{"key":"g1"}]}`, signOpts{}, 422, "guests_unsupported"},
		{"unknown organizer", "PUT", meetID, putBody("sub-x", "Sync"), signOpts{}, 404, "no_user"},
		{"administrator", "PUT", meetID, putBody("sub-r", "Sync"), signOpts{}, 404, "no_user"},
		{"locked chat account", "PUT", meetID, putBody("sub-b", "Sync"), signOpts{}, 404, "no_user"},
		{"other issuer", "PUT", meetID, strings.Replace(ok, calIssuer, "https://evil.example", 1), signOpts{}, 404, "no_user"},
	} {
		t.Run(c.name, func(t *testing.T) { wantCode(t, e.call(t, c.method, c.id, c.body, c.o), c.status, c.code) })
	}
	// Empty guests and windows are fine.
	decode[map[string]any](t, e.call(t, "PUT", meetID, `{"sub":"sub-a","issuer":"`+calIssuer+`","title":"Sync","guests":[],"windows":[{"start":"x"}]}`, signOpts{}))
	if audits := strings.Join(e.audits(t), "\n"); strings.Count(audits, "refused: bad_body") != 8 || !strings.Contains(audits, "refused: no_user") {
		t.Errorf("refusals audited:\n%s", audits)
	}
}

func TestMeetingsOffWhenUnpaired(t *testing.T) {
	e := setupMeetings(t, false)
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}), 404, "kycalendar_off")
	wantCode(t, e.call(t, "DELETE", meetID, "", signOpts{}), 404, "kycalendar_off")
}

// A failure after verification lets KyCalendar's retry of the same event through.
func TestMeetingRetryAfterOutage(t *testing.T) {
	e := setupMeetings(t, true)
	e.rooms.callErr = errors.New("synapse down")
	o := signOpts{eventID: "put-retry"}
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o), 503, "unavailable")
	e.rooms.callErr = nil
	e.rooms.take()
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o))
	if got := e.rooms.take(); !slices.Equal(got, []string{"create " + aliceMX, "invite " + aliceMX}) {
		t.Fatalf("retry: %v", got)
	}
	e.mas.usersErr = errors.New("mas down")
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}), 503, "unavailable")
}

func TestMeetingBannedMemberIsSkipped(t *testing.T) {
	e := setupMeetings(t, true)
	e.rooms.banned[erinMX] = true
	out := decode[struct {
		Skipped []string `json:"skipped_subs"`
	}](t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync", "sub-e"), signOpts{}))
	if !slices.Equal(out.Skipped, []string{"sub-e"}) {
		t.Fatalf("skipped: %v", out.Skipped)
	}
}

func TestMeetingRateLimit(t *testing.T) {
	e := setupMeetings(t, true)
	for i := range 120 {
		if w := e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}); w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	o := signOpts{eventID: "put-limited"}
	w := e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o)
	wantCode(t, w, 429, "rate_limited")
	if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After %q", ra)
	}
	// Not recorded as seen: the retry is limited again, not acknowledged as a duplicate.
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o), 429, "rate_limited")
	wantCode(t, e.call(t, "DELETE", meetID, "", signOpts{}), 429, "rate_limited")
	// Another organizer has their own budget.
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-e", "Sync"), signOpts{}))
}

// The replay guard is in the database: a restart does not forget an accepted event.
func TestMeetingReplaySurvivesRestart(t *testing.T) {
	e := setupMeetings(t, true)
	o := signOpts{eventID: "put-restart"}
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o))
	e.srv = api.NewServer(e.cfg, e.st)
	e.srv.SetMatrixAdmin(e.mas)
	w := e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), o)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("after restart: %d %s", w.Code, w.Body)
	}
}

// A room closed from the console's Rooms page closes the meeting.
func TestMeetingClosedFromConsole(t *testing.T) {
	e := setupMeetings(t, true)
	decode[map[string]any](t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}))
	e.rooms.blocked[meetRoomID] = true
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}), 409, "closed")
	wantCode(t, e.call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{}), 409, "closed")
}

func TestKyCalendarStatusNeverShowsTheSecret(t *testing.T) {
	e := setupMeetings(t, true)
	admin := loginAs(t, e.srv, e.st, "root", "admin")
	w := adminDo(t, e.srv, admin, "GET", "/api/admin/kycalendar", nil)
	got := decode[map[string]any](t, w)
	if got["connected"] != true || got["source"] != "environment" || got["url"] != "https://cal.example.com" || strings.Contains(w.Body.String(), calSecret) {
		t.Fatalf("status: %s", w.Body)
	}
	member := loginAs(t, e.srv, e.st, "member", "user")
	if w := adminDo(t, e.srv, member, "GET", "/api/admin/kycalendar", nil); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	off := setupMeetings(t, false)
	admin = loginAs(t, off.srv, off.st, "root", "admin")
	if got := decode[map[string]any](t, adminDo(t, off.srv, admin, "GET", "/api/admin/kycalendar", nil)); got["connected"] != false || got["reason"] != "pending" {
		t.Fatalf("unpaired: %v", got)
	}
}

// fakePeerDirectory is KyIdentity's /api/suite/peer for KyMessages' client.
type fakePeerDirectory struct {
	mu     sync.Mutex
	status int
	url    string
	secret []byte
}

func (d *fakePeerDirectory) set(status int, url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status, d.url = status, url
}

func TestKyCalendarPairsThroughKyIdentity(t *testing.T) {
	d := &fakePeerDirectory{status: 200, url: "https://cal.example.com", secret: []byte(strings.Repeat("k", 32))}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := r.BasicAuth(); !ok || id != "kymessage" || secret != "client-secret" || r.URL.Path != "/api/suite/peer" || r.URL.Query().Get("client") != "kycalendar" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		w.WriteHeader(d.status)
		switch d.status {
		case 200:
			_ = json.NewEncoder(w).Encode(map[string]string{"client": "kycalendar", "url": d.url, "secret": base64.StdEncoding.EncodeToString(d.secret)})
		case 404:
			_, _ = w.Write([]byte(`{"error":"no_peer"}`))
		}
	}))
	defer hs.Close()
	_, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.SSO.Enabled, cfg.SSO.KyIdentityIssuer, cfg.SSO.KyIdentityClientID, cfg.SSO.KyIdentitySecret = true, hs.URL, "kymessage", "client-secret"
	srv := api.NewServer(cfg, st)
	srv.SetMatrixAdmin(&fakeMAS{finished: map[string]bool{}})
	admin := loginAs(t, srv, st, "root", "admin")
	status := func() map[string]any {
		return decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/kycalendar", nil))
	}

	ctx := context.Background()
	if !api.ResolveKyCalendarForTest(srv, ctx) || status()["source"] != "kyidentity" {
		t.Fatalf("paired: %v", status())
	}
	if w := (meetingEnv{srv: srv}).call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{secret: string(d.secret)}); !strings.Contains(w.Body.String(), `"no_user"`) {
		t.Fatalf("pair secret refused: %d %s", w.Code, w.Body)
	}
	d.set(500, "")
	if api.ResolveKyCalendarForTest(srv, ctx) || status()["connected"] != true {
		t.Fatalf("an outage keeps the pair: %v", status())
	}
	d.set(200, "https://cal.example.com/calendar")
	if api.ResolveKyCalendarForTest(srv, ctx) || status()["reason"] != "invalid_peer" {
		t.Fatalf("a URL with a path: %v", status())
	}
	d.set(404, "")
	if api.ResolveKyCalendarForTest(srv, ctx) || status()["reason"] != "no_peer" {
		t.Fatalf("unpaired in KyIdentity: %v", status())
	}
	wantCode(t, (meetingEnv{srv: srv}).call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{secret: string(d.secret)}), 404, "kycalendar_off")
	d.set(401, "")
	if api.ResolveKyCalendarForTest(srv, ctx) || status()["reason"] != "unauthorized" {
		t.Fatalf("refused credentials: %v", status())
	}
}
