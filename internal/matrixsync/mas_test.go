package matrixsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type fakeServer struct {
	mu        sync.Mutex
	tokens    int
	reject    int // 401 this many Bearer-authenticated calls
	providers int
	nextLink  string // replaces page 1's links.next of the links list
	dupLink   bool   // page 2 also links U1 to a second subject
	posts     []string
	bodies    []string
	requested []string
	srv       *httptest.Server
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{providers: 1}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) client() *Client { return NewClient(f.srv.URL, "cid", "secret-value") }

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/oauth2/token" {
		id, sec, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || id != "cid" || sec != "secret-value" || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != "urn:mas:admin" {
			http.Error(w, "bad token request", 400)
			return
		}
		f.tokens++
		fmt.Fprintf(w, `{"access_token":"tok%d","token_type":"Bearer","expires_in":300}`, f.tokens)
		return
	}
	f.requested = append(f.requested, r.URL.Path)
	if r.Header.Get("Authorization") != fmt.Sprintf("Bearer tok%d", f.tokens) {
		http.Error(w, "bad bearer", 401)
		return
	}
	if f.reject > 0 {
		f.reject--
		http.Error(w, "expired", 401)
		return
	}
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/api/admin/v1/upstream-oauth-providers":
		switch f.providers {
		case 0:
			fmt.Fprint(w, `{"data":[],"links":{}}`)
		case 1:
			fmt.Fprint(w, `{"data":[{"type":"upstream-oauth-provider","id":"PROV"}],"links":{}}`)
		default:
			fmt.Fprint(w, `{"data":[{"type":"upstream-oauth-provider","id":"PROV"},{"type":"upstream-oauth-provider","id":"P2"}],"links":{}}`)
		}
	case r.URL.Path == "/api/admin/v1/upstream-oauth-links":
		if q.Get("filter[provider]") != "PROV" || q.Get("page[first]") != "100" {
			http.Error(w, "bad query", 400)
			return
		}
		if q.Get("page[after]") == "" {
			next := f.nextLink
			if next == "" {
				next = "/api/admin/v1/upstream-oauth-links?filter[provider]=PROV&page[first]=100&page[after]=L1"
			}
			fmt.Fprintf(w, `{"data":[{"type":"upstream-oauth-link","id":"L1","attributes":{"provider_id":"PROV","subject":"sub-a","user_id":"U1"}}],"links":{"next":%q}}`, next)
			return
		}
		dup := ""
		if f.dupLink {
			dup = `,{"type":"upstream-oauth-link","id":"L3","attributes":{"provider_id":"PROV","subject":"sub-b","user_id":"U1"}}`
		}
		fmt.Fprintf(w, `{"data":[{"type":"upstream-oauth-link","id":"L2","attributes":{"provider_id":"PROV","subject":"sub-x","user_id":null}}%s],"links":{}}`, dup)
	case r.URL.Path == "/api/admin/v1/users":
		if q.Get("page[first]") != "100" {
			http.Error(w, "bad query", 400)
			return
		}
		fmt.Fprint(w, `{"data":[
{"type":"user","id":"U1","attributes":{"username":"alice","locked_at":null,"deactivated_at":null}},
{"type":"user","id":"U2","attributes":{"username":"bob","locked_at":"2026-10-01T00:00:00Z","deactivated_at":null}}],"links":{}}`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/admin/v1/users/"):
		b, _ := io.ReadAll(r.Body)
		f.posts = append(f.posts, strings.TrimPrefix(r.URL.Path, "/api/admin/v1/users/"))
		f.bodies = append(f.bodies, string(b))
		fmt.Fprint(w, `{"data":{"type":"user","id":"x","attributes":{}}}`)
	default:
		http.NotFound(w, r)
	}
}

