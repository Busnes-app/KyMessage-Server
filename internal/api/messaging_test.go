package api_test

import (
	"bytes"
	"context"
	stdcrypto "crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Seed the verified account/session boundary for authorization tests. The OIDC
// integration test below separately exercises a signed local test issuer.
func messagingLogin(t *testing.T, st store.Store, user string) string {
	t.Helper()
	ctx := context.Background()
	if err := st.Users().CreateUser(ctx, &store.User{ID: user, Username: user, Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: user}); err != nil {
		t.Fatal(err)
	}
	token := crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: crypto.SHA256Hex([]byte(token)), UserID: user, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	return token
}

func messagingRequest(t *testing.T, srv *api.Server, method, path, session, device string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if session != "" {
		r.Header.Set("Authorization", "Bearer "+session)
	}
	if device != "" {
		r.Header.Set("X-KyMessages-Device", device)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func messagingCode(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("want %d got %d: %s", code, w.Code, w.Body.String())
	}
}

type enrolledDevice struct {
	ID, Token, Status string
	Key               ed25519.PrivateKey
	Input             string
}

func requestEnrollment(t *testing.T, srv *api.Server, session string) enrolledDevice {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token := crypto.RandomHex(32)
	w := messagingRequest(t, srv, "POST", "/api/messaging/devices", session, "", map[string]string{
		"name": "Browser", "public_key": base64.StdEncoding.EncodeToString(public), "token_hash": crypto.SHA256Hex([]byte(token)),
	})
	messagingCode(t, w, 201)
	var response struct {
		Device       struct{ ID, Status string }
		SigningInput string `json:"signing_input"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("raw device credential leaked")
	}
	return enrolledDevice{ID: response.Device.ID, Token: token, Status: response.Device.Status, Key: private, Input: response.SigningInput}
}

func enrollmentProof(t *testing.T, d enrolledDevice) map[string]string {
	t.Helper()
	input, err := base64.StdEncoding.DecodeString(d.Input)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(d.Key, input))}
}

func verifyEnrollment(t *testing.T, srv *api.Server, session string, d enrolledDevice) enrolledDevice {
	t.Helper()
	w := messagingRequest(t, srv, "POST", "/api/messaging/devices/"+d.ID+"/verify", session, "", enrollmentProof(t, d))
	messagingCode(t, w, 200)
	var response struct{ Device struct{ Status string } }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	d.Status = response.Device.Status
	return d
}

func TestMessagingDeviceTrustLifecycle(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	bob := messagingLogin(t, st, "bob")
	first := requestEnrollment(t, srv, session)
	bad := first
	_, bad.Key, _ = ed25519.GenerateKey(rand.Reader)
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+first.ID+"/verify", session, "", enrollmentProof(t, bad)), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+first.ID+"/verify", bob, "", enrollmentProof(t, first)), 404)
	first = verifyEnrollment(t, srv, session, first)
	if first.Status != "approved" {
		t.Fatal(first.Status)
	}
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+first.ID+"/verify", session, "", enrollmentProof(t, first)), 404)
	second := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	if second.Status != "pending" {
		t.Fatal(second.Status)
	}
	approve := "/api/messaging/devices/" + second.ID + "/approve"
	messagingCode(t, messagingRequest(t, srv, "POST", approve, session, "", nil), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", approve, session, second.Token, nil), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", approve, bob, first.Token, nil), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", approve, session, first.Token, nil), 200)
	list := messagingRequest(t, srv, "GET", "/api/messaging/devices", session, "", nil)
	messagingCode(t, list, 200)
	for _, forbidden := range []string{first.Token, second.Token, "token_hash", "challenge", "enrollment_session", first.Input} {
		if strings.Contains(list.Body.String(), forbidden) {
			t.Fatalf("list leaked %q", forbidden)
		}
	}
	if !strings.Contains(list.Body.String(), `"approved_by":"`+first.ID+`"`) {
		t.Fatal("approval provenance missing")
	}
	for _, device := range []enrolledDevice{first, second} {
		messagingCode(t, messagingRequest(t, srv, "DELETE", "/api/messaging/devices/"+device.ID, session, "", nil), 200)
		messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms", session, device.Token, nil), 403)
	}
	replacement := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	if replacement.Status != "pending" {
		t.Fatal("revocation reset bootstrap trust")
	}
	logs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(logs)
	if bytes.Contains(data, []byte(first.Token)) || bytes.Contains(data, []byte(first.Input)) {
		t.Fatal("audit leaked credential")
	}
}

func TestMessagingRoomConsentAndIsolation(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	alice := messagingLogin(t, st, "alice")
	bob := messagingLogin(t, st, "bob")
	charlie := messagingLogin(t, st, "charlie")
	a := verifyEnrollment(t, srv, alice, requestEnrollment(t, srv, alice))
	b := verifyEnrollment(t, srv, bob, requestEnrollment(t, srv, bob))
	c := verifyEnrollment(t, srv, charlie, requestEnrollment(t, srv, charlie))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", alice, a.Token, map[string]string{"name": "Team"})
	messagingCode(t, w, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/members", bob, b.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", charlie, c.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", bob, b.Token, map[string]string{"user_id": "charlie"}), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", alice, a.Token, map[string]string{"user_id": "bob"}), 200)
	w = messagingRequest(t, srv, "GET", "/api/messaging/rooms", bob, b.Token, nil)
	messagingCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"membership":"invited"`) {
		t.Fatal("invitation missing")
	}
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/members", bob, b.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", bob, b.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/members", bob, b.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "DELETE", path+"/members/alice", alice, a.Token, nil), 409)
	messagingCode(t, messagingRequest(t, srv, "DELETE", path+"/members/bob", alice, a.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/members", bob, b.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", bob, b.Token, nil), 404)
	w = messagingRequest(t, srv, "GET", "/api/messaging/rooms", charlie, c.Token, nil)
	if strings.Contains(w.Body.String(), room.ID) {
		t.Fatal("room leaked to unrelated user")
	}
	if err := st.Sessions().DeleteSession(context.Background(), crypto.SHA256Hex([]byte(alice))); err != nil {
		t.Fatal(err)
	}
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/members", alice, a.Token, nil), 401)
}

func TestMessagingHTTPBoundaries(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	device := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/devices", "", device.Token, nil), 401)
	admin := loginAs(t, srv, st, "operator", "admin")
	messagingCode(t, do(t, srv, "GET", "/api/messaging/devices", admin), 403)
	for _, body := range []string{`{"name":"Test","owner_id":"bob"}`, `{"name":"Test"}{}`, `{"name":"` + strings.Repeat("x", 9000) + `"}`} {
		r := httptest.NewRequest("POST", "/api/messaging/rooms", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+session)
		r.Header.Set("X-KyMessages-Device", device.Token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		messagingCode(t, w, 400)
	}
	for _, tc := range []struct {
		origin string
		csrf   bool
		code   int
	}{{cfg.Server.AppURL, false, 403}, {"https://attacker.invalid", true, 403}, {cfg.Server.AppURL, true, 201}} {
		r := httptest.NewRequest("POST", "/api/messaging/rooms", strings.NewReader(`{"name":"Test"}`))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-KyMessages-Device", device.Token)
		if tc.csrf {
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
			r.Header.Set(auth.HeaderCSRF, "csrf")
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		messagingCode(t, w, tc.code)
	}
	messagingCode(t, messagingRequest(t, srv, "PUT", "/api/messaging/devices", session, device.Token, nil), 404)
	w := messagingRequest(t, srv, "GET", "/api/messaging/devices", session, "", nil)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private data cacheable")
	}
	user, err := st.Users().GetUserByID(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	user.Status = "inactive"
	if err := st.Users().UpdateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms", session, device.Token, nil), 401)
}

func TestMessagingEnrollmentAfterVerifiedOIDCCallback(t *testing.T) {
	_, st, cfg := setupTestServer(t)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code_verifier") == "" {
				http.Error(w, "missing PKCE verifier", 400)
				return
			}
			mu.Lock()
			token := tokens[r.Form.Get("code")]
			delete(tokens, r.Form.Get("code"))
			mu.Unlock()
			if token == "" {
				http.Error(w, "invalid code", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-only-access-token", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	cfg.SSO.KySignOnIssuer = issuer
	cfg.SSO.KySignOnClientID = "messaging-client"
	cfg.SSO.AutoProvision = true
	srv := api.NewServer(cfg, st)
	for _, validNonce := range []bool{false, true} {
		login := httptest.NewRecorder()
		srv.ServeHTTP(login, httptest.NewRequest("GET", "/api/sso/kysignon/login", nil))
		messagingCode(t, login, 302)
		authorize, err := url.Parse(login.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		nonce := authorize.Query().Get("nonce")
		if !validNonce {
			nonce = "wrong-nonce"
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
		claims, err := json.Marshal(map[string]any{"iss": issuer, "aud": "messaging-client", "sub": "oidc-alice", "preferred_username": "oidc-alice", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce})
		if err != nil {
			t.Fatal(err)
		}
		input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		code := crypto.RandomHex(16)
		mu.Lock()
		tokens[code] = input + "." + base64.RawURLEncoding.EncodeToString(signature)
		mu.Unlock()
		callback := httptest.NewRequest("GET", "/api/sso/kysignon/callback?code="+code+"&state="+authorize.Query().Get("state"), nil)
		for _, cookie := range login.Result().Cookies() {
			callback.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, callback)
		if !validNonce {
			messagingCode(t, w, 401)
			continue
		}
		messagingCode(t, w, 302)
		var session string
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				session = cookie.Value
			}
		}
		if session == "" {
			t.Fatal("callback did not issue a session")
		}
		device := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
		if device.Status != "approved" {
			t.Fatal(device.Status)
		}
		user, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "oidc-alice")
		if err != nil || user.PasswordHash != "" {
			t.Fatalf("unexpected SSO account: %v %v", user, err)
		}
	}
}

func TestMessagingDirectRoomBoundary(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	alice := messagingLogin(t, st, "direct-alice")
	bob := messagingLogin(t, st, "direct-bob")
	charlie := messagingLogin(t, st, "direct-charlie")
	a := verifyEnrollment(t, srv, alice, requestEnrollment(t, srv, alice))
	b := verifyEnrollment(t, srv, bob, requestEnrollment(t, srv, bob))
	c := verifyEnrollment(t, srv, charlie, requestEnrollment(t, srv, charlie))
	for _, invalid := range []struct {
		peer   string
		status int
	}{{"direct-alice", 409}, {"missing", 404}, {"bad\naccount", 400}, {strings.Repeat("x", 65), 400}} {
		messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms", alice, a.Token, map[string]string{"name": "Direct", "peer_user_id": invalid.peer}), invalid.status)
	}
	w := messagingRequest(t, srv, "GET", "/api/messaging/rooms", alice, a.Token, nil)
	if strings.Contains(w.Body.String(), `"id"`) {
		t.Fatal("failed creation left a room")
	}
	w = messagingRequest(t, srv, "POST", "/api/messaging/rooms", alice, a.Token, map[string]string{"name": "Direct", "peer_user_id": "direct-bob"})
	messagingCode(t, w, 201)
	var room struct {
		ID   string
		Peer string `json:"peer_user_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	if room.Peer != "direct-bob" {
		t.Fatal("missing direct binding", w.Body.String())
	}
	path := "/api/messaging/rooms/" + room.ID
	w = messagingRequest(t, srv, "GET", "/api/messaging/rooms", bob, b.Token, nil)
	if !strings.Contains(w.Body.String(), `"membership":"invited"`) || !strings.Contains(w.Body.String(), `"peer_user_id":"direct-bob"`) {
		t.Fatal(w.Body.String())
	}
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/delivery", bob, b.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", charlie, c.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", alice, a.Token, map[string]string{"user_id": "direct-charlie"}), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", bob, b.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", bob, b.Token, map[string]string{"user_id": "direct-charlie"}), 404)
	messagingCode(t, messagingRequest(t, srv, "DELETE", path+"/members/direct-bob", alice, a.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", alice, a.Token, map[string]string{"user_id": "direct-charlie"}), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", alice, a.Token, map[string]string{"user_id": "direct-bob"}), 200)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", bob, b.Token, nil), 200)
	if err := st.Users().DeleteUser(context.Background(), "direct-bob"); err != nil {
		t.Fatal(err)
	}
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", alice, a.Token, map[string]string{"user_id": "direct-charlie"}), 403)
}

// suspend clears a device's credential as restore-messages imports it.
func suspend(t *testing.T, cfg *config.Config, id string) {
	t.Helper()
	driver := cfg.Database.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `UPDATE messaging_devices SET token_hash = NULL WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// messagingSessionAt adds a suite session for an existing account, signed in at signedIn.
func messagingSessionAt(t *testing.T, st store.Store, user string, signedIn time.Time) string {
	t.Helper()
	token := crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(context.Background(), &store.Session{TokenHash: crypto.SHA256Hex([]byte(token)), UserID: user, CreatedAt: signedIn.UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	return token
}

func deviceStatusHTTP(t *testing.T, srv *api.Server, session, id string) string {
	t.Helper()
	w := messagingRequest(t, srv, "GET", "/api/messaging/devices", session, "", nil)
	messagingCode(t, w, 200)
	var list struct{ Devices []struct{ ID, Status string } }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, d := range list.Devices {
		if d.ID == id {
			return d.Status
		}
	}
	t.Fatalf("device %s missing", id)
	return ""
}

func TestMessagingResumeSuspendedDevice(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	other := messagingLogin(t, st, "alice")
	d := verifyEnrollment(t, srv, other, requestEnrollment(t, srv, other))
	pending := verifyEnrollment(t, srv, other, requestEnrollment(t, srv, other))
	suspend(t, cfg, d.ID)
	if got := deviceStatusHTTP(t, srv, other, d.ID); got != "suspended" {
		t.Fatal(got)
	}
	// The imported device's old credential is dead for reads and approvals.
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms/any/delivery", other, d.Token, nil), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+pending.ID+"/approve", other, d.Token, nil), 403)
	stale := messagingSessionAt(t, st, "alice", time.Now().Add(-11*time.Minute))

	token := crypto.RandomHex(32)
	body := map[string]string{"token_hash": crypto.SHA256Hex([]byte(token))}
	w := messagingRequest(t, srv, "POST", "/api/messaging/devices/"+d.ID+"/resume", stale, "", body)
	messagingCode(t, w, 403)
	if !strings.Contains(w.Body.String(), `"code":"reauthentication_required"`) || !strings.Contains(w.Body.String(), `"reauth_url":"/api/sso/kysignon/login?fresh=1"`) {
		t.Fatal(w.Body.String())
	}
	fresh := messagingSessionAt(t, st, "alice", time.Now())
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+d.ID+"/resume", fresh, "", map[string]string{"token_hash": "nothex"}), 400)
	w = messagingRequest(t, srv, "POST", "/api/messaging/devices/"+d.ID+"/resume", fresh, "", body)
	messagingCode(t, w, 200)
	var started struct {
		SigningInput string `json:"signing_input"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	input, err := base64.StdEncoding.DecodeString(started.SigningInput)
	if err != nil || !strings.Contains(string(input), `"Domain":"KyMessages resume v1"`) || !strings.Contains(string(input), d.ID) {
		t.Fatalf("%s %v", input, err)
	}
	if left := time.Until(time.Unix(started.ExpiresAt, 0)); left <= 4*time.Minute || left > 5*time.Minute {
		t.Fatal(left)
	}
	d.Input = started.SigningInput
	path := "/api/messaging/devices/" + d.ID + "/resume/verify"
	wrong := d
	_, wrong.Key, _ = ed25519.GenerateKey(rand.Reader)
	messagingCode(t, messagingRequest(t, srv, "POST", path, fresh, "", enrollmentProof(t, wrong)), 403)
	if got := deviceStatusHTTP(t, srv, fresh, d.ID); got != "suspended" {
		t.Fatal(got)
	}
	// The resume is bound to the session that started it.
	messagingCode(t, messagingRequest(t, srv, "POST", path, other, "", enrollmentProof(t, d)), 404)
	w = messagingRequest(t, srv, "POST", path, fresh, "", enrollmentProof(t, d))
	messagingCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"status":"approved"`) {
		t.Fatal(w.Body.String())
	}
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms", fresh, token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms", fresh, d.Token, nil), 403)
	messagingCode(t, messagingRequest(t, srv, "POST", path, fresh, "", enrollmentProof(t, d)), 404)
	list := messagingRequest(t, srv, "GET", "/api/messaging/devices", fresh, "", nil)
	for _, forbidden := range []string{token, crypto.SHA256Hex([]byte(token)), "resume_token_hash", "token_hash"} {
		if strings.Contains(list.Body.String(), forbidden) {
			t.Fatalf("list leaked %q", forbidden)
		}
	}
}

func TestMessagingAdminSuspendedDevices(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	alice := messagingLogin(t, st, "alice")
	d := verifyEnrollment(t, srv, alice, requestEnrollment(t, srv, alice))
	bob := messagingLogin(t, st, "bob")
	live := verifyEnrollment(t, srv, bob, requestEnrollment(t, srv, bob))
	suspend(t, cfg, d.ID)
	admin := loginAs(t, srv, st, "operator", "admin")
	messagingCode(t, adminDo(t, srv, admin, "GET", "/api/admin/messaging/devices", nil), 400)
	w := adminDo(t, srv, admin, "GET", "/api/admin/messaging/devices?status=suspended", nil)
	messagingCode(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("admin device list cacheable")
	}
	var list struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 1 || list.Devices[0]["id"] != d.ID || list.Devices[0]["username"] != "alice" || list.Devices[0]["fingerprint"] == "" {
		t.Fatal(w.Body.String())
	}
	for _, forbidden := range []string{"public_key", "token", "challenge", "session"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("admin list leaked %q: %s", forbidden, w.Body.String())
		}
	}
	messagingCode(t, adminDo(t, srv, admin, "POST", "/api/admin/messaging/devices/"+live.ID+"/revoke", nil), 404)
	messagingCode(t, messagingRequest(t, srv, "GET", "/api/messaging/rooms", bob, live.Token, nil), 200)
	messagingCode(t, adminDo(t, srv, admin, "POST", "/api/admin/messaging/devices/"+d.ID+"/revoke", nil), 200)
	if got := deviceStatusHTTP(t, srv, alice, d.ID); got != "revoked" {
		t.Fatal(got)
	}
	if rows := auditRows(t, st, "messaging.device_revoked_by_admin"); len(rows) != 1 || rows[0].Resource != d.ID || rows[0].UserID != "usr_operator" {
		t.Fatalf("%+v", rows)
	}
	// Revoking needs a recent sign-in.
	operator, err := st.Users().GetUserByID(context.Background(), "usr_operator")
	if err != nil {
		t.Fatal(err)
	}
	stale := crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(context.Background(), &store.Session{TokenHash: crypto.SHA256Hex([]byte(stale)), UserID: operator.ID, CreatedAt: time.Now().UTC().Add(-11 * time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour)}, operator.PasswordHash); err != nil {
		t.Fatal(err)
	}
	staleCookie := &http.Cookie{Name: auth.SessionCookieName, Value: stale}
	w = adminDo(t, srv, staleCookie, "POST", "/api/admin/messaging/devices/"+d.ID+"/revoke", nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
