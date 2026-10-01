package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func postJSON(t *testing.T, srv *api.Server, path, peer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = peer
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func createLocalUser(t *testing.T, st store.Store, id, username, role, pass string) {
	t.Helper()
	hash, err := password.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(context.Background(), &store.User{
		ID: id, Username: username, PasswordHash: hash, Role: role, Status: "active", SSOProvider: "local",
	}); err != nil {
		t.Fatal(err)
	}
}

// A cross-site form can POST text/plain without a preflight. Routes that set session cookies
// without a CSRF token must refuse it, so a victim is never signed into the attacker's account.
func TestSessionMintingRoutesRefuseFormPosts(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	createLocalUser(t, st, "usr_atk", "attacker", "user", "AttackerPass123!")

	for _, path := range []string{"/api/auth/login", "/api/auth/mfa/totp", "/api/auth/mfa/recovery-code", "/api/devices/pair/verify"} {
		body := `{"username":"attacker","password":"AttackerPass123!","pad":"="}`
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: got %d, want 415", path, w.Code)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: a form post set cookies", path)
		}
	}
}

// The anonymous verify route issues a full session, so it must accept only the 24-byte QR
// secret. A six-digit code was guessable inside the 90-second window.
func TestPairVerifyAcceptsOnlyTheSecret(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	createLocalUser(t, st, "usr_boss", "boss", "admin", "BossPassword123!")
	secret := crypto.RandomHex(24)
	if err := st.Devices().CreatePairing(context.Background(), &store.DevicePairing{
		Secret: secret, UserID: "usr_boss", Status: "pending",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(90 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	for _, guess := range []string{"123456", secret[:6]} {
		w := postJSON(t, srv, "/api/devices/pair/verify", "192.0.2.10:1000", map[string]string{"secret": guess, "platform": "pwa"})
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "session_token") {
			t.Fatalf("guess %q: got %d %s", guess, w.Code, w.Body.String())
		}
	}
	w := postJSON(t, srv, "/api/devices/pair/verify", "192.0.2.10:1000", map[string]string{"secret": secret, "platform": "pwa"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "session_token") {
		t.Fatalf("real secret: got %d %s", w.Code, w.Body.String())
	}
	poll := httptest.NewRecorder()
	srv.ServeHTTP(poll, httptest.NewRequest("GET", "/api/devices/pair/poll?secret="+secret, nil))
	if !strings.Contains(poll.Body.String(), `"consumed"`) {
		t.Fatalf("poll after pairing: %s", poll.Body.String())
	}
}

// One IPv6 /64 is one client. Keying each /128 separately let a single subscriber mint a fresh
// window for every request.
func TestIPv6AddressesInOne64ShareAWindow(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	body := map[string]string{"username": "nobody", "password": "WrongPass123!"}
	for i := 1; i <= 20; i++ {
		peer := fmt.Sprintf("[2001:db8:1:2::%x]:40000", i)
		if w := postJSON(t, srv, "/api/auth/login", peer, body); w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d throttled, want no earlier than 21", i)
		}
	}
	if w := postJSON(t, srv, "/api/auth/login", "[2001:db8:1:2::ffff]:40000", body); w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 from the same /64 answered %d, want 429", w.Code)
	}
	if w := postJSON(t, srv, "/api/auth/login", "[2001:db8:1:3::1]:40000", body); w.Code == http.StatusTooManyRequests {
		t.Fatal("a different /64 shared the window")
	}
}

// Password guesses spread across many addresses must still meet one per-account window.
func TestLoginPerAccountWindow(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	createLocalUser(t, st, "usr_admin", "admin", "admin", "RealAdminPass123!")

	for i := 1; i <= 10; i++ {
		peer := "203.0.113." + strconv.Itoa(i) + ":40000"
		if w := postJSON(t, srv, "/api/auth/login", peer, map[string]string{"username": "admin", "password": "guess-" + strconv.Itoa(i)}); w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d answered %d, want 401", i, w.Code)
		}
	}
	if w := postJSON(t, srv, "/api/auth/login", "203.0.113.99:40000", map[string]string{"username": "admin", "password": "RealAdminPass123!"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt answered %d, want 429", w.Code)
	}
}

// Per-account windows must survive a flood of anonymous keys. Sharing one randomly evicted
// map let a flood reset a victim's exhausted MFA counter.
func TestAccountWindowSurvivesClientFlood(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	ctx := context.Background()
	passHash, _ := password.Hash("SuperSecretPass123!")
	if err := st.Users().CreateUser(ctx, &store.User{
		ID: "usr_carol", Username: "carol", PasswordHash: passHash, Role: "user", Status: "active", SSOProvider: "local",
	}); err != nil {
		t.Fatal(err)
	}
	attempt := func(i int) int {
		raw := "flood-challenge-" + strconv.Itoa(i)
		if err := st.Sessions().CreateMFAChallenge(ctx, &store.MFAChallenge{
			TokenHash: crypto.SHA256Hex([]byte(raw)), UserID: "usr_carol", ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}, passHash); err != nil {
			t.Fatal(err)
		}
		return postJSON(t, srv, "/api/auth/mfa/totp", "203.0.113."+strconv.Itoa(i)+":40000", map[string]string{"mfa_token": raw, "code": "000000"}).Code
	}
	for i := 1; i <= 5; i++ {
		attempt(i)
	}
	for _, k := range api.AttemptKeysForTest(srv) {
		if strings.HasPrefix(k, "mfa-user:") {
			t.Fatalf("per-account key %q lives in the evictable client map", k)
		}
	}
	for i := 0; i < api.AttemptsCapForTest+50; i++ {
		api.AllowAttemptForTest(srv, "login:flood-"+strconv.Itoa(i), 20, time.Minute)
	}
	if code := attempt(6); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 6 after a flood answered %d, want 429", code)
	}
}

