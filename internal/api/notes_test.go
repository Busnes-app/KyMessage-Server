package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const (
	uNotes      = "01J9ZK8V6N3W4X5Y6Z7A8BNOTE"
	notesMXID   = "@kymessages-notes:example.com"
	notesSecret = "nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"
)

func (f *fakeMAS) EnsureNotesUser(context.Context) (string, error) { return uNotes, nil }

func (f *fakeMAS) MintNotesToken(ctx context.Context, userID, deviceID string) (string, string, error) {
	return f.MintAgentToken(ctx, userID, deviceID)
}

// AdminInvite answers from invites: room → the bot's inviter, admin or not.
func (f *fakeRooms) AdminInvite(_ context.Context, tok, room, user string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", false, f.err
	}
	inv := f.invites[room]
	return inv.by, inv.admin, nil
}

type botInvite struct {
	by    string
	admin bool
}

func setupNotes(t *testing.T) (*api.Server, store.Store, *fakeMAS, *fakeRooms, *http.Cookie) {
	t.Helper()
	srv, st, cfg, f := setupMatrixServer(t)
	cfg.Matrix.NotesSecret = notesSecret
	cfg.Matrix.RTCHost = "https://sfu.example.com"
	f.users = append(f.users, matrixsync.User{ID: uNotes, Username: matrixsync.NotesUsername})
	rooms := newFakeRooms()
	rooms.invites = map[string]botInvite{}
	api.SetRoomAdminForTest(srv, rooms)
	return srv, st, f, rooms, loginAs(t, srv, st, "root", "admin")
}

func notesSession(t *testing.T, srv *api.Server, secret, device string, want int) map[string]string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/notes/session", strings.NewReader(fmt.Sprintf(`{"device_id":%q}`, device)))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("session: %d want %d: %s", w.Code, want, w.Body)
	}
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func TestNotesSessionFollowsTheSwitch(t *testing.T) {
	srv, st, mas, _, admin := setupNotes(t)
	notesSession(t, srv, "", "", http.StatusUnauthorized)
	notesSession(t, srv, strings.Repeat("x", 64), "", http.StatusUnauthorized)
	// Off by default.
	notesSession(t, srv, notesSecret, "", http.StatusForbidden)
	if w := adminDo(t, srv, admin, "POST", "/api/admin/notes/enable", nil); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	first := notesSession(t, srv, notesSecret, "", http.StatusOK)
	if first["user_id"] != notesMXID || len(first["device_id"]) != 16 || !strings.HasPrefix(first["access_token"], "mpt_") {
		t.Fatalf("first %v", first)
	}
	// The same device keeps its session and keys.
	again := notesSession(t, srv, notesSecret, first["device_id"], http.StatusOK)
	if again["device_id"] != first["device_id"] || len(mas.ag.regenerated) != 1 {
		t.Fatalf("again %v regenerated %v", again, mas.ag.regenerated)
	}
	// A bot that lost its store gets a new device; the old session is revoked.
	fresh := notesSession(t, srv, notesSecret, "", http.StatusOK)
	if fresh["device_id"] == first["device_id"] || len(mas.ag.revoked) != 1 {
		t.Fatalf("fresh %v revoked %v", fresh, mas.ag.revoked)
	}
	// A session MAS no longer regenerates (revoked outside KyMessages) gives way to a new device.
	mas.ag.rotateErr = errors.New("session revoked")
	moved := notesSession(t, srv, notesSecret, fresh["device_id"], http.StatusOK)
	mas.ag.rotateErr = nil
	if moved["device_id"] == fresh["device_id"] || len(mas.ag.revoked) != 2 {
		t.Fatalf("after a failed regenerate %v revoked %v", moved, mas.ag.revoked)
	}
	if w := adminDo(t, srv, admin, "POST", "/api/admin/notes/disable", nil); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if len(mas.ag.revoked) != 3 {
		t.Fatalf("disable revoked %v", mas.ag.revoked)
	}
	notesSession(t, srv, notesSecret, moved["device_id"], http.StatusForbidden)
	status := decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/notes", nil))
	if status["enabled"] != false || status["available"] != true || status["checkin"] == "" {
		t.Fatalf("status %v", status)
	}
	var actions []string
	recs, _, _ := st.Audit().ListAuditRecords(context.Background(), 0, 20)
	for _, r := range recs {
		actions = append(actions, r.Action)
		if strings.Contains(r.Details, "mpt_") {
			t.Fatalf("token in audit: %s", r.Details)
		}
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "notes.disable") || !strings.Contains(got, "notes.session") || !strings.Contains(got, "notes.enable") {
		t.Fatalf("audit %s", got)
	}
}

func TestNotesUnconfiguredIs404(t *testing.T) {
	srv, _, cfg, _ := setupMatrixServer(t)
	cfg.Matrix.NotesSecret = ""
	notesSession(t, srv, notesSecret, "", http.StatusNotFound)
}

