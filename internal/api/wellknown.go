package api

import "net/http"

const (
	wellKnownMatrixClient = "/.well-known/matrix/client"
	wellKnownMatrixServer = "/.well-known/matrix/server"
)

// publicDiscovery makes a Matrix discovery file readable from any origin (the spec requires
// CORS *) with no credentials and no HSTS (it is served on the apex too). It answers a
// preflight itself and then reports true.
func publicDiscovery(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Del("Strict-Transport-Security")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Del("Access-Control-Allow-Credentials")
	if r.Method != http.MethodOptions {
		return false
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With, Content-Type, Authorization")
	w.WriteHeader(http.StatusOK)
	return true
}

// handleMatrixClientWellKnown tells Matrix clients where the homeserver is.
func (s *Server) handleMatrixClientWellKnown(w http.ResponseWriter, r *http.Request) {
	if publicDiscovery(w, r) {
		return
	}
	if s.config.Matrix.Host == "" {
		s.writeError(w, http.StatusNotFound, "Matrix is not configured")
		return
	}
	type homeserver struct {
		BaseURL string `json:"base_url"`
	}
	out := map[string]any{"m.homeserver": homeserver{BaseURL: s.config.Matrix.Host}}
	if s.config.Matrix.RTCHost != "" {
		out["org.matrix.msc4143.rtc_foci"] = []map[string]string{{"type": "livekit", "livekit_service_url": s.config.Server.AppURL + rtcPrefix}}
	}
	s.writeJSON(w, http.StatusOK, out)
}
