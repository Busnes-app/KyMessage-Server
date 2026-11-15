package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// Meeting guests (docs/KYCALENDAR.md "Guests"): people outside the organisation join a
// KyCalendar meeting room during its windows, from a waiting room. Off unless an administrator
// turns it on. A guest is a short-lived unlinked MAS account guest-<random>; Synapse's
// confinement module keeps it to its meeting room.

const (
	guestsEnabledKey = "meeting_guests_enabled"
	guestSuffix      = " (guest)"
	guestNameMax     = 64
	// guestCodeTTL bounds the one-time code the chat-origin bootstrap page trades for the session.
	guestCodeTTL = 2 * time.Minute
	// A joined guest out of the call this long is removed and must knock again; one admitted
	// but never seen in the call gets guestFirstCall to get there.
	guestDeparture = 20 * time.Second
	guestFirstCall = 2 * time.Minute
	// GuestPagePath is the bootstrap page on Element's origin (matrix-init renders it).
	GuestPagePath   = "/kymessages-guest.html"
	meetSessionPath = "/api/meet/session"
)

var guestTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// guestState is what the server holds only in memory: unredeemed sign-in codes, which carry a
// fresh access token for at most guestCodeTTL, and when each joined guest was last in its call.
type guestState struct {
	mu    sync.Mutex
	codes map[string]guestCode // by code hash
	seen  map[string]guestSeen // by guest Matrix ID
}

type guestCode struct {
	token, mxid, device, room string
	expires                   time.Time
}

type guestSeen struct {
	at     time.Time
	inCall bool // at is the last time in the call, not when it was first seen joined
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomToken is 256 random bits, base64url without padding (43 characters).
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// guestsOn: Matrix is configured and an administrator turned guests on.
func (s *Server) guestsOn(ctx context.Context) (bool, error) {
	if s.mas == nil {
		return false, nil
	}
	v, err := s.setting(ctx, guestsEnabledKey)
	return v == "true", err
}

func isGuest(mxid string) bool { return strings.HasPrefix(mxid, "@"+matrixsync.GuestPrefix) }

// guestName is 1 to 64 characters with no control, format (bidi overrides, zero-width) or
// line-separator characters, trimmed.
func guestName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || s == "" || utf8.RuneCountInString(s) > guestNameMax {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs) || r == utf8.RuneError {
			return "", false
		}
	}
	return s, true
}

// openWindow is the window now is in (the one ending last if several overlap).
func openWindow(ws []store.MeetingWindow, now time.Time) (store.MeetingWindow, bool) {
	var best store.MeetingWindow
	found := false
	for _, w := range ws {
		if !now.Before(w.Start) && now.Before(w.End) && (!found || w.End.After(best.End)) {
			best, found = w, true
		}
	}
	return best, found
}

// nextWindow is the earliest window starting after now.
func nextWindow(ws []store.MeetingWindow, now time.Time) (store.MeetingWindow, bool) {
	for _, w := range ws { // oldest first
		if w.Start.After(now) {
			return w, true
		}
	}
	return store.MeetingWindow{}, false
}

func (s *Server) auditGuest(ctx context.Context, r *http.Request, action string, g *store.MeetingGuest, outcome string, kv ...string) {
	if r == nil {
		r = &http.Request{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roomAuditTimeout)
	defer cancel()
	kv = append([]string{"key", g.KeyHash[:16]}, kv...)
	if g.MXID != "" {
		kv = append(kv, "account", g.MXID)
	}
	s.audit(ctx, "", r, action, g.MeetingID, auditFields(outcome, kv...))
}

// ---------------------------------------------------------------------------------------
// The administrator switch.

func (s *Server) handleGuests(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	on, err := s.guestsOn(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the setting")
		return
	}
	live, err := s.store.Meetings().ActiveGuests(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read guests")
		return
	}
	n := 0
	for _, g := range live {
		if g.MXID != "" {
			n++
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": on, "live": n})
}

// handleGuestsSwitch turns guests on (step-up, checked by the route) or off. Off refuses every
// guest at once and ends every guest session before answering.
func (s *Server) handleGuestsSwitch(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.matrixOn(w) {
			return
		}
		actor := s.actorID(r)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.roomBudget)
		defer cancel()
		action := "guests.disable"
		if on {
			action = "guests.enable"
		}
		if err := s.store.Settings().SetSetting(ctx, guestsEnabledKey, fmt.Sprint(on)); err != nil {
			s.writeError(w, http.StatusInternalServerError, "Could not save the setting")
			return
		}
		outcome := action[len("guests."):] + "d"
		if !on {
			if err := s.ReconcileGuests(ctx); err != nil {
				outcome += "; some guest sessions not ended yet, retried every 5 seconds: " + err.Error()
			}
		}
		s.audit(ctx, actor, r, action, "meeting-guests", auditFields(outcome))
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome})
	}
}

