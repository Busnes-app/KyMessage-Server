package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// KY_SSO_ENABLED=false turns suite sign-in off, not just its button.
func TestSSORoutesAre404WhenDisabled(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	newFakeIdP(t, cfg)
	cfg.SSO.Enabled = false
	srv := api.NewServer(cfg, st)
	for _, path := range []string{"/api/sso/kyidentity/login", "/api/sso/kyidentity/login?fresh=1", "/api/sso/kyidentity/callback?code=c&state=s"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: got %d, location %q, %d cookies", path, w.Code, w.Header().Get("Location"), len(w.Result().Cookies()))
		}
	}
}

// A disabled account gets no session from suite sign-in; an accepted sign-in is audited.
func TestSSOSignInRefusesInactiveAccountAndAudits(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	for _, u := range []*store.User{
		{ID: "usr_off", Username: "off", Role: "admin", Status: "inactive", SSOProvider: "kyidentity", SSOSubject: "sub-off"},
		{ID: "usr_on", Username: "on", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-on"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)

	if w := idp.signIn(srv, map[string]any{"sub": "sub-off"}); w.Code != http.StatusForbidden || sessionCookie(w) != nil {
		t.Fatalf("inactive account: %d %s (session %v)", w.Code, w.Body.String(), sessionCookie(w))
	}
	if w := idp.signIn(srv, map[string]any{"sub": "sub-on"}); w.Code != http.StatusFound || sessionCookie(w) == nil {
		t.Fatalf("active account: %d %s", w.Code, w.Body.String())
	}
	recs, _, err := st.Audit().ListAuditRecords(ctx, 0, 10, "sso.")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.Action] = r.UserID
	}
	if got["sso.login"] != "usr_on" || got["sso.login_refused"] != "usr_off" || len(recs) != 2 {
		t.Fatalf("audit rows: %+v", got)
	}
}

// Upstream error text goes to the log, never to the browser.
func TestSSOErrorsAreNotEchoed(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream-secret-detail", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	cfg.SSO.KyIdentityIssuer, cfg.SSO.KyIdentityClientID = broken.URL, "client"
	srv := api.NewServer(cfg, st)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "upstream-secret-detail") || strings.Contains(w.Body.String(), "500") {
		t.Fatalf("discovery failure: %d %s", w.Code, w.Body.String())
	}

	// A code the IdP does not know: its token reply carries no ID token.
	newFakeIdP(t, cfg)
	srv = api.NewServer(cfg, st)
	login := httptest.NewRecorder()
	srv.ServeHTTP(login, httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil))
	state := login.Result().Cookies()[0].Name[len("ky_pkce_"):]
	callback := httptest.NewRequest("GET", "/api/sso/kyidentity/callback?code=unknown&state="+state, nil)
	for _, c := range login.Result().Cookies() {
		callback.AddCookie(c)
	}
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, callback)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "id_token") || sessionCookie(w) != nil {
		t.Fatalf("exchange failure: %d %s", w.Code, w.Body.String())
	}
}

