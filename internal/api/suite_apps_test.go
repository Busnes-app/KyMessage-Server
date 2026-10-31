package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// fakeDirectory is KyIdentity's /api/suite/apps: per-sub lists ("" for the anonymous one),
// Basic client credentials required, every call counted by sub.
type fakeDirectory struct {
	mu    sync.Mutex
	calls map[string]int
	apps  map[string][]map[string]string
	fail  bool
}

func newFakeDirectory(t *testing.T, cfg *config.Config) *fakeDirectory {
	t.Helper()
	d := &fakeDirectory{calls: map[string]int{}, apps: map[string][]map[string]string{}}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := r.BasicAuth(); !ok || id != "kymessage" || secret != "client-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/suite/apps" {
			http.NotFound(w, r)
			return
		}
		sub := r.URL.Query().Get("sub")
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls[sub]++
		if d.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		apps := d.apps[sub]
		if apps == nil {
			apps = []map[string]string{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apps": apps})
	}))
	t.Cleanup(hs.Close)
	cfg.SSO.Enabled = true
	cfg.SSO.KyIdentityIssuer = hs.URL
	cfg.SSO.KyIdentityClientID = "kymessage"
	cfg.SSO.KyIdentitySecret = "client-secret"
	return d
}

func (d *fakeDirectory) count(sub string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[sub]
}

func app(id, name string) map[string]string {
	return map[string]string{"id": id, "name": name, "url": "https://" + id + ".example.com/sso/login"}
}

// ssoUser signs in a local account and turns it into a KyIdentity one with sub.
func ssoUser(t *testing.T, srv *api.Server, st store.Store, username, sub string) *http.Cookie {
	t.Helper()
	c := loginAs(t, srv, st, username, "user")
	u, err := st.Users().GetUserByID(context.Background(), "usr_"+username)
	if err != nil {
		t.Fatal(err)
	}
	u.SSOProvider, u.SSOSubject = "kyidentity", sub
	if err := st.Users().UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return c
}

func getApps(t *testing.T, srv *api.Server, c *http.Cookie) []map[string]string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/suite/apps", nil)
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if c == nil {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", w.Code)
		}
		return nil
	}
	var body struct {
		Apps []map[string]string `json:"apps"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Apps == nil {
		t.Fatalf("apps: %d %s", w.Code, w.Body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	return body.Apps
}

func TestSuiteAppsPerUser(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	dir := newFakeDirectory(t, cfg)
	dir.apps["sub-a"] = []map[string]string{app("kypost", "KyPost"), app("kymessage", "KyMessages")}
	dir.apps["sub-b"] = []map[string]string{app("kydrive", "KyDrive")}
	srv := api.NewServer(cfg, st)

	getApps(t, srv, nil)
	alice := ssoUser(t, srv, st, "alice", "sub-a")
	bob := ssoUser(t, srv, st, "bob", "sub-b")
	local := loginAs(t, srv, st, "carol", "user")

	for range 2 {
		if got := getApps(t, srv, alice); len(got) != 2 || got[0]["id"] != "kypost" || got[0]["url"] != "https://kypost.example.com/sso/login" {
			t.Fatalf("alice: %v", got)
		}
	}
	if got := getApps(t, srv, bob); len(got) != 1 || got[0]["id"] != "kydrive" {
		t.Fatalf("bob got another user's list: %v", got)
	}
	if n := dir.count("sub-a"); n != 1 {
		t.Errorf("alice's list fetched %d times, want 1 (cached)", n)
	}
	if n := dir.count("sub-b"); n != 1 {
		t.Errorf("bob's list fetched %d times", n)
	}
	if got := getApps(t, srv, local); len(got) != 0 {
		t.Errorf("local user: %v", got)
	}
	if n := dir.count(""); n != 0 {
		t.Errorf("a local user asked KyIdentity without a sub")
	}
}

func TestSuiteAppsFailureIsEmpty(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	dir := newFakeDirectory(t, cfg)
	dir.fail = true
	srv := api.NewServer(cfg, st)
	if got := getApps(t, srv, ssoUser(t, srv, st, "alice", "sub-a")); len(got) != 0 {
		t.Fatalf("failure: %v", got)
	}

	// Wrong credentials are a failure too.
	_, st2, cfg2 := setupTestServer(t)
	newFakeDirectory(t, cfg2)
	cfg2.SSO.KyIdentitySecret = "wrong"
	srv2 := api.NewServer(cfg2, st2)
	if got := getApps(t, srv2, ssoUser(t, srv2, st2, "alice", "sub-a")); len(got) != 0 {
		t.Fatalf("refused credentials: %v", got)
	}
}

func TestSuiteAppsWithoutSSO(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	dir := newFakeDirectory(t, cfg)
	dir.apps["sub-a"] = []map[string]string{app("kypost", "KyPost")}
	cfg.SSO.Enabled = false
	srv := api.NewServer(cfg, st)
	if got := getApps(t, srv, ssoUser(t, srv, st, "alice", "sub-a")); len(got) != 0 || dir.count("sub-a") != 0 {
		t.Fatalf("SSO off: %v", got)
	}
}

func elementHome(t *testing.T, srv *api.Server, origin string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", api.ElementHomePath, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("home: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	return w
}

func TestElementHomeListsTheSuite(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	dir := newFakeDirectory(t, cfg)
	dir.apps[""] = []map[string]string{app("kymessage", "KyMessages"), app("kypost", "KyPost"), app("kymessage-chat", "KyMessages"), app("kynew", `<b>New</b>`)}
	cfg.Server.AppURL = "https://admin.example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	srv := api.NewServer(cfg, st)

	w := elementHome(t, srv, "https://chat.example.com", nil)
	body := w.Body.String()
	if !strings.Contains(body, "<h1>Welcome to KyMessages</h1>") {
		t.Errorf("heading: %s", body)
	}
	for _, want := range []string{
		`href="https://kypost.example.com/sso/login"`,
		`src="https://admin.example.com/element-home/marks/kypost"`,
		`<span>KyPost</span>`,
		`<span>&lt;b&gt;New&lt;/b&gt;</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "kymessage") {
		t.Errorf("lists KyMessages itself: %s", body)
	}
	// Element has no session here: the list is the anonymous one, and only the chat host may
	// read it, never with credentials.
	if dir.count("") != 1 || len(dir.calls) != 1 {
		t.Errorf("directory calls: %v", dir.calls)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://chat.example.com" {
		t.Errorf("ACAO %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("credentials allowed: %q", got)
	}

	// Another origin, the console's own (which normally gets credentialed CORS) and a signed-in
	// request all get the same page and the same single allowed origin, and set no cookie.
	c := loginAs(t, srv, st, "alice", "user")
	for _, tc := range []struct {
		origin string
		c      *http.Cookie
	}{{"https://evil.example.com", nil}, {"https://admin.example.com", nil}, {"https://chat.example.com", c}} {
		w := elementHome(t, srv, tc.origin, tc.c)
		if w.Body.String() != body || w.Header().Get("Access-Control-Allow-Origin") != "https://chat.example.com" ||
			w.Header().Get("Access-Control-Allow-Credentials") != "" || len(w.Result().Cookies()) != 0 {
			t.Errorf("origin %s: ACAO %q ACAC %q cookies %v", tc.origin, w.Header().Get("Access-Control-Allow-Origin"),
				w.Header().Get("Access-Control-Allow-Credentials"), w.Result().Cookies())
		}
	}
	if dir.count("") != 1 {
		t.Errorf("anonymous list not cached: %d calls", dir.count(""))
	}
}

