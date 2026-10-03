package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// auditKinds maps the console's filter to action prefixes.
var auditKinds = map[string][]string{
	"auth":     {"auth.", "device.", "sso."},
	"backup":   {"backup.", "admin.backup_", "restore."},
	"branding": {"admin.brand_"},
	"matrix":   {"matrix."},
	"scim":     {"scim."},
}

type auditView struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Outcome string    `json:"outcome"`
	Details string    `json:"details"`
	IP      string    `json:"ip"`
}

var (
	detailsStrict = regexp.MustCompile(`^\w+="(?:[^"\\]|\\.)*"(?: \w+="(?:[^"\\]|\\.)*")*$`)
	detailField   = regexp.MustCompile(`(\w+)=("(?:[^"\\]|\\.)*")`)
)

// detailOutcome is the outcome= field of a row whose whole details string is key="quoted"
// tokens, unquoted; "" otherwise. Older rows hold unquoted remote text, which could pose as an
// outcome, so they show none.
func detailOutcome(details string) string {
	if !detailsStrict.MatchString(details) {
		return ""
	}
	for _, m := range detailField.FindAllStringSubmatch(details, -1) {
		if m[1] == "outcome" {
			v, _ := strconv.Unquote(m[2])
			return v
		}
	}
	return ""
}

// handleAudit pages the audit log newest first, optionally by kind. Read-only, any admin.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	offset, limit, ok := pageParams(r)
	kind := r.URL.Query().Get("kind")
	prefixes, known := auditKinds[kind]
	if !ok || (r.URL.Query().Has("kind") && !known) {
		s.writeError(w, http.StatusBadRequest, "Invalid paging or kind")
		return
	}
	recs, total, err := s.store.Audit().ListAuditRecords(r.Context(), offset, limit, prefixes...)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the audit log")
		return
	}
	names := map[string]string{}
	out := make([]auditView, 0, len(recs))
	for _, rec := range recs {
		actor := "scim" // SCIM rows carry the provisioned user's ID, not the actor's
		if !strings.HasPrefix(rec.Action, "scim.") {
			actor = s.actorName(r.Context(), rec.UserID, names)
		}
		// Stored fields are unbounded; the web refuses a page with one past its bound.
		out = append(out, auditView{ID: rec.ID, At: rec.CreatedAt, Actor: clip200(actor),
			Action: rec.Action, Target: clip200(rec.Resource), Outcome: clipTo(detailOutcome(rec.Details), 1024),
			Details: clipTo(rec.Details, 4096), IP: clipTo(rec.IPAddress, 64)})
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"records": out, "total": total, "offset": offset, "limit": limit})
}

// actorName resolves a row's user ID to a username, once per ID per page. Rows the server
// writes itself (scheduler, sweep) have none; a deleted user shows its ID.
func (s *Server) actorName(ctx context.Context, id string, seen map[string]string) string {
	if id == "" {
		return "system"
	}
	if n, ok := seen[id]; ok {
		return n
	}
	n := id
	if u, err := s.store.Users().GetUserByID(ctx, id); err == nil {
		n = u.Username
	}
	seen[id] = n
	return n
}
