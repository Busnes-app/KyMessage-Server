package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// handleSyncStatus reports KyIdentity's directory sync as KyMessages sees it: the last
// acknowledged webhook and the last sweep, each written only by its owner, and the deliveries
// refused since this process started (memory only: their senders are unauthenticated). The
// page derives the hints.
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	var webhook *sso.WebhookRecord
	var sweep *matrixsync.SweepRecord
	for _, rec := range []struct {
		key  string
		into any
	}{{sso.WebhookRecordKey, &webhook}, {matrixsync.SweepRecordKey, &sweep}} {
		raw, err := s.store.Settings().GetSetting(r.Context(), rec.key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Could not read the sync status")
			return
		}
		_ = json.Unmarshal([]byte(raw), rec.into) // an unreadable record shows as none
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"webhook":        webhook,
		"rejected":       s.kyidentity.Rejections(),
		"sweep":          sweep,
		"kyidentity_url": s.config.SSO.KyIdentityIssuer,
	})
}
