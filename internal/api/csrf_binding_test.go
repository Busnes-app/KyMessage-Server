package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
)

// signIn logs an existing loginAs user in again: a second, independent session.
func signIn(t *testing.T, srv *api.Server, username string, extra ...*http.Cookie) (session, csrf string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": "SuperSecretPass123!"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range extra {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sign in %s: %d %s", username, w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookieName:
			session = c.Value
		case auth.CSRFCookieName:
			csrf = c.Value
		}
	}
	if session == "" || csrf == "" {
		t.Fatalf("sign in %s: cookies missing", username)
	}
	return session, csrf
}

// A CSRF token must only work for the session it was issued to. Before it was bound, any
// equal ky_csrf cookie and header passed, so a sibling subdomain that could toss a cookie
// could forge writes for any signed-in user.
func TestCSRFTokenIsBoundToSession(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	loginAs(t, srv, st, "alice", "admin") // the theme write is an admin route
	loginAs(t, srv, st, "mallory", "admin")
	victim, victimCSRF := signIn(t, srv, "alice")
	_, otherCSRF := signIn(t, srv, "alice") // another session of the same account
	_, attackerCSRF := signIn(t, srv, "mallory")
	if victimCSRF == otherCSRF {
		t.Fatal("two sessions share a CSRF token")
	}

	write := func(cookie, header string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/theme", strings.NewReader(`{"theme":"oled"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: victim})
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: cookie})
		}
		if header != "" {
			req.Header.Set(auth.HeaderCSRF, header)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}

	for name, got := range map[string]int{
		"attacker's own pair tossed into the victim's browser": write(attackerCSRF, attackerCSRF),
		"pair from another session of the same account":        write(otherCSRF, otherCSRF),
		"victim cookie, other session's header":                write(victimCSRF, otherCSRF),
		"missing header":                                       write(victimCSRF, ""),
		"forged double-submit pair":                            write("forged", "forged"),
		"forged token of the right shape":                      write("", strings.Repeat("0", len(victimCSRF))),
		"raw session token as the CSRF token":                  write("", victim),
	} {
		if got != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, got)
		}
	}
	if got := write(victimCSRF, victimCSRF); got != http.StatusOK {
		t.Fatalf("legitimate write: status %d", got)
	}
	// The header alone carries the proof; the cookie only tells the web app the value.
	if got := write("", victimCSRF); got != http.StatusOK {
		t.Fatalf("legitimate write without the CSRF cookie: status %d", got)
	}
}

// Sign-in creates the session, so it carries no token; it stays reachable with a stale or
// planted session cookie, and a cross-site form still cannot post it.
func TestSignInNeedsNoCSRFTokenButStillRequiresJSON(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	loginAs(t, srv, st, "alice", "user")
	stale := &http.Cookie{Name: auth.SessionCookieName, Value: "stale-or-planted"}
	signIn(t, srv, "alice", stale)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"alice","password":"SuperSecretPass123!"}`))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form-style sign-in: status %d, want 415", w.Code)
	}
}

// A session minted before tokens were bound holds a random ky_csrf; reading /api/auth/me
// (what the web app does on load) hands it the bound one.
func TestMeRefreshesCSRFCookieToTheBoundToken(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	loginAs(t, srv, st, "alice", "user")
	session, csrf := signIn(t, srv, "alice")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "pre-upgrade-random-value"})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var refreshed string
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CSRFCookieName {
			refreshed = c.Value
			if c.HttpOnly {
				t.Fatal("refreshed CSRF cookie is HttpOnly")
			}
		}
	}
	if w.Code != http.StatusOK || refreshed != csrf {
		t.Fatalf("me: status %d, refreshed %q, want %q", w.Code, refreshed, csrf)
	}
}
