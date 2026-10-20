package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Meeting notes: docs/superpowers/plans/2026-10-20-meeting-notes.md. One server-owned bot
// account, off by default, that joins a call only on an invite from the room's creator or an
// admin (rtc_handlers.go) and gets its session here with matrix-init's notes secret.
const (
	notesEnabledKey = "notes_enabled"
	notesSessionKey = "notes_session" // the bot's MAS personal session ID
	notesDeviceKey  = "notes_device"
	notesCheckinKey = "notes_checkin" // RFC 3339 time of the bot's last session request
	notesScreensKey = "notes_screens" // shared-screen snapshots allowed (plan ...-meeting-notes-screens.md)
	notesTimeout    = 30 * time.Second
)

func (s *Server) notesMXID() string {
	return "@" + matrixsync.NotesUsername + ":" + s.config.Matrix.ServerName
}

func (s *Server) setting(ctx context.Context, key string) (string, error) {
	v, err := s.store.Settings().GetSetting(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return v, err
}

// notesOn: configured (the notes secret is set) and turned on in Settings.
func (s *Server) notesOn(ctx context.Context) (bool, error) {
	if s.config.Matrix.NotesSecret == "" || s.mas == nil {
		return false, nil
	}
	v, err := s.setting(ctx, notesEnabledKey)
	return v == "true", err
}

func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	on, err := s.notesOn(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the setting")
		return
	}
	checkin, _ := s.setting(r.Context(), notesCheckinKey)
	screens, _ := s.setting(r.Context(), notesScreensKey)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"available": s.config.Matrix.NotesSecret != "",
		"enabled":   on,
		"screens":   screens == "true",
		"user":      s.notesMXID(),
		"checkin":   checkin,
	})
}

// handleNotesEnable creates the account if needed; the bot then gets a session on its next
// request.
func (s *Server) handleNotesEnable(w http.ResponseWriter, r *http.Request) {
	if s.config.Matrix.NotesSecret == "" || s.mas == nil {
		s.writeError(w, http.StatusNotFound, "Meeting notes are not configured")
		return
	}
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), notesTimeout)
	defer cancel()
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	if _, err := s.mas.EnsureNotesUser(ctx); err != nil {
		s.audit(ctx, actor, r, "notes.enable", s.notesMXID(), auditFields("error: "+err.Error()))
		s.writeError(w, http.StatusBadGateway, "Could not prepare the notes account")
		return
	}
	if err := s.store.Settings().SetSetting(ctx, notesEnabledKey, "true"); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not save the setting")
		return
	}
	s.audit(ctx, actor, r, "notes.enable", s.notesMXID(), auditFields("enabled"))
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "enabled"})
}

// handleNotesDisable turns the feature off and revokes the bot's session, which deletes its
// device: the call sweep evicts it within 5 seconds and no new session or grant is issued.
func (s *Server) handleNotesDisable(w http.ResponseWriter, r *http.Request) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), notesTimeout)
	defer cancel()
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	if err := s.store.Settings().SetSetting(ctx, notesEnabledKey, "false"); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not save the setting")
		return
	}
	outcome := "disabled"
	if sess, _ := s.setting(ctx, notesSessionKey); sess != "" && s.mas != nil {
		if err := s.mas.RevokeAgentSession(ctx, sess); err != nil {
			outcome = "disabled; session not revoked: " + err.Error()
		} else {
			_ = s.store.Settings().DeleteSetting(ctx, notesSessionKey)
			_ = s.store.Settings().DeleteSetting(ctx, notesDeviceKey)
		}
	}
	s.audit(ctx, actor, r, "notes.disable", s.notesMXID(), auditFields(outcome))
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome})
}

