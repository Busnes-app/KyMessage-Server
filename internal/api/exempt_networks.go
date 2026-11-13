package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/netallow"
)

const exemptNetworksSetting = "rate_limit_exempt_networks"

// combineExemptNetworks parses the environment's networks and the admin's as one list.
func (s *Server) combineExemptNetworks(stored string) (*netallow.List, error) {
	return netallow.Parse(s.config.Security.RateLimitExemptNetworks + "," + stored)
}

// loadExemptNetworks builds the allowlist at startup. A stored value that cannot be read or no
// longer parses is dropped with a log line, leaving the environment's list: fewer exemptions,
// never more.
func (s *Server) loadExemptNetworks() {
	stored, err := s.setting(context.Background(), exemptNetworksSetting)
	var list *netallow.List
	if err == nil {
		list, err = s.combineExemptNetworks(stored)
	}
	if err != nil {
		log.Printf("[SECURITY] stored rate-limit exempt networks ignored: %v", err)
		list, _ = s.combineExemptNetworks("")
	}
	s.exempt.Store(list)
}

func (s *Server) exemptNetworksStatus(stored string) map[string]any {
	return map[string]any{
		"networks":    stored,
		"environment": s.config.Security.RateLimitExemptNetworks,
		"exempted":    s.exempt.Load().Exempted(),
	}
}

func (s *Server) handleGetExemptNetworks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	stored, err := s.setting(r.Context(), exemptNetworksSetting)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to load settings")
		return
	}
	s.writeJSON(w, http.StatusOK, s.exemptNetworksStatus(stored))
}

// handleSetExemptNetworks replaces the admin's half of the allowlist; the environment's half is
// not editable here. Adding a network needs a recent sign-in, like widening federation; a
// change that only removes networks does not. The exempted count restarts with the new list.
func (s *Server) handleSetExemptNetworks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, sess, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req struct {
		Networks string `json:"networks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid JSON request body")
		return
	}
	own, err := netallow.Parse(req.Networks)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "netallow: "))
		return
	}
	stored := own.String()
	combined, err := s.combineExemptNetworks(stored)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "netallow: "))
		return
	}

	ctx := context.WithoutCancel(r.Context()) // the saved list and its audit row land together
	s.exemptMu.Lock()
	defer s.exemptMu.Unlock()
	previous, err := s.setting(ctx, exemptNetworksSetting)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to load settings")
		return
	}
	if addsNetwork(previous, stored) && time.Since(sess.CreatedAt) > stepUpWindow {
		s.writeStepUp(w, user)
		return
	}
	if err := s.store.Settings().SetSetting(ctx, exemptNetworksSetting, stored); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to save the networks")
		return
	}
	s.exempt.Store(combined)
	s.audit(ctx, user.ID, r, "admin.rate_limit_networks", "settings", fmt.Sprintf("from=%q to=%q", previous, stored))
	s.writeJSON(w, http.StatusOK, s.exemptNetworksStatus(stored))
}

// addsNetwork reports whether next, a canonical list, holds a network previous does not hold
// exactly. A narrower range inside an old one counts as added: the check stays conservative.
func addsNetwork(previous, next string) bool {
	old := strings.Split(previous, ",")
	for _, n := range strings.Split(next, ",") {
		if n != "" && !slices.Contains(old, n) {
			return true
		}
	}
	return false
}
