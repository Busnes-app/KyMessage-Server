package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// recoveryCodeCount is how many single-use recovery codes enrolment issues.
const recoveryCodeCount = 10

// totpAccount is the signed-in local account behind a TOTP route; requireFreshAdmin has run.
// Suite accounts keep their second factor in KyIdentity.
func (s *Server) totpAccount(w http.ResponseWriter, r *http.Request) (*store.User, *store.Session, bool) {
	w.Header().Set("Cache-Control", "no-store")
	user, sess, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return nil, nil, false
	}
	if user.SSOProvider != "local" {
		s.writeError(w, http.StatusConflict, "Two-factor sign-in for this account is managed by its identity provider")
		return nil, nil, false
	}
	return user, sess, true
}

// totpCode reads {"code"} and applies the per-account MFA window, so enrolment and disabling
// cannot be used to guess codes faster than sign-in.
func (s *Server) totpCode(w http.ResponseWriter, r *http.Request, user *store.User) (string, bool) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" || len(req.Code) > 64 {
		s.writeError(w, http.StatusBadRequest, "A code is required")
		return "", false
	}
	if !s.allowAccountAttempt("mfa-user:"+user.ID, 5, 5*time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many MFA attempts for this account")
		return "", false
	}
	return req.Code, true
}

// validTOTP checks code against the stored secret and spends its counter.
func (s *Server) validTOTP(r *http.Request, user *store.User, code string) (bool, error) {
	secret, err := crypto.DecryptAESGCM(user.TOTPSecretEnc, s.config.Security.EncryptionKey)
	if err != nil {
		return false, err
	}
	counter, ok := auth.ValidateTOTP(string(secret), code)
	return ok && s.store.Users().SpendTOTPCounter(r.Context(), user.ID, counter) == nil, nil
}

// handleTOTPBegin stores a new encrypted secret, not yet enabled, and returns it once.
func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.totpAccount(w, r)
	if !ok {
		return
	}
	if user.TOTPEnabled {
		s.writeError(w, http.StatusConflict, "Two-factor sign-in is already on")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to start setup")
		return
	}
	enc, err := crypto.EncryptAESGCM([]byte(secret), s.config.Security.EncryptionKey)
	if err == nil {
		err = s.store.Users().SetPendingTOTP(r.Context(), user.ID, enc)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusConflict, "Two-factor sign-in is already on")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to start setup")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"secret":      secret,
		"otpauth_uri": auth.GenerateTOTPURL(s.config.Server.AppName, user.Username, secret),
	})
}

// handleTOTPConfirm enables the pending secret once one code from it verifies. Enabling
// revokes every grant issued on the password alone; this session is reissued with its own
// credential time, and the recovery codes are returned this once.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.totpAccount(w, r)
	if !ok {
		return
	}
	code, ok := s.totpCode(w, r, user)
	if !ok {
		return
	}
	if user.TOTPEnabled || user.TOTPSecretEnc == "" {
		s.writeError(w, http.StatusConflict, "Start two-factor setup first")
		return
	}
	valid, err := s.validTOTP(r, user, code)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to read the setup secret")
		return
	}
	if !valid {
		s.writeError(w, http.StatusBadRequest, "That code is not valid; check the time on your device and try the next one")
		return
	}
	codes, hashes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to create recovery codes")
		return
	}
	// The secret must still be the one the code was checked against.
	if err := s.store.Users().EnableTOTP(r.Context(), user.ID, user.TOTPSecretEnc, hashes); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusConflict, "Setup changed meanwhile; start again")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "Failed to turn on two-factor sign-in")
		return
	}
	s.audit(r.Context(), user.ID, r, "auth.totp_enabled", "user", "")
	if _, _, err := s.sessions.IssueDerivedSession(r.Context(), w, r, user, sess.CreatedAt); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Two-factor sign-in is on; sign in again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": true, "recovery_codes": codes})
}

// handleTOTPDisable turns TOTP off given a current code or an unused recovery code.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.totpAccount(w, r)
	if !ok {
		return
	}
	code, ok := s.totpCode(w, r, user)
	if !ok {
		return
	}
	if !user.TOTPEnabled {
		s.writeError(w, http.StatusConflict, "Two-factor sign-in is already off")
		return
	}
	valid, err := s.validTOTP(r, user, code)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to read the secret")
		return
	}
	if !valid {
		updated, redeemed := auth.RedeemRecoveryCode(code, user.RecoveryCodesHash)
		valid = redeemed && s.store.Users().UpdateRecoveryCodes(r.Context(), user.ID, user.RecoveryCodesHash, updated) == nil
	}
	if !valid {
		s.writeError(w, http.StatusBadRequest, "That code is not valid")
		return
	}
	if err := s.store.Users().DisableTOTP(r.Context(), user.ID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to turn off two-factor sign-in")
		return
	}
	s.audit(r.Context(), user.ID, r, "auth.totp_disabled", "user", "")
	s.writeJSON(w, http.StatusOK, map[string]bool{"totp_enabled": false})
}