func TestElementHomeWithoutSSOIsJustTheHeading(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityIssuer = ""
	srv := api.NewServer(cfg, st)
	w := elementHome(t, srv, "", nil)
	if got := strings.TrimSpace(w.Body.String()); got != "<h1>Welcome to KyMessages</h1>" {
		t.Fatalf("body %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no chat host, yet ACAO %q", got)
	}
}

func TestElementMarks(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	for _, tc := range []struct {
		path, ctype, prefix string
		code                int
	}{
		{"/element-home/marks/kypost", "image/png", "\x89PNG", 200},
		{"/element-home/marks/kycalendar", "image/svg+xml", "<svg", 200},
		{"/element-home/marks/kyfuture", "image/svg+xml", "<svg", 200},
		{"/element-home/marks/Bad%20id", "", "", 404},
		{"/element-home/marks/" + strings.Repeat("a", 65), "", "", 404},
	} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.path, w.Code)
			continue
		}
		if tc.code == 200 && (w.Header().Get("Content-Type") != tc.ctype || !strings.HasPrefix(w.Body.String(), tc.prefix) ||
			w.Header().Get("Content-Security-Policy") != "default-src 'none'") {
			t.Errorf("%s: %q %q %.8q", tc.path, w.Header().Get("Content-Type"), w.Header().Get("Content-Security-Policy"), w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/element-home/marks/kyfuture", nil))
	if !strings.Contains(w.Body.String(), ">F</text>") {
		t.Errorf("letter badge: %s", w.Body)
	}
}

// Installs whose Element config predates the home page get it from the reconcile, brand intact.
func TestReconcileAddsElementHome(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.Server.AppURL = "https://admin.example.com"
	srv := api.NewServer(cfg, st)
	if err := st.Settings().SetSetting(ctx, "brand_name", "Acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := withElement(t, srv, cfg, elementJSON)
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var el map[string]any
	if err := json.Unmarshal(b, &el); err != nil {
		t.Fatal(err)
	}
	pages, _ := el["embedded_pages"].(map[string]any)
	if el["brand"] != "Acme" || pages["home_url"] != "https://admin.example.com/element-home.html" || el["disable_guests"] != true {
		t.Fatalf("config: %s", b)
	}
	fi, _ := os.Stat(p)
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(p); string(again) != string(b) {
		t.Fatalf("second reconcile changed the file: %s", again)
	}
	if fi2, _ := os.Stat(p); !fi2.ModTime().Equal(fi.ModTime()) {
		t.Error("second reconcile rewrote the file")
	}
}
