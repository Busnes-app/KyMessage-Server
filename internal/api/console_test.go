package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// MAS IDs are ULIDs: 26 Crockford base32 characters.
const (
	uAlice  = "01J9ZK8V6N3W4X5Y6Z7A8B9C0A"
	uBob    = "01J9ZK8V6N3W4X5Y6Z7A8B9C0B"
	uCarol  = "01J9ZK8V6N3W4X5Y6Z7A8B9C0C"
	uDave   = "01J9ZK8V6N3W4X5Y6Z7A8B9C0D"
	sOAuth  = "01J9ZK8V6N3W4X5Y6Z7A8B9C1A"
	sCompat = "01J9ZK8V6N3W4X5Y6Z7A8B9C1B"
	sNoUser = "01J9ZK8V6N3W4X5Y6Z7A8B9C1C"
)

type fakeMAS struct {
	mu        sync.Mutex
	users     []matrixsync.User
	usersErr  error
	sessions  map[string][]matrixsync.Session // by user ID; "" holds sessions without a user
	finished  map[string]bool
	finishErr error
	finishes  []string

	consoleErr  error
	consoleHang chan struct{}
	consoleRuns int
}

// AsConsole stands in for the console session: fn gets a fixed token unless consoleErr is set.
// consoleHang blocks it, ignoring ctx, as a stuck MAS would.
func (f *fakeMAS) AsConsole(ctx context.Context, fn func(context.Context, string) error) error {
	f.mu.Lock()
	f.consoleRuns++
	err, hang := f.consoleErr, f.consoleHang
	f.mu.Unlock()
	if hang != nil {
		<-hang
	}
	if err != nil {
		return err
	}
	return fn(ctx, "console-token")
}

func notFound(path string) error {
	return &matrixsync.StatusError{Method: "GET", Path: path, Status: http.StatusNotFound}
}

func (f *fakeMAS) Users(context.Context) ([]matrixsync.User, error) { return f.users, f.usersErr }

func (f *fakeMAS) User(_ context.Context, id string) (matrixsync.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return matrixsync.User{}, notFound("/api/admin/v1/users/" + id)
}

func (f *fakeMAS) Sessions(_ context.Context, userID string) ([]matrixsync.Session, error) {
	list, ok := f.sessions[userID]
	if !ok {
		return nil, notFound("/api/admin/v1/user-sessions")
	}
	return list, nil
}

func (f *fakeMAS) Session(_ context.Context, kind matrixsync.SessionKind, id string) (matrixsync.Session, error) {
	for _, list := range f.sessions {
		for _, s := range list {
			if s.Kind == kind && s.ID == id {
				return s, nil
			}
		}
	}
	return matrixsync.Session{}, notFound("/api/admin/v1/sessions/" + id)
}

func (f *fakeMAS) FinishSession(_ context.Context, kind matrixsync.SessionKind, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes = append(f.finishes, string(kind)+"/"+id)
	if f.finishErr != nil {
		return false, f.finishErr
	}
	already := f.finished[id]
	f.finished[id] = true
	return already, nil
}

func (f *fakeMAS) Version(context.Context) (string, error) { return health.Pins["mas"], nil }

