package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
)

const (
	officePeer  = "203.0.113.5:41000" // inside officeRange
	officeRange = "203.0.113.0/24"
	outsidePeer = "198.51.100.9:41000"
	networksAPI = "/api/settings/rate-limit-networks"
)

func putNetworks(t *testing.T, srv *api.Server, session *http.Cookie, networks string) *httptest.ResponseRecorder {
	t.Helper()
	return adminDo(t, srv, session, "PUT", networksAPI, map[string]string{"networks": networks})
}

func networksStatus(t *testing.T, w *httptest.ResponseRecorder) (networks, environment string, exempted float64) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out["networks"].(string), out["environment"].(string), out["exempted"].(float64)
}

// limited fires 25 failed sign-ins from peer and reports whether any was refused with 429.
func limited(t *testing.T, srv *api.Server, peer string) bool {
	t.Helper()
	for i := 0; i < 25; i++ {
		if loginStatus(t, srv, peer, "") == http.StatusTooManyRequests {
			return true
		}
	}
	return false
}

// An allowlisted office goes past the per-client window; everyone else keeps it.
func TestExemptNetworkSkipsThePerIPLoginLimit(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 60; i++ {
		if code := loginStatus(t, srv, officePeer, ""); code != http.StatusUnauthorized {
			t.Fatalf("office attempt %d answered %d, want the wrong-password 401", i, code)
		}
	}
	for i := 1; i <= 20; i++ {
		if code := loginStatus(t, srv, outsidePeer, ""); code == http.StatusTooManyRequests {
			t.Fatalf("outside attempt %d throttled early", i)
		}
	}
	if code := loginStatus(t, srv, outsidePeer, ""); code != http.StatusTooManyRequests {
		t.Fatalf("outside attempt 21 answered %d, want 429", code)
	}
	for _, k := range api.AttemptKeysForTest(srv) {
		if strings.HasPrefix(k, "login:203.0.113.") {
			t.Fatalf("an exempt request opened a per-IP window: %q", k)
		}
	}
}

// The exemption covers the per-IP window only: ten wrong guesses at one account from the
// office still close that account's window there, so guessing stays bounded per account.
func TestExemptNetworkKeepsThePerAccountWindow(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	createLocalUser(t, st, "usr_carol", "carol", "user", "SuperSecretPass123!")
	good := map[string]string{"username": "carol", "password": "SuperSecretPass123!"}
	wrong := map[string]string{"username": "carol", "password": "WrongPass123!"}
	for i := 1; i <= 10; i++ {
		if w := postJSON(t, srv, "/api/auth/login", officePeer, wrong); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, w.Code)
		}
	}
	for i := 1; i <= 30; i++ {
		if w := postJSON(t, srv, "/api/auth/login", officePeer, good); w.Code != http.StatusUnauthorized {
			t.Fatalf("exhausted account window from the office answered %d at attempt %d, want 401", w.Code, i)
		}
	}
}

// With no trusted proxy, X-Forwarded-For is the caller's own claim and buys no exemption.
func TestForgedForwardedForCannotClaimAnExemptNetwork(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 20; i++ {
		loginStatus(t, srv, outsidePeer, "203.0.113.5")
	}
	if code := loginStatus(t, srv, outsidePeer, "203.0.113.5"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 answered %d, want 429: a forged header claimed the office", code)
	}
}

// Behind a trusted proxy the forwarded client is the one matched, and a client that prepends an
// office address to the chain is still matched on the entry the proxy appended.
func TestExemptNetworkMatchesTheClientATrustedProxyReports(t *testing.T) {
	t.Setenv("KY_TRUSTED_PROXIES", "192.0.2.0/24")
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 30; i++ {
		if code := loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5"); code == http.StatusTooManyRequests {
			t.Fatalf("office client behind the proxy throttled at attempt %d", i)
		}
	}
	for i := 1; i <= 20; i++ {
		loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5, 198.51.100.9")
	}
	if code := loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5, 198.51.100.9"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 answered %d, want 429: a prepended office address was believed", code)
	}
}

