package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
)

func networkCheck(t *testing.T, srv *api.Server, session *http.Cookie, remote, host string, headers map[string]string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/admin/network-check", nil)
	req.RemoteAddr = remote
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.AddCookie(session)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
	req.Header.Set(auth.HeaderCSRF, "test-csrf")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("network-check: got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNetworkCheckTrustsForwardedHeadersOnlyFromTrustedPeer(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "https://chat.example.com"
	cfg.Security.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.91.0.10/32")}
	session := loginAs(t, srv, st, "net-admin", "admin")
	got := networkCheck(t, srv, session, "10.91.0.10:5555", "chat.example.com", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"})
	want := map[string]any{"peer_ip": "10.91.0.10", "client_ip": "203.0.113.9", "forwarded_trusted": true, "forwarded_proto": "https", "app_url_https": true, "host_matches": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNetworkCheckIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "http://localhost:8080"
	session := loginAs(t, srv, st, "net-admin2", "admin")
	got := networkCheck(t, srv, session, "192.0.2.7:4444", "evil.example", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"})
	want := map[string]any{"peer_ip": "192.0.2.7", "client_ip": "192.0.2.7", "forwarded_trusted": false, "forwarded_proto": "", "app_url_https": false, "host_matches": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