// setupMatrixServer: four MAS users (active, locked, deactivated, unlinked) and a KyIdentity
// directory in which sub-a is active, sub-b disabled and sub-c unknown.
func setupMatrixServer(t *testing.T) (*api.Server, store.Store, *config.Config, *fakeMAS) {
	t.Helper()
	srv, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.SSO.KyIdentityIssuer = "https://id.example.com"
	f := &fakeMAS{
		users: []matrixsync.User{
			{ID: uAlice, Username: "alice", Subject: "sub-a"},
			{ID: uBob, Username: "bob", Subject: "sub-b", Locked: true},
			{ID: uCarol, Username: "carol", Subject: "sub-c", Deactivated: true},
			{ID: uDave, Username: "dave"},
		},
		sessions: map[string][]matrixsync.Session{
			uAlice: {
				{Kind: matrixsync.OAuth2Session, ID: sOAuth, UserID: uAlice, Device: "DEVA", Client: "Element", IP: "192.0.2.7", CreatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
				{Kind: matrixsync.CompatSession, ID: sCompat, UserID: uAlice, Device: "DEVB", Client: "bot"},
			},
			uDave: {},
			"":    {{Kind: matrixsync.OAuth2Session, ID: sNoUser}},
		},
		finished: map[string]bool{},
	}
	srv.SetMatrixAdmin(f)
	api.SetRoomAdminForTest(srv, newFakeRooms())
	for _, u := range []store.User{
		{ID: "usr_dir_a", Username: "alice-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-a"},
		{ID: "usr_dir_b", Username: "bob-ky", Role: "user", Status: "disabled", SSOProvider: "kyidentity", SSOSubject: "sub-b"},
	} {
		if err := st.Users().CreateUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	return srv, st, cfg, f
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type componentsBody struct {
	Matrix     bool
	Components []health.Component
}

func TestMatrixRoutesAre404WithoutMatrix(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/admin/matrix/users"},
		{"GET", "/api/admin/matrix/users/" + uAlice + "/sessions"},
		{"POST", "/api/admin/matrix/sessions/oauth2/" + sOAuth + "/finish"},
		{"GET", "/api/admin/matrix/rooms"},
		{"GET", "/api/admin/matrix/rooms/%21grp:example.com"},
		{"GET", "/api/admin/matrix/rooms/%21grp:example.com/delete-status"},
		{"POST", "/api/admin/matrix/rooms/%21grp:example.com/close"},
		{"POST", "/api/admin/matrix/rooms/%21grp:example.com/delete"},
		{"GET", "/api/admin/matrix/sync-status"},
	} {
		w := adminDo(t, srv, admin, rt.method, rt.path, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"matrix_disabled"`) {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	h := decode[componentsBody](t, adminDo(t, srv, admin, "GET", "/api/admin/health", nil))
	if h.Matrix || len(h.Components) != 2 || h.Components[0].Name != "kymessages" || h.Components[1].Name != "database" ||
		h.Components[0].Status != "up" || h.Components[1].Status != "up" {
		t.Fatalf("health without Matrix: %+v", h)
	}
}

func TestMatrixUsersStatusesSearchAndPaging(t *testing.T) {
	srv, st, _, _ := setupMatrixServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type page struct {
		Users []struct {
			ID, Username, MXID, Status, KyIdentity string
		}
		Total, Offset, Limit int
		DirectoryURL         string `json:"directory_url"`
	}
	get := func(q string) page {
		return decode[page](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/users"+q, nil))
	}
	all := get("")
	var rows []string
	for _, u := range all.Users {
		rows = append(rows, fmt.Sprintf("%s %s %s %s", u.MXID, u.Username, u.Status, u.KyIdentity))
	}
	want := []string{
		"@alice:example.com alice active active", "@bob:example.com bob locked disabled",
		"@carol:example.com carol deactivated unknown", "@dave:example.com dave not_linked ",
	}
	if strings.Join(rows, "|") != strings.Join(want, "|") || all.Total != 4 || all.Limit != 50 || all.DirectoryURL != "https://id.example.com" {
		t.Fatalf("all: %v total=%d limit=%d url=%q", rows, all.Total, all.Limit, all.DirectoryURL)
	}
	p := get("?search=A&limit=2&offset=1") // alice, carol, dave match "a"
	if p.Total != 3 || len(p.Users) != 2 || p.Users[0].Username != "carol" || p.Users[1].Username != "dave" {
		t.Fatalf("search page: %+v", p)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users?search=zzz", nil).Body.String(); !strings.Contains(body, `"users":[]`) {
		t.Errorf("empty page is not an empty array: %s", body)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?offset=-1", "?limit=x"} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

func TestMatrixUsersReportsMASFailure(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	f.usersErr = errors.New("MAS token: HTTP 401")
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/matrix/users", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "MAS token: HTTP 401") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestMatrixSessionsListed(t *testing.T) {
	srv, st, _, _ := setupMatrixServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type sessions struct {
		Sessions []struct {
			Kind, ID, Device, Client, IP string
			LastActiveAt                 *string `json:"last_active_at"`
		}
	}
	got := decode[sessions](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uAlice+"/sessions", nil))
	if len(got.Sessions) != 2 {
		t.Fatalf("%+v", got)
	}
	s := got.Sessions[0]
	if s.Kind != "oauth2" || s.ID != sOAuth || s.Device != "DEVA" || s.Client != "Element" || s.IP != "192.0.2.7" || s.LastActiveAt != nil {
		t.Errorf("%+v", s)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uDave+"/sessions", nil).Body.String(); !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("no sessions is not an empty array: %s", body)
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uBob+"/sessions", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown to MAS: %d", w.Code)
	}
	for _, id := range []string{"not-a-ulid", "..%2F..%2Fusers", strings.ToLower(uAlice)} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+id+"/sessions", nil); w.Code != http.StatusBadRequest {
			t.Errorf("id %q: %d", id, w.Code)
		}
	}
}

func TestMatrixSessionsClipLongDeviceAndClient(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	long := strings.Repeat("é", 300)
	f.sessions[uAlice] = []matrixsync.Session{{Kind: matrixsync.OAuth2Session, ID: sOAuth, UserID: uAlice, Device: long, Client: long}}
	var got struct {
		Sessions []struct{ Device, Client string }
	}
	got = decode[struct {
		Sessions []struct{ Device, Client string }
	}](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uAlice+"/sessions", nil))
	if len(got.Sessions) != 1 {
		t.Fatalf("%+v", got)
	}
	for _, v := range []string{got.Sessions[0].Device, got.Sessions[0].Client} {
		if len(v) > 200 || !utf8.ValidString(v) || v == "" {
			t.Errorf("not clipped to 200 valid bytes: %d bytes", len(v))
		}
	}
}

func staleAdmin(t *testing.T, st store.Store) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	createLocalUser(t, st, "usr_old", "old-admin", "admin", "OldAdminPass123!")
	user, err := st.Users().GetUserByID(ctx, "usr_old")
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(ctx, &store.Session{
		TokenHash: crypto.SHA256Hex([]byte(raw)), UserID: "usr_old",
		CreatedAt: time.Now().UTC().Add(-11 * time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, user.PasswordHash); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: raw}
}

func TestSessionFinishNeedsFreshAdminAndIsAudited(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	path := "/api/admin/matrix/sessions/oauth2/" + sOAuth + "/finish"
	w := adminDo(t, srv, staleAdmin(t, st), "POST", path, nil)
	body := w.Body.String()
	if w.Code != http.StatusForbidden || !strings.Contains(body, `"code":"reauthentication_required"`) ||
		!strings.Contains(body, "Confirm it's you") || strings.Contains(body, "backup") || strings.Contains(body, "reauth_url") {
		t.Fatalf("stale local admin: %d %s", w.Code, body)
	}
	if len(f.finishes) != 0 {
		t.Fatalf("a stale session reached MAS: %v", f.finishes)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, want := range []string{"ended", "already_ended"} {
		w := adminDo(t, srv, admin, "POST", path, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"`+want+`"`) || !strings.Contains(w.Body.String(), `"mxid":"@alice:example.com"`) {
			t.Fatalf("want %s: %d %s", want, w.Code, w.Body.String())
		}
	}
	f.finishErr = errors.New("MAS POST /api/admin/v1/oauth2-sessions/x/finish: HTTP 500")
	if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("MAS failure: %d %s", w.Code, w.Body.String())
	}
	rows := auditRows(t, st, "matrix.session_end") // newest first
	if len(rows) != 3 {
		t.Fatalf("%d session_end rows", len(rows))
	}
	for i, outcome := range []string{`outcome="error: MAS POST`, `outcome="already_ended"`, `outcome="ended"`} {
		r := rows[i]
		if r.Resource != "@alice:example.com" || r.UserID != "usr_root" || r.IPAddress == "" ||
			!strings.Contains(r.Details, `session="`+sOAuth+`"`) || !strings.Contains(r.Details, `kind="oauth2"`) || !strings.Contains(r.Details, outcome) {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	// Refused before MAS: unknown kind, malformed ID.
	for _, p := range []string{"/api/admin/matrix/sessions/device/" + sOAuth + "/finish", "/api/admin/matrix/sessions/oauth2/nope/finish"} {
		if w := adminDo(t, srv, admin, "POST", p, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// MAS's own client sessions belong to no user: refused, audited, never finished.
	if w := adminDo(t, srv, admin, "POST", "/api/admin/matrix/sessions/oauth2/"+sNoUser+"/finish", nil); w.Code != http.StatusBadRequest {
		t.Errorf("no-user session: %d", w.Code)
	}
	rows = auditRows(t, st, "matrix.session_end")
	if len(rows) != 4 || !strings.Contains(rows[0].Details, `outcome="refused: no user"`) || len(f.finishes) != 3 {
		t.Errorf("rows %d newest %+v finishes %v", len(rows), rows[0], f.finishes)
	}
}

func TestHealthWithMatrixReportsEveryComponent(t *testing.T) {
	srv, st, cfg, _ := setupMatrixServer(t)
	mux := func(routes map[string]string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b, ok := routes[r.URL.Path]; ok {
				fmt.Fprint(w, b)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	api.SetHealthTargetsForTest(srv, health.Targets{
		Synapse: mux(map[string]string{"/health": "OK", "/_synapse/admin/v1/server_version": `{"server_version":"` + health.Pins["synapse"] + `"}`}),
		MAS:     mux(map[string]string{"/.well-known/openid-configuration": "{}"}),
		Element: mux(map[string]string{"/version": "1.0.0"}),
	})
	cfg.Matrix.DBHost = "postgres.invalid"
	cfg.Matrix.BackupDBPassword = "pw-hunter2"
	cfg.Matrix.AdminSecret = "mas-secret-xyz"
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/health", nil)
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "pw-hunter2") || strings.Contains(w.Body.String(), "mas-secret-xyz") {
		t.Fatalf("headers %v body %s", w.Header(), w.Body.String())
	}
	h := decode[componentsBody](t, w)
	by := map[string]health.Component{}
	var names []string
	for _, c := range h.Components {
		by[c.Name] = c
		names = append(names, c.Name)
	}
	if !h.Matrix || strings.Join(names, ",") != "kymessages,database,synapse,mas,element,postgres,synapse-admin" {
		t.Fatalf("components %v", names)
	}
	if c := by["synapse"]; c.Status != "up" || c.Version != health.Pins["synapse"] || c.Mismatch || c.Source == "" {
		t.Errorf("synapse %+v", c)
	}
	if c := by["mas"]; c.Status != "up" || c.Version != health.Pins["mas"] || c.Mismatch {
		t.Errorf("mas %+v", c)
	}
	if c := by["synapse-admin"]; c.Status != "up" || c.Version != "" || c.Pinned != "" || c.Error != "" {
		t.Errorf("synapse-admin %+v", c)
	}
	if c := by["element"]; c.Status != "up" || !c.Mismatch || c.Pinned != health.Pins["element"] {
		t.Errorf("element %+v", c)
	}
	if c := by["postgres"]; c.Status != "down" || c.Error == "" {
		t.Errorf("postgres %+v", c)
	}
}

func TestAuditListsNewestFirstByKind(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin") // writes auth.login
	ctx := context.Background()
	for _, r := range []store.AuditRecord{
		{UserID: "usr_root", Action: "admin.backup_run", Resource: "cap-1", Details: `outcome="success" trigger="admin"`, IPAddress: "192.0.2.9"},
		{Action: "matrix.lock", Resource: "@bob:example.com", Details: `subject="s" reason="r" outcome="ok"`},
		{UserID: "usr_gone", Action: "scim.user.create", Resource: "erin"},
	} {
		if err := st.Audit().LogAudit(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	type rec struct{ Action, Actor, Target, Outcome, Details, IP string }
	type page struct {
		Records []rec
		Total   int
	}
	get := func(q string) page { return decode[page](t, adminDo(t, srv, admin, "GET", "/api/admin/audit"+q, nil)) }
	all := get("")
	if len(all.Records) < 4 || all.Records[0].Action != "scim.user.create" || all.Records[1].Action != "matrix.lock" || all.Records[2].Action != "admin.backup_run" {
		t.Fatalf("order: %+v", all.Records)
	}
	if r := all.Records[0]; r.Actor != "scim" || r.Target != "erin" || r.Outcome != "" {
		t.Errorf("scim row: %+v", r)
	}
	if r := all.Records[1]; r.Actor != "system" || r.Outcome != "ok" {
		t.Errorf("sweep row: %+v", r)
	}
	if r := all.Records[2]; r.Actor != "root" || r.Outcome != "success" || r.Target != "cap-1" || r.IP != "192.0.2.9" || !strings.Contains(r.Details, `trigger="admin"`) {
		t.Errorf("backup row: %+v", r)
	}
	if b := get("?kind=backup"); b.Total != 1 || b.Records[0].Action != "admin.backup_run" {
		t.Errorf("backup kind: %+v", b)
	}
	for _, r := range get("?kind=auth").Records {
		if !strings.HasPrefix(r.Action, "auth.") && !strings.HasPrefix(r.Action, "device.") {
			t.Errorf("auth kind returned %s", r.Action)
		}
	}
	if p := get("?limit=1&offset=1"); len(p.Records) != 1 || p.Records[0] != all.Records[1] || p.Total != all.Total {
		t.Errorf("page: %+v", p)
	}
	for _, bad := range []string{"?kind=", "?kind=messaging", "?limit=500", "?offset=x"} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/audit"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

func TestSessionFinishClipsLongErrorsInAudit(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	f.finishErr = errors.New("MAS POST https://mas.invalid/" + strings.Repeat("x", 400))
	adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "POST", "/api/admin/matrix/sessions/oauth2/"+sOAuth+"/finish", nil)
	r := auditRows(t, st, "matrix.session_end")[0]
	if !strings.HasPrefix(r.Details, `outcome="error: MAS POST`) || !strings.Contains(r.Details, `session="`+sOAuth+`"`) {
		t.Fatalf("details %q", r.Details)
	}
	if got := api.DetailOutcomeForTest(r.Details); !strings.HasPrefix(got, "error: MAS POST") || strings.Contains(got, `"`) {
		t.Errorf("outcome %q", got)
	}
}

func TestAuditOutcomeIgnoresSpoofs(t *testing.T) {
	for details, want := range map[string]string{
		`trigger="admin" outcome="success"`:                 "success",
		`remote="boom outcome=ok" outcome="failed"`:         "failed",
		`remote="a \" outcome=ok"`:                          "",
		`subject="s" reason="r"`:                            "",
		`error=recovery pairing: HTTP 400: outcome=success`: "",
		`bad.outcome=delivered`:                             "",
		`outcome="error: abc`:                               "",
		`outcome="success" trailing outcome=failure`:        "",
		`outcome="success"  trigger="x"`:                    "",
	} {
		if got := api.DetailOutcomeForTest(details); got != want {
			t.Errorf("%q: got %q want %q", details, got, want)
		}
	}
}

func TestSessionFinishMultibyteErrorKeepsQuotes(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	f.finishErr = errors.New(strings.Repeat("\U0001F4A5", 120))
	adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "POST", "/api/admin/matrix/sessions/oauth2/"+sOAuth+"/finish", nil)
	r := auditRows(t, st, "matrix.session_end")[0]
	if !strings.HasSuffix(r.Details, `kind="oauth2"`) || !strings.HasPrefix(api.DetailOutcomeForTest(r.Details), "error: ") {
		t.Fatalf("details %q", r.Details)
	}
}

// One oversized username or resource must not break the page: the web rejects the whole page
// when a field exceeds its bound (actor 255, target 1024).
func TestAuditClipsLongActorAndTarget(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	ctx := context.Background()
	// 100 characters (within Postgres's varchar(128) username) but 400 bytes, past the 200-byte clip.
	long := strings.Repeat("\U0001F600", 100)
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_long", Username: long, Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.AuditRecord{
		{UserID: "usr_long", Action: "auth.login", Resource: long},
		{UserID: "usr_long", Action: "scim.user.create", Resource: long},
	} {
		if err := st.Audit().LogAudit(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	type rec struct{ Action, Actor, Target string }
	recs := decode[struct{ Records []rec }](t, adminDo(t, srv, admin, "GET", "/api/admin/audit?limit=2", nil)).Records
	if len(recs) != 2 {
		t.Fatalf("records %+v", recs)
	}
	for _, r := range recs {
		if len(r.Target) > 200 || !utf8.ValidString(r.Target) || !strings.HasPrefix(long, r.Target) || r.Target == "" {
			t.Errorf("%s target %d bytes", r.Action, len(r.Target))
		}
	}
	if a := recs[0].Actor; recs[0].Action != "scim.user.create" || a != "scim" {
		t.Errorf("scim actor %q", a)
	}
	if a := recs[1].Actor; len(a) > 200 || !utf8.ValidString(a) || !strings.HasPrefix(long, a) || a == "" {
		t.Errorf("long actor %d bytes", len(a))
	}
}

// Details, outcome and IP are bounded too (web: details 4096, outcome 1024, ip 64).
func TestAuditClipsLongDetailsOutcomeAndIP(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	// 64 characters fit Postgres's varchar(64) but are 256 bytes.
	ip := strings.Repeat("\U0001F600", 64)
	outcome := strings.Repeat("\U0001F600", 1500)
	details := `outcome="` + outcome + `"`
	if err := st.Audit().LogAudit(context.Background(), &store.AuditRecord{Action: "matrix.lock", Details: details, IPAddress: ip}); err != nil {
		t.Fatal(err)
	}
	type rec struct{ Details, Outcome, IP string }
	recs := decode[struct{ Records []rec }](t, adminDo(t, srv, admin, "GET", "/api/admin/audit?limit=1", nil)).Records
	if len(recs) != 1 {
		t.Fatalf("records %+v", recs)
	}
	for _, f := range []struct {
		name, got, full string
		max             int
	}{{"details", recs[0].Details, details, 4096}, {"outcome", recs[0].Outcome, outcome, 1024}, {"ip", recs[0].IP, ip, 64}} {
		if len(f.got) > f.max || f.got == "" || !utf8.ValidString(f.got) || !strings.HasPrefix(f.full, f.got) {
			t.Errorf("%s %d bytes", f.name, len(f.got))
		}
	}
}