func TestClientListsUsersWithSubjects(t *testing.T) {
	f := newFake(t)
	got, err := f.client().Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []User{{ID: "U1", Username: "alice", Subject: "sub-a"}, {ID: "U2", Username: "bob", Locked: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if f.tokens != 1 {
		t.Fatalf("token fetches = %d, want 1", f.tokens)
	}
}

// A user linked to two subjects has no single directory record to follow: Plan locks it.
func TestClientFlagsUserWithSeveralSubjects(t *testing.T) {
	f := newFake(t)
	f.dupLink = true
	got, err := f.client().Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Ambiguous || got[0].ID != "U1" || got[1].Ambiguous {
		t.Fatalf("got %+v", got)
	}
}

// Docker can inject HTTP_PROXY; the admin credentials and token must never go to a proxy.
// Proxy settings are read once per process, so the check runs in a fresh child.
func TestClientIgnoresProxyEnvironment(t *testing.T) {
	if os.Getenv("MATRIXSYNC_PROXY_CHILD") == "1" {
		_, err := NewClient("http://mas-admin.invalid:8081", "cid", "secret-value").Users(context.Background())
		if err == nil {
			t.Fatal("unresolvable admin host answered")
		}
		// Control: the default client does use the proxy here.
		if resp, err := http.Get("http://control.invalid/"); err == nil {
			resp.Body.Close()
		}
		return
	}
	var mu sync.Mutex
	var hosts []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		http.Error(w, "proxy", http.StatusBadGateway)
	}))
	defer proxy.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClientIgnoresProxyEnvironment$")
	cmd.Env = append(os.Environ(), "MATRIXSYNC_PROXY_CHILD=1", "HTTP_PROXY="+proxy.URL, "http_proxy="+proxy.URL, "NO_PROXY=", "no_proxy=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(hosts, []string{"control.invalid"}) {
		t.Fatalf("proxy saw %v, want only the control request", hosts)
	}
}

func TestClientDeactivateNeverErases(t *testing.T) {
	f := newFake(t)
	c := f.client()
	ctx := context.Background()
	for _, err := range []error{c.Lock(ctx, "U1"), c.Unlock(ctx, "U1"), c.Deactivate(ctx, "U1")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"U1/lock", "U1/unlock", "U1/deactivate"}; !reflect.DeepEqual(f.posts, want) {
		t.Fatalf("posts %v", f.posts)
	}
	if f.bodies[2] != `{"skip_erase":true}` {
		t.Fatalf("deactivate body %q", f.bodies[2])
	}
	for _, b := range f.bodies[:2] {
		if b != "" && b != "{}" {
			t.Fatalf("lock/unlock body %q", b)
		}
	}
}

func TestClientRefetchesTokenOn401(t *testing.T) {
	f := newFake(t)
	f.reject = 1
	if err := f.client().Lock(context.Background(), "U1"); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 {
		t.Fatalf("token fetches = %d, want 2", f.tokens)
	}
	f = newFake(t)
	f.reject = 2
	if err := f.client().Lock(context.Background(), "U1"); err == nil {
		t.Fatal("second 401 must fail")
	}
	if f.tokens != 2 {
		t.Fatalf("token fetches = %d, want 2 (no loop)", f.tokens)
	}
}

func TestClientRefusesForeignNextLink(t *testing.T) {
	for _, next := range []string{"https://evil.example/api/admin/v1/users", "/elsewhere"} {
		f := newFake(t)
		f.nextLink = next
		if _, err := f.client().Users(context.Background()); err == nil {
			t.Fatalf("next %q accepted", next)
		}
		for _, p := range f.requested {
			if p == "/elsewhere" {
				t.Fatalf("requested %s", p)
			}
		}
	}
}

func TestClientRequiresExactlyOneProvider(t *testing.T) {
	for _, n := range []int{0, 2} {
		f := newFake(t)
		f.providers = n
		if _, err := f.client().Users(context.Background()); err == nil {
			t.Fatalf("%d providers accepted", n)
		}
	}
}

func TestClientNeverLogsSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sec, _ := r.BasicAuth()
		w.WriteHeader(500)
		fmt.Fprintf(w, "failed for %s %s", sec, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "cid", "secret-value").Users(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("err = %v", err)
	}
}
