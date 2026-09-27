package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type recoveryAuthState struct {
	Request                       sso.ReauthenticationRequest
	UserID, SessionHash, DeviceID string
	ResetConfirmed                bool
}

func (s *Server) handleMessagingRecoveryAuth(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var input struct {
		ConfirmIdentityReset bool `json:"confirm_identity_reset"`
	}
	if !s.messagingJSON(w, r, &input) {
		return
	}
	if input.ConfirmIdentityReset && !s.config.Messaging.IdentityResetEnabled {
		s.writeError(w, 403, "Identity reset is disabled until the suite issuer is verified")
		return
	}
	target := r.PathValue("device")
	id, err := uuid.Parse(target)
	if err != nil || id.String() != target {
		s.writeError(w, 400, "Canonical device UUID required")
		return
	}
	user, err := s.store.Users().GetUserByID(r.Context(), actor.UserID)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	request := sso.ReauthenticationRequest{RedirectURI: s.config.Server.AppURL + "/api/messaging/recovery-auth/callback", State: crypto.RandomHex(32), Verifier: oauth2.GenerateVerifier(), Nonce: crypto.RandomHex(32), Subject: user.SSOSubject, StartedAt: time.Now().UTC()}
	authURL, err := s.kysignon.BuildReauthenticationURL(r.Context(), request)
	if err != nil {
		s.writeError(w, 502, "Suite reauthentication unavailable")
		return
	}
	payload, err := json.Marshal(recoveryAuthState{Request: request, UserID: actor.UserID, SessionHash: actor.SessionHash, DeviceID: target, ResetConfirmed: input.ConfirmIdentityReset})
	if err != nil {
		s.writeError(w, 500, "Cannot prepare reauthentication")
		return
	}
	sealed, err := crypto.EncryptAESGCM(payload, crypto.DeriveKey(s.config.Security.EncryptionKey, "messaging-recovery-auth"))
	if err != nil {
		s.writeError(w, 500, "Cannot protect reauthentication")
		return
	}
	record := store.MessagingRecoveryAuthentication{StateHash: crypto.SHA256Hex([]byte(request.State)), DeviceID: target, SealedRequest: sealed, CreatedAt: request.StartedAt.Unix(), ExpiresAt: request.StartedAt.Unix() + 300, ResetConfirmed: input.ConfirmIdentityReset}
	if err := s.store.Messaging().BeginRecoveryAuthentication(r.Context(), actor, record); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, 201, map[string]any{"authorization_url": authURL, "expires_at": record.ExpiresAt, "identity_reset_available": s.config.Messaging.IdentityResetEnabled, "reset_requested": input.ConfirmIdentityReset})
}

func (s *Server) handleMessagingRecoveryAuthCallback(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	if len(q["state"]) != 1 || len(q["code"]) != 1 || !messagingHex(state) || len(code) == 0 || len(code) > 4096 || q.Has("error") {
		s.writeError(w, 400, "Invalid reauthentication callback")
		return
	}
	hash := crypto.SHA256Hex([]byte(state))
	// A receipt only reports a completed mutation to its original live session.
	// It never authorizes another mutation or repeats the one-use code exchange.
	if receipt, err := s.store.Messaging().ResetReceipt(r.Context(), actor, hash); err == nil {
		s.messagingResetResult(w, r, receipt)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.messagingError(w, err)
		return
	}
	record, err := s.store.Messaging().RecoveryAuthentication(r.Context(), actor, hash)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	payload, err := crypto.DecryptAESGCM(record.SealedRequest, crypto.DeriveKey(s.config.Security.EncryptionKey, "messaging-recovery-auth"))
	if err != nil {
		s.writeError(w, 401, "Invalid reauthentication state")
		return
	}
	var saved recoveryAuthState
	if err := json.Unmarshal(payload, &saved); err != nil || saved.UserID != actor.UserID || saved.SessionHash != actor.SessionHash || saved.DeviceID != record.DeviceID || saved.Request.State != state || saved.Request.Subject != record.Subject || saved.Request.StartedAt.Unix() != record.CreatedAt || saved.ResetConfirmed != record.ResetConfirmed || saved.Request.RedirectURI != s.config.Server.AppURL+"/api/messaging/recovery-auth/callback" {
		s.writeError(w, 401, "Invalid reauthentication binding")
		return
	}
	if saved.ResetConfirmed && !s.config.Messaging.IdentityResetEnabled {
		s.writeError(w, 403, "Identity reset is disabled")
		return
	}
	claims, err := s.kysignon.ExchangeReauthenticationCode(r.Context(), code, state, saved.Request)
	if err != nil {
		s.writeError(w, 401, "Fresh authentication for the original account is required")
		return
	}
	if saved.ResetConfirmed {
		receipt, err := s.store.Messaging().ResetIdentity(r.Context(), actor, hash, claims.Subject)
		if err != nil {
			s.messagingError(w, err)
			return
		}
		s.messagingResetResult(w, r, receipt)
		return
	}
	if err := s.store.Messaging().CompleteRecoveryAuthentication(r.Context(), actor, hash, claims.Subject); err != nil {
		s.messagingError(w, err)
		return
	}
	// Authentication is consumed and audited, but cannot grant reset or approval.
	s.writeJSON(w, 200, map[string]any{"reauthenticated": true, "device_id": record.DeviceID, "identity_reset_available": false})
}

func (s *Server) messagingResetResult(w http.ResponseWriter, r *http.Request, receipt store.MessagingResetReceipt) {
	// Fixed same-origin destination; no client-supplied return URL or credentials.
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, 200, map[string]any{"identity_reset": true, "receipt": receipt})
}
