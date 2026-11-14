package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"

	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// MeetingRooms is the Synapse surface meeting rooms use; *synapseadmin.Client implements it.
type MeetingRooms interface {
	CreateMeetingRoom(ctx context.Context, token, name, organizer string) (string, error)
	Memberships(ctx context.Context, token, room string) (map[string]string, error)
	Invite(ctx context.Context, token, room, user string) error
	Kick(ctx context.Context, token, room, user string) error
	SetRoomName(ctx context.Context, token, room, name string) error
	SetOrganizer(ctx context.Context, token, room, organizer string) error
	ExemptFromRatelimit(ctx context.Context, token, user string) error
}

// Signed KyCalendar requests: 120 per organizer per hour, PUT and DELETE together.
const (
	meetingPerUser = 120
	meetingWindow  = time.Hour
)

var (
	errLimited       = errors.New("rate limited")
	errMeetingNoUser = errors.New("organizer has no chat account")
	errMeetingClosed = errors.New("meeting closed")
	errGuard         = errors.New("replay guard unavailable")
)

// meetingReplay is the persistent replay guard: an event ID is held through its signed time
// plus syncauth's window, across restarts.
type meetingReplay struct {
	ctx context.Context
	st  store.MeetingStore
}

func (g meetingReplay) Check(id string, at time.Time) error {
	if len(id) > 255 {
		return syncauth.ErrMissingFields
	}
	err := g.st.RememberMeetingEvent(g.ctx, id, at.Add(syncauth.DefaultWindow+time.Second))
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		return syncauth.ErrReplay
	case err != nil:
		return fmt.Errorf("%w: %v", errGuard, err)
	}
	return nil
}

// meetingRefusal writes a {"error","code"} refusal.
func (s *Server) meetingRefusal(w http.ResponseWriter, status int, code, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// meetingRequest verifies a signed KyCalendar request for the route's event type and returns
// its body and event. It writes the answer itself and returns ok false: 404 kycalendar_off
// while unpaired, 200 duplicate for a verified replay, 401 bad_signature for anything else
// unverified, 503 when the replay guard cannot decide. Unverified requests are not audited,
// so an anonymous caller cannot fill the audit log.
func (s *Server) meetingRequest(w http.ResponseWriter, r *http.Request, event string) ([]byte, syncauth.Event, bool) {
	peer := s.kycal.Peer()
	if peer == nil || s.mas == nil {
		s.meetingRefusal(w, http.StatusNotFound, "kycalendar_off", "KyCalendar is not paired with this server")
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
			s.kickKyCalendar() // KyCalendar may hold a rotated pair secret; lookups stay rate limited
		}
		s.meetingRefusal(w, http.StatusUnauthorized, "bad_signature", "Bad signature")
		return nil, ev, false
	}
	return body, ev, true
}

// forgetMeetingEvent lets KyCalendar's retry of ev through the guard: it was verified but not
// acted on.
func (s *Server) forgetMeetingEvent(ev syncauth.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), roomAuditTimeout)
	defer cancel()
	_ = s.store.Meetings().ForgetMeetingEvent(ctx, ev.ID)
}

// meetingLimited spends one of subject's requests; past the limit it answers 429.
func (s *Server) meetingLimited(w http.ResponseWriter, subject string) bool {
	key := "meeting:" + subject
	if s.allowAccountAttempt(key, meetingPerUser, meetingWindow) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(s.accountAttempts.resetIn(key).Seconds())+1))
	s.meetingRefusal(w, http.StatusTooManyRequests, "rate_limited", "Too many meeting changes")
	return true
}

// meetingPeople maps each subject who may chat to their Matrix user ID, by the rule call grants
// use: an unlocked MAS account linked to a subject the directory reports active (owned
// dedicated administrators are reported inactive). Subjects are this server's KyIdentity's.
func (s *Server) meetingPeople(ctx context.Context) (map[string]string, error) {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := s.store.Users().DirectoryStatuses(ctx, "kyidentity", s.config.SCIM.Enabled)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, u := range users {
		if u.Subject != "" && !u.Ambiguous && !u.Locked && !u.Deactivated && statuses[u.Subject] == "active" {
			out[u.Subject] = "@" + u.Username + ":" + s.config.Matrix.ServerName
		}
	}
	return out, nil
}

