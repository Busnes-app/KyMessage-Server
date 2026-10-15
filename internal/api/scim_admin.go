package api

import (
	"net/http"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

type scimUserView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Status      string `json:"status"`
}

// handleSCIMUsers lists the accounts SCIM provisioned, read from the store: /scim/v2 needs the
// provider's bearer token, which the console never holds. token_set says whether
// KY_SCIM_TOKEN is configured; the token itself never leaves the process.
func (s *Server) handleSCIMUsers(w http.ResponseWriter, r *http.Request) {
	const page = 500
	users := []scimUserView{}
	for offset := 0; ; offset += page {
		rows, total, err := s.store.Users().ListUsers(r.Context(), offset, page, store.UserFilter{})
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Failed to list users")
			return
		}
		for _, u := range rows {
			if u.SSOProvider == "scim" {
				users = append(users, scimUserView{u.ID, u.Username, u.DisplayName, u.Email, u.Status})
			}
		}
		if len(rows) < page || offset+len(rows) >= total {
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   s.config.SCIM.Enabled,
		"token_set": s.config.SCIM.Enabled && s.config.SCIM.TokenSet,
		"users":     users,
	})
}
