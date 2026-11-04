package api_test

import (
	"context"
	stdcrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
)

// An IdP may silently reuse an old login, so an SSO session is only as fresh as the signed
// auth_time. Old or missing auth_time must not pass backup step-up; a fresh one must.
// Deposit needs no step-up (TestDepositNeedsAdminNotStepUp), so it is not listed.
func TestSSOStepUpUsesSignedAuthTime(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	if err := st.Users().CreateUser(context.Background(), &store.User{
		ID: "usr_sso_admin", Username: "sso-admin", Role: "admin", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-admin",
	}); err != nil {
		t.Fatal(err)
	}
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)

	ssoLogin := func(authTime int64) *http.Cookie {
		claims := map[string]any{"sub": "sub-admin", "roles": []string{"admin"}}
		if authTime != 0 {
			claims["auth_time"] = authTime
		}
		w := idp.signIn(srv, claims)
		if c := sessionCookie(w); c != nil {
			return c
		}
		t.Fatalf("callback issued no session: %d %s", w.Code, w.Body.String())
		return nil
	}

	priv, _ := recoverykey.Generate()
	routes := []struct{ method, path string }{
		{"POST", "/api/backup/export-capsule"}, {"POST", "/api/backup/pair-remote"},
		{"DELETE", "/api/backup/pairing"}, {"POST", "/api/backup/pin-key"}, {"PUT", "/api/backup/schedule"},
		{"POST", "/api/admin/matrix/sessions/oauth2/01J9ZK8V6N3W4X5Y6Z7A8B9C1A/finish"},
	}
	for name, authTime := range map[string]int64{"old": time.Now().Add(-time.Hour).Unix(), "missing": 0} {
		session := ssoLogin(authTime)
		for _, rt := range routes {
			w := adminDo(t, srv, session, rt.method, rt.path, pinBody(priv.Public(), 2, 3))
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"reauth_url":"/api/sso/kyidentity/login?fresh=1"`) {
				t.Errorf("%s auth_time, %s %s: got %d %s", name, rt.method, rt.path, w.Code, w.Body.String())
			}
		}
	}
	fresh := ssoLogin(time.Now().Unix())
	if w := adminDo(t, srv, fresh, "PUT", "/api/backup/schedule", map[string]int64{"interval_sec": 0}); w.Code != http.StatusOK {
		t.Fatalf("fresh auth_time: schedule got %d %s", w.Code, w.Body.String())
	}
}

// ssoError is the code a refused callback sends the browser to the sign-in page with, or "".
func ssoError(w *httptest.ResponseRecorder) string {
	code, ok := strings.CutPrefix(w.Header().Get("Location"), "/#/login?sso_error=")
	if w.Code != http.StatusFound || !ok {
		return ""
	}
	return code
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

// fakeIdP is a KyIdentity issuer for callback tests: discovery, JWKS and a token endpoint that
// returns the ID token signIn minted.
type fakeIdP struct {
	httpClient *http.Client
	issuer     string
	key        *rsa.PrivateKey
	mu         sync.Mutex
	tokens     map[string]string
}

// newFakeIdP starts the issuer and points cfg's KyIdentity settings at it.
func newFakeIdP(t *testing.T, cfg *config.Config) *fakeIdP { return makeFakeIdP(t, cfg, false) }
func makeFakeIdP(t *testing.T, cfg *config.Config, tls bool) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, tokens: map[string]string{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize", "token_endpoint": f.issuer + "/token", "jwks_uri": f.issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/token":
			_ = r.ParseForm()
			f.mu.Lock()
			token := f.tokens[r.Form.Get("code")]
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	})
	var idp *httptest.Server
	if tls {
		idp = httptest.NewTLSServer(handler)
		f.httpClient = idp.Client()
	} else {
		idp = httptest.NewServer(handler)
	}
	t.Cleanup(idp.Close)
	f.issuer = idp.URL
	cfg.SSO.KyIdentityIssuer = f.issuer
	cfg.SSO.KyIdentityClientID = "client"
	return f
}

// signIn runs login and callback against srv with an ID token carrying claims (iss, aud, exp,
// iat and nonce are filled in) and returns the callback's response.
func (f *fakeIdP) signIn(srv *api.Server, claims map[string]any) *httptest.ResponseRecorder {
	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil)
	if f.httpClient != nil {
		loginRequest = loginRequest.WithContext(oidc.ClientContext(loginRequest.Context(), f.httpClient))
	}
	srv.ServeHTTP(login, loginRequest)
	authorize, _ := url.Parse(login.Header().Get("Location"))
	full := map[string]any{"iss": f.issuer, "aud": "client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": authorize.Query().Get("nonce")}
	maps.Copy(full, claims)
	body, _ := json.Marshal(full)
	input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k"}`)) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, stdcrypto.SHA256, digest[:])
	code := crypto.RandomHex(16)
	f.mu.Lock()
	f.tokens[code] = input + "." + base64.RawURLEncoding.EncodeToString(sig)
	f.mu.Unlock()
	callback := httptest.NewRequest("GET", "/api/sso/kyidentity/callback?code="+code+"&state="+authorize.Query().Get("state"), nil)
	for _, c := range login.Result().Cookies() {
		callback.AddCookie(c)
	}
	w := httptest.NewRecorder()
	if f.httpClient != nil {
		callback = callback.WithContext(oidc.ClientContext(callback.Context(), f.httpClient))
	}
	srv.ServeHTTP(w, callback)
	return w
}