// A solved challenge buys one login, not every login until it expires.
func TestPoWSolutionIsSingleUse(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	cfg.Captcha.Provider = "pow"
	createLocalUser(t, st, "usr_pow", "pow", "user", "PowUserPass123!")

	challenge, err := auth.GeneratePoWChallenge(50, cfg.Security.SessionSecret)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for n := 1; n <= challenge.MaxNumber; n++ {
		if crypto.SHA256Hex([]byte(challenge.Salt+strconv.Itoa(n))) == challenge.Challenge {
			sol, _ := json.Marshal(auth.PoWSolution{Algorithm: challenge.Algorithm, Salt: challenge.Salt, Challenge: challenge.Challenge,
				Number: n, MaxNumber: challenge.MaxNumber, ExpiresAt: challenge.ExpiresAt, Signature: challenge.Signature})
			token = base64.StdEncoding.EncodeToString(sol)
		}
	}
	body := map[string]string{"username": "pow", "password": "wrong-password", "captcha_token": token}
	if w := postJSON(t, srv, "/api/auth/login", "192.0.2.1:1", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("first use answered %d, want 401 (captcha passed, password wrong)", w.Code)
	}
	if w := postJSON(t, srv, "/api/auth/login", "192.0.2.1:1", body); w.Code != http.StatusForbidden {
		t.Fatalf("replayed solution answered %d, want 403", w.Code)
	}
}

// Password login resolves only local accounts: an SSO row whose name differs only in case must
// not shadow the local administrator.
func TestLoginIgnoresSSOCaseVariant(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	ctx := context.Background()
	createLocalUser(t, st, "usr_admin", "admin", "admin", "RealAdminPass123!")
	if err := st.Users().CreateUser(ctx, &store.User{
		ID: "usr_shadow", Username: "ADMIN", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second login runs after the first rewrote the local row
		if w := postJSON(t, srv, "/api/auth/login", "192.0.2.1:1", map[string]string{"username": "admin", "password": "RealAdminPass123!"}); w.Code != http.StatusOK {
			t.Fatalf("login %d: got %d %s", i, w.Code, w.Body.String())
		}
	}
}