// With secure cookies and no KY_COOKIE_DOMAIN every auth cookie is __Host- prefixed, so a
// sibling subdomain cannot plant one; the unprefixed name is ignored. The callback deletes the
// one-flow PKCE and nonce cookies.
func TestSecureCookiesUseHostPrefix(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.Security.CookieSecure = true
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)
	createLocalUser(t, st, "usr_boss", "boss", "admin", "BossPassword123!")

	login := postJSON(t, srv, "/api/auth/login", "192.0.2.1:1000", map[string]string{"username": "boss", "password": "BossPassword123!"})
	jar := map[string]*http.Cookie{}
	for _, c := range login.Result().Cookies() {
		if !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Errorf("cookie %s is not host-only, Secure, Path=/: %+v", c.Name, c)
		}
		jar[c.Name] = c
	}
	session, csrf := jar["__Host-ky_session"], jar["__Host-ky_csrf"]
	if session == nil || csrf == nil || len(jar) != 2 {
		t.Fatalf("login cookies: %v", jar)
	}

	me := func(c *http.Cookie) string {
		req := httptest.NewRequest("GET", "/api/auth/me", nil)
		req.AddCookie(c)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Body.String()
	}
	if body := me(&http.Cookie{Name: "ky_session", Value: session.Value}); !strings.Contains(body, `"authenticated":false`) {
		t.Fatalf("an unprefixed session cookie authenticated: %s", body)
	}
	if body := me(session); !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("the prefixed session cookie did not authenticate: %s", body)
	}
	// The CSRF cookie carries no trust: the header must be the token bound to the session.
	theme := func(csrfName, value string) int {
		req := httptest.NewRequest("POST", "/api/settings/theme", strings.NewReader(`{"theme":"oled"}`))
		req.AddCookie(session)
		req.AddCookie(&http.Cookie{Name: csrfName, Value: value})
		req.Header.Set("X-CSRF-Token", value)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := theme("ky_csrf", "planted"); code != http.StatusForbidden {
		t.Fatalf("a planted unprefixed CSRF pair passed: %d", code)
	}
	if code := theme("__Host-ky_csrf", "planted"); code != http.StatusForbidden {
		t.Fatalf("a planted prefixed CSRF pair passed: %d", code)
	}
	if code := theme("__Host-ky_csrf", csrf.Value); code != http.StatusOK {
		t.Fatalf("the session's own CSRF token failed: %d", code)
	}

	flow := httptest.NewRecorder()
	srv.ServeHTTP(flow, httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil))
	for _, c := range flow.Result().Cookies() {
		if !(strings.HasPrefix(c.Name, "__Host-ky_pkce_") || strings.HasPrefix(c.Name, "__Host-ky_nonce_")) || c.Path != "/" || !c.Secure {
			t.Errorf("SSO flow cookie: %+v", c)
		}
	}
	w := idp.signIn(srv, map[string]any{"sub": "sub-new", "preferred_username": "newcomer"})
	deleted := 0
	gotSession := false
	for _, c := range w.Result().Cookies() {
		switch {
		case strings.HasPrefix(c.Name, "__Host-ky_pkce_"), strings.HasPrefix(c.Name, "__Host-ky_nonce_"):
			if c.MaxAge < 0 {
				deleted++
			}
		case c.Name == "__Host-ky_session":
			gotSession = true
		}
	}
	if w.Code != http.StatusFound || deleted != 2 || !gotSession {
		t.Fatalf("callback: %d, %d flow cookies deleted, session %v", w.Code, deleted, gotSession)
	}
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	if c := sessionCookie(w); c != nil {
		return c
	}
	t.Fatalf("no session cookie: %d %s", w.Code, w.Body.String())
	return nil
}

// Local two-factor is removed (owner decision, KyMessage Q1 A): an account that turned it on
// before the upgrade signs in with its password alone, its stored columns untouched, and the
// enrolment and MFA routes are gone. Removing a factor locks nobody out.
func TestEnrolledTOTPAccountSignsInWithPasswordOnly(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	createLocalUser(t, st, "usr_root", "root", "admin", "SuperSecretPass123!")
	driver := map[string]string{"sqlite": "sqlite", "postgres": "pgx"}[cfg.Database.Driver]
	db, err := sql.Open(driver, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const enrolled = `UPDATE users SET totp_enabled = TRUE, totp_secret_enc = 'sealed', recovery_codes_hash = '["h"]' WHERE id = 'usr_root'`
	if _, err := db.Exec(enrolled); err != nil {
		t.Fatal(err)
	}

	w := postJSON(t, srv, "/api/auth/login", "192.0.2.5:1", map[string]string{"username": "root", "password": "SuperSecretPass123!"})
	if w.Code != http.StatusOK || sessionCookie(w) == nil || strings.Contains(w.Body.String(), "mfa") || strings.Contains(w.Body.String(), "totp") {
		t.Fatalf("password sign-in of an enrolled account: %d %s", w.Code, w.Body.String())
	}
	session := sessionCookie(w)
	for _, path := range []string{"/api/auth/mfa/totp", "/api/auth/mfa/recovery-code", "/api/auth/totp/begin", "/api/auth/totp/confirm", "/api/auth/totp/disable"} {
		if code := adminDo(t, srv, session, "POST", path, map[string]string{"code": "123456"}).Code; code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", path, code)
		}
	}
	var on bool
	var secret, codes string
	if err := db.QueryRow(`SELECT totp_enabled, totp_secret_enc, recovery_codes_hash FROM users WHERE id = 'usr_root'`).Scan(&on, &secret, &codes); err != nil {
		t.Fatal(err)
	}
	if !on || secret != "sealed" || codes != `["h"]` {
		t.Errorf("stored two-factor data changed: %v %q %q", on, secret, codes)
	}
}
