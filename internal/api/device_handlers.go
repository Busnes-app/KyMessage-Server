package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/devices"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type VerifyDeviceRequest struct {
	Secret     string `json:"secret"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	PushToken  string `json:"push_token,omitempty"`
}

func (s *Server) handlePairInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user, _, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !s.allowAccountAttempt("pair-init:"+user.ID, 10, time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many pairing requests")
		return
	}

	res, err := s.pairing.InitPairing(r.Context(), user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to initialize pairing")
		return
	}

	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) handlePairVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req VerifyDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !s.allowClientAttempt("pair", r, 10, time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many pairing attempts")
		return
	}
	if len(req.DeviceName) > 255 || len(req.Platform) > 32 || len(req.PushToken) > 4096 {
		s.writeError(w, http.StatusBadRequest, "Device fields are too long")
		return
	}

	pairing, user, err := s.pairing.VerifyPairing(r.Context(), req.Secret, req.DeviceName, req.Platform, req.PushToken)
	if errors.Is(err, devices.ErrPairingNotFound) {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Pairing failed")
		return
	}
	_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{
		UserID:    user.ID,
		Action:    "device.paired",
		Resource:  "session",
		Details:   "platform=" + auditValue(req.Platform),
		IPAddress: s.requestIP(r),
	})

	// Issue session token for the mobile device if pairing had user
	var sessionToken string
	if pairing.UserID != "" {
		_, rawToken, err := s.sessions.IssueSession(r.Context(), w, r, user)
		if err == nil {
			sessionToken = rawToken
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"paired":        true,
		"session_token": sessionToken,
		"device_name":   pairing.DeviceName,
	})
}

func (s *Server) handlePairPoll(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		s.writeError(w, http.StatusBadRequest, "secret query parameter required")
		return
	}

	pairing, err := s.pairing.PollPairingStatus(r.Context(), secret)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "Pairing not found or expired")
		return
	}

	// The poll route is unauthenticated: project, never marshal the record. Secret, user_id
	// and push_token stay on the server.
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":      pairing.Status,
		"expires_at":  pairing.ExpiresAt.Unix(),
		"device_name": pairing.DeviceName,
	})
}
