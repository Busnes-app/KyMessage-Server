package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const agentPathID = "6f1c2b9e-0a4d-4f7e-9c3b-2d5e8a7f1b20"

// agentMAS is MAS's agent surface: users by name, personal sessions by ID.
type agentMAS struct {
	taken       map[string]bool
	n           int
	minted      []string // "user device"
	revoked     []string
	regenerated []string
	deactivated []string
	activity    map[string]matrixsync.Activity
	activityErr error
	mintErr     error
	// mintHeld, when set, receives each mint's arrival; the mint then waits for mintGate.
	mintHeld, mintGate chan struct{}
	// onRevoke, when set, runs before a revoke is recorded: a concurrent change landing then.
	onRevoke func(sessionID string)
}

func (f *fakeMAS) CreateAgentUser(_ context.Context, username string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ag.taken[username] {
		return "", matrixsync.ErrNameTaken
	}
	if f.ag.taken == nil {
		f.ag.taken = map[string]bool{}
	}
	f.ag.taken[username] = true
	f.ag.n++
	return fmt.Sprintf("01J9ZK8V6N3W4X5Y6Z7A8BAG%02d", f.ag.n), nil
}

func (f *fakeMAS) MintAgentToken(_ context.Context, userID, deviceID string) (string, string, error) {
	if f.ag.mintHeld != nil {
		f.ag.mintHeld <- struct{}{}
		<-f.ag.mintGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ag.mintErr != nil {
		return "", "", f.ag.mintErr
	}
	f.ag.n++
	f.ag.minted = append(f.ag.minted, userID+" "+deviceID)
	return fmt.Sprintf("SESS%02d", f.ag.n), fmt.Sprintf("mpt_secret%02d", f.ag.n), nil
}

// RotateAgentToken: MAS regenerates a live session in place; without one it mints.
func (f *fakeMAS) RotateAgentToken(ctx context.Context, userID, deviceID, sessionID string) (string, string, error) {
	if sessionID == "" {
		return f.MintAgentToken(ctx, userID, deviceID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ag.n++
	f.ag.regenerated = append(f.ag.regenerated, sessionID)
	return sessionID, fmt.Sprintf("mpt_secret%02d", f.ag.n), nil
}

func (f *fakeMAS) RevokeAgentSession(_ context.Context, sessionID string) error {
	if hook := f.ag.onRevoke; hook != nil {
		f.ag.onRevoke = nil
		hook(sessionID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ag.revoked = append(f.ag.revoked, sessionID)
	return nil
}

func (f *fakeMAS) AgentActivity(_ context.Context, sessionID string) (matrixsync.Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ag.activityErr != nil {
		return matrixsync.Activity{}, f.ag.activityErr
	}
	return f.ag.activity[sessionID], nil
}

func (f *fakeMAS) Deactivate(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ag.deactivated = append(f.ag.deactivated, id)
	return nil
}

type agentReply struct {
	Agent struct {
		ID, Username, MXID string
		DeviceID           string `json:"device_id"`
		TokenState         string `json:"token_state"`
		Owner              struct{ MXID string }
	}
	Token   string
	Configs map[string]string
}

func setupAgents(t *testing.T) (*api.Server, store.Store, *fakeMAS, *http.Cookie) {
	t.Helper()
	srv, st, cfg, f := setupMatrixServer(t)
	cfg.Matrix.Host = "https://matrix.example.com"
	return srv, st, f, loginAs(t, srv, st, "root", "admin")
}

func createAgent(t *testing.T, srv *api.Server, admin *http.Cookie, name string) agentReply {
	t.Helper()
	return decode[agentReply](t, adminDo(t, srv, admin, "POST", "/api/admin/agents", map[string]string{"name": name, "owner": "usr_dir_a"}))
}

func agentAudit(t *testing.T, st store.Store) []*store.AuditRecord {
	t.Helper()
	recs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 100, "agent.")
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestAgentLifecycle(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()

	c := createAgent(t, srv, admin, "agent-helper")
	if c.Token != "mpt_secret02" || c.Agent.MXID != "@agent-helper:example.com" || c.Agent.Owner.MXID != "@alice:example.com" || len(c.Agent.DeviceID) != 16 {
		t.Fatalf("create reply %+v", c)
	}
	for _, rt := range []string{"openclaw", "hermes"} {
		cfg := c.Configs[rt]
		if !strings.Contains(cfg, c.Token) || !strings.Contains(cfg, "@alice:example.com") || !strings.Contains(cfg, "https://matrix.example.com") {
			t.Errorf("%s config:\n%s", rt, cfg)
		}
	}
	if len(f.ag.minted) != 1 || f.ag.minted[0] != "01J9ZK8V6N3W4X5Y6Z7A8BAG01 "+c.Agent.DeviceID {
		t.Fatalf("minted %v", f.ag.minted)
	}
	row, err := st.Agents().GetAgent(ctx, c.Agent.ID)
	if err != nil || row.SessionID != "SESS02" || row.OwnerSubject != "sub-a" || row.TokenState != store.AgentTokenActive {
		t.Fatalf("row %+v %v", row, err)
	}

	list := adminDo(t, srv, admin, "GET", "/api/admin/agents", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"username":"agent-helper"`) || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"mxid":"@alice:example.com"`) {
		t.Fatalf("owners missing alice: %s", list.Body.String())
	}

	// A live token is regenerated in place: same session, same device, so the keys survive.
	r := decode[agentReply](t, adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/rotate", nil))
	if r.Token == c.Token || r.Token == "" || r.Agent.DeviceID != c.Agent.DeviceID || strings.Join(f.ag.regenerated, ",") != "SESS02" || len(f.ag.revoked) != 0 {
		t.Fatalf("rotate %+v regenerated %v revoked %v", r, f.ag.regenerated, f.ag.revoked)
	}

	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/revoke", nil); w.Code != http.StatusOK {
		t.Fatalf("revoke %d %s", w.Code, w.Body.String())
	}
	if row, _ := st.Agents().GetAgent(ctx, c.Agent.ID); row.SessionID != "" || row.TokenState != store.AgentTokenRevoked {
		t.Fatalf("after revoke %+v", row)
	}

	// Revoking deleted the device, so a new token comes on a new one.
	r2 := decode[agentReply](t, adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/rotate", nil))
	row, _ = st.Agents().GetAgent(ctx, c.Agent.ID)
	if r2.Agent.DeviceID == c.Agent.DeviceID || row.DeviceID != r2.Agent.DeviceID || row.TokenState != store.AgentTokenActive || row.SessionID != "SESS04" {
		t.Fatalf("reissue %+v row %+v", r2.Agent, row)
	}

	if w := adminDo(t, srv, admin, "DELETE", "/api/admin/agents/"+c.Agent.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	if _, err := st.Agents().GetAgent(ctx, c.Agent.ID); !errors.Is(err, store.ErrNotFound) || strings.Join(f.ag.deactivated, ",") != "01J9ZK8V6N3W4X5Y6Z7A8BAG01" || strings.Join(f.ag.revoked, ",") != "SESS02,SESS04" {
		t.Fatalf("after delete %v deactivated %v", err, f.ag.deactivated)
	}

	var actions []string
	for _, rec := range agentAudit(t, st) {
		actions = append(actions, rec.Action+" "+rec.Details)
		if strings.Contains(rec.Details+rec.Resource, "mpt_") {
			t.Errorf("token in audit: %+v", rec)
		}
	}
	want := `agent.delete outcome="deleted"|agent.rotate outcome="reissued on a new device"|agent.revoke outcome="revoked"|agent.rotate outcome="rotated"|agent.create outcome="created"`
	if strings.Join(actions, "|") != want {
		t.Fatalf("audit %q", actions)
	}
	if strings.Contains(adminDo(t, srv, admin, "GET", "/api/admin/agents", nil).Body.String(), "mpt_") {
		t.Fatal("token in list")
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/audit?kind=matrix", nil); !strings.Contains(w.Body.String(), `"agent.create"`) {
		t.Fatalf("agent rows missing from the matrix audit kind: %s", w.Body.String())
	}
}

func TestAgentCreateRefusals(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()
	for _, u := range []store.User{
		{ID: "usr_dir_z", Username: "zed", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-z"},                                       // no MAS account
		{ID: "usr_dir_m", Username: "mallory", Email: "Agent-Clash@example.com", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-m"}, // localpart agent-clash
		{ID: "usr_local", Username: "localop", Status: "active", SSOProvider: "local"},
	} {
		if err := st.Users().CreateUser(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}
	f.ag.taken = map[string]bool{"agent-taken": true}
	for _, tc := range []struct {
		name, owner string
		code        int
	}{
		{"helper", "usr_dir_a", http.StatusBadRequest},    // no prefix
		{"agent-", "usr_dir_a", http.StatusBadRequest},    // empty
		{"agent-UP", "usr_dir_a", http.StatusBadRequest},  // case
		{"agent-x_y", "usr_dir_a", http.StatusBadRequest}, // underscore
		{"agent-ok", "usr_dir_b", http.StatusBadRequest},  // owner disabled
		{"agent-ok", "usr_local", http.StatusBadRequest},  // owner not KyIdentity
		{"agent-ok", "nobody", http.StatusBadRequest},     // owner unknown
		{"agent-ok", "usr_dir_z", http.StatusConflict},    // owner never signed in to chat
		{"agent-clash", "usr_dir_a", http.StatusConflict}, // a member's email gives this name
		{"agent-taken", "usr_dir_a", http.StatusConflict}, // MAS holds it
	} {
		w := adminDo(t, srv, admin, "POST", "/api/admin/agents", map[string]string{"name": tc.name, "owner": tc.owner})
		if w.Code != tc.code {
			t.Errorf("%s/%s: %d %s", tc.name, tc.owner, w.Code, w.Body.String())
		}
	}
	if len(f.ag.minted) != 0 {
		t.Fatalf("minted on a refusal: %v", f.ag.minted)
	}
	recs := agentAudit(t, st)
	if len(recs) != 10 {
		t.Fatalf("want 10 refusals audited, got %d", len(recs))
	}
	for _, rec := range recs {
		if !strings.HasPrefix(rec.Details, `outcome="refused: `) {
			t.Errorf("audit %+v", rec)
		}
	}
	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents", map[string]any{"name": "agent-ok", "owner": "usr_dir_a", "extra": 1}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", w.Code)
	}
}

// A store failure after the mint must not leave a working token nobody can see.
func TestAgentCreateRevokesWhenTheRowFails(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	if err := st.Agents().CreateAgent(context.Background(), &store.Agent{ID: "pre", Username: "agent-dup", MASUserID: "other", DeviceID: "dev",
		OwnerSubject: "sub-a", SessionID: "S", TokenState: store.AgentTokenActive, CreatedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	w := adminDo(t, srv, admin, "POST", "/api/admin/agents", map[string]string{"name": "agent-dup", "owner": "usr_dir_a"})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "mpt_") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Join(f.ag.revoked, ",") != "SESS02" {
		t.Fatalf("revoked %v", f.ag.revoked)
	}
}

func TestAgentStepUp(t *testing.T) {
	srv, st, _, admin := setupAgents(t)
	c := createAgent(t, srv, admin, "agent-helper")
	stale := staleAdmin(t, st)
	for _, rt := range []struct{ method, path string }{
		{"POST", "/api/admin/agents"},
		{"POST", "/api/admin/agents/" + c.Agent.ID + "/rotate"},
		{"DELETE", "/api/admin/agents/" + c.Agent.ID},
	} {
		w := adminDo(t, srv, stale, rt.method, rt.path, map[string]string{"name": "agent-x", "owner": "usr_dir_a"})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	// Cutting access off never waits on step-up.
	if w := adminDo(t, srv, stale, "POST", "/api/admin/agents/"+c.Agent.ID+"/revoke", nil); w.Code != http.StatusOK {
		t.Fatalf("stale revoke %d %s", w.Code, w.Body.String())
	}
}

func TestAgentPathIDs(t *testing.T) {
	srv, _, _, admin := setupAgents(t)
	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents/not-a-uuid/revoke", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad id %d", w.Code)
	}
	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents/"+agentPathID+"/revoke", nil); w.Code != http.StatusNotFound {
		t.Errorf("missing id %d", w.Code)
	}
}

func TestExpireIdleAgents(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	ids := map[string]string{}
	for _, name := range []string{"agent-busy", "agent-edge", "agent-idle", "agent-fresh", "agent-never"} {
		c := createAgent(t, srv, admin, name)
		row, _ := st.Agents().GetAgent(ctx, c.Agent.ID)
		ids[name] = row.SessionID
	}
	f.ag.activity = map[string]matrixsync.Activity{
		ids["agent-busy"]:  {CreatedAt: now.Add(-400 * day), LastActive: at(89 * day)},
		ids["agent-edge"]:  {CreatedAt: now.Add(-400 * day), LastActive: at(90 * day)},
		ids["agent-idle"]:  {CreatedAt: now.Add(-400 * day), LastActive: at(200 * day)},
		ids["agent-fresh"]: {CreatedAt: now.Add(-1 * day)},  // rotated yesterday, not used yet
		ids["agent-never"]: {CreatedAt: now.Add(-91 * day)}, // minted and never used
	}
	if err := srv.ExpireIdleAgents(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.ag.revoked, ","); got != ids["agent-edge"]+","+ids["agent-idle"]+","+ids["agent-never"] {
		t.Fatalf("revoked %s", got)
	}
	states := map[string]string{}
	agents, _ := st.Agents().ListAgents(ctx)
	for _, a := range agents {
		states[a.Username] = a.TokenState
	}
	if states["agent-busy"] != "active" || states["agent-fresh"] != "active" || states["agent-edge"] != "expired" || states["agent-never"] != "expired" {
		t.Fatalf("states %v", states)
	}
	expired := 0
	for _, rec := range agentAudit(t, st) {
		if rec.Action == "agent.expire" {
			expired++
		}
	}
	if expired != 3 {
		t.Fatalf("expire audits %d", expired)
	}
	// A second run finds nothing more; an unreadable activity never revokes.
	f.ag.revoked, f.ag.activityErr = nil, errors.New("MAS down")
	if err := srv.ExpireIdleAgents(ctx, now.Add(1000*day)); err == nil || len(f.ag.revoked) != 0 {
		t.Fatalf("on read failure: %v revoked %v", err, f.ag.revoked)
	}
}

// Two reissues on a revoked agent race: the loser revokes its own token, so every session MAS
// minted is either recorded or revoked.
func TestAgentConcurrentReissue(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()
	c := createAgent(t, srv, admin, "agent-race")
	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/revoke", nil); w.Code != http.StatusOK {
		t.Fatalf("revoke %d", w.Code)
	}
	f.ag.mintHeld, f.ag.mintGate = make(chan struct{}), make(chan struct{})
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			codes <- adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/rotate", nil).Code
		}()
	}
	<-f.ag.mintHeld
	<-f.ag.mintHeld // both rotates read the revoked row before either mints
	close(f.ag.mintGate)
	got := []int{<-codes, <-codes}
	if got[0]+got[1] != http.StatusOK+http.StatusConflict {
		t.Fatalf("codes %v", got)
	}
	row, _ := st.Agents().GetAgent(ctx, c.Agent.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	// SESS02 is create's and was revoked; the race minted SESS03 and SESS04.
	for _, sess := range []string{"SESS03", "SESS04"} {
		if sess != row.SessionID && !slices.Contains(f.ag.revoked, sess) {
			t.Errorf("%s is neither recorded (%s) nor revoked %v", sess, row.SessionID, f.ag.revoked)
		}
	}
	if row.TokenState != store.AgentTokenActive || slices.Contains(f.ag.revoked, row.SessionID) {
		t.Fatalf("row %+v revoked %v", row, f.ag.revoked)
	}
}

// A revoke that read the row before a concurrent revoke and reissue landed ends the reissued
// session too, rather than recording "revoked" over it.
func TestAgentRevokeChasesAReissue(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()
	c := createAgent(t, srv, admin, "agent-chase")
	f.ag.onRevoke = func(string) {
		if err := st.Agents().SetAgentToken(ctx, c.Agent.ID, "SESS02", "D1", "", store.AgentTokenRevoked); err != nil {
			t.Error(err)
		}
		if err := st.Agents().SetAgentToken(ctx, c.Agent.ID, "", "D2", "SESS09", store.AgentTokenActive); err != nil {
			t.Error(err)
		}
	}
	if w := adminDo(t, srv, admin, "POST", "/api/admin/agents/"+c.Agent.ID+"/revoke", nil); w.Code != http.StatusOK {
		t.Fatalf("revoke %d %s", w.Code, w.Body.String())
	}
	row, _ := st.Agents().GetAgent(ctx, c.Agent.ID)
	if row.SessionID != "" || row.TokenState != store.AgentTokenRevoked || strings.Join(f.ag.revoked, ",") != "SESS02,SESS09" {
		t.Fatalf("row %+v revoked %v", row, f.ag.revoked)
	}
}

// Idle expiry that finds the agent reissued since it listed leaves the new session alone.
func TestExpireIdleAgentsSkipsAReissue(t *testing.T) {
	srv, st, f, admin := setupAgents(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := createAgent(t, srv, admin, "agent-stale")
	f.ag.activity = map[string]matrixsync.Activity{"SESS02": {CreatedAt: now.Add(-200 * 24 * time.Hour)}}
	f.ag.onRevoke = func(string) {
		_ = st.Agents().SetAgentToken(ctx, c.Agent.ID, "SESS02", "D1", "", store.AgentTokenRevoked)
		_ = st.Agents().SetAgentToken(ctx, c.Agent.ID, "", "D2", "SESS09", store.AgentTokenActive)
	}
	if err := srv.ExpireIdleAgents(ctx, now); err != nil {
		t.Fatal(err)
	}
	row, _ := st.Agents().GetAgent(ctx, c.Agent.ID)
	if row.SessionID != "SESS09" || row.TokenState != store.AgentTokenActive || strings.Join(f.ag.revoked, ",") != "SESS02" {
		t.Fatalf("row %+v revoked %v", row, f.ag.revoked)
	}
}

func TestAgentSkillDownload(t *testing.T) {
	srv, _, _, admin := setupAgents(t)
	w := adminDo(t, srv, admin, "GET", "/api/admin/agents/skill", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("%d %s", w.Code, w.Header())
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
		t.Fatal(err)
	}
}
