package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// agentFake is MAS's admin API for agent accounts: user creation and personal sessions.
type agentFake struct {
	mu          sync.Mutex
	bodies      []map[string]any // personal-session mints
	revoked     []string
	status      map[string]int // path → forced status
	noToken     bool
	regenID     string // the session ID regenerate answers with
	regenerated []string
	sessionAt   string // JSON attributes for GET personal-sessions/{id}
}

func newAgentFake(t *testing.T) (*agentFake, *Client) {
	t.Helper()
	f := &agentFake{status: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/oauth2/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":300}`)
			return
		}
		if code := f.status[r.Method+" "+r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/admin/v1/users":
			var b map[string]string
			_ = json.Unmarshal(body, &b)
			fmt.Fprintf(w, `{"data":{"id":"01AGENTUSER0000000000000001","attributes":{"username":%q}}}`, b["username"])
		case r.Method == "POST" && r.URL.Path == "/api/admin/v1/personal-sessions":
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			f.bodies = append(f.bodies, b)
			tok := `"mpt_secret"`
			if f.noToken {
				tok = `null`
			}
			fmt.Fprintf(w, `{"data":{"id":"01SESSION000000000000000001","attributes":{"access_token":%s}}}`, tok)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/regenerate"):
			f.regenerated = append(f.regenerated, string(body))
			fmt.Fprintf(w, `{"data":{"id":%q,"attributes":{"access_token":"mpt_new"}}}`, f.regenID)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/revoke"):
			f.revoked = append(f.revoked, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/admin/v1/personal-sessions/"), "/revoke"))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/admin/v1/personal-sessions/"):
			fmt.Fprintf(w, `{"data":{"id":"x","attributes":%s}}`, f.sessionAt)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL, "cid", "secret")
}

func TestCreateAgentUser(t *testing.T) {
	f, c := newAgentFake(t)
	id, err := c.CreateAgentUser(context.Background(), "agent-helper")
	if err != nil || id != "01AGENTUSER0000000000000001" {
		t.Fatalf("%q %v", id, err)
	}
	f.status["POST /api/admin/v1/users"] = http.StatusConflict
	if _, err := c.CreateAgentUser(context.Background(), "agent-helper"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("conflict: %v", err)
	}
}

// The scope is the token's whole power: client API on one device, nothing admin, no expiry.
func TestMintAgentTokenExactScopeNoExpiry(t *testing.T) {
	f, c := newAgentFake(t)
	id, tok, err := c.MintAgentToken(context.Background(), "01USER", "DEVICE0123456789")
	if err != nil || id != "01SESSION000000000000000001" || tok != "mpt_secret" {
		t.Fatalf("%q %q %v", id, tok, err)
	}
	want := map[string]any{"actor_user_id": "01USER", "human_name": "KyMessages agent",
		"scope": "urn:matrix:client:api:* urn:matrix:client:device:DEVICE0123456789"}
	if !reflect.DeepEqual(f.bodies[0], want) {
		t.Fatalf("mint body %v", f.bodies[0])
	}
}

func TestMintAgentTokenRevokesASessionWithoutAToken(t *testing.T) {
	f, c := newAgentFake(t)
	f.noToken = true
	if _, _, err := c.MintAgentToken(context.Background(), "01USER", "DEVICE0123456789"); err == nil {
		t.Fatal("accepted a session without a token")
	}
	if strings.Join(f.revoked, ",") != "01SESSION000000000000000001" {
		t.Fatalf("revoked %v", f.revoked)
	}
}

// A mint MAS completes while the caller gives up must still return its session, or it would
// be a working token nobody knows of.
func TestMintAgentTokenIsDetached(t *testing.T) {
	_, c := newAgentFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, _, err := c.MintAgentToken(ctx, "01USER", "DEVICE0123456789"); err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestRevokeAgentSession(t *testing.T) {
	f, c := newAgentFake(t)
	if err := c.RevokeAgentSession(context.Background(), "01S"); err != nil {
		t.Fatal(err)
	}
	f.status["POST /api/admin/v1/personal-sessions/01GONE/revoke"] = http.StatusConflict
	if err := c.RevokeAgentSession(context.Background(), "01GONE"); err != nil {
		t.Fatalf("already revoked: %v", err)
	}
	f.status["POST /api/admin/v1/personal-sessions/01BAD/revoke"] = http.StatusInternalServerError
	if err := c.RevokeAgentSession(context.Background(), "01BAD"); err == nil {
		t.Fatal("hid a failed revoke")
	}
}

func TestAgentActivity(t *testing.T) {
	f, c := newAgentFake(t)
	f.sessionAt = `{"created_at":"2026-07-01T00:00:00Z","last_active_at":"2026-10-01T08:00:00Z"}`
	a, err := c.AgentActivity(context.Background(), "01S")
	if err != nil || a.Since() != time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC) {
		t.Fatalf("%+v %v", a, err)
	}
	f.sessionAt = `{"created_at":"2026-07-01T00:00:00Z","last_active_at":null}`
	if a, _ := c.AgentActivity(context.Background(), "01S"); a.LastActive != nil || a.Since() != time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("never used: %+v", a)
	}
}

// Regenerating keeps the session and device, with no expiry; no session means a fresh mint.
func TestRotateAgentToken(t *testing.T) {
	f, c := newAgentFake(t)
	f.regenID = "01LIVE"
	id, tok, err := c.RotateAgentToken(context.Background(), "01USER", "DEVICE0123456789", "01LIVE")
	if err != nil || id != "01LIVE" || tok != "mpt_new" || strings.Join(f.regenerated, ",") != "{}" || len(f.bodies) != 0 {
		t.Fatalf("%q %q %v regenerated %v minted %v", id, tok, err, f.regenerated, f.bodies)
	}
	f.regenID = "01OTHER"
	if _, _, err := c.RotateAgentToken(context.Background(), "01USER", "DEVICE0123456789", "01LIVE"); err == nil {
		t.Fatal("accepted another session from regenerate")
	}
	id, _, err = c.RotateAgentToken(context.Background(), "01USER", "NEWDEVICE0123456", "")
	if err != nil || id != "01SESSION000000000000000001" || f.bodies[0]["scope"] != "urn:matrix:client:api:* urn:matrix:client:device:NEWDEVICE0123456" {
		t.Fatalf("reissue %q %v %v", id, err, f.bodies)
	}
}