// A KyIdentity sign-in whose username a local account holds is refused with 409 and audited; it
// never signs in as, links to or alters the local account.
func TestSSOSignInRefusesUsernameHeldByLocalAccount(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	local := &store.User{ID: "usr_local_admin", Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active", SSOProvider: "local"}
	if err := st.Users().CreateUser(ctx, local); err != nil {
		t.Fatal(err)
	}
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)

	w := idp.signIn(srv, map[string]any{"sub": "kid-root", "preferred_username": "admin"})
	if ssoError(w) != "username_conflict" || sessionCookie(w) != nil {
		t.Fatalf("clash: %d %s (session %v)", w.Code, w.Body.String(), sessionCookie(w))
	}
	if _, err := st.Users().GetUserBySSO(ctx, "kyidentity", "kid-root"); err == nil {
		t.Fatal("an SSO row was created")
	}
	got, err := st.Users().GetUserByID(ctx, local.ID)
	if err != nil || got.SSOProvider != "local" || got.SSOSubject != "" || got.PasswordHash != "hash" {
		t.Fatalf("local admin changed: %v %+v", err, got)
	}
	recs, _, err := st.Audit().ListAuditRecords(ctx, 0, 10, "sso.")
	if err != nil || len(recs) != 1 || recs[0].Action != "sso.login_conflict" || recs[0].Resource != "admin" || !strings.Contains(recs[0].Details, `"kid-root"`) {
		t.Fatalf("audit: %v %+v", err, recs)
	}

	// Another username still provisions.
	if w := idp.signIn(srv, map[string]any{"sub": "kid-root", "preferred_username": "root"}); sessionCookie(w) == nil {
		t.Fatalf("non-clashing sign-in: %d %s", w.Code, w.Body.String())
	}
}