// ---------------------------------------------------------------------------------------
// KyCalendar's PUT and DELETE.

// reconcileGuests applies req's guests and windows to meeting m after its room is reconciled:
// windows replaced, dropped keys revoked, new keys given a link. It returns the new links by key.
// Links are added last, and removed again on failure, so a retried PUT issues them afresh.
func (s *Server) reconcileGuests(ctx context.Context, tok string, m *store.Meeting, req kycalendar.PutRequest) (map[string]string, error) {
	st := s.store.Meetings()
	windows := make([]store.MeetingWindow, 0, len(req.Windows))
	for _, w := range req.Windows {
		windows = append(windows, store.MeetingWindow{Start: w.Start, End: w.End})
	}
	if err := st.ReplaceWindows(ctx, m.ID, windows); err != nil {
		return nil, err
	}
	want := map[string]string{} // key hash -> key
	for _, g := range req.Guests {
		want[hashHex(g.Key)] = g.Key
	}
	rows, err := st.Guests(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range rows {
		key, listed := want[g.KeyHash]
		switch {
		case listed && !g.Revoked:
			delete(want, g.KeyHash) // its link stands
		case !listed && !g.Revoked:
			g.Revoked = true // the reconciler ends its session
			if err := st.UpdateGuest(ctx, &g); err != nil {
				return nil, err
			}
		case listed: // listed again before its revocation finished: finish it now
			if err := s.finishGuest(ctx, tok, g, m.RoomID, "revoked"); err != nil {
				return nil, err
			}
			want[g.KeyHash] = key
		}
	}
	if len(req.Guests) > 0 {
		if err := s.meetingRooms.AllowKnocks(ctx, tok, m.RoomID); err != nil {
			return nil, err
		}
	}
	urls := map[string]string{}
	var added []string
	for hash, key := range want {
		token := randomToken()
		err := st.AddGuest(ctx, &store.MeetingGuest{MeetingID: m.ID, KeyHash: hash, LinkHash: hashHex(token)})
		if err != nil {
			for _, h := range added {
				_ = st.DeleteGuest(context.WithoutCancel(ctx), m.ID, h)
			}
			return nil, err
		}
		added = append(added, hash)
		urls[key] = strings.TrimSuffix(s.config.Server.AppURL, "/") + "/meet#" + token
	}
	return urls, nil
}

// revokeGuests revokes every link of a closed meeting; the reconciler ends the sessions.
func (s *Server) revokeGuests(ctx context.Context, id string) error {
	st := s.store.Meetings()
	if err := st.ReplaceWindows(ctx, id, nil); err != nil {
		return err
	}
	rows, err := st.Guests(ctx, id)
	if err != nil {
		return err
	}
	for _, g := range rows {
		if !g.Revoked {
			g.Revoked = true
			if err := st.UpdateGuest(ctx, &g); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// The guest's side: /meet#<token> (the console's single-page app) calls these. Nothing here
// needs a session; the link token is the credential and is never logged or audited.

type meetRequest struct {
	Token   string `json:"token"`
	Name    string `json:"name"`
	Captcha string `json:"captcha"`
}

// meetGuest resolves a link: unknown, revoked, closed meeting and guests switched off all read
// the same.
func (s *Server) meetGuest(ctx context.Context, token string) (*store.MeetingGuest, *store.Meeting, []store.MeetingWindow, error) {
	if !guestTokenPattern.MatchString(token) {
		return nil, nil, nil, store.ErrNotFound
	}
	on, err := s.guestsOn(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	g, err := s.store.Meetings().GuestByLink(ctx, hashHex(token))
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := s.store.Meetings().GetMeeting(ctx, g.MeetingID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !on || g.Revoked || m.State != store.MeetingOpen {
		return nil, nil, nil, store.ErrNotFound
	}
	ws, err := s.store.Meetings().Windows(ctx, g.MeetingID)
	return g, m, ws, err
}

func (s *Server) meetRefusal(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.meetingRefusal(w, http.StatusNotFound, "not_found", "This link isn't valid")
		return
	}
	s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Try again in a moment")
}

func (s *Server) meetDecode(w http.ResponseWriter, r *http.Request, scope string, limit int, window time.Duration) (meetRequest, bool) {
	w.Header().Set("Cache-Control", "no-store")
	var req meetRequest
	if !s.allowClientAttempt(scope, r, limit, window) {
		w.Header().Set("Retry-After", "60")
		s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts; wait a few minutes")
		return req, false
	}
	if s.mas == nil {
		s.meetRefusal(w, store.ErrNotFound)
		return req, false
	}
	if !jsonRequest(r) || !decodeStrict(w, r, &req) {
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid request")
		return req, false
	}
	return req, true
}

// handleMeetStatus says whether the link's meeting is open now, or when it opens.
func (s *Server) handleMeetStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := s.meetDecode(w, r, "meet-status", 60, time.Minute)
	if !ok {
		return
	}
	_, _, ws, err := s.meetGuest(r.Context(), req.Token)
	if err != nil {
		s.meetRefusal(w, err)
		return
	}
	now := time.Now()
	if open, ok := openWindow(ws, now); ok {
		s.writeJSON(w, http.StatusOK, map[string]any{"state": "open", "closes_at": open.End})
	} else if next, ok := nextWindow(ws, now); ok {
		s.writeJSON(w, http.StatusOK, map[string]any{"state": "waiting", "opens_at": next.Start, "closes_at": next.End})
	} else {
		s.writeJSON(w, http.StatusOK, map[string]any{"state": "ended"})
	}
}

// handleMeetJoin seats the link's guest: inside a window, with a name and a solved challenge,
// it makes a guest account whose session ends with the window, marks it in the meeting room and
// knocks. One live account per link: a second join ends the first. The answer is where the
// browser goes next, Element's bootstrap page with a one-time code in the fragment.
func (s *Server) handleMeetJoin(w http.ResponseWriter, r *http.Request) {
	req, ok := s.meetDecode(w, r, "meet-join", 10, 15*time.Minute)
	if !ok {
		return
	}
	if !s.pow.Spend(req.Captcha, s.config.Security.SessionSecret) {
		s.meetingRefusal(w, http.StatusForbidden, "captcha", "The security check failed; try again")
		return
	}
	name, ok := guestName(req.Name)
	if !ok {
		s.meetingRefusal(w, http.StatusBadRequest, "bad_name", "Enter a name of 1 to 64 characters")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(roomWriteTimeout(s.roomBudget)))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.roomBudget)
	defer cancel()
	s.meetingMu.Lock()
	defer s.meetingMu.Unlock()
	g, m, ws, err := s.meetGuest(ctx, req.Token)
	if err != nil {
		s.meetRefusal(w, err)
		return
	}
	now := time.Now()
	win, open := openWindow(ws, now)
	if !open {
		s.meetingRefusal(w, http.StatusConflict, "not_open", "This meeting isn't open now")
		return
	}
	if !s.allowAccountAttempt("meet-link:"+g.KeyHash, 10, 15*time.Minute) {
		s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts; wait a few minutes")
		return
	}
	var code string
	err = s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		if g.MXID != "" { // the link's earlier visitor gives up the seat
			if err := s.finishGuest(ctx, tok, *g, m.RoomID, "replaced"); err != nil {
				return err
			}
			g.MXID, g.MASUserID, g.SessionID, g.SessionEnd, g.Membership = "", "", "", time.Time{}, ""
		}
		var err error
		code, err = s.seatGuest(ctx, tok, g, m, name, win.End)
		return err
	})
	if err != nil {
		s.auditGuest(ctx, r, "guest.knock", g, "error: "+err.Error())
		s.meetRefusal(w, err)
		return
	}
	s.auditGuest(ctx, r, "guest.knock", g, "knocked")
	s.writeJSON(w, http.StatusOK, map[string]string{"next": strings.TrimSuffix(s.config.Matrix.ChatHost, "/") + GuestPagePath + "#" + code})
}

// seatGuest makes the account, records it before anything else can fail (so the reconciler can
// always end it), names it, marks it in the room and knocks. On failure it ends the account.
func (s *Server) seatGuest(ctx context.Context, tok string, g *store.MeetingGuest, m *store.Meeting, name string, end time.Time) (string, error) {
	var localpart, masID string
	var err error
	for range 3 {
		localpart = matrixsync.GuestPrefix + randomHex(10)
		if masID, err = s.mas.CreateAgentUser(ctx, localpart); !errors.Is(err, matrixsync.ErrNameTaken) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	device := "KYGUEST" + strings.ToUpper(randomHex(5))
	sessionID, token, err := s.mas.MintGuestToken(ctx, masID, device, time.Until(end))
	g.MXID, g.MASUserID, g.SessionID, g.SessionEnd = "@"+localpart+":"+s.config.Matrix.ServerName, masID, sessionID, end
	if err == nil {
		err = s.store.Meetings().UpdateGuest(ctx, g)
	}
	if err == nil {
		err = s.meetingRooms.SetDisplayName(ctx, token, g.MXID, name+guestSuffix)
	}
	if err == nil {
		err = s.meetingRooms.MarkGuest(ctx, tok, m.RoomID, localpart)
	}
	if err == nil {
		err = s.meetingRooms.Knock(ctx, token, m.RoomID)
	}
	if err == nil {
		g.Membership = "knock"
		err = s.store.Meetings().UpdateGuest(ctx, g)
	}
	if err != nil {
		g.SessionID = sessionID
		if ferr := s.finishGuest(ctx, tok, *g, m.RoomID, "failed"); ferr != nil {
			return "", errors.Join(err, ferr)
		}
		return "", err
	}
	code := randomToken()
	s.guests.mu.Lock()
	defer s.guests.mu.Unlock()
	if s.guests.codes == nil {
		s.guests.codes = map[string]guestCode{}
	}
	s.guests.codes[hashHex(code)] = guestCode{token: token, mxid: g.MXID, device: device, room: m.RoomID, expires: time.Now().Add(guestCodeTTL)}
	return code, nil
}

// handleMeetSession trades a one-time code for the guest's Matrix session. Only Element's
// origin may read the answer (the bootstrap page there writes it where Element restores
// sessions from); the body is the code as text, so the browser sends no preflight.
func (s *Server) handleMeetSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowClientAttempt("meet-session", r, 30, time.Minute) {
		s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts")
		return
	}
	if r.Header.Get("Origin") != strings.TrimSuffix(s.config.Matrix.ChatHost, "/") || s.config.Matrix.ChatHost == "" {
		s.meetingRefusal(w, http.StatusForbidden, "bad_origin", "Wrong origin")
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 128))
	code := strings.TrimSpace(string(b))
	if err != nil || !guestTokenPattern.MatchString(code) {
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid code")
		return
	}
	s.guests.mu.Lock()
	c, ok := s.guests.codes[hashHex(code)]
	delete(s.guests.codes, hashHex(code))
	s.guests.mu.Unlock()
	if !ok || time.Now().After(c.expires) {
		s.meetingRefusal(w, http.StatusNotFound, "not_found", "This sign-in has expired; open your meeting link again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"homeserver": s.config.Matrix.Host, "user_id": c.mxid, "device_id": c.device,
		"access_token": c.token, "room_id": c.room})
}

