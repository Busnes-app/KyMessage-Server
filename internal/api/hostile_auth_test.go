package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/totp"
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
	theme := func(csrfName string) int {
		req := httptest.NewRequest("POST", "/api/settings/theme", strings.NewReader(`{"theme":"oled"}`))
		req.AddCookie(session)
		req.AddCookie(&http.Cookie{Name: csrfName, Value: csrf.Value})
		req.Header.Set("X-CSRF-Token", csrf.Value)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := theme("ky_csrf"); code != http.StatusForbidden {
		t.Fatalf("an unprefixed CSRF cookie passed: %d", code)
	}
	if code := theme("__Host-ky_csrf"); code != http.StatusOK {
		t.Fatalf("the prefixed CSRF cookie failed: %d", code)
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

// Enrolment needs a code from the new secret before it turns on, then password alone no
// longer signs in, so step-up (a recent sign-in) repeats password plus TOTP. Grants issued on
// the password alone are revoked; disabling needs a valid code.
func TestTOTPEnrolment(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	const pass = "SuperSecretPass123!"
	creds := map[string]string{"username": "root", "password": pass}
	admin := loginAs(t, srv, st, "root", "admin")
	other := sessionFrom(t, postJSON(t, srv, "/api/auth/login", "192.0.2.5:1", creds))

	begin := decode[struct {
		Secret     string `json:"secret"`
		OTPAuthURI string `json:"otpauth_uri"`
	}](t, adminDo(t, srv, admin, "POST", "/api/auth/totp/begin", nil))
	if begin.Secret == "" || !strings.HasPrefix(begin.OTPAuthURI, "otpauth://totp/") || !strings.Contains(begin.OTPAuthURI, "secret="+begin.Secret) {
		t.Fatalf("begin: %+v", begin)
	}
	// Pending is not enabled: the password still signs in on its own.
	if w := postJSON(t, srv, "/api/auth/login", "192.0.2.5:1", creds); !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatalf("a pending secret changed sign-in: %s", w.Body.String())
	}

	stale, _ := totp.Code(begin.Secret, time.Now().Add(-10*time.Minute))
	if w := adminDo(t, srv, admin, "POST", "/api/auth/totp/confirm", map[string]string{"code": stale}); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong code: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_root"); u.TOTPEnabled {
		t.Fatal("a wrong code turned TOTP on")
	}
	now, _ := totp.Code(begin.Secret, time.Now())
	w := adminDo(t, srv, admin, "POST", "/api/auth/totp/confirm", map[string]string{"code": now})
	confirmed := decode[struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}](t, w)
	if len(confirmed.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes: %v", confirmed.RecoveryCodes)
	}
	current := sessionFrom(t, w)
	if code := do(t, srv, "GET", "/api/backup/status", other).Code; code != http.StatusUnauthorized {
		t.Fatalf("a password-only session survived enabling: %d", code)
	}
	if code := do(t, srv, "GET", "/api/backup/status", admin).Code; code != http.StatusUnauthorized {
		t.Fatalf("the replaced session survived: %d", code)
	}
	if code := adminDo(t, srv, current, "PUT", "/api/backup/schedule", map[string]int64{"interval_sec": 0}).Code; code != http.StatusOK {
		t.Fatalf("the reissued session lost its step-up: %d", code)
	}
	if code := adminDo(t, srv, current, "POST", "/api/auth/totp/begin", nil).Code; code != http.StatusConflict {
		t.Fatalf("begin while on: %d", code)
	}

	// Password alone now yields an MFA transaction, never a session.
	login := postJSON(t, srv, "/api/auth/login", "192.0.2.5:1", creds)
	if sessionCookie(login) != nil || !strings.Contains(login.Body.String(), `"mfa_required":true`) {
		t.Fatalf("password alone after enabling: %s", login.Body.String())
	}
	var challenge struct {
		MFAToken string `json:"mfa_token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &challenge)
	next, _ := totp.Code(begin.Secret, time.Now().Add(30*time.Second)) // the current step is spent
	stepped := sessionFrom(t, postJSON(t, srv, "/api/auth/mfa/totp", "192.0.2.5:1", map[string]string{"mfa_token": challenge.MFAToken, "code": next}))
	if code := adminDo(t, srv, stepped, "PUT", "/api/backup/schedule", map[string]int64{"interval_sec": 0}).Code; code != http.StatusOK {
		t.Fatalf("password plus TOTP did not pass step-up: %d", code)
	}

	if w := adminDo(t, srv, stepped, "POST", "/api/auth/totp/disable", map[string]string{"code": "not-a-code"}); w.Code != http.StatusBadRequest {
		t.Fatalf("disable with a wrong code: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_root"); !u.TOTPEnabled {
		t.Fatal("a wrong code turned TOTP off")
	}
	if w := adminDo(t, srv, stepped, "POST", "/api/auth/totp/disable", map[string]string{"code": confirmed.RecoveryCodes[0]}); w.Code != http.StatusOK {
		t.Fatalf("disable with a recovery code: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_root"); u.TOTPEnabled || u.TOTPSecretEnc != "" {
		t.Fatalf("still on: %+v", u)
	}
}

// Every TOTP route needs a recent sign-in, and suite accounts keep their second factor in
// KyIdentity.
func TestTOTPRoutesNeedFreshLocalAdmin(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	if err := st.Users().CreateUser(context.Background(), &store.User{
		ID: "usr_sso", Username: "sso", Role: "admin", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-sso",
	}); err != nil {
		t.Fatal(err)
	}
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)
	stale := staleAdmin(t, st)
	for _, path := range []string{"/api/auth/totp/begin", "/api/auth/totp/confirm", "/api/auth/totp/disable"} {
		if w := adminDo(t, srv, stale, "POST", path, map[string]string{"code": "123456"}); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
			t.Errorf("%s with a stale session: %d %s", path, w.Code, w.Body.String())
		}
	}
	member := loginAs(t, srv, st, "member", "user")
	if code := adminDo(t, srv, member, "POST", "/api/auth/totp/begin", nil).Code; code != http.StatusForbidden {
		t.Errorf("member began TOTP: %d", code)
	}
	sso := sessionFrom(t, idp.signIn(srv, map[string]any{"sub": "sub-sso", "auth_time": time.Now().Unix()}))
	if w := adminDo(t, srv, sso, "POST", "/api/auth/totp/begin", nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "identity provider") {
		t.Errorf("suite admin began TOTP: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(context.Background(), "usr_sso"); u.TOTPSecretEnc != "" {
		t.Error("a suite account got a TOTP secret")
	}
}
