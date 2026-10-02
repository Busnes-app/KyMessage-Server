package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const consoleID = "01J9ZK8V6N3W4X5Y6Z7A8B9C9C"

// consoleFake is MAS's admin API as the console account sees it. user is nil until created.
type consoleFake struct {
	mu           sync.Mutex
	user         map[string]any
	hideOnce     bool // the first by-username read misses a user that exists (a create race)
	linked       bool
	created      []string
	setAdmin     []string
	minted       []map[string]any
	revoked      []string
	revokeStatus int    // non-zero: revoke answers this
	onMint       func() // runs while MAS handles the mint, before it answers
	srv          *httptest.Server
}

func newConsoleFake(t *testing.T) *consoleFake {
	t.Helper()
	f := &consoleFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *consoleFake) client() *Client { return NewClient(f.srv.URL, "cid", "secret-value") }

func (f *consoleFake) handle(w http.ResponseWriter, r *http.Request) {
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
	body, _ := io.ReadAll(r.Body)
	userDoc := func() string {
		a, _ := json.Marshal(f.user)
		return fmt.Sprintf(`{"data":{"type":"user","id":%q,"attributes":%s}}`, consoleID, a)
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/admin/v1/users/by-username/kymessages-console":
		if f.user == nil || f.hideOnce {
			f.hideOnce = false
			http.Error(w, `{"errors":[{"title":"User not found"}]}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodPost && p == "/api/admin/v1/users":
		f.created = append(f.created, string(body))
		if f.user != nil {
			http.Error(w, `{"errors":[{"title":"User already exists"}]}`, http.StatusConflict)
			return
		}
		f.user = map[string]any{"username": "kymessages-console", "admin": false, "locked_at": nil, "deactivated_at": nil}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodGet && p == "/api/admin/v1/upstream-oauth-links":
		if r.URL.Query().Get("filter[user]") != consoleID {
			http.Error(w, "bad filter", http.StatusBadRequest)
			return
		}
		data := ""
		if f.linked {
			data = `{"type":"upstream-oauth-link","id":"L1","attributes":{"subject":"s","user_id":"` + consoleID + `"}}`
		}
		fmt.Fprintf(w, `{"data":[%s],"links":{}}`, data)
	case r.Method == http.MethodPost && p == "/api/admin/v1/users/"+consoleID+"/set-admin":
		f.setAdmin = append(f.setAdmin, string(body))
		f.user["admin"] = true
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodPost && p == "/api/admin/v1/personal-sessions":
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.minted = append(f.minted, m)
		if f.onMint != nil {
			f.onMint()
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"data":{"type":"personal-session","id":"PS%d","attributes":{"access_token":"mpt_secret%d"}}}`, len(f.minted), len(f.minted))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/api/admin/v1/personal-sessions/") && strings.HasSuffix(p, "/revoke"):
		f.revoked = append(f.revoked, strings.TrimSuffix(strings.TrimPrefix(p, "/api/admin/v1/personal-sessions/"), "/revoke"))
		if f.revokeStatus != 0 {
			http.Error(w, `{"errors":[{"title":"no"}]}`, f.revokeStatus)
			return
		}
		fmt.Fprint(w, `{"data":{"type":"personal-session","id":"PS1","attributes":{}}}`)
	default:
		http.NotFound(w, r)
	}
}

// The console account never gets MAS admin: personal sessions do not need it, and it would let
// an interactive login as the account request urn:mas:admin.
func TestEnsureConsoleUserCreatesOnceWithoutAdmin(t *testing.T) {
	f := newConsoleFake(t)
	c := f.client()
	for range 2 {
		id, err := c.EnsureConsoleUser(context.Background())
		if err != nil || id != consoleID {
			t.Fatalf("ensure: %q %v", id, err)
		}
	}
	if !reflect.DeepEqual(f.created, []string{`{"username":"kymessages-console"}`}) {
		t.Errorf("created %v", f.created)
	}
	if len(f.setAdmin) != 0 {
		t.Errorf("set-admin %v", f.setAdmin)
	}
}

func TestEnsureConsoleUserSurvivesACreateRace(t *testing.T) {
	f := newConsoleFake(t)
	f.user = map[string]any{"username": "kymessages-console", "admin": false, "locked_at": nil, "deactivated_at": nil}
	f.hideOnce = true
	id, err := f.client().EnsureConsoleUser(context.Background())
	if err != nil || id != consoleID || len(f.created) != 1 || len(f.setAdmin) != 0 {
		t.Fatalf("id %q err %v created %v set-admin %v", id, err, f.created, f.setAdmin)
	}
}

// A locked, deactivated, linked or MAS-admin account is refused, and no session is minted for
// it: a linked one is a person, never the console; admin is a privilege it must not hold.
func TestEnsureConsoleUserRefusesBrokenAccounts(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*consoleFake)
		want string
	}{
		"locked":      {func(f *consoleFake) { f.user["locked_at"] = "2026-10-02T10:00:00Z" }, "console account is locked in MAS"},
		"deactivated": {func(f *consoleFake) { f.user["deactivated_at"] = "2026-10-02T10:00:00Z" }, "console account is deactivated in MAS"},
		"linked":      {func(f *consoleFake) { f.linked = true }, "console account is linked to an upstream identity"},
		"admin":       {func(f *consoleFake) { f.user["admin"] = true }, "console account has MAS admin"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newConsoleFake(t)
			f.user = map[string]any{"username": "kymessages-console", "admin": false, "locked_at": nil, "deactivated_at": nil}
			tc.edit(f)
			called := false
			err := f.client().AsConsole(context.Background(), func(context.Context, string) error { called = true; return nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) || called || len(f.minted) != 0 || len(f.setAdmin) != 0 {
				t.Fatalf("err %v called %v minted %v set-admin %v", err, called, f.minted, f.setAdmin)
			}
		})
	}
}