// handleNotesSession gives the bot a token for its device: the same session regenerated when
// it still holds the device's keys, else a new device (its old session revoked). Bearer is the
// notes secret; the token is returned, never stored or logged.
func (s *Server) handleNotesSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.notesBot(w, r) {
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if !decodeStrict(w, r, &req) {
		s.writeError(w, http.StatusBadRequest, `Body must be {"device_id": "..."}`)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), notesTimeout)
	defer cancel()
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	on, err := s.notesOn(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the setting")
		return
	}
	_ = s.store.Settings().SetSetting(ctx, notesCheckinKey, time.Now().UTC().Format(time.RFC3339))
	if !on {
		s.writeError(w, http.StatusForbidden, "Meeting notes are turned off")
		return
	}
	userID, err := s.mas.EnsureNotesUser(ctx)
	if err != nil {
		s.audit(ctx, "", r, "notes.session", s.notesMXID(), auditFields("error: "+err.Error()))
		s.writeError(w, http.StatusBadGateway, "Notes account unavailable")
		return
	}
	sess, _ := s.setting(ctx, notesSessionKey)
	device, _ := s.setting(ctx, notesDeviceKey)
	token, outcome := "", "regenerated"
	if sess != "" && req.DeviceID != "" && req.DeviceID == device {
		var regenerated string
		// A session MAS no longer regenerates (revoked or expired outside KyMessages) gives way
		// to a new device below.
		if regenerated, token, err = s.mas.RotateAgentToken(ctx, userID, device, sess); err == nil {
			sess = regenerated
		}
	}
	if token == "" {
		if sess != "" {
			if err := s.mas.RevokeAgentSession(ctx, sess); err != nil {
				s.audit(ctx, "", r, "notes.session", s.notesMXID(), auditFields("error: old session not revoked: "+err.Error()))
				s.writeError(w, http.StatusBadGateway, "Notes account unavailable")
				return
			}
			_ = s.store.Settings().DeleteSetting(ctx, notesSessionKey)
		}
		device = newDeviceID()
		sess, token, err = s.mas.MintNotesToken(ctx, userID, device)
		outcome = "new device"
	}
	if err == nil {
		err = errors.Join(s.store.Settings().SetSetting(ctx, notesSessionKey, sess), s.store.Settings().SetSetting(ctx, notesDeviceKey, device))
	}
	if err != nil {
		s.audit(ctx, "", r, "notes.session", s.notesMXID(), auditFields("error: "+err.Error()))
		s.writeError(w, http.StatusBadGateway, "Notes account unavailable")
		return
	}
	s.audit(ctx, "", r, "notes.session", s.notesMXID(), auditFields(outcome, "device", device))
	s.writeJSON(w, http.StatusOK, map[string]string{"user_id": s.notesMXID(), "device_id": device, "access_token": token})
}

// notesBot authenticates the bot: the notes secret as Bearer, 10 requests a minute per address.
func (s *Server) notesBot(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	secret := s.config.Matrix.NotesSecret
	if secret == "" || s.mas == nil {
		s.writeError(w, http.StatusNotFound, "Meeting notes are not configured")
		return false
	}
	if !s.allowClientAttempt("notes:", r, 10, time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many requests")
		return false
	}
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(secret)) != 1 {
		s.writeError(w, http.StatusUnauthorized, "Invalid notes secret")
		return false
	}
	return true
}

// handleNotesConfig tells the bot what it may do: screens only while notes are on too.
func (s *Server) handleNotesConfig(w http.ResponseWriter, r *http.Request) {
	if !s.notesBot(w, r) {
		return
	}
	on, err := s.notesOn(r.Context())
	screens, err2 := s.setting(r.Context(), notesScreensKey)
	if err != nil || err2 != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the setting")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"enabled": on, "screens": on && screens == "true"})
}

// handleNotesScreens turns shared-screen snapshots on (fresh sign-in) or off (one click).
func (s *Server) handleNotesScreens(on bool) http.HandlerFunc {
	action, value := "notes.screens_disable", "false"
	if on {
		action, value = "notes.screens_enable", "true"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.Matrix.NotesSecret == "" || s.mas == nil {
			s.writeError(w, http.StatusNotFound, "Meeting notes are not configured")
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), notesTimeout)
		defer cancel()
		if err := s.store.Settings().SetSetting(ctx, notesScreensKey, value); err != nil {
			s.writeError(w, http.StatusInternalServerError, "Could not save the setting")
			return
		}
		outcome := map[bool]string{true: "enabled", false: "disabled"}[on]
		s.audit(ctx, s.actorID(r), r, action, s.notesMXID(), auditFields(outcome))
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome})
	}
}
