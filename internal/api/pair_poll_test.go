package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func pendingPairing(t *testing.T, st store.Store) string {
	t.Helper()
	createLocalUser(t, st, "usr_boss", "boss", "admin", "BossPassword123!")
	boss, err := st.Users().GetUserByID(context.Background(), "usr_boss")
	if err != nil {
		t.Fatal(err)
	}
	secret := crypto.RandomHex(24)
	if err := st.Devices().CreatePairing(context.Background(), &store.DevicePairing{
		Secret: secret, UserID: "usr_boss", Status: "pending",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(90 * time.Second),
	}, boss.PasswordHash); err != nil {
		t.Fatal(err)
	}
	return secret
}

// The poll secret is the credential pair/verify redeems for a session, so it must never travel
// in a URL, where proxy and access logs keep it.
func TestPairPollRefusesTheSecretInTheURL(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	secret := pendingPairing(t, st)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/devices/pair/poll?secret="+secret, nil))
	if w.Code != http.StatusMethodNotAllowed || strings.Contains(w.Body.String(), "pending") {
		t.Fatalf("GET with the secret: got %d %s, want 405 and no status", w.Code, w.Body.String())
	}
	if w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("Allow: got %q, want POST", w.Header().Get("Allow"))
	}

	// A POST that still puts the secret in the query is refused too: only the body counts.
	q := postJSON(t, srv, "/api/devices/pair/poll?secret="+secret, "192.0.2.10:1000", map[string]string{})
	if q.Code != http.StatusBadRequest {
		t.Fatalf("POST with the secret in the query: got %d, want 400", q.Code)
	}

	w = postJSON(t, srv, "/api/devices/pair/poll", "192.0.2.10:1000", map[string]string{"secret": secret})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pending"`) {
		t.Fatalf("POST with the secret in the body: got %d %s", w.Code, w.Body.String())
	}
}

func TestPairPollBodyIsStrict(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	secret := pendingPairing(t, st)
	for _, body := range []string{`{"secret":"` + secret + `","x":1}`, `{"secret":"` + secret + `"}{}`, `not json`} {
		req := httptest.NewRequest("POST", "/api/devices/pair/poll", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, w.Code)
		}
	}
	// Oversized bodies never reach the store.
	big := `{"secret":"` + strings.Repeat("a", 8<<10) + `"}`
	req := httptest.NewRequest("POST", "/api/devices/pair/poll", strings.NewReader(big))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: got %d, want 400", w.Code)
	}
}

// The signed-in browser polls with its session cookie, so the CSRF check applies.
func TestPairPollFromASessionNeedsCSRF(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	secret := pendingPairing(t, st)
	cookie := loginAs(t, srv, st, "alice", "admin")
	body := []byte(`{"secret":"` + secret + `"}`)

	req := httptest.NewRequest("POST", "/api/devices/pair/poll", bytes.NewReader(body))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no CSRF token: got %d, want 403", w.Code)
	}

	req = httptest.NewRequest("POST", "/api/devices/pair/poll", bytes.NewReader(body))
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
	req.Header.Set(auth.HeaderCSRF, "test-csrf")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("with CSRF token: got %d %s", w.Code, w.Body.String())
	}
}