// meetSessionCORS answers the bootstrap page's origin only, without credentials.
func (s *Server) meetSessionCORS(w http.ResponseWriter, r *http.Request) bool {
	chat := strings.TrimSuffix(s.config.Matrix.ChatHost, "/")
	if chat != "" && r.Header.Get("Origin") == chat {
		w.Header().Set("Access-Control-Allow-Origin", chat)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// ---------------------------------------------------------------------------------------
// Ending a guest, and the reconciler that does it.

// finishGuest ends g's account if it has one (out of the call and the room, session revoked,
// account deactivated) and audits guest.expire with reason; then a revoked link is deleted and
// any other row cleared. A failure leaves the row for the next pass.
func (s *Server) finishGuest(ctx context.Context, tok string, g store.MeetingGuest, room, reason string) error {
	if g.MXID != "" {
		if err := s.endGuestAccount(ctx, tok, g, room); err != nil {
			s.auditGuest(ctx, nil, "guest.expire", &g, "error: "+err.Error(), "reason", reason)
			return err
		}
		s.auditGuest(ctx, nil, "guest.expire", &g, "ended", "reason", reason)
		s.guests.mu.Lock()
		delete(s.guests.seen, g.MXID)
		s.guests.mu.Unlock()
	}
	if g.Revoked {
		return s.store.Meetings().DeleteGuest(ctx, g.MeetingID, g.KeyHash)
	}
	g.MXID, g.MASUserID, g.SessionID, g.SessionEnd, g.Membership = "", "", "", time.Time{}, ""
	return s.store.Meetings().UpdateGuest(ctx, &g)
}

func (s *Server) endGuestAccount(ctx context.Context, tok string, g store.MeetingGuest, room string) error {
	var errs []error
	if s.rtc != nil {
		if rooms, err := s.rtc.Rooms(ctx); err != nil {
			errs = append(errs, err)
		} else {
			for _, rm := range rooms {
				ps, err := s.rtc.Participants(ctx, rm.Name)
				errs = append(errs, err)
				for _, p := range ps {
					if p.Metadata == g.MXID {
						errs = append(errs, s.rtc.Remove(ctx, rm.Name, p.Identity))
					}
				}
			}
		}
	}
	members, err := s.meetingRooms.Memberships(ctx, tok, room)
	var se *synapseadmin.Error
	switch {
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
	case err != nil:
		errs = append(errs, err)
	default:
		if st := members[g.MXID]; st == "join" || st == "invite" || st == "knock" {
			errs = append(errs, s.meetingRooms.Kick(ctx, tok, room, g.MXID))
		}
	}
	if g.SessionID != "" {
		errs = append(errs, s.mas.RevokeAgentSession(ctx, g.SessionID))
	}
	if g.MASUserID != "" {
		errs = append(errs, s.mas.Deactivate(ctx, g.MASUserID))
	}
	return errors.Join(errs...)
}

// ReconcileGuests runs every 5 seconds with the call reconciler. It ends guests whose link was
// revoked, whose meeting closed, whose window ended, or every guest while guests are off; it
// audits knocks and admissions; and it removes a joined guest who left the call, so they must
// knock again. One guest's failure does not stop the others.
func (s *Server) ReconcileGuests(ctx context.Context) error {
	if s.mas == nil {
		return nil
	}
	now := time.Now()
	s.guests.mu.Lock()
	for h, c := range s.guests.codes {
		if now.After(c.expires) {
			delete(s.guests.codes, h)
		}
	}
	s.guests.mu.Unlock()
	if rows, err := s.store.Meetings().ActiveGuests(ctx); err != nil || len(rows) == 0 {
		return err
	}
	on, err := s.guestsOn(ctx)
	if err != nil {
		return err // grants and /meet fail closed on the same read
	}
	inCall, callErr := s.guestsInCalls(ctx)
	// Rows are read again under the lock: a join or PUT in between changed them.
	s.meetingMu.Lock()
	defer s.meetingMu.Unlock()
	rows, err := s.store.Meetings().ActiveGuests(ctx)
	if err != nil {
		return err
	}
	var errs []error
	err = s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		meetings := map[string]*store.Meeting{}
		members := map[string]map[string]synapseadmin.Member{}
		for _, g := range rows {
			m, ok := meetings[g.MeetingID]
			if !ok {
				if m, err = s.store.Meetings().GetMeeting(ctx, g.MeetingID); err != nil {
					errs = append(errs, err)
					continue
				}
				meetings[g.MeetingID] = m
			}
			reason := ""
			switch {
			case g.Revoked:
				reason = "revoked"
			case !on:
				reason = "guests_off"
			case m.State != store.MeetingOpen:
				reason = "closed"
			case !now.Before(g.SessionEnd):
				reason = "window_ended"
			}
			if reason != "" {
				errs = append(errs, s.finishGuest(ctx, tok, g, m.RoomID, reason))
				continue
			}
			ms, ok := members[m.RoomID]
			if !ok {
				if ms, err = s.meetingRooms.MemberStates(ctx, tok, m.RoomID); err != nil {
					errs = append(errs, err)
					continue
				}
				members[m.RoomID] = ms
			}
			errs = append(errs, s.guestMembership(ctx, tok, g, m, ms[g.MXID], inCall, callErr == nil && inCall != nil, now))
		}
		return nil
	})
	return errors.Join(append(errs, err, callErr)...)
}

