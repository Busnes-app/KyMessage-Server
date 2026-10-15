package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	agentskill "github.com/Busnes-app/ky_server_base/agent-skill"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

// agentIdle is how long a token may go unused before ExpireIdleAgents revokes it (D3).
const agentIdle = 90 * 24 * time.Hour

// agentName: the agent- prefix marks agents in every Matrix ID (D5).
var agentName = regexp.MustCompile(`^agent-[a-z0-9-]{1,32}$`).MatchString

type agentOwnerView struct {
	ID          string `json:"id"` // KyMessages user ID
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	MXID        string `json:"mxid"`
	Status      string `json:"status"` // the owner's directory status, or "unknown"
}

type agentView struct {
	ID            string         `json:"id"`
	Username      string         `json:"username"`
	MXID          string         `json:"mxid"`
	DeviceID      string         `json:"device_id"`
	Owner         agentOwnerView `json:"owner"`
	TokenState    string         `json:"token_state"` // active, revoked, expired
	Locked        bool           `json:"locked"`
	LastUsedAt    *time.Time     `json:"last_used_at"`
	LastUsedError string         `json:"last_used_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// agentOwners resolves owners: directory users by subject, and their MAS username by link.
type agentOwners struct {
	bySubject map[string]*store.User
	mas       map[string]matrixsync.User // by subject
}

func (s *Server) loadAgentOwners(ctx context.Context) (agentOwners, []matrixsync.User, error) {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return agentOwners{}, nil, err
	}
	o := agentOwners{bySubject: map[string]*store.User{}, mas: map[string]matrixsync.User{}}
	for _, u := range users {
		if u.Subject != "" && !u.Deactivated {
			o.mas[u.Subject] = u
		}
	}
	dir, _, err := s.store.Users().ListUsers(ctx, 0, 10000, store.UserFilter{})
	if err != nil {
		return agentOwners{}, nil, err
	}
	for _, u := range dir {
		if u.SSOProvider == "kyidentity" && u.SSOSubject != "" {
			o.bySubject[u.SSOSubject] = u
		}
	}
	return o, users, nil
}

func (s *Server) ownerView(o agentOwners, subject string) agentOwnerView {
	v := agentOwnerView{Status: "unknown"}
	if u := o.bySubject[subject]; u != nil {
		v.ID, v.Username, v.DisplayName, v.Status = u.ID, u.Username, u.DisplayName, u.Status
	}
	if m, ok := o.mas[subject]; ok {
		v.MXID = s.mxid(m.Username)
	}
	return v
}

func (s *Server) agentView(ctx context.Context, a *store.Agent, o agentOwners, mas []matrixsync.User) agentView {
	v := agentView{ID: a.ID, Username: a.Username, MXID: s.mxid(a.Username), DeviceID: a.DeviceID,
		Owner: s.ownerView(o, a.OwnerSubject), TokenState: a.TokenState, CreatedAt: a.CreatedAt}
	for _, u := range mas {
		if u.ID == a.MASUserID {
			v.Locked = u.Locked || u.Deactivated
		}
	}
	if a.SessionID != "" {
		act, err := s.mas.AgentActivity(ctx, a.SessionID)
		if err != nil {
			v.LastUsedError = err.Error()
		}
		v.LastUsedAt = act.LastActive
	}
	return v
}

// handleAgents lists agents and the members who can own one.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	o, mas, err := s.loadAgentOwners(ctx)
	if err != nil {
		s.matrixError(w, err)
		return
	}
	agents, err := s.store.Agents().ListAgents(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read agents")
		return
	}
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		out = append(out, s.agentView(ctx, a, o, mas))
	}
	owners := []agentOwnerView{}
	for subject, u := range o.bySubject {
		if v := s.ownerView(o, subject); u.Status == "active" && v.MXID != "" {
			owners = append(owners, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"agents": out, "owners": owners, "idle_days": int(agentIdle / (24 * time.Hour))})
}

// localpart is MAS's localpart for a KyIdentity email (matrixinit's claims template).
func localpart(email string) string {
	local, _, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789._=-", c) {
			return c
		}
		return '_'
	}, strings.ToLower(local))
}

// newDeviceID: 16 of [A-Z0-9], inside MAS's device rules ([A-Za-z0-9-], at least 10).
func newDeviceID() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

type agentTokenReply struct {
	Agent   agentView         `json:"agent"`
	Token   string            `json:"token"`
	Configs map[string]string `json:"configs"`
}

// tokenReply is the only reply that carries a token; it is never stored or logged.
func (s *Server) tokenReply(w http.ResponseWriter, v agentView, token string) {
	configs := map[string]string{}
	for _, rt := range agentskill.Runtimes {
		c, err := agentskill.Config(rt, agentskill.Values{Homeserver: s.config.Matrix.Host, AccessToken: token, Owner: v.Owner.MXID})
		if err == nil {
			configs[rt] = c
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, agentTokenReply{Agent: v, Token: token, Configs: configs})
}

// agentAction runs an agent change detached, so a dropped connection cannot leave a token
// minted or revoked without its audit row. record audits action with the outcome.
func (s *Server) agentAction(r *http.Request, action string) (ctx context.Context, cancel func(), record func(resource, outcome string)) {
	actor := s.actorID(r)
	ctx, cancel = context.WithTimeout(context.WithoutCancel(r.Context()), 2*masTimeout)
	record = func(resource, outcome string) {
		actx, acancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer acancel()
		s.audit(actx, actor, r, action, resource, auditFields(outcome))
	}
	return ctx, cancel, record
}

func (s *Server) handleAgentCreate(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Owner string `json:"owner"` // KyMessages user ID
	}
	if !decodeStrict(w, r, &req) {
		s.writeError(w, http.StatusBadRequest, "Body must be {\"name\", \"owner\"}")
		return
	}
	ctx, cancel, record := s.agentAction(r, "agent.create")
	defer cancel()
	mxid := s.mxid(req.Name)
	refuse := func(status int, msg string) {
		record(clip200(mxid), "refused: "+msg)
		s.writeError(w, status, msg)
	}
	if !agentName(req.Name) {
		refuse(http.StatusBadRequest, "Name must be agent- followed by 1-32 of a-z, 0-9, -")
		return
	}
	o, _, err := s.loadAgentOwners(ctx)
	if err != nil {
		record(mxid, "error: "+err.Error())
		s.matrixError(w, err)
		return
	}
	owner, err := s.store.Users().GetUserByID(ctx, req.Owner)
	if err != nil || owner.SSOProvider != "kyidentity" || owner.SSOSubject == "" || owner.Status != "active" {
		refuse(http.StatusBadRequest, "The owner must be an active KyIdentity member")
		return
	}
	if _, ok := o.mas[owner.SSOSubject]; !ok {
		refuse(http.StatusConflict, "The owner has not signed in to chat yet")
		return
	}
	for _, u := range o.bySubject {
		if localpart(u.Email) == req.Name {
			refuse(http.StatusConflict, "A member's email gives the same Matrix name")
			return
		}
	}
	masID, err := s.mas.CreateAgentUser(ctx, req.Name)
	if errors.Is(err, matrixsync.ErrNameTaken) {
		refuse(http.StatusConflict, "That Matrix name is taken")
		return
	}
	if err != nil {
		record(mxid, "error: "+err.Error())
		s.matrixError(w, err)
		return
	}
	device := newDeviceID()
	sessionID, token, err := s.mas.MintAgentToken(ctx, masID, device)
	if err != nil {
		// The unregistered account stays locked by the offboarding sweep (fail closed).
		record(mxid, "error: "+err.Error())
		s.matrixError(w, err)
		return
	}
	a := &store.Agent{ID: uuid.NewString(), Username: req.Name, MASUserID: masID, DeviceID: device, OwnerSubject: owner.SSOSubject,
		SessionID: sessionID, TokenState: store.AgentTokenActive, CreatedBy: s.actorID(r)}
	if err := s.store.Agents().CreateAgent(ctx, a); err != nil {
		if rerr := s.mas.RevokeAgentSession(ctx, sessionID); rerr != nil {
			err = errors.Join(err, rerr)
		}
		record(mxid, "error: "+err.Error())
		s.writeError(w, http.StatusInternalServerError, "Could not record the agent; its token was revoked")
		return
	}
	record(mxid, "created")
	s.tokenReply(w, s.agentView(ctx, a, o, nil), token)
}

// agentFor loads the path's agent, answering 400/404 itself.
func (s *Server) agentFor(w http.ResponseWriter, r *http.Request, ctx context.Context) (*store.Agent, bool) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid agent ID")
		return nil, false
	}
	a, err := s.store.Agents().GetAgent(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "No such agent")
		return nil, false
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read the agent")
		return nil, false
	}
	return a, true
}

// handleAgentRotate replaces a live token on the same device (the old one stops within 2
// minutes), or issues one on a new device for an agent whose token was revoked or expired.
func (s *Server) handleAgentRotate(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	ctx, cancel, record := s.agentAction(r, "agent.rotate")
	defer cancel()
	a, ok := s.agentFor(w, r, ctx)
	if !ok {
		return
	}
	mxid := s.mxid(a.Username)
	device := a.DeviceID
	if a.SessionID == "" {
		device = newDeviceID() // revoking deleted the old device and its keys
	}
	sessionID, token, err := s.mas.RotateAgentToken(ctx, a.MASUserID, device, a.SessionID)
	if err != nil {
		record(mxid, "error: "+err.Error())
		s.matrixError(w, err)
		return
	}
	if err := s.store.Agents().SetAgentToken(ctx, a.ID, a.SessionID, device, sessionID, store.AgentTokenActive); err != nil {
		// Another change won the row: an unrecorded session would outlive revoke and expiry.
		raced := errors.Is(err, store.ErrNotFound)
		if rerr := s.mas.RevokeAgentSession(ctx, sessionID); rerr != nil {
			err = errors.Join(err, rerr)
		}
		record(mxid, "error: "+err.Error())
		if raced {
			s.writeError(w, http.StatusConflict, "The agent changed meanwhile; the new token was revoked")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "Could not record the new token; it was revoked")
		return
	}
	outcome := "rotated"
	if device != a.DeviceID {
		outcome = "reissued on a new device"
	}
	a.DeviceID, a.SessionID, a.TokenState = device, sessionID, store.AgentTokenActive
	record(mxid, outcome)
	o, mas, err := s.loadAgentOwners(ctx)
	if err != nil {
		o = agentOwners{}
	}
	s.tokenReply(w, s.agentView(ctx, a, o, mas), token)
}

// endAgentToken revokes a's session in MAS, then records state. A session MAS already ended
// counts as revoked. store.ErrNotFound: the agent is gone or now holds another session.
func (s *Server) endAgentToken(ctx context.Context, a *store.Agent, state string) error {
	if a.SessionID != "" {
		if err := s.mas.RevokeAgentSession(ctx, a.SessionID); err != nil {
			return err
		}
	}
	if err := s.store.Agents().SetAgentToken(ctx, a.ID, a.SessionID, a.DeviceID, "", state); err != nil {
		return err
	}
	a.SessionID, a.TokenState = "", state
	return nil
}

// revokeAgent ends whichever session the agent holds, re-reading it when a concurrent rotate
// recorded a new one.
func (s *Server) revokeAgent(ctx context.Context, a *store.Agent) error {
	for range 3 {
		err := s.endAgentToken(ctx, a, store.AgentTokenRevoked)
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if a, err = s.store.Agents().GetAgent(ctx, a.ID); err != nil {
			return err
		}
	}
	return errors.New("the agent's token kept changing; try again")
}

// agentEndError answers a failed revoke or delete.
func (s *Server) agentEndError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "No such agent")
		return
	}
	s.matrixError(w, err)
}

func (s *Server) handleAgentRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	ctx, cancel, record := s.agentAction(r, "agent.revoke")
	defer cancel()
	a, ok := s.agentFor(w, r, ctx)
	if !ok {
		return
	}
	mxid := s.mxid(a.Username)
	if err := s.revokeAgent(ctx, a); err != nil {
		record(mxid, "error: "+err.Error())
		s.agentEndError(w, err)
		return
	}
	record(mxid, "revoked")
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "revoked"})
}

// handleAgentDelete ends the token and deactivates the account; messages stay (D6).
func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	ctx, cancel, record := s.agentAction(r, "agent.delete")
	defer cancel()
	a, ok := s.agentFor(w, r, ctx)
	if !ok {
		return
	}
	mxid := s.mxid(a.Username)
	err := s.revokeAgent(ctx, a)
	if err == nil {
		err = s.mas.Deactivate(ctx, a.MASUserID)
	}
	if err == nil {
		err = s.store.Agents().DeleteAgent(ctx, a.ID)
	}
	if err != nil {
		record(mxid, "error: "+err.Error())
		s.agentEndError(w, err)
		return
	}
	record(mxid, "deleted")
	s.writeJSON(w, http.StatusOK, map[string]string{"outcome": "deleted"})
}

// handleAgentSkill serves the skill folder as a zip; it holds no secret.
func (s *Server) handleAgentSkill(w http.ResponseWriter, r *http.Request) {
	data, err := agentskill.Zip()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not build the skill")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="kymessages-skill.zip"`)
	_, _ = w.Write(data)
}