// The bot joins only calls that exist, only while on, only on an admin's invite; the sweep
// evicts it when the invite no longer qualifies.
func TestNotesCallGrant(t *testing.T) {
	srv, st, _, rooms, _ := setupNotes(t)
	var created, removed []string
	listed := true
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/") {
		case "CreateRoom":
			created = append(created, "x")
			w.Write([]byte(`{}`))
		case "ListRooms":
			if !listed {
				w.Write([]byte(`{"rooms":[]}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"rooms": []map[string]string{{"name": matrixrtc.Hash(rGroup, "m.call#ROOM"), "metadata": rGroup}}})
		case "ListParticipants":
			fmt.Fprintf(w, `{"participants":[{"identity":"bot","metadata":%q},{"identity":"alice","metadata":"@alice:example.com"}]}`, notesMXID)
		case "RemoveParticipant":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			removed = append(removed, in["identity"])
			w.Write([]byte(`{}`))
		}
	}))
	defer live.Close()
	api.SetRTCForTest(srv, matrixrtc.New(live.URL, "key", strings.Repeat("s", 64)))
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"sub": notesMXID})
	}))
	defer verify.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: verify.URL})
	rooms.members[rGroup] = append(rooms.members[rGroup], notesMXID)
	grant := func(want int, reason string) map[string]string {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/matrix/rtc/sfu/get", strings.NewReader(`{"room":"`+rGroup+`","device_id":"NOTESDEV","openid_token":{"access_token":"o","matrix_server_name":"example.com"}}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != want || !strings.Contains(w.Body.String(), reason) {
			t.Fatalf("grant %d %s, want %d %q", w.Code, w.Body, want, reason)
		}
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	grant(403, "Chat access is disabled") // off
	if err := st.Settings().SetSetting(context.Background(), "notes_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	grant(403, "need an invite from the room's creator or an admin") // no invite
	rooms.invites[rGroup] = botInvite{"@bob:example.com", false}
	grant(403, "need an invite")
	rooms.invites[rGroup] = botInvite{"@alice:example.com", true}
	listed = false
	grant(403, "to start the call") // the bot never starts a call
	listed = true
	out := grant(200, "jwt")
	claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(out["jwt"], ".")[1])
	if !strings.Contains(string(claims), `"sub":"`+notesMXID+`:NOTESDEV"`) || len(created) != 0 {
		t.Fatalf("claims %s created %v", claims, created)
	}
	if err := srv.ReconcileCalls(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("sweep kept nobody: %v %v", removed, err)
	}
	// The inviter lost admin: evicted on the next pass.
	rooms.invites[rGroup] = botInvite{"@alice:example.com", false}
	if err := srv.ReconcileCalls(context.Background()); err != nil || strings.Join(removed, ",") != "bot" {
		t.Fatalf("removed %v %v", removed, err)
	}
	// Off again: evicted before any membership check.
	removed = nil
	rooms.invites[rGroup] = botInvite{"@alice:example.com", true}
	if err := st.Settings().SetSetting(context.Background(), "notes_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileCalls(context.Background()); err != nil || strings.Join(removed, ",") != "bot" {
		t.Fatalf("off: removed %v %v", removed, err)
	}
}

func notesConfig(t *testing.T, srv *api.Server, secret string, want int) map[string]bool {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/notes/config", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != want || (want == 200 && w.Header().Get("Cache-Control") != "no-store") {
		t.Fatalf("config: %d want %d: %s", w.Code, want, w.Body)
	}
	var out map[string]bool
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

// Screens need both switches; notes off turns them off for the bot whatever the setting says.
func TestNotesScreensSwitch(t *testing.T) {
	srv, st, _, _, admin := setupNotes(t)
	notesConfig(t, srv, strings.Repeat("x", 64), http.StatusUnauthorized)
	if got := notesConfig(t, srv, notesSecret, 200); got["enabled"] || got["screens"] {
		t.Fatalf("defaults %v", got)
	}
	if w := adminDo(t, srv, admin, "POST", "/api/admin/notes/screens/enable", nil); w.Code != http.StatusOK {
		t.Fatalf("screens enable: %d %s", w.Code, w.Body)
	}
	if got := notesConfig(t, srv, notesSecret, 200); got["screens"] {
		t.Fatalf("screens without notes %v", got)
	}
	adminDo(t, srv, admin, "POST", "/api/admin/notes/enable", nil)
	if got := notesConfig(t, srv, notesSecret, 200); !got["enabled"] || !got["screens"] {
		t.Fatalf("both on %v", got)
	}
	if status := decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/notes", nil)); status["screens"] != true {
		t.Fatalf("status %v", status)
	}
	adminDo(t, srv, admin, "POST", "/api/admin/notes/screens/disable", nil)
	if got := notesConfig(t, srv, notesSecret, 200); got["screens"] {
		t.Fatalf("screens off %v", got)
	}
	recs, _, _ := st.Audit().ListAuditRecords(context.Background(), 0, 20)
	var actions []string
	for _, r := range recs {
		actions = append(actions, r.Action)
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "notes.screens_enable") || !strings.Contains(got, "notes.screens_disable") {
		t.Fatalf("audit %s", got)
	}
}