// guestMembership audits g's membership change and removes it from the room once it has been
// out of the call too long.
func (s *Server) guestMembership(ctx context.Context, tok string, g store.MeetingGuest, m *store.Meeting, cur synapseadmin.Member, inCall map[string]string, callsKnown bool, now time.Time) error {
	if cur.Membership != g.Membership {
		switch cur.Membership {
		case "knock":
			s.auditGuest(ctx, nil, "guest.knock", &g, "knocked")
		case "invite":
			s.auditGuest(ctx, nil, "guest.admit", &g, "admitted", "by", cur.Sender)
		case "join":
			if g.Membership != "invite" { // admitted and accepted between two passes
				by, _, _ := s.rooms.AdminInvite(ctx, tok, m.RoomID, g.MXID)
				s.auditGuest(ctx, nil, "guest.admit", &g, "admitted", "by", by)
			}
		}
		g.Membership = cur.Membership
		if err := s.store.Meetings().UpdateGuest(ctx, &g); err != nil {
			return err
		}
	}
	s.guests.mu.Lock()
	defer s.guests.mu.Unlock()
	if s.guests.seen == nil {
		s.guests.seen = map[string]guestSeen{}
	}
	if cur.Membership != "join" || !callsKnown {
		if cur.Membership != "join" {
			delete(s.guests.seen, g.MXID)
		}
		return nil
	}
	seen, ok := s.guests.seen[g.MXID]
	switch {
	case inCall[g.MXID] == m.RoomID:
		s.guests.seen[g.MXID] = guestSeen{now, true}
		return nil
	case !ok:
		s.guests.seen[g.MXID] = guestSeen{now, false}
		return nil
	case seen.inCall && now.Sub(seen.at) < guestDeparture, !seen.inCall && now.Sub(seen.at) < guestFirstCall:
		return nil
	}
	delete(s.guests.seen, g.MXID)
	if err := s.meetingRooms.Kick(ctx, tok, m.RoomID, g.MXID); err != nil {
		return err
	}
	s.auditGuest(ctx, nil, "guest.leave", &g, "removed; knocks again to return")
	g.Membership = "leave"
	return s.store.Meetings().UpdateGuest(ctx, &g)
}