// ExpireIdleAgents revokes every active agent token unused for agentIdle (D3), audited
// agent.expire. A token whose last use MAS cannot report is kept and logged: never cut off a
// working agent on a read failure. cmd/server's maintenance loop runs it hourly.
func (s *Server) ExpireIdleAgents(ctx context.Context, now time.Time) error {
	if s.mas == nil {
		return nil
	}
	agents, err := s.store.Agents().ListAgents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range agents {
		if a.TokenState != store.AgentTokenActive {
			continue
		}
		act, err := s.mas.AgentActivity(ctx, a.SessionID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		since := act.Since()
		if since.IsZero() || now.Sub(since) < agentIdle {
			continue
		}
		outcome := "expired"
		// ErrNotFound: an admin changed the agent since listing. The idle session is still
		// ended; the newer one is not this pass's to judge.
		if err := s.endAgentToken(ctx, a, store.AgentTokenExpired); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
			outcome = "error: " + err.Error()
		}
		actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := s.store.Audit().LogAudit(actx, &store.AuditRecord{Action: "agent.expire", Resource: s.mxid(a.Username),
			Details: auditFields(outcome, "idle_since", since.UTC().Format(time.RFC3339))}); err != nil {
			errs = append(errs, err)
		}
		acancel()
	}
	return errors.Join(errs...)
}
