package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
)

// masTimeout bounds one console request's MAS calls, under the listener's 15s WriteTimeout.
const masTimeout = 10 * time.Second

// masID matches MAS's ULIDs, so a path value can never steer an admin call elsewhere.
var masID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`).MatchString

// pageParams reads offset (default 0) and limit (default 50, at most 100).
func pageParams(r *http.Request) (offset, limit int, ok bool) {
	offset, limit = 0, 50
	q := r.URL.Query()
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, false
		}
		limit = n
	}
	return offset, limit, true
}

// auditDetailsMax is AuditSafe's byte cap on details.
const auditDetailsMax = 200

// sessionEndDetails formats the row, shortening the outcome by whole runes until the quoted
// details fit the audit cap, so the cut can never take a closing quote.
func sessionEndDetails(outcome, id string, kind matrixsync.SessionKind) string {
	r := []rune(outcome)
	for {
		d := fmt.Sprintf("outcome=%q session=%q kind=%q", string(r), id, kind)
		if len(d) <= auditDetailsMax || len(r) == 0 {
			return d
		}
		r = r[:len(r)-1]
	}
}

// matrixOn answers 404 when the Matrix stack is not configured: its routes do not exist then.
func (s *Server) matrixOn(w http.ResponseWriter) bool {
	if s.mas != nil {
		return true
	}
	s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat (Matrix) is not set up on this server", "code": "matrix_disabled"})
	return false
}

// matrixError reports a failed MAS call with its cause; the client's errors carry no secret.
func (s *Server) matrixError(w http.ResponseWriter, err error) {
	var se *matrixsync.StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		s.writeError(w, http.StatusNotFound, "No such Matrix user or session")
		return
	}
	s.writeError(w, http.StatusBadGateway, "Matrix admin API unavailable: "+err.Error())
}

type matrixUserView struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	MXID       string `json:"mxid"`
	Status     string `json:"status"`     // active, locked, deactivated, not_linked
	KyIdentity string `json:"kyidentity"` // the linked account's directory status, "unknown", or "" when not linked
}

func (s *Server) mxid(username string) string {
	return "@" + username + ":" + s.config.Matrix.ServerName
}

// handleMatrixUsers lists MAS users with their KyIdentity link. MAS is read whole (pages of
// 100), because every row's status needs the link list anyway; search and paging run here.
func (s *Server) handleMatrixUsers(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	offset, limit, ok := pageParams(r)
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	if !ok || len(search) > 255 {
		s.writeError(w, http.StatusBadRequest, "Invalid paging or search")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	users, err := s.mas.Users(ctx)
	if err != nil {
		s.matrixError(w, err)
		return
	}
	dir, err := s.store.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the KyIdentity directory")
		return
	}
	matched := []matrixUserView{}
	for _, u := range users {
		if search != "" && !strings.Contains(strings.ToLower(u.Username), search) {
			continue
		}
		v := matrixUserView{ID: u.ID, Username: u.Username, MXID: s.mxid(u.Username), Status: "active"}
		switch {
		case u.Deactivated:
			v.Status = "deactivated"
		case u.Locked:
			v.Status = "locked"
		case u.Subject == "":
			v.Status = "not_linked"
		}
		if u.Subject != "" {
			v.KyIdentity = "unknown"
			if st, known := dir[u.Subject]; known {
				v.KyIdentity = st
			}
		}
		matched = append(matched, v)
	}
	total := len(matched)
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"users": matched[min(offset, total):min(offset+limit, total)], "total": total, "offset": offset, "limit": limit,
		"directory_url": s.config.SSO.KyIdentityIssuer,
	})
}

type sessionView struct {
	Kind         string     `json:"kind"`
	ID           string     `json:"id"`
	Device       string     `json:"device"`
	Client       string     `json:"client"`
	IP           string     `json:"ip"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
}

// handleMatrixUserSessions lists one user's active sessions. IPs and clients are personal
// data: shown to admins, never logged.
func (s *Server) handleMatrixUserSessions(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !masID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid Matrix user ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	sessions, err := s.mas.Sessions(ctx, id)
	if err != nil {
		s.matrixError(w, err)
		return
	}
	out := make([]sessionView, 0, len(sessions))
	for _, x := range sessions {
		out = append(out, sessionView{Kind: string(x.Kind), ID: x.ID, Device: clip200(x.Device), Client: clip200(x.Client), IP: x.IP, CreatedAt: x.CreatedAt, LastActiveAt: x.LastActiveAt})
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// clip200 bounds a MAS-supplied label (device ids are client-chosen) to 200 bytes of valid UTF-8.
func clip200(v string) string {
	if len(v) <= 200 {
		return v
	}
	return strings.ToValidUTF8(v[:200], "")
}

// handleMatrixSessionFinish ends one MAS session. It runs detached, so a dropped connection
// cannot end a session without its audit row; every outcome after input parsing is audited.
// Ending an ended session succeeds as already_ended.
func (s *Server) handleMatrixSessionFinish(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	kind, ok := matrixsync.ParseSessionKind(r.PathValue("kind"))
	id := r.PathValue("id")
	if !ok || !masID(id) {
		s.writeError(w, http.StatusBadRequest, "Unknown session kind or ID")
		return
	}
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), masTimeout)
	defer cancel()
	mxid := ""
	record := func(outcome string) {
		actx, acancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer acancel()
		s.audit(actx, actor, r, "matrix.session_end", mxid, sessionEndDetails(outcome, id, kind))
	}
	fail := func(err error) {
		record("error: " + err.Error())
		s.matrixError(w, err)
	}
	sess, err := s.mas.Session(ctx, kind, id)
	if err != nil {
		fail(err)
		return
	}
	if sess.UserID == "" {
		record("refused: no user")
		s.writeError(w, http.StatusBadRequest, "That session belongs to no user")
		return
	}
	user, err := s.mas.User(ctx, sess.UserID)
	if err != nil {
		fail(err)
		return
	}
	mxid = s.mxid(user.Username)
	already, err := s.mas.FinishSession(ctx, kind, id)
	if err != nil {
		fail(err)
		return
	}
	outcome := "ended"
	if already {
		outcome = "already_ended"
	}
	record(outcome)
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome, "mxid": mxid})
}
