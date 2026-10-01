package api

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Busnes-app/ky_server_base/internal/auth"
)

// handleNetworkCheck shows an admin how a request reached the server, so a proxy setup
// can be confirmed instead of assumed. Admin-only: it describes the deployment's wiring.
func (s *Server) handleNetworkCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	trusted := auth.TrustedPeer(r, s.config.Security.TrustedProxies)
	proto := ""
	if trusted {
		proto = r.Header.Get("X-Forwarded-Proto")
	}
	app, err := url.Parse(s.config.Server.AppURL)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"peer_ip":           auth.PeerIP(r),
		"client_ip":         s.requestIP(r),
		"forwarded_trusted": trusted,
		"forwarded_proto":   proto,
		"app_url_https":     err == nil && strings.EqualFold(app.Scheme, "https"),
		"host_matches":      err == nil && hostMatches(r.Host, app),
		// Any address inside a trusted subnet can forge X-Forwarded-For.
		"trusted_proxies_narrow": singleAddresses(s.config.Security.TrustedProxies),
	})
}

func singleAddresses(prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Bits() != p.Addr().BitLen() {
			return false
		}
	}
	return len(prefixes) > 0
}

// hostMatches compares host names case-insensitively and ports with the scheme's default
// filled in, so "chat.example.com:443" matches an https KY_APP_URL.
func hostMatches(reqHost string, app *url.URL) bool {
	host, port, err := net.SplitHostPort(reqHost)
	if err != nil {
		host, port = strings.Trim(reqHost, "[]"), ""
	}
	def := func(p string) string {
		if p != "" {
			return p
		}
		if strings.EqualFold(app.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(host, app.Hostname()) && def(port) == def(app.Port())
}
