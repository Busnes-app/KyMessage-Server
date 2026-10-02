package api_test

import (
	"context"
	stdcrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var mu sync.Mutex
	tokens := map[string]string{}
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			token := tokens[r.Form.Get("code")]
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	cfg.SSO.KyIdentityIssuer = issuer
	cfg.SSO.KyIdentityClientID = "client"
	srv := api.NewServer(cfg, st)

	ssoLogin := func(authTime int64) *http.Cookie {
		login := httptest.NewRecorder()
		srv.ServeHTTP(login, httptest.NewRequest("GET", "/api/sso/kyidentity/login", nil))
		authorize, _ := url.Parse(login.Header().Get("Location"))
		claims := map[string]any{"iss": issuer, "aud": "client", "sub": "sub-admin", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": authorize.Query().Get("nonce")}
		if authTime != 0 {
			claims["auth_time"] = authTime
		}
		body, _ := json.Marshal(claims)
		input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k"}`)) + "." + base64.RawURLEncoding.EncodeToString(body)
		digest := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, digest[:])
		code := crypto.RandomHex(16)
		mu.Lock()
		tokens[code] = input + "." + base64.RawURLEncoding.EncodeToString(sig)
		mu.Unlock()
		callback := httptest.NewRequest("GET", "/api/sso/kyidentity/callback?code="+code+"&state="+authorize.Query().Get("state"), nil)
		for _, c := range login.Result().Cookies() {
			callback.AddCookie(c)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, callback)
		for _, c := range w.Result().Cookies() {
			if c.Name == auth.SessionCookieName {
				return c
			}
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
