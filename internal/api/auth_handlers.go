package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type LoginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	CaptchaToken string `json:"captcha_token,omitempty"`
}

// Failed password logins per loginFailWindow: loginFailLimit per account and client.
const (
	loginFailLimit  = 10
	loginFailWindow = 15 * time.Minute
)

func (s *Server) handlePoWChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	challenge, err := auth.GeneratePoWChallenge(s.config.Captcha.DifficultyPoW, s.config.Security.SessionSecret)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to generate PoW challenge")
		return
	}

	s.writeJSON(w, http.StatusOK, challenge)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !s.perIPExempt(r) && !s.allowClientAttempt("login", r, 20, time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many login attempts")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		s.writeError(w, http.StatusBadRequest, "Username and password are required")
		return
	}

	// Config refuses any provider other than "pow" and "none" at startup.
	if s.config.Captcha.Provider == "pow" && !s.pow.Spend(req.CaptchaToken, s.config.Security.SessionSecret) {
		s.writeError(w, http.StatusForbidden, "Security check failed. Please complete the CAPTCHA puzzle.")
		return
	}

	// Only local accounts sign in with a password; an SSO row with a case-variant name must
	// not shadow one.
	user, err := s.store.Users().GetLocalUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			password.DummyVerify() // unknown names cost the same as wrong passwords
			s.writeError(w, http.StatusUnauthorized, "Invalid credentials")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "Authentication error")
		return
	}
	// Only failures count, per account and client (IPv6 by /64): nobody can keep the owner
	// locked out from another address, so there is no account-wide block. An exhausted
	// window answers exactly as a wrong password does, before Verify, so a locked-out caller
	// learns nothing from a right password or from the answer.
	pairKey := "login-fail:" + user.ID + ":" + s.clientKey(r)
	if s.clientAttempts.blocked(pairKey, loginFailLimit) {
		password.DummyVerify()
		s.writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	ok, err := password.Verify(req.Password, user.PasswordHash)
	if err != nil || !ok {
		s.clientAttempts.allow(pairKey, loginFailLimit, loginFailWindow)
		s.writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	if user.Status != "active" {
		s.writeError(w, http.StatusForbidden, "Account is disabled or inactive")
		return
	}

	// Issue active session
	_, _, err = s.sessions.IssueSession(r.Context(), w, r, user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to issue session")
		return
	}

	_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{
		UserID:   user.ID,
		Action:   "auth.login",
		Resource: "session",
	})

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          user,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	_ = s.sessions.RevokeSession(r.Context(), w, r)
	s.writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, sess, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	s.sessions.RefreshCSRF(w, r, sess)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          user,
	})
}