// Password change: the office skips the per-IP window (10 a minute) but each account keeps its
// own (5 a minute).
func TestExemptNetworkPasswordChangeKeepsTheAccountLimit(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	change := func(session *http.Cookie, peer string) int {
		req := httptest.NewRequest("POST", "/api/auth/change-password", strings.NewReader(`{"current_password":"nope","new_password":"AnotherPass456!"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = peer
		req.AddCookie(session)
		addCSRF(t, req)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	// Three accounts, four tries each: twelve from one office address, over the per-IP ten.
	for _, name := range []string{"dave", "erin", "fred"} {
		session := loginAs(t, srv, st, name, "user")
		for i := 1; i <= 4; i++ {
			if code := change(session, officePeer); code == http.StatusTooManyRequests {
				t.Fatalf("%s attempt %d from the office throttled", name, i)
			}
		}
	}
	gina := loginAs(t, srv, st, "gina", "user")
	for i := 1; i <= 5; i++ {
		if code := change(gina, officePeer); code == http.StatusTooManyRequests {
			t.Fatalf("gina attempt %d throttled early", i)
		}
	}
	if code := change(gina, officePeer); code != http.StatusTooManyRequests {
		t.Fatalf("gina attempt 6 answered %d, want 429 from the per-account limit", code)
	}

	// The same twelve from an address outside the list hit the per-IP window.
	refused := false
	for _, name := range []string{"hank", "iris", "jack"} {
		session := loginAs(t, srv, st, name, "user")
		for i := 1; i <= 4; i++ {
			refused = refused || change(session, outsidePeer) == http.StatusTooManyRequests
		}
	}
	if !refused {
		t.Fatal("twelve password changes from one outside address were never throttled")
	}
}

// An admin's list takes effect at once, is stored canonically, audited, counted, and still there
// after a restart; the environment's list stays alongside it.
func TestAdminSetsExemptNetworks(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", "198.18.7.0/24")
	srv, st, cfg := setupTestServer(t)
	admin := loginAs(t, srv, st, "alice", "admin")

	networks, environment, exempted := networksStatus(t, do(t, srv, "GET", networksAPI, admin))
	if networks != "" || environment != "198.18.7.0/24" || exempted != 0 {
		t.Fatalf("before: networks=%q environment=%q exempted=%v", networks, environment, exempted)
	}

	networks, _, _ = networksStatus(t, putNetworks(t, srv, admin, " 203.0.113.77/24 , 2001:db8:1::/48"))
	if networks != "203.0.113.0/24,2001:db8:1::/48" {
		t.Fatalf("stored %q, want the canonical list", networks)
	}
	if limited(t, srv, officePeer) || limited(t, srv, "198.18.7.50:41000") {
		t.Fatal("an allowlisted network was throttled")
	}
	if !limited(t, srv, outsidePeer) {
		t.Fatal("an address outside the list was not throttled")
	}
	if _, _, exempted = networksStatus(t, do(t, srv, "GET", networksAPI, admin)); exempted != 50 {
		t.Fatalf("exempted = %v, want 50", exempted)
	}

	records, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, rec := range records {
		if rec.Action == "admin.rate_limit_networks" && rec.UserID == "usr_alice" &&
			rec.Details == `from="" to="203.0.113.0/24,2001:db8:1::/48"` {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("no audit row for the change in %d records", len(records))
	}

	if restarted := api.NewServer(cfg, st); limited(t, restarted, officePeer) {
		t.Fatal("the stored list was not loaded at startup")
	}

	// Clearing it restores the limit for the office; the environment's network stays exempt.
	if networks, _, _ = networksStatus(t, putNetworks(t, srv, admin, "")); networks != "" {
		t.Fatalf("cleared list reads %q", networks)
	}
	if !limited(t, srv, "203.0.113.200:41000") {
		t.Fatal("a removed network is still exempt")
	}
	if limited(t, srv, "198.18.7.51:41000") {
		t.Fatal("clearing the admin list dropped the environment's network")
	}
}

// A list that would switch the limit off widely is refused and nothing changes.
func TestExemptNetworksRefuseBroadOrInvalidRanges(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "alice", "admin")
	if w := putNetworks(t, srv, admin, officeRange); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8", "2001:db8::/32", "office", "127.0.0.1", "203.0.113.0/24,nope"} {
		w := putNetworks(t, srv, admin, bad)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %q answered %d, want 400", bad, w.Code)
		}
		if strings.Contains(w.Body.String(), "netallow:") {
			t.Errorf("PUT %q: refusal carries the package prefix: %s", bad, w.Body.String())
		}
	}
	if networks, _, _ := networksStatus(t, do(t, srv, "GET", networksAPI, admin)); networks != officeRange {
		t.Fatalf("a refused list replaced the stored one: %q", networks)
	}
	if !limited(t, srv, "10.9.9.9:41000") {
		t.Fatal("an address in a refused range was not throttled")
	}
}

// Adding a network needs a recent sign-in; removing one does not.
func TestAddingExemptNetworksNeedsAFreshSignIn(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	fresh := loginAs(t, srv, st, "alice", "admin")
	if w := putNetworks(t, srv, fresh, officeRange+",198.18.7.0/24"); w.Code != http.StatusOK {
		t.Fatalf("fresh admin: %d %s", w.Code, w.Body.String())
	}
	stale := staleAdmin(t, st)
	for _, add := range []string{officeRange + ",198.18.7.0/24,192.0.2.0/24", "203.0.113.0/25"} {
		if w := putNetworks(t, srv, stale, add); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
			t.Fatalf("stale admin adding %q: %d %s", add, w.Code, w.Body.String())
		}
	}
	if !limited(t, srv, "192.0.2.50:41000") {
		t.Fatal("a refused addition took effect")
	}
	if networks, _, _ := networksStatus(t, putNetworks(t, srv, stale, "198.18.7.0/24")); networks != "198.18.7.0/24" {
		t.Fatalf("stale admin removing a network stored %q", networks)
	}
	if !limited(t, srv, officePeer) {
		t.Fatal("the removed office network is still exempt")
	}
}

// A stored list that no longer parses (written by an older release or by hand) is dropped at
// startup in favour of the environment's list: fewer exemptions, never more.
func TestStartupDropsAnUnparsableStoredList(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", "198.18.7.0/24")
	_, st, cfg := setupTestServer(t)
	if err := st.Settings().SetSetting(context.Background(), "rate_limit_exempt_networks", "10.0.0.0/8,"+officeRange); err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(cfg, st)
	if !limited(t, srv, "10.1.2.3:41000") || !limited(t, srv, officePeer) {
		t.Fatal("an unparsable stored list exempted an address")
	}
	if limited(t, srv, "198.18.7.9:41000") {
		t.Fatal("the environment's list was lost with the stored one")
	}
}

// TestPrivilegedEndpointsRequireAdmin covers both methods for anonymous and member callers;
// this pins that a member learns nothing and changes nothing.
func TestMembersCannotReadExemptNetworks(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	member := loginAs(t, srv, st, "bob", "user")
	if w := do(t, srv, "GET", networksAPI, member); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "203.0.113") {
		t.Fatalf("member read: %d %s", w.Code, w.Body.String())
	}
	if w := putNetworks(t, srv, member, "198.18.7.0/24"); w.Code != http.StatusForbidden {
		t.Fatalf("member write: %d %s", w.Code, w.Body.String())
	}
	if !limited(t, srv, "198.18.7.9:41000") {
		t.Fatal("a member's refused write took effect")
	}
}
