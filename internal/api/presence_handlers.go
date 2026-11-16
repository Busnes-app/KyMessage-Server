package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"

	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/presence"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Per-user budgets an hour (docs/PRESENCE.md).
const (
	presenceCalendarPerUser = 120
	presenceOOOPerUser      = 60
	presenceMePerUser       = 60
	presenceLookupPerUser   = 300
	presenceWindow          = time.Hour
)

// presenceRequest verifies a signed request for event under pair, answering refusals itself as
// meetingRequest does: 404 off while unpaired, 200 duplicate, 401 bad_signature, 503.
func (s *Server) presenceRequest(w http.ResponseWriter, r *http.Request, pair *suitePair, event, off string) ([]byte, syncauth.Event, bool) {
	peer := pair.Peer()
	if peer == nil {
		s.meetingRefusal(w, http.StatusNotFound, off, "Not paired with this server")
		return nil, syncauth.Event{}, false
	}
	body, ev, replayed, err := kycalendar.Verify(r, peer.Secret, event, meetingReplay{r.Context(), s.store.Meetings()})
	switch {
	case replayed:
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "duplicate"})
		return nil, ev, false
	case errors.Is(err, errGuard):
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Try again later")
		return nil, ev, false
	case err != nil:
		if peer.Source == kycalendar.SourceKyIdentity {
			pair.kickNow()
		}
		s.meetingRefusal(w, http.StatusUnauthorized, "bad_signature", "Bad signature")
		return nil, ev, false
	}
	return body, ev, true
}

// presenceUser resolves the request's user and spends one of their budget. It answers 404
// no_user or 429 itself and returns nil.
func (s *Server) presenceUser(w http.ResponseWriter, r *http.Request, ev syncauth.Event, u presence.User, action string, limit int) *store.User {
	user := s.meetingOrganizer(r.Context(), u.Sub, u.Issuer)
	if user == nil {
		s.auditMeeting(r, "", action, "", "refused: no_user")
		s.meetingRefusal(w, http.StatusNotFound, "no_user", "You have no KyMessages account")
		return nil
	}
	key := action + ":" + u.Sub
	if !s.allowAccountAttempt(key, limit, presenceWindow) {
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, user.ID, action, "", "refused: rate_limited")
		w.Header().Set("Retry-After", strconv.Itoa(int(s.accountAttempts.resetIn(key).Seconds())+1))
		s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many status requests")
		return nil
	}
	return user
}

func (s *Server) presenceBadBody(w http.ResponseWriter, r *http.Request, action string) {
	s.auditMeeting(r, "", action, "", "refused: bad_body")
	s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid request")
}

// presenceFailed lets the sender retry under the same event ID.
func (s *Server) presenceFailed(w http.ResponseWriter, r *http.Request, ev syncauth.Event, userID, action string) {
	s.forgetMeetingEvent(ev)
	s.auditMeeting(r, userID, action, "", "error")
	s.meetingRefusal(w, http.StatusInternalServerError, "error", "Try again")
}

// handlePresenceCalendar answers POST /api/suite/presence/calendar: KyCalendar's busy blocks.
func (s *Server) handlePresenceCalendar(w http.ResponseWriter, r *http.Request) {
	const action = "presence.calendar"
	body, ev, ok := s.presenceRequest(w, r, s.kycal, presence.EventBusy, "kycalendar_off")
	if !ok {
		return
	}
	p, err := presence.DecodeBusy(body, time.Now())
	if err != nil {
		s.presenceBadBody(w, r, action)
		return
	}
	user := s.presenceUser(w, r, ev, p.User, action, presenceCalendarPerUser)
	if user == nil {
		return
	}
	busy := make([]store.MeetingWindow, len(p.Busy))
	for i, b := range p.Busy {
		busy[i] = store.MeetingWindow{Start: b.Start, End: b.End}
	}
	out, err := s.store.Presence().SetCalendar(r.Context(), p.Sub, p.Revision, busy)
	if err != nil {
		s.presenceFailed(w, r, ev, user.ID, action)
		return
	}
	s.auditMeeting(r, user.ID, action, "", string(out))
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": string(out)})
}

// handlePresenceOOO answers POST /api/suite/presence/out-of-office: KyPost's out-of-office.
func (s *Server) handlePresenceOOO(w http.ResponseWriter, r *http.Request) {
	const action = "presence.ooo"
	body, ev, ok := s.presenceRequest(w, r, s.kypost, presence.EventOOO, "kypost_off")
	if !ok {
		return
	}
	p, err := presence.DecodeOOO(body)
	if err != nil {
		s.presenceBadBody(w, r, action)
		return
	}
	user := s.presenceUser(w, r, ev, p.User, action, presenceOOOPerUser)
	if user == nil {
		return
	}
	var start, end time.Time
	if p.Away {
		start, end = *p.Start, *p.End
	}
	out, err := s.store.Presence().SetOutOfOffice(r.Context(), p.Sub, p.Revision, start, end)
	if err != nil {
		s.presenceFailed(w, r, ev, user.ID, action)
		return
	}
	s.auditMeeting(r, user.ID, action, "", string(out))
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": string(out)})
}

