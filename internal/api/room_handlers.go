package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// A room ID is "!" and an opaque part, plus ":server" before room version 12. Never "/", so a
// path value cannot reach another admin route.
var roomIDPattern = regexp.MustCompile(`^![0-9A-Za-z._~=+-]+(?::[0-9A-Za-z.-]+(?::[0-9]{1,5})?)?$`)

func validRoomID(id string) bool { return len(id) <= 255 && roomIDPattern.MatchString(id) }

var (
	errRoomBusy = errors.New("a close or delete of this room is still running")
	errConfirm  = errors.New("confirmation does not match")
)

const maxMembersShown = 1000

// Room change budgets. The steps before the final Close or Delete (account, mint, reads, media)
// get roomChangeTimeout; that call gets roomStartTimeout of its own, and a recheck of the jobs
// after it fails gets masTimeout. Their sum stays well inside the console session's 5 minutes.
const (
	roomChangeTimeout = 2 * time.Minute
	roomStartTimeout  = 30 * time.Second
)

var (
	joinRules   = map[string]bool{"public": true, "invite": true, "knock": true, "restricted": true, "knock_restricted": true, "private": true}
	jobStatuses = map[string]bool{"scheduled": true, "active": true, "complete": true, "failed": true, "cancelled": true}
)

type roomView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Creator     string `json:"creator"`
	Members     int    `json:"members"`
	Encrypted   bool   `json:"encrypted"`
	Public      bool   `json:"public"`
	JoinRule    string `json:"join_rule"`
	StateEvents int    `json:"state_events"`
	Closed      bool   `json:"closed"`
}

func roomViewOf(r synapseadmin.Room, closed bool) roomView {
	rule := r.JoinRule
	if rule != "" && !joinRules[rule] {
		rule = "other"
	}
	return roomView{ID: r.ID, Name: clip200(r.Name), Alias: clip200(r.Alias), Creator: clip200(r.Creator), Members: r.Members,
		Encrypted: r.Encryption != "", Public: r.Public, JoinRule: rule, StateEvents: r.StateEvents, Closed: closed}
}

type jobView struct {
	DeleteID string `json:"delete_id"`
	Status   string `json:"status"`
}

func jobViews(jobs []synapseadmin.DeleteJob) []jobView {
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		st := j.Status
		if !jobStatuses[st] {
			st = "unknown"
		}
		out = append(out, jobView{DeleteID: clip200(j.ID), Status: st})
	}
	return out
}

// confirmText is what the admin types to delete a room: its name as the console shows it, or
// the room ID when the name is empty or holds characters nobody can type (controls, bidi and
// other format characters).
func confirmText(r synapseadmin.Room) string {
	name := clip200(r.Name)
	if name == "" || strings.IndexFunc(name, func(c rune) bool { return !unicode.IsPrint(c) || unicode.Is(unicode.Cf, c) }) >= 0 {
		return r.ID
	}
	return name
}

// roomBusy is our own check or Synapse's 400 "Purge already in progress".
func roomBusy(err error) bool {
	var se *synapseadmin.Error
	return errors.Is(err, errRoomBusy) ||
		errors.As(err, &se) && se.Status == http.StatusBadRequest && strings.Contains(se.Message, "already in progress")
}

// roomError reports a failed room call. Anything but a missing room or a running job means
// Synapse admin access is not working; the cause is shown (no error here carries a token).
func (s *Server) roomError(w http.ResponseWriter, err error) {
	var se *synapseadmin.Error
	switch {
	case roomBusy(err):
		s.writeError(w, http.StatusConflict, "A close or delete of this room is still running")
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		s.writeError(w, http.StatusNotFound, "No such room")
	default:
		s.writeError(w, http.StatusBadGateway, "Synapse admin access is not working: "+err.Error())
	}
}

// handleRooms lists rooms by name. Synapse's list has no blocked flag, so each listed room's is
// read too (at most 100, on the internal network).
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	offset, limit, ok := pageParams(r)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if !ok || len(search) > 255 {
		s.writeError(w, http.StatusBadRequest, "Invalid paging or search")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var total int
	views := []roomView{}
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		page, err := s.rooms.Rooms(ctx, tok, synapseadmin.RoomQuery{Offset: offset, Limit: limit, Search: search})
		if err != nil {
			return err
		}
		total = page.Total
		for _, rm := range page.Rooms {
			blocked, err := s.rooms.Blocked(ctx, tok, rm.ID)
			if err != nil {
				return err
			}
			views = append(views, roomViewOf(rm, blocked))
		}
		return nil
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"rooms": views, "total": total, "offset": offset, "limit": limit})
}

// handleRoom shows one room: its facts, members (never messages), the delete confirmation text
// and its close or delete jobs.
func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var body map[string]any
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		members, err := s.rooms.Members(ctx, tok, id)
		if err != nil {
			return err
		}
		blocked, err := s.rooms.Blocked(ctx, tok, id)
		if err != nil {
			return err
		}
		jobs, err := s.rooms.DeleteJobs(ctx, tok, id)
		if err != nil {
			return err
		}
		shown := make([]string, 0, min(len(members), maxMembersShown))
		for _, m := range members[:min(len(members), maxMembersShown)] {
			shown = append(shown, clip200(m))
		}
		body = map[string]any{"room": roomViewOf(room, blocked), "members": shown, "members_total": len(members),
			"confirm_text": confirmText(room), "jobs": jobViews(jobs)}
		return nil
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, body)
}

