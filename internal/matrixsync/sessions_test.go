package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// sessionFake is MAS's admin API for user U1 with one active session of each kind, plus N1,
// an admin-client session that belongs to no user.
type sessionFake struct {
	mu       sync.Mutex
	finished map[string]bool
	finishes []string
	srv      *httptest.Server
}

func newSessionFake(t *testing.T) *sessionFake {
	t.Helper()
	f := &sessionFake{finished: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sessionFake) client() *Client { return NewClient(f.srv.URL, "cid", "secret-value") }

var fakeSessions = map[string]struct{ path, attrs string }{
	"B1": {"user-sessions", `{"created_at":"2026-10-01T10:00:00Z","finished_at":null,"user_id":"U1","user_agent":"Firefox","last_active_at":"2026-10-02T09:00:00Z","last_active_ip":"192.0.2.7"}`},
	"O1": {"oauth2-sessions", `{"created_at":"2026-10-01T10:01:00Z","finished_at":null,"user_id":"U1","user_session_id":"B1","client_id":"C1","scope":"openid urn:matrix:client:api:* urn:matrix:client:device:DEVOAUTH","user_agent":"Element","last_active_at":null,"last_active_ip":null,"human_name":"Element on Firefox"}`},
	"C1": {"compat-sessions", `{"user_id":"U1","device_id":"DEVCOMPAT","user_session_id":null,"redirect_uri":null,"created_at":"2026-10-01T10:02:00Z","user_agent":"legacy-bot/1","last_active_at":null,"last_active_ip":"198.51.100.4","finished_at":null,"human_name":null}`},
	"N1": {"oauth2-sessions", `{"created_at":"2026-10-01T10:03:00Z","finished_at":null,"user_id":null,"client_id":"C2","scope":"urn:mas:admin","user_agent":null,"last_active_at":null,"last_active_ip":null,"human_name":null}`},
}

func (f *sessionFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/oauth2/token" {
		fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer","expires_in":300}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "bad bearer", http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/admin/v1/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	attrs := func(id string) string {
		a := fakeSessions[id].attrs
		if f.finished[id] {
			a = strings.Replace(a, `"finished_at":null`, `"finished_at":"2026-10-02T10:00:00Z"`, 1)
		}
		return a
	}
	parts := strings.Split(rest, "/")
	switch {
	case rest == "version":
		fmt.Fprint(w, `{"version":"v1.26.0"}`)
	case rest == "users/U1":
		fmt.Fprint(w, `{"data":{"type":"user","id":"U1","attributes":{"username":"alice","locked_at":null,"deactivated_at":null}}}`)
	case len(parts) == 1:
		q := r.URL.Query()
		if q.Get("filter[user]") != "U1" || q.Get("filter[status]") != "active" || q.Get("page[first]") != "100" {
			http.Error(w, "bad query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		var data []string
		for _, id := range []string{"B1", "O1", "C1", "N1"} {
			s := fakeSessions[id]
			if s.path == parts[0] && !f.finished[id] && strings.Contains(s.attrs, `"user_id":"U1"`) {
				data = append(data, fmt.Sprintf(`{"type":"session","id":%q,"attributes":%s}`, id, attrs(id)))
			}
		}
		fmt.Fprintf(w, `{"data":[%s],"links":{}}`, strings.Join(data, ","))
	default:
		s, known := fakeSessions[parts[1]]
		if !known || s.path != parts[0] {
			http.Error(w, `{"errors":[{"title":"not found"}]}`, http.StatusNotFound)
			return
		}
		if len(parts) == 3 && parts[2] == "finish" && r.Method == http.MethodPost {
			f.finishes = append(f.finishes, parts[0]+"/"+parts[1])
			if f.finished[parts[1]] {
				http.Error(w, `{"errors":[{"title":"session is already finished"}]}`, http.StatusBadRequest)
				return
			}
			f.finished[parts[1]] = true
		}
		fmt.Fprintf(w, `{"data":{"type":"session","id":%q,"attributes":%s}}`, parts[1], attrs(parts[1]))
	}
}

func TestSessionsListsEveryKindForTheUser(t *testing.T) {
	got, err := newSessionFake(t).client().Sessions(context.Background(), "U1")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind                         SessionKind
		id, user, device, client, ip string
	}
	var rows []row
	for _, s := range got {
		rows = append(rows, row{s.Kind, s.ID, s.UserID, s.Device, s.Client, s.IP})
	}
	want := []row{
		{BrowserSession, "B1", "U1", "", "Firefox", "192.0.2.7"},
		{OAuth2Session, "O1", "U1", "DEVOAUTH", "Element on Firefox", ""},
		{CompatSession, "C1", "U1", "DEVCOMPAT", "legacy-bot/1", "198.51.100.4"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %+v\nwant %+v", rows, want)
	}
	if got[0].LastActiveAt == nil || !got[0].LastActiveAt.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) || got[1].LastActiveAt != nil {
		t.Fatalf("last active: %v, %v", got[0].LastActiveAt, got[1].LastActiveAt)
	}
}

// Ending an ended session is success: MAS answers 400 and the read-back shows it finished.
func TestFinishSessionIsIdempotent(t *testing.T) {
	f := newSessionFake(t)
	c := f.client()
	for i, want := range []bool{false, true} {
		already, err := c.FinishSession(context.Background(), OAuth2Session, "O1")
		if err != nil || already != want {
			t.Fatalf("finish %d: already=%v err=%v", i, already, err)
		}
	}
	if !reflect.DeepEqual(f.finishes, []string{"oauth2-sessions/O1", "oauth2-sessions/O1"}) {
		t.Fatalf("finishes %v", f.finishes)
	}
}

func TestFinishSessionFailures(t *testing.T) {
	c := newSessionFake(t).client()
	// MAS has no compat session O1: a 404 is an error, never "already ended".
	_, err := c.FinishSession(context.Background(), CompatSession, "O1")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("wrong kind: %v", err)
	}
	if _, err := c.FinishSession(context.Background(), SessionKind("device"), "O1"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestParseSessionKind(t *testing.T) {
	for _, k := range []string{"oauth2", "compat", "browser"} {
		if got, ok := ParseSessionKind(k); !ok || string(got) != k {
			t.Errorf("%q refused", k)
		}
	}
	for _, k := range []string{"", "user-sessions", "OAUTH2", "oauth2-sessions"} {
		if _, ok := ParseSessionKind(k); ok {
			t.Errorf("%q accepted", k)
		}
	}
}

func TestSessionUserAndVersion(t *testing.T) {
	c := newSessionFake(t).client()
	ctx := context.Background()
	s, err := c.Session(ctx, OAuth2Session, "N1")
	if err != nil || s.UserID != "" || s.ID != "N1" {
		t.Fatalf("session N1: %+v %v", s, err)
	}
	u, err := c.User(ctx, "U1")
	if err != nil || u.Username != "alice" || u.Locked || u.Deactivated {
		t.Fatalf("user U1: %+v %v", u, err)
	}
	v, err := c.Version(ctx)
	if err != nil || v != "1.26.0" {
		t.Fatalf("version %q %v", v, err)
	}
}