func (s *Server) consoleMXID() string {
	return "@" + matrixsync.ConsoleUsername + ":" + s.config.Matrix.ServerName
}

// meetingChange runs fn under the console account on a context detached from the request, so
// a dropped connection cannot leave a half-made change unaudited. Meeting changes run one at a
// time, so two first PUTs cannot make two rooms.
func (s *Server) meetingChange(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context) error) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(roomWriteTimeout(s.roomBudget)))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.roomBudget)
	defer cancel()
	s.meetingMu.Lock()
	defer s.meetingMu.Unlock()
	return fn(ctx)
}

func (s *Server) auditMeeting(r *http.Request, userID, action, id, outcome string, kv ...string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), roomAuditTimeout)
	defer cancel()
	s.audit(ctx, userID, r, action, id, auditFields(outcome, kv...))
}

// handleMeetingPut creates or reconciles a meeting's room (docs/KYCALENDAR.md).
func (s *Server) handleMeetingPut(w http.ResponseWriter, r *http.Request) {
	body, ev, ok := s.meetingRequest(w, r, kycalendar.EventPut)
	if !ok {
		return
	}
	id := r.PathValue("id")
	req, err := kycalendar.DecodePut(body)
	switch {
	case !kycalendar.ValidID(id):
		s.auditMeeting(r, "", "meeting.put", "", "refused: bad_body")
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid meeting ID")
		return
	case errors.Is(err, kycalendar.ErrGuests):
		s.auditMeeting(r, "", "meeting.put", id, "refused: guests_unsupported")
		s.meetingRefusal(w, http.StatusUnprocessableEntity, "guests_unsupported", "Guests cannot join meetings yet")
		return
	case err != nil:
		s.auditMeeting(r, "", "meeting.put", id, "refused: bad_body")
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid meeting")
		return
	}
	organizer := s.meetingOrganizer(r.Context(), req.Sub, req.Issuer)
	if organizer == nil {
		s.auditMeeting(r, "", "meeting.put", id, "refused: no_user")
		s.meetingRefusal(w, http.StatusNotFound, "no_user", "The organizer has no KyMessages account")
		return
	}
	if s.meetingLimited(w, req.Sub) {
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, organizer.ID, "meeting.put", id, "refused: rate_limited")
		return
	}
	var res meetingResult
	skipped := []string{}
	roomID := ""
	err = s.meetingChange(w, r, func(ctx context.Context) error {
		people, err := s.meetingPeople(ctx)
		if err != nil {
			return err
		}
		orgMXID, ok := people[req.Sub]
		if !ok {
			return errMeetingNoUser // no Matrix account yet, or locked
		}
		listed := []string{orgMXID}
		subOf := map[string]string{orgMXID: req.Sub}
		for _, sub := range req.MemberSubs {
			mxid, ok := people[sub]
			switch {
			case !ok:
				if !slices.Contains(skipped, sub) {
					skipped = append(skipped, sub)
				}
			case !slices.Contains(listed, mxid):
				listed = append(listed, mxid)
				subOf[mxid] = sub
			}
		}
		m, err := s.store.Meetings().GetMeeting(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			m = nil
		case err != nil:
			return err
		case m.State == store.MeetingClosed:
			roomID = m.RoomID
			return errMeetingClosed
		}
		return s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
			m, err := s.reconcileMeeting(ctx, tok, id, m, req, orgMXID, listed, &res)
			if m != nil {
				roomID = m.RoomID
			}
			for _, mxid := range res.refused {
				skipped = append(skipped, subOf[mxid])
			}
			return err
		})
	})
	kv := []string{"room", roomID, "invited", strconv.Itoa(res.invited), "removed", strconv.Itoa(res.removed), "skipped", strconv.Itoa(len(skipped))}
	switch {
	case errors.Is(err, errMeetingNoUser):
		s.auditMeeting(r, organizer.ID, "meeting.put", id, "refused: no_user")
		s.meetingRefusal(w, http.StatusNotFound, "no_user", "The organizer has no KyMessages account")
	case errors.Is(err, errMeetingClosed):
		s.auditMeeting(r, organizer.ID, "meeting.put", id, "refused: closed", "room", roomID)
		s.meetingRefusal(w, http.StatusConflict, "closed", "This meeting was closed")
	case err != nil:
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, organizer.ID, "meeting.put", id, "error: "+err.Error(), kv...)
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Chat is unavailable; try again")
	default:
		s.auditMeeting(r, organizer.ID, "meeting.put", id, res.outcome, kv...)
		s.writeJSON(w, http.StatusOK, map[string]any{"member_url": s.config.Matrix.ChatHost + "/#/room/" + roomID, "skipped_subs": skipped})
	}
}