// A SCIM row whose externalId is the KyIdentity subject is that person: sign-in uses it (SCIM
// keeps owning it), instead of clashing on its username. Off when SCIM is off.
func TestSSOSignInAdoptsTheSCIMRowForTheSubject(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	for _, u := range []*store.User{
		{ID: "usr_scim_sam", Username: "sam", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "kid-sam"},
		{ID: "usr_scim_ivy", Username: "ivy", Role: "user", Status: "inactive", SSOProvider: "scim", SSOSubject: "kid-ivy"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	idp := newFakeIdP(t, cfg)

	cfg.SCIM.Enabled = false
	if w := idp.signIn(api.NewServer(cfg, st), map[string]any{"sub": "kid-sam", "preferred_username": "sam"}); ssoError(w) != "username_conflict" || sessionCookie(w) != nil {
		t.Fatalf("SCIM off: %d %s", w.Code, w.Body.String())
	}

	cfg.SCIM.Enabled = true
	srv := api.NewServer(cfg, st)
	w := idp.signIn(srv, map[string]any{"sub": "kid-sam", "preferred_username": "sam"})
	c := sessionCookie(w)
	if c == nil {
		t.Fatalf("adoption: %d %s", w.Code, w.Body.String())
	}
	sess, err := st.Sessions().GetSession(ctx, crypto.SHA256Hex([]byte(c.Value)))
	if err != nil || sess.UserID != "usr_scim_sam" {
		t.Fatalf("session for %v (%v), want usr_scim_sam", sess, err)
	}
	if got, _ := st.Users().GetUserByID(ctx, "usr_scim_sam"); got.SSOProvider != "scim" {
		t.Fatalf("the SCIM row changed owner: %+v", got)
	}
	if _, err := st.Users().GetUserBySSO(ctx, "kyidentity", "kid-sam"); err == nil {
		t.Fatal("a second row was created")
	}
	if w := idp.signIn(srv, map[string]any{"sub": "kid-ivy", "preferred_username": "ivy"}); ssoError(w) != "disabled" || sessionCookie(w) != nil {
		t.Fatalf("inactive SCIM row: %d %s", w.Code, w.Body.String())
	}
	// A SCIM admin row becomes a console admin by sign-in only with KY_SCIM_ALLOW_ADMIN.
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_scim_boss", Username: "boss", Role: "admin", Status: "active", SSOProvider: "scim", SSOSubject: "kid-boss"}); err != nil {
		t.Fatal(err)
	}
	if w := idp.signIn(srv, map[string]any{"sub": "kid-boss", "preferred_username": "boss"}); ssoError(w) != "not_assigned" || sessionCookie(w) != nil {
		t.Fatalf("SCIM admin row without the opt-in: %d %s", w.Code, w.Body.String())
	}
	cfg.SCIM.AllowAdmin = true
	if w := idp.signIn(api.NewServer(cfg, st), map[string]any{"sub": "kid-boss", "preferred_username": "boss"}); sessionCookie(w) == nil {
		t.Fatalf("SCIM admin row with the opt-in: %d %s", w.Code, w.Body.String())
	}
	cfg.SCIM.AllowAdmin = false
	// Two SCIM rows with one externalId are ambiguous: refused, never one picked at random.
	for _, id := range []string{"usr_scim_dup1", "usr_scim_dup2"} {
		if err := st.Users().CreateUser(ctx, &store.User{ID: id, Username: id, Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "kid-dup"}); err != nil {
			t.Fatal(err)
		}
	}
	if w := idp.signIn(srv, map[string]any{"sub": "kid-dup", "preferred_username": "dup"}); ssoError(w) != "adoption_conflict" || sessionCookie(w) != nil {
		t.Fatalf("two SCIM rows for one subject: %d %s", w.Code, w.Body.String())
	}
	// An adopted SCIM admin's step-up goes through KyIdentity, forcing credentials.
	if err := st.Users().UpdateProfile(ctx, &store.User{ID: "usr_scim_sam", Username: "sam", Role: "admin", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if w := adminDo(t, srv, c, "PUT", "/api/backup/schedule", map[string]int{"interval_sec": 0}); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh=1") {
		t.Fatalf("adopted SCIM admin step-up: %d %s", w.Code, w.Body.String())
	}
}

// KyIdentity's explicit app role applies at each sign-in (owner answer Q4): empty or
// missing roles demote old admins, and a global role cannot promote. A change revokes
// sessions and is audited. SCIM rows keep their role.
func TestSSOSignInAppliesKyIdentityRole(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.SCIM.Enabled = true
	for _, u := range []*store.User{
		{ID: "usr_amy", Username: "amy", Role: "admin", Status: "active", SSOProvider: "kyidentity", SSOSubject: "kid-amy"},
		{ID: "usr_scim_sam", Username: "sam", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "kid-sam"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	idp := newFakeIdP(t, cfg)
	srv := api.NewServer(cfg, st)
	signIn := func(sub string, claims map[string]any) *http.Cookie {
		t.Helper()
		claims["sub"] = sub
		w := idp.signIn(srv, claims)
		c := sessionCookie(w)
		if c == nil {
			t.Fatalf("%s %v: %d %s", sub, claims, w.Code, w.Body.String())
		}
		return c
	}
	role := func(id string) string {
		t.Helper()
		u, err := st.Users().GetUserByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u.Role
	}
	live := func(c *http.Cookie) bool {
		_, err := st.Sessions().GetSession(ctx, crypto.SHA256Hex([]byte(c.Value)))
		return err == nil
	}

	first := signIn("kid-amy", map[string]any{"roles": []string{}})
	if role("usr_amy") != "user" {
		t.Fatal("empty app roles retained the old admin role")
	}
	promoted := signIn("kid-amy", map[string]any{"roles": []string{"editor", "admin"}})
	if role("usr_amy") != "admin" || live(first) || !live(promoted) {
		t.Fatal("explicit admin did not promote and revoke the old session")
	}
	second := signIn("kid-amy", map[string]any{"roles": []string{"admin"}})
	if role("usr_amy") != "admin" || !live(promoted) {
		t.Fatal("an unchanged role revoked sessions or changed the role")
	}
	before, err := st.Users().GetUserByID(ctx, "usr_amy")
	if err != nil {
		t.Fatal(err)
	}
	demoted := signIn("kid-amy", map[string]any{"roles": []string{"editor"}, "role": "admin"})
	if role("usr_amy") != "user" || live(first) || live(second) || !live(demoted) {
		t.Fatalf("demotion: role %q, old sessions %v %v, new %v", role("usr_amy"), live(first), live(second), live(demoted))
	}
	// A grant authorised before the change (an in-flight pairing or session) cannot land after it.
	now := time.Now().UTC()
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "stale", UserID: "usr_amy", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, before.PasswordHash); err == nil {
		t.Fatal("a grant on the pre-change snapshot was issued")
	}
	if w := adminDo(t, srv, demoted, "GET", "/api/admin/audit", nil); w.Code != http.StatusForbidden {
		t.Fatalf("demoted user reached an admin route: %d", w.Code)
	}
	for _, claims := range []map[string]any{
		{"roles": []string{}, "role": "admin"},
		{"role": "admin"},
	} {
		c := signIn("kid-amy", claims)
		if role("usr_amy") != "user" || !live(demoted) {
			t.Fatalf("global role promoted or revoked unchanged user: %v", claims)
		}
		if w := adminDo(t, srv, c, "GET", "/api/admin/audit", nil); w.Code != http.StatusForbidden {
			t.Fatalf("global admin reached admin route: %d", w.Code)
		}
	}
	// Missing roles also remove an existing admin, even with a global admin claim.
	signIn("kid-amy", map[string]any{"roles": []string{"admin"}})
	signIn("kid-amy", map[string]any{"role": "admin"})
	if role("usr_amy") != "user" {
		t.Fatal("missing app roles retained administration")
	}
	recs, _, err := st.Audit().ListAuditRecords(ctx, 0, 20, "sso.role_changed")
	if err != nil || len(recs) != 5 || recs[0].UserID != "usr_amy" || recs[0].Details != `from="admin" to="user"` {
		t.Fatalf("audit: %v %+v", err, recs)
	}

	signIn("kid-sam", map[string]any{"roles": []string{"admin"}})
	if role("usr_scim_sam") != "user" {
		t.Fatal("a token promoted a SCIM-owned row")
	}

	// Auto-provisioning must use the same explicit app-role rule on the first sign-in.
	for _, tc := range []struct {
		sub    string
		claims map[string]any
		want   string
	}{
		{"kid-global-empty", map[string]any{"role": "admin", "roles": []string{}}, "user"},
		{"kid-global-missing", map[string]any{"role": "admin"}, "user"},
		{"kid-app-admin", map[string]any{"roles": []string{"editor", "admin"}}, "admin"},
	} {
		tc.claims["preferred_username"] = tc.sub
		c := signIn(tc.sub, tc.claims)
		u, err := st.Users().GetUserBySSO(ctx, "kyidentity", tc.sub)
		if err != nil || u.Role != tc.want {
			t.Fatalf("auto-provision %s: %v %+v", tc.sub, err, u)
		}
		w := adminDo(t, srv, c, "GET", "/api/admin/audit", nil)
		if (tc.want == "admin" && w.Code != http.StatusOK) || (tc.want == "user" && w.Code != http.StatusForbidden) {
			t.Fatalf("auto-provision %s admin route: %d %s", tc.sub, w.Code, w.Body.String())
		}
	}

	cfg.SSO.AutoProvision = false
	if w := idp.signIn(api.NewServer(cfg, st), map[string]any{"sub": "kid-new"}); ssoError(w) != "not_provisioned" || sessionCookie(w) != nil {
		t.Fatalf("unprovisioned: %d %s", w.Code, w.Header().Get("Location"))
	}
}
