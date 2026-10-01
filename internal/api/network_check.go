package api

import (
	"net/http"
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
		"host_matches":      err == nil && strings.EqualFold(r.Host, app.Host),
	})
}