type presenceManual struct {
	State string    `json:"state"`
	Until time.Time `json:"until"`
}

// handlePresenceMe answers POST /api/suite/presence/me: the user's own switch and manual
// status, after any change the request asks for.
func (s *Server) handlePresenceMe(w http.ResponseWriter, r *http.Request) {
	const action = "presence.me"
	body, ev, ok := s.presenceRequest(w, r, s.kycal, presence.EventMe, "kycalendar_off")
	if !ok {
		return
	}
	now := time.Now()
	req, manual, clear, err := presence.DecodeMe(body, now)
	if err != nil {
		s.presenceBadBody(w, r, action)
		return
	}
	user := s.presenceUser(w, r, ev, req.User, action, presenceMePerUser)
	if user == nil {
		return
	}
	st, outcome := s.store.Presence(), "read"
	if req.Share != nil {
		if err := st.SetSharing(r.Context(), req.Sub, *req.Share); err != nil {
			s.presenceFailed(w, r, ev, user.ID, action)
			return
		}
		outcome = "changed"
	}
	if manual != nil || clear {
		m := presence.Manual{}
		if manual != nil {
			m = *manual
		}
		err := st.SetManual(r.Context(), req.Sub, m.State, m.Until)
		if errors.Is(err, store.ErrSharingOff) {
			s.auditMeeting(r, user.ID, action, "", "refused: sharing_off")
			s.meetingRefusal(w, http.StatusConflict, "sharing_off", "Turn on status sharing first")
			return
		}
		if err != nil {
			s.presenceFailed(w, r, ev, user.ID, action)
			return
		}
		outcome = "changed"
	}
	rows, err := st.Presence(r.Context(), []string{req.Sub})
	if err != nil {
		s.presenceFailed(w, r, ev, user.ID, action)
		return
	}
	p := rows[req.Sub]
	out := map[string]any{"share": !p.ShareOff, "manual": nil}
	if p.ManualState != "" && now.Before(p.ManualUntil) {
		out["manual"] = presenceManual{p.ManualState, p.ManualUntil}
	}
	s.auditMeeting(r, user.ID, action, "", outcome)
	s.writeJSON(w, http.StatusOK, out)
}

type presencePerson struct {
	Sub   string     `json:"sub"`
	State string     `json:"state"`
	Until *time.Time `json:"until,omitempty"`
}

// handlePresenceLookup answers POST /api/suite/presence/lookup: organisation members' coarse
// status. Anyone else, and anyone who opted out, reads unknown.
func (s *Server) handlePresenceLookup(w http.ResponseWriter, r *http.Request) {
	const action = "presence.lookup"
	pair, event, off := s.kypost, presence.EventLookupKyPost, "kypost_off"
	if syncauth.FromRequest(r).EventType == presence.EventLookupKyCalendar {
		pair, event, off = s.kycal, presence.EventLookupKyCalendar, "kycalendar_off"
	}
	body, ev, ok := s.presenceRequest(w, r, pair, event, off)
	if !ok {
		return
	}
	req, err := presence.DecodeLookup(body)
	if errors.Is(err, presence.ErrTooMany) {
		s.auditMeeting(r, "", action, "", "refused: too_many", "via", pair.client)
		s.meetingRefusal(w, http.StatusBadRequest, "too_many", "At most 50 people at a time")
		return
	}
	if err != nil {
		s.presenceBadBody(w, r, action)
		return
	}
	user := s.presenceUser(w, r, ev, req.User, action, presenceLookupPerUser)
	if user == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var members []string
	for _, sub := range req.Subs {
		if s.presenceMember(ctx, sub) {
			members = append(members, sub)
		}
	}
	rows, err := s.store.Presence().Presence(ctx, members)
	if err != nil {
		s.presenceFailed(w, r, ev, user.ID, action)
		return
	}
	now := time.Now()
	people := make([]presencePerson, len(req.Subs))
	for i, sub := range req.Subs {
		p, ok := rows[sub]
		state, until := presence.Status(p, ok, now)
		people[i] = presencePerson{Sub: sub, State: state}
		if !until.IsZero() {
			u := until.UTC()
			people[i].Until = &u
		}
	}
	s.auditMeeting(r, user.ID, action, "", "answered", "via", pair.client, "count", strconv.Itoa(len(req.Subs)))
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"people": people})
}

// presenceMember is meetingOrganizer's rule for a subject at the issuer already checked: an
// active everyday account, never an administrator.
func (s *Server) presenceMember(ctx context.Context, sub string) bool {
	u, err := s.store.Users().GetUserBySSO(ctx, "kyidentity", sub)
	return err == nil && u.Role == "user" && u.Status == "active"
}
