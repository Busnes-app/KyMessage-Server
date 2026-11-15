package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/ky-primitives/syncauth"

	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
)

// Chat from mail and calendar (docs/SUITE-CHAT.md): one route, an event type per pair.
const (
	chatEventKyPost     = "kypost.chat.open"
	chatEventKyCalendar = "kycalendar.chat.open"
	// 60 lookups per caller an hour, across both pairs.
	chatPerUser = 60
	chatWindow  = time.Hour
)

// chatRequest names the caller and the person to chat with by KyIdentity subject.
type chatRequest struct {
	Sub       string `json:"sub"`
	Issuer    string `json:"issuer"`
	TargetSub string `json:"target_sub"`
}

// chatText is 1 to max bytes of UTF-8 without control characters.
func chatText(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// handleSuiteChat answers POST /api/suite/chat with Element's user view of the target, so the
// caller's app can open a direct chat. It creates nothing.
func (s *Server) handleSuiteChat(w http.ResponseWriter, r *http.Request) {
	// The event type picks the pair; Verify then requires that pair's secret, so neither app
	// can speak for the other.
	pair, event, off := s.kypost, chatEventKyPost, "kypost_off"
	if syncauth.FromRequest(r).EventType == chatEventKyCalendar {
		pair, event, off = s.kycal, chatEventKyCalendar, "kycalendar_off"
	}
	peer := pair.Peer()
	if peer == nil || s.mas == nil {
		s.meetingRefusal(w, http.StatusNotFound, off, "Not paired with this server")
		return
	}
	body, ev, replayed, err := kycalendar.Verify(r, peer.Secret, event, meetingReplay{r.Context(), s.store.Meetings()})
	switch {
	case replayed:
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "duplicate"})
		return
	case errors.Is(err, errGuard):
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Try again later")
		return
	case err != nil:
		if peer.Source == kycalendar.SourceKyIdentity {
			pair.kickNow() // the peer may hold a rotated secret; lookups stay rate limited
		}
		s.meetingRefusal(w, http.StatusUnauthorized, "bad_signature", "Bad signature")
		return
	}
	via := pair.client
	var req chatRequest
	if json.Unmarshal(body, &req) != nil || !chatText(req.Sub, 255) || !chatText(req.Issuer, 512) || !chatText(req.TargetSub, 255) {
		s.auditMeeting(r, "", "chat.open", "", "refused: bad_body", "via", via)
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid request")
		return
	}
	caller := s.meetingOrganizer(r.Context(), req.Sub, req.Issuer)
	if caller == nil {
		s.auditMeeting(r, "", "chat.open", "", "refused: no_user", "via", via)
		s.meetingRefusal(w, http.StatusNotFound, "no_user", "You have no KyMessages account")
		return
	}
	key := "chat:" + req.Sub
	if !s.allowAccountAttempt(key, chatPerUser, chatWindow) {
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, caller.ID, "chat.open", "", "refused: rate_limited", "via", via)
		w.Header().Set("Retry-After", strconv.Itoa(int(s.accountAttempts.resetIn(key).Seconds())+1))
		s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many chat requests")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	people, err := s.meetingPeople(ctx)
	if err != nil {
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, caller.ID, "chat.open", "", "error: "+err.Error(), "via", via)
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Chat is unavailable; try again")
		return
	}
	if _, ok := people[req.Sub]; !ok { // no Matrix account yet, or locked
		s.auditMeeting(r, caller.ID, "chat.open", "", "refused: no_user", "via", via)
		s.meetingRefusal(w, http.StatusNotFound, "no_user", "You have no KyMessages account")
		return
	}
	// The target passes the caller's rule: an everyday account, never an administrator.
	target := s.meetingOrganizer(r.Context(), req.TargetSub, req.Issuer)
	mxid, ok := people[req.TargetSub]
	if req.TargetSub == req.Sub || target == nil || !ok {
		s.auditMeeting(r, caller.ID, "chat.open", "", "refused: no_target", "via", via)
		s.meetingRefusal(w, http.StatusNotFound, "no_target", "This person isn't on KyMessages yet")
		return
	}
	s.auditMeeting(r, caller.ID, "chat.open", target.ID, "opened", "via", via)
	s.writeJSON(w, http.StatusOK, map[string]string{"url": s.config.Matrix.ChatHost + "/#/user/" + mxid})
}