// meetingOrganizer is the active everyday account linked to sub at this server's KyIdentity
// issuer, nil if none. Dedicated administrators and other roles are refused.
func (s *Server) meetingOrganizer(ctx context.Context, sub, issuer string) *store.User {
	dir, err := s.kyidentity.SuiteDirectory()
	if err != nil || dir == nil || strings.TrimSuffix(dir.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return nil
	}
	u, err := s.store.Users().GetUserBySSO(ctx, "kyidentity", sub)
	if err != nil || u.Role != "user" || u.Status != "active" {
		return nil
	}
	return u
}

type meetingResult struct {
	outcome          string // created or updated
	invited, removed int
	refused          []string // listed users Synapse would not invite (banned)
}

// reconcileMeeting creates the room on first use, then brings name, organizer and members in
// line with req. Only users the meeting itself listed before are ever removed, so people the
// organizer invited by hand stay. It returns the meeting row as stored.
func (s *Server) reconcileMeeting(ctx context.Context, tok, id string, m *store.Meeting, req kycalendar.PutRequest, orgMXID string, listed []string, res *meetingResult) (*store.Meeting, error) {
	if err := s.meetingRooms.ExemptFromRatelimit(ctx, tok, s.consoleMXID()); err != nil {
		return m, err
	}
	res.outcome = "updated"
	if m == nil {
		roomID, err := s.meetingRooms.CreateMeetingRoom(ctx, tok, req.Title, orgMXID)
		if err != nil {
			return nil, err
		}
		m = &store.Meeting{ID: id, RoomID: roomID, OrganizerSubject: req.Sub}
		if err := s.store.Meetings().CreateMeeting(ctx, m); err != nil {
			// Not recorded, so not reachable again: close it rather than leave it open.
			_, _ = s.rooms.Close(ctx, tok, roomID)
			return nil, err
		}
		res.outcome = "created"
	} else {
		blocked, err := s.rooms.Blocked(ctx, tok, m.RoomID)
		if err != nil {
			return m, err
		}
		if blocked { // closed from the console's Rooms page
			if err := s.store.Meetings().CloseMeeting(ctx, id); err != nil {
				return m, err
			}
			return m, errMeetingClosed
		}
		room, err := s.rooms.Room(ctx, tok, m.RoomID)
		if err != nil {
			return m, err
		}
		if room.Name != req.Title {
			if err := s.meetingRooms.SetRoomName(ctx, tok, m.RoomID, req.Title); err != nil {
				return m, err
			}
		}
		if m.OrganizerSubject != req.Sub {
			if err := s.meetingRooms.SetOrganizer(ctx, tok, m.RoomID, orgMXID); err != nil {
				return m, err
			}
		}
	}
	// Record every invite target before inviting: an invite whose reply is lost, or a failure
	// after it, must still leave that person removable by a later PUT.
	managed := slices.Clone(m.Members)
	for _, u := range listed {
		if !slices.Contains(managed, u) {
			managed = append(managed, u)
		}
	}
	if err := s.store.Meetings().UpdateMeeting(ctx, id, m.OrganizerSubject, managed); err != nil {
		return m, err
	}
	current, err := s.meetingRooms.Memberships(ctx, tok, m.RoomID)
	if err != nil {
		return m, err
	}
	for _, u := range m.Members {
		if st := current[u]; !slices.Contains(listed, u) && (st == "join" || st == "invite" || st == "knock") {
			if err := s.meetingRooms.Kick(ctx, tok, m.RoomID, u); err != nil {
				return m, err
			}
			res.removed++
		}
	}
	kept := make([]string, 0, len(listed))
	for _, u := range listed {
		if st := current[u]; st != "join" && st != "invite" {
			err := s.meetingRooms.Invite(ctx, tok, m.RoomID, u)
			var se *synapseadmin.Error
			if errors.As(err, &se) && se.Status == http.StatusForbidden {
				res.refused = append(res.refused, u) // banned from this room
				continue
			}
			if err != nil {
				return m, err
			}
			res.invited++
		}
		kept = append(kept, u)
	}
	if err := s.store.Meetings().UpdateMeeting(ctx, id, req.Sub, kept); err != nil {
		return m, err
	}
	m.OrganizerSubject, m.Members = req.Sub, kept
	return m, nil
}