func TestAsConsoleMintsExactScopeAndRevokes(t *testing.T) {
	f := newConsoleFake(t)
	var got string
	if err := f.client().AsConsole(context.Background(), func(_ context.Context, tok string) error { got = tok; return nil }); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"actor_user_id": consoleID, "human_name": "KyMessages console",
		"scope": "urn:matrix:client:api:* urn:synapse:admin:*", "expires_in": float64(300)}
	if got != "mpt_secret1" || len(f.minted) != 1 || !reflect.DeepEqual(f.minted[0], want) {
		t.Fatalf("token %q minted %v", got, f.minted)
	}
	if !reflect.DeepEqual(f.revoked, []string{"PS1"}) {
		t.Errorf("revoked %v", f.revoked)
	}
}

func TestAsConsoleRevokesWhenTheCallFailsOrIsCancelled(t *testing.T) {
	f := newConsoleFake(t)
	c := f.client()
	boom := errors.New("synapse said no")
	if err := c.AsConsole(context.Background(), func(context.Context, string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error not returned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := c.AsConsole(ctx, func(ctx context.Context, _ string) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if !reflect.DeepEqual(f.revoked, []string{"PS1", "PS2"}) {
		t.Fatalf("revoked %v", f.revoked)
	}
}

// A mint whose request is cancelled while MAS creates the session is still revoked: MAS may
// commit it, and only its ID lets us end it early.
func TestAsConsoleRevokesASessionMintedAsTheRequestDies(t *testing.T) {
	f := newConsoleFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.onMint = cancel
	called := false
	err := f.client().AsConsole(ctx, func(ctx context.Context, _ string) error { called = true; return ctx.Err() })
	if !errors.Is(err, context.Canceled) || !called || !reflect.DeepEqual(f.revoked, []string{"PS1"}) {
		t.Fatalf("err %v called %v revoked %v", err, called, f.revoked)
	}
}

// A refused revoke does not turn a done action into a failure; the session expires on its
// own. 409 means it was already revoked. Neither the token nor the secret reaches the log.
func TestAsConsoleRevokeFailureIsLoggedNotReturned(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, status := range []int{http.StatusConflict, http.StatusInternalServerError} {
		f := newConsoleFake(t)
		f.revokeStatus = status
		if err := f.client().AsConsole(context.Background(), func(context.Context, string) error { return nil }); err != nil {
			t.Errorf("revoke %d: %v", status, err)
		}
	}
	out := buf.String()
	if strings.Count(out, "not revoked") != 1 || strings.Contains(out, "mpt_secret") || strings.Contains(out, "secret-value") {
		t.Fatalf("log %q", out)
	}
}

func TestPlanExemptsOnlyTheUnlinkedConsoleAccount(t *testing.T) {
	got := Plan([]User{
		{ID: "c", Username: ConsoleUsername},                   // exempt
		{ID: "x1", Username: ConsoleUsername + "2"},            // lock: not the exact name
		{ID: "x2", Username: "kymessages"},                     // lock
		{ID: "x3", Username: ConsoleUsername, Subject: "i"},    // lock: linked, so a person
		{ID: "x4", Username: ConsoleUsername, Ambiguous: true}, // lock: linked twice
		{ID: "x5", Username: "Kymessages-console"},             // lock: case differs
		{ID: "x6", Username: "kym\u0435ssages-console"},        // lock: Cyrillic e
		{ID: "x7", Username: "kymessages\u2010console"},        // lock: Unicode hyphen
	}, map[string]string{"i": "inactive"})
	var locked []string
	for _, a := range got {
		if a.Kind != Lock {
			t.Fatalf("unexpected %+v", a)
		}
		locked = append(locked, a.User.ID)
	}
	if strings.Join(locked, ",") != "x1,x2,x3,x4,x5,x6,x7" {
		t.Fatalf("locked %v", locked)
	}
}
