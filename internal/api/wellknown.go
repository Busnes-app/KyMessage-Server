package api

import "net/http"

// handleMatrixClientWellKnown tells Matrix clients where the homeserver is. Any origin may read
// it (the spec requires CORS *), so it carries no credentials.
func (s *Server) handleMatrixClientWellKnown(w http.ResponseWriter, r *http.Request) {
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