// handleMeetingDelete closes a meeting's room the way the console's Close does: blocked,
// members out, history kept. Unknown and closed meetings answer closed too.
func (s *Server) handleMeetingDelete(w http.ResponseWriter, r *http.Request) {
	_, ev, ok := s.meetingRequest(w, r, kycalendar.EventClose)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !kycalendar.ValidID(id) {
		s.auditMeeting(r, "", "meeting.close", "", "refused: bad_body")
		s.meetingRefusal(w, http.StatusBadRequest, "bad_body", "Invalid meeting ID")
		return
	}
	var res roomResult
	var m *store.Meeting
	err := s.meetingChange(w, r, func(ctx context.Context) error {
		var err error
		m, err = s.store.Meetings().GetMeeting(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			m, res.outcome = nil, "unknown"
			return nil
		case err != nil:
			return err
		}
		// A closed meeting is checked again: its Close job may have failed after it started.
		if s.meetingLimited(w, m.OrganizerSubject) {
			return errLimited
		}
		err = s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
			room, err := s.rooms.Room(ctx, tok, m.RoomID)
			var se *synapseadmin.Error
			if errors.As(err, &se) && se.Status == http.StatusNotFound {
				res.outcome = "room_gone"
				return nil
			}
			if err != nil {
				return err
			}
			seen, err := s.idle(ctx, tok, m.RoomID)
			if err != nil {
				return err
			}
			blocked, err := s.rooms.Blocked(ctx, tok, m.RoomID)
			if err != nil {
				return err
			}
			if blocked && room.LocalMembers == 0 {
				res.outcome = "already_closed"
				return nil
			}
			return s.startJob(ctx, tok, m.RoomID, seen, &res, s.rooms.Close)
		})
		if err != nil || m.State == store.MeetingClosed {
			return err
		}
		return s.store.Meetings().CloseMeeting(ctx, id)
	})
	roomID := ""
	if m != nil {
		roomID = m.RoomID
	}
	kv := []string{"room", roomID}
	if res.deleteID != "" {
		kv = append(kv, "delete_id", res.deleteID)
	}
	switch {
	case errors.Is(err, errLimited):
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, "", "meeting.close", id, "refused: rate_limited", kv...)
	case roomBusy(err):
		// A close is already running; KyCalendar's retry finds it done.
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, "", "meeting.close", id, "refused: a close or delete is running", kv...)
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "The room is still closing; try again")
	case err != nil:
		s.forgetMeetingEvent(ev)
		s.auditMeeting(r, "", "meeting.close", id, "error: "+err.Error(), kv...)
		s.meetingRefusal(w, http.StatusServiceUnavailable, "unavailable", "Chat is unavailable; try again")
	default:
		s.auditMeeting(r, "", "meeting.close", id, res.outcome, kv...)
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "closed"})
	}
}
