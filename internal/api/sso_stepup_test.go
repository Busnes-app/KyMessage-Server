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
)

// An IdP may silently reuse an old login, so an SSO session is only as fresh as the signed
// auth_time. Old or missing auth_time must not pass backup step-up; a fresh one must.
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
		claims := map[string]any{"sub": "sub-admin"}
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
		{"POST", "/api/backup/export-capsule"}, {"POST", "/api/backup/pair-remote"}, {"POST", "/api/backup/deposit"},
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
	issuer string
	key    *rsa.PrivateKey
	mu     sync.Mutex
	tokens map[string]string
}

// newFakeIdP starts the issuer and points cfg's KyIdentity settings at it.
func newFakeIdP(t *testing.T, cfg *config.Config) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, tokens: map[string]string{}}
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	srv.ServeHTTP(login, httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil))
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
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `\"admin\"`) || sessionCookie(w) != nil {
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
