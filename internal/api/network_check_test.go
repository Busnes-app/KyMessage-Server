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
	"github.com/Busnes-app/ky_server_base/internal/config"
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
	want := map[string]any{"peer_ip": "10.91.0.10", "client_ip": "203.0.113.9", "forwarded_trusted": true, "forwarded_proto": "https", "app_url_https": true, "host_matches": true, "trusted_proxies_narrow": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNetworkCheckIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "http://localhost:8080"
	cfg.Security.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.91.0.10/32")}
	session := loginAs(t, srv, st, "net-admin2", "admin")
	got := networkCheck(t, srv, session, "192.0.2.7:4444", "evil.example", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"})
	want := map[string]any{"peer_ip": "192.0.2.7", "client_ip": "192.0.2.7", "forwarded_trusted": false, "forwarded_proto": "", "app_url_https": false, "host_matches": false, "trusted_proxies_narrow": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNetworkCheckHostMatchUsesSchemeDefaultPort(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "https://chat.example.com"
	session := loginAs(t, srv, st, "net-admin3", "admin")
	for host, want := range map[string]bool{"chat.example.com:443": true, "CHAT.example.com": true, "chat.example.com:8443": false} {
		got := networkCheck(t, srv, session, "192.0.2.7:4444", host, nil)
		if got["host_matches"] != want {
			t.Errorf("host %q: host_matches = %v, want %v", host, got["host_matches"], want)
		}
	}
}

func TestNetworkCheckFlagsTrustedProxySubnets(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	session := loginAs(t, srv, st, "net-admin4", "admin")
	for raw, want := range map[string]bool{
		"":                           false,
		"10.91.0.10":                 true,
		"10.91.0.10/32, 2001:db8::1": true,
		"2001:db8::1/128":            true,
		"10.91.0.10, 10.91.0.0/24":   false,
		"2001:db8::/64":              false,
		"::ffff:10.91.0.10/128":      true,
	} {
		prefixes, err := config.ParseTrustedProxies(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		cfg.Security.TrustedProxies = prefixes
		got := networkCheck(t, srv, session, "192.0.2.7:4444", "localhost", nil)
		if got["trusted_proxies_narrow"] != want {
			t.Errorf("KY_TRUSTED_PROXIES=%q: trusted_proxies_narrow = %v, want %v", raw, got["trusted_proxies_narrow"], want)
		}
	}
}