// guestsInCalls maps each guest in a call to that call's Matrix room; nil with calls off.
func (s *Server) guestsInCalls(ctx context.Context) (map[string]string, error) {
	if s.rtc == nil {
		return nil, nil
	}
	out := map[string]string{}
	rooms, err := s.rtc.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	for _, rm := range rooms {
		ps, err := s.rtc.Participants(ctx, rm.Name)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if isGuest(p.Metadata) {
				out[p.Metadata] = rm.Metadata
			}
		}
	}
	return out, nil
}

// guestCallers is the call grant rule for guests: each guest whose account is live, whose link
// stands, whose meeting is open and inside one of its windows now, while guests are on, mapped
// to its meeting's room. Membership (exactly join) is checked by the caller.
func (s *Server) guestCallers(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	on, err := s.guestsOn(ctx)
	if err != nil || !on {
		return out, err
	}
	rows, err := s.store.Meetings().ActiveGuests(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, g := range rows {
		if g.MXID == "" || g.Revoked || !now.Before(g.SessionEnd) {
			continue
		}
		m, err := s.store.Meetings().GetMeeting(ctx, g.MeetingID)
		if err != nil {
			return nil, err
		}
		ws, err := s.store.Meetings().Windows(ctx, g.MeetingID)
		if err != nil {
			return nil, err
		}
		if _, open := openWindow(ws, now); open && m.State == store.MeetingOpen {
			out[g.MXID] = m.RoomID
		}
	}
	return out, nil
}
