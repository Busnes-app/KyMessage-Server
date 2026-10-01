package api

import "net/http"

const wellKnownMatrixClient = "/.well-known/matrix/client"

// handleMatrixClientWellKnown tells Matrix clients where the homeserver is. Any origin may read
// it (the spec requires CORS *), so it carries no credentials.
func (s *Server) handleMatrixClientWellKnown(w http.ResponseWriter, r *http.Request) {
	// Served on the apex too; never pin HSTS there.
	w.Header().Del("Strict-Transport-Security")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Del("Access-Control-Allow-Credentials")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With, Content-Type, Authorization")
		w.WriteHeader(http.StatusOK)
		return
	}
	if s.config.Matrix.Host == "" {
		s.writeError(w, http.StatusNotFound, "Matrix is not configured")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Del("Access-Control-Allow-Credentials")
	type homeserver struct {
		BaseURL string `json:"base_url"`
	}
	s.writeJSON(w, http.StatusOK, map[string]homeserver{"m.homeserver": {BaseURL: s.config.Matrix.Host}})
}