// handleRoomJobs lists the room's close and delete jobs. It does not need the room to exist:
// a purged room's job stays listed for a week.
func (s *Server) handleRoomJobs(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var jobs []synapseadmin.DeleteJob
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		var err error
		jobs, err = s.rooms.DeleteJobs(ctx, tok, id)
		return err
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobViews(jobs)})
}

// idle refuses a change while one of the room's jobs is listed as running, and returns the
// IDs it saw. Synapse v1.162.0 lists only active, complete and failed tasks
// (synapse/handlers/pagination.py:409-412), so a task still scheduled is not seen here.
func (s *Server) idle(ctx context.Context, tok, id string) (map[string]bool, error) {
	jobs, err := s.rooms.DeleteJobs(ctx, tok, id)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		if j.Status == "scheduled" || j.Status == "active" {
			return nil, errRoomBusy
		}
		seen[j.ID] = true
	}
	return seen, nil
}

// startJob makes the final Close or Delete call on a deadline of its own, so the steps before
// it cannot use up its time. When the call fails other than by Synapse's refusal, the job may
// still have started with its reply lost, so the jobs are read again and one not seen before
// is reported as started.
func (s *Server) startJob(ctx context.Context, tok, id string, seen map[string]bool, res *roomResult,
	call func(ctx context.Context, token, id string) (string, error)) error {
	jctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roomStartTimeout)
	defer cancel()
	jobID, err := call(jctx, tok, id)
	if err != nil && !roomBusy(err) {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), masTimeout)
		defer rcancel()
		if jobs, lerr := s.rooms.DeleteJobs(rctx, tok, id); lerr == nil {
			for _, j := range jobs {
				if !seen[j.ID] {
					jobID, err = j.ID, nil
					break
				}
			}
		}
	}
	if err != nil {
		return err
	}
	res.deleteID, res.outcome = jobID, "started"
	return nil
}

type roomResult struct{ outcome, deleteID, media string }

// roomChange runs one audited room change on a context detached from the request, so a dropped
// connection cannot start a job without its audit row. Every outcome is audited.
func (s *Server) roomChange(w http.ResponseWriter, r *http.Request, action, id string, change func(ctx context.Context, tok string, res *roomResult) error) {
	actor := s.actorID(r)
	// The listener's 15s WriteTimeout is for quick replies; this one may take minutes.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.roomBudget + roomStartTimeout + masTimeout + 15*time.Second))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.roomBudget)
	defer cancel()
	var res roomResult
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error { return change(ctx, tok, &res) })
	switch {
	case errors.Is(err, errConfirm):
		res.outcome = "refused: confirmation does not match"
	case roomBusy(err):
		res.outcome = "refused: a close or delete is running"
	case err != nil:
		res.outcome = "error: " + err.Error()
	}
	var kv []string
	if res.deleteID != "" {
		kv = append(kv, "delete_id", res.deleteID)
	}
	if res.media != "" {
		kv = append(kv, "media", res.media)
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	s.audit(actx, actor, r, action, id, auditFields(res.outcome, kv...))
	acancel()
	switch {
	case errors.Is(err, errConfirm):
		s.writeError(w, http.StatusBadRequest, "Type the room's name exactly as shown to delete it")
	case err != nil:
		s.roomError(w, err)
	default:
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": res.outcome, "delete_id": res.deleteID})
	}
}

// handleRoomClose removes every member and blocks the room; history stays. Closing a closed,
// empty room succeeds as already_closed.
func (s *Server) handleRoomClose(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	s.roomChange(w, r, "matrix.room_close", id, func(ctx context.Context, tok string, res *roomResult) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		seen, err := s.idle(ctx, tok, id)
		if err != nil {
			return err
		}
		blocked, err := s.rooms.Blocked(ctx, tok, id)
		if err != nil {
			return err
		}
		if blocked && room.LocalMembers == 0 {
			res.outcome = "already_closed"
			return nil
		}
		return s.startJob(ctx, tok, id, seen, res, s.rooms.Close)
	})
}

// decodeStrict decodes exactly one JSON object with only known fields.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil && dec.Decode(&struct{}{}) == io.EOF
}

// handleRoomDelete deletes the media Synapse can attribute to the room, then starts the purge.
// The confirmation is checked here against confirmText, never trusted from the page.
func (s *Server) handleRoomDelete(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !validRoomID(id) || !decodeStrict(w, r, &req) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID or request")
		return
	}
	s.roomChange(w, r, "matrix.room_delete", id, func(ctx context.Context, tok string, res *roomResult) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		if req.Confirm != confirmText(room) {
			return errConfirm
		}
		seen, err := s.idle(ctx, tok, id)
		if err != nil {
			return err
		}
		media, err := s.rooms.RoomMedia(ctx, tok, id)
		if err != nil {
			return err
		}
		res.media = "0"
		for i, m := range media {
			if err := s.rooms.DeleteMedia(ctx, tok, m); err != nil {
				return err
			}
			res.media = strconv.Itoa(i + 1)
		}
		return s.startJob(ctx, tok, id, seen, res, s.rooms.Delete)
	})
}
