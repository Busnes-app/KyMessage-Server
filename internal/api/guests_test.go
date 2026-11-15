package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// Meeting guests (guest_handlers.go). The fakes: guest sessions mint through MintAgentToken's
// counter; Synapse's knock and profile calls are recorded on fakeMeetingRooms.

func (f *fakeMAS) MintGuestToken(ctx context.Context, userID, deviceID string, ttl time.Duration) (string, string, error) {
	f.mu.Lock()
	f.ag.minted = append(f.ag.minted, "ttl "+strconv.Itoa(int(ttl.Round(time.Minute).Minutes())))
	f.mu.Unlock()
	return f.MintAgentToken(ctx, userID, deviceID)
}

func (f *fakeMeetingRooms) AllowKnocks(_ context.Context, tok, room string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms[room].JoinRule = "knock"
	return f.record("knocks " + room)
}

func (f *fakeMeetingRooms) MarkGuest(_ context.Context, tok, room, localpart string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("mark " + localpart)
}

func (f *fakeMeetingRooms) SetDisplayName(_ context.Context, tok, user, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guestToken[user] = tok
	return f.record("name " + tok + " " + name)
}

func (f *fakeMeetingRooms) Knock(_ context.Context, tok, room string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("knock " + tok); err != nil {
		return err
	}
	for u := range f.guestToken {
		if f.guestToken[u] == tok {
			f.member[room][u] = "knock"
		}
	}
	return nil
}

func (f *fakeMeetingRooms) MemberStates(_ context.Context, tok, room string) (map[string]synapseadmin.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]synapseadmin.Member{}
	for u, m := range f.member[room] {
		out[u] = synapseadmin.Member{Membership: m, Sender: f.sender[u]}
	}
	return out, f.callErr
}

type guestEnv struct {
	meetingEnv
	admin *http.Cookie
}

// setupGuests: setupMeetings with an admin and a PoW-solving client; guests off.
func setupGuests(t *testing.T) guestEnv {
	t.Helper()
	e := setupMeetings(t, true)
	e.cfg.Server.AppURL = "https://admin.example.com"
	e.cfg.Matrix.Host = "https://matrix.example.com"
	e.rooms.guestToken = map[string]string{}
	e.rooms.sender = map[string]string{}
	return guestEnv{e, loginAs(t, e.srv, e.st, "guestadmin", "admin")}
}

func (e guestEnv) on(t *testing.T) {
	t.Helper()
	if w := adminDo(t, e.srv, e.admin, "POST", "/api/admin/meeting-guests/enable", nil); w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
}

// window is now-10m to now+50m unless shifted.
func guestPut(title string, keys []string, windows ...[2]time.Time) string {
	var gs []map[string]string
	for _, k := range keys {
		gs = append(gs, map[string]string{"key": k})
	}
	var ws []map[string]string
	for _, w := range windows {
		ws = append(ws, map[string]string{"start": w[0].UTC().Format(time.RFC3339), "end": w[1].UTC().Format(time.RFC3339)})
	}
	b, _ := json.Marshal(map[string]any{"id": meetID, "sub": "sub-a", "issuer": calIssuer, "title": title, "member_subs": []string{"sub-e"}, "guests": gs, "windows": ws})
	return string(b)
}

func nowWindow() [2]time.Time {
	return [2]time.Time{time.Now().Add(-10 * time.Minute), time.Now().Add(50 * time.Minute)}
}

func (e guestEnv) put(t *testing.T, id, body string) map[string]string {
	t.Helper()
	w := e.call(t, "PUT", id, body, signOpts{})
	out := decode[struct {
		URLs map[string]string `json:"guest_urls"`
	}](t, w)
	if out.URLs == nil {
		t.Fatalf("no guest_urls: %s", w.Body)
	}
	return out.URLs
}

func token(t *testing.T, url string) string {
	t.Helper()
	tok, ok := strings.CutPrefix(url, "https://admin.example.com/meet#")
	if !ok || len(tok) != 43 {
		t.Fatalf("guest URL %q", url)
	}
	return tok
}

func solvePoW(t *testing.T, secret string) string {
	t.Helper()
	c, err := auth.GeneratePoWChallenge(50, secret)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= c.MaxNumber; n++ {
		if crypto.SHA256Hex([]byte(c.Salt+strconv.Itoa(n))) == c.Challenge {
			sol, _ := json.Marshal(auth.PoWSolution{Algorithm: c.Algorithm, Salt: c.Salt, Challenge: c.Challenge, Number: n, MaxNumber: c.MaxNumber, ExpiresAt: c.ExpiresAt, Signature: c.Signature})
			return base64.StdEncoding.EncodeToString(sol)
		}
	}
	t.Fatal("unsolvable")
	return ""
}

func (e guestEnv) meet(t *testing.T, path, peer string, body map[string]string) (int, map[string]any) {
	t.Helper()
	w := postJSON(t, e.srv, "/api/meet/"+path, peer, body)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s answered without no-store", path)
	}
	return w.Code, out
}

func (e guestEnv) join(t *testing.T, tok, name, peer string) (int, map[string]any) {
	t.Helper()
	return e.meet(t, "join", peer, map[string]string{"token": tok, "name": name, "captcha": solvePoW(t, e.cfg.Security.SessionSecret)})
}

func (e guestEnv) session(t *testing.T, origin, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/meet/session", strings.NewReader(code))
	req.Header.Set("Content-Type", "text/plain")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	return w
}

func (e guestEnv) guestAudits(t *testing.T) []string {
	t.Helper()
	rows, _, err := e.st.Audit().ListAuditRecords(context.Background(), 0, 200, "guest")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].Action+" "+rows[i].Resource+" "+rows[i].Details)
	}
	return out
}

func TestGuestsAreOffByDefault(t *testing.T) {
	e := setupGuests(t)
	if got := decode[map[string]any](t, adminDo(t, e.srv, e.admin, "GET", "/api/admin/meeting-guests", nil)); got["enabled"] != false {
		t.Fatalf("status %v", got)
	}
	wantCode(t, e.call(t, "PUT", meetID, guestPut("Sync", []string{"guestkey_000000000001"}, nowWindow()), signOpts{}), 422, "guests_off")
	// Without guests the PUT works as before, with an empty guest_urls.
	if urls := e.put(t, meetID, guestPut("Sync", nil, nowWindow())); len(urls) != 0 {
		t.Fatalf("urls %v", urls)
	}
	// Not even a well-formed link reaches anything while off.
	if code, out := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": strings.Repeat("A", 43)}); code != 404 || out["code"] != "not_found" {
		t.Fatalf("status while off: %d %v", code, out)
	}
	// Turning guests on needs a recent sign-in.
	stale := staleAdmin(t, e.st)
	if w := adminDo(t, e.srv, stale, "POST", "/api/admin/meeting-guests/enable", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale enable: %d %s", w.Code, w.Body)
	}
	if w := adminDo(t, e.srv, stale, "POST", "/api/admin/meeting-guests/disable", nil); w.Code != 200 {
		t.Fatalf("disable needs no step-up: %d %s", w.Code, w.Body)
	}
	member := loginAs(t, e.srv, e.st, "plainmember", "user")
	if w := adminDo(t, e.srv, member, "POST", "/api/admin/meeting-guests/disable", nil); w.Code != 403 {
		t.Fatalf("member disable: %d", w.Code)
	}
}

func TestGuestLinksFollowTheKeys(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k1, k2 := "guestkey_000000000001", "guestkey_000000000002"
	urls := e.put(t, meetID, guestPut("Sync", []string{k1, k2}, nowWindow()))
	if len(urls) != 2 || urls[k1] == urls[k2] {
		t.Fatalf("urls %v", urls)
	}
	t1, t2 := token(t, urls[k1]), token(t, urls[k2])
	if got := e.rooms.take(); !slices.Contains(got, "knocks "+meetRoomID) {
		t.Fatalf("the room did not become a waiting room: %v", got)
	}
	// The same keys again: no new links.
	if again := e.put(t, meetID, guestPut("Sync", []string{k2, k1}, nowWindow())); len(again) != 0 {
		t.Fatalf("re-issued %v", again)
	}
	if code, out := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": t1}); code != 200 || out["state"] != "open" {
		t.Fatalf("status %d %v", code, out)
	}
	// Dropping k1 revokes its link at once; k2 stands. Re-adding k1 issues a new link.
	if again := e.put(t, meetID, guestPut("Sync", []string{k2}, nowWindow())); len(again) != 0 {
		t.Fatalf("urls %v", again)
	}
	if code, _ := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": t1}); code != 404 {
		t.Fatalf("revoked link: %d", code)
	}
	if code, _ := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": t2}); code != 200 {
		t.Fatalf("kept link: %d", code)
	}
	back := e.put(t, meetID, guestPut("Sync", []string{k1, k2}, nowWindow()))
	if len(back) != 1 || token(t, back[k1]) == t1 {
		t.Fatalf("re-added %v", back)
	}
	// The token, the key and the title are never audited.
	for _, a := range append(e.audits(t), e.guestAudits(t)...) {
		if strings.Contains(a, t1) || strings.Contains(a, t2) || strings.Contains(a, k1) || strings.Contains(a, "Sync") {
			t.Fatalf("audit holds a token, key or title: %s", a)
		}
	}
	// A closed meeting takes its links with it.
	if w := e.call(t, "DELETE", meetID, `{}`, signOpts{}); w.Code != 200 {
		t.Fatalf("close %d %s", w.Code, w.Body)
	}
	if code, _ := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": t2}); code != 404 {
		t.Fatalf("link of a closed meeting: %d", code)
	}
}

func TestGuestWindows(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k := "guestkey_000000000001"
	later := [2]time.Time{time.Now().Add(2 * time.Hour), time.Now().Add(3 * time.Hour)}
	tok := token(t, e.put(t, meetID, guestPut("Sync", []string{k}, later))[k])
	code, out := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": tok})
	if code != 200 || out["state"] != "waiting" || out["opens_at"] == nil {
		t.Fatalf("before: %d %v", code, out)
	}
	// Outside every window the guest gets nothing: no account, no knock.
	if code, out := e.join(t, tok, "Visitor", "192.0.2.2:1"); code != 409 || out["code"] != "not_open" {
		t.Fatalf("join before: %d %v", code, out)
	}
	if len(e.mas.ag.minted) != 0 {
		t.Fatalf("minted outside a window: %v", e.mas.ag.minted)
	}
	past := [2]time.Time{time.Now().Add(-3 * time.Hour), time.Now().Add(-2 * time.Hour)}
	e.put(t, meetID, guestPut("Sync", []string{k}, past))
	if _, out := e.meet(t, "status", "192.0.2.1:1", map[string]string{"token": tok}); out["state"] != "ended" {
		t.Fatalf("after: %v", out)
	}
	// Windows past 91 days, or not UTC, are refused.
	far := [2]time.Time{time.Now().Add(92 * 24 * time.Hour), time.Now().Add(92*24*time.Hour + time.Hour)}
	wantCode(t, e.call(t, "PUT", meetID, guestPut("Sync", []string{k}, far), signOpts{}), 400, "bad_body")
	wantCode(t, e.call(t, "PUT", meetID, strings.Replace(guestPut("Sync", []string{k}, nowWindow()), "Z\"", "+02:00\"", 1), signOpts{}), 400, "bad_body")
	wantCode(t, e.call(t, "PUT", meetID, guestPut("Sync", []string{"alice@example.com"}, nowWindow()), signOpts{}), 400, "bad_body")
}

func TestGuestJoinAndSession(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k := "guestkey_000000000001"
	tok := token(t, e.put(t, meetID, guestPut("Sync", []string{k}, nowWindow()))[k])
	e.rooms.take()

	// The challenge is required, and spent once.
	if code, out := e.meet(t, "join", "192.0.2.2:1", map[string]string{"token": tok, "name": "Visitor"}); code != 403 || out["code"] != "captcha" {
		t.Fatalf("no captcha: %d %v", code, out)
	}
	// Names that would deceive members are refused.
	for _, name := range []string{"", "   ", strings.Repeat("x", 65), "Eve\u202Eniamda", "Eve\u200B", "Eve\nAdmin", "Eve\x00", "Eve\u2028x"} {
		if code, out := e.join(t, tok, name, "192.0.2.3:1"); code != 400 || out["code"] != "bad_name" {
			t.Fatalf("name %q: %d %v", name, code, out)
		}
	}
	// Token guessing: malformed and unknown tokens read the same.
	for _, guess := range []string{"", "short", strings.Repeat("A", 43), tok[:42] + "!", tok + "A"} {
		if code, out := e.join(t, guess, "Visitor", "192.0.2.4:1"); code != 404 && code != 400 {
			t.Fatalf("guess %q: %d %v", guess, code, out)
		}
	}

	code, out := e.join(t, tok, "  Visitor Ünal ", "192.0.2.5:1")
	if code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	code64, ok := strings.CutPrefix(out["next"].(string), "https://chat.example.com/kymessages-guest.html#")
	if !ok || len(code64) != 43 {
		t.Fatalf("next %v", out["next"])
	}
	calls := e.rooms.take()
	var guest string
	for _, c := range calls {
		if l, ok := strings.CutPrefix(c, "mark "); ok {
			guest = "@" + l + ":example.com"
		}
	}
	if !strings.HasPrefix(guest, "@guest-") || len(guest) != len("@guest-")+20+len(":example.com") {
		t.Fatalf("guest account %q from %v", guest, calls)
	}
	if !slices.Contains(calls, "name mpt_secret02 Visitor Ünal (guest)") || !slices.Contains(calls, "knock mpt_secret02") {
		t.Fatalf("calls %v", calls)
	}
	if m := e.mas.ag.minted; len(m) != 2 || m[0] != "ttl 50" || !strings.HasPrefix(m[1], "01J9ZK8V6N3W4X5Y6Z7A8BAG01 KYGUEST") {
		t.Fatalf("session must end with the window: %v", m)
	}

	// The code is for Element's origin only, once.
	if w := e.session(t, "https://evil.example.com", code64); w.Code != 403 {
		t.Fatalf("other origin: %d", w.Code)
	}
	if w := e.session(t, "", code64); w.Code != 403 {
		t.Fatalf("no origin: %d", w.Code)
	}
	w := e.session(t, "https://chat.example.com", code64)
	var s map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != 200 || s["access_token"] != "mpt_secret02" || s["room_id"] != meetRoomID || s["homeserver"] != "https://matrix.example.com" ||
		!strings.HasPrefix(s["device_id"], "KYGUEST") || w.Header().Get("Access-Control-Allow-Origin") != "https://chat.example.com" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session %d %v %v", w.Code, s, w.Header())
	}
	if w := e.session(t, "https://chat.example.com", code64); w.Code != 404 {
		t.Fatalf("replayed code: %d", w.Code)
	}

	// A second join on the same link (a forwarded link, or the same visitor again) ends the
	// first account: one seat per link.
	if code, _ := e.join(t, tok, "Second", "192.0.2.6:1"); code != 200 {
		t.Fatalf("second join %d", code)
	}
	if !slices.Equal(e.mas.ag.deactivated, []string{"01J9ZK8V6N3W4X5Y6Z7A8BAG01"}) || len(e.mas.ag.revoked) != 1 {
		t.Fatalf("first seat not ended: %v %v", e.mas.ag.deactivated, e.mas.ag.revoked)
	}
	audits := strings.Join(e.guestAudits(t), "\n")
	for _, want := range []string{"guest.knock " + meetID, "guest.expire " + meetID, `reason="replaced"`} {
		if !strings.Contains(audits, want) {
			t.Fatalf("audit lacks %q:\n%s", want, audits)
		}
	}
	for _, secret := range []string{"Visitor", "Second", tok, code64, "mpt_secret", k} {
		if strings.Contains(audits, secret) {
			t.Fatalf("audit holds %q:\n%s", secret, audits)
		}
	}
}

func TestGuestJoinRateLimits(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k := "guestkey_000000000001"
	tok := token(t, e.put(t, meetID, guestPut("Sync", []string{k}, nowWindow()))[k])
	for i := range 10 {
		if code, _ := e.join(t, tok, "", "192.0.2.9:1"); code != 400 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, out := e.join(t, tok, "Visitor", "192.0.2.9:1"); code != 429 || out["code"] != "rate_limited" {
		t.Fatalf("11th from one address: %d %v", code, out)
	}
	// Per link too, from many addresses.
	for i := range 10 {
		if code, _ := e.join(t, tok, "Visitor", fmt.Sprintf("198.51.100.%d:1", i)); code != 200 {
			t.Fatalf("join %d: %d", i, code)
		}
	}
	if code, _ := e.join(t, tok, "Visitor", "198.51.100.99:1"); code != 429 {
		t.Fatalf("11th on one link: %d", code)
	}
}

// The reconciler: knocks and admissions audited, a guest out of the call removed, every guest
// ended at its window's end, on revocation, on close and when guests are turned off.
func TestReconcileGuests(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	ctx := context.Background()
	k1, k2 := "guestkey_000000000001", "guestkey_000000000002"
	urls := e.put(t, meetID, guestPut("Sync", []string{k1, k2}, nowWindow()))
	seat := func(k string) string {
		t.Helper()
		before := e.rooms.take()
		_ = before
		if code, out := e.join(t, token(t, urls[k]), "Visitor", "192.0.2.7:1"); code != 200 {
			t.Fatalf("join %d %v", code, out)
		}
		for _, c := range e.rooms.take() {
			if l, ok := strings.CutPrefix(c, "mark "); ok {
				return "@" + l + ":example.com"
			}
		}
		t.Fatal("no account")
		return ""
	}
	g1, g2 := seat(k1), seat(k2)
	// The fake knock went by token; record both knocks.
	e.rooms.mu.Lock()
	e.rooms.member[meetRoomID][g1], e.rooms.member[meetRoomID][g2] = "invite", "knock"
	e.rooms.sender[g1] = aliceMX
	e.rooms.mu.Unlock()
	if err := e.srv.ReconcileGuests(ctx); err != nil {
		t.Fatal(err)
	}
	if a := strings.Join(e.guestAudits(t), "\n"); !strings.Contains(a, "guest.admit "+meetID) || !strings.Contains(a, `by="`+aliceMX+`"`) {
		t.Fatalf("admission not audited:\n%s", a)
	}

	// Both are allowed to call, each only in its own meeting's room.
	callers, err := api.GuestCallersForTest(e.srv, ctx)
	if err != nil || callers[g1] != meetRoomID || callers[g2] != meetRoomID || len(callers) != 2 {
		t.Fatalf("callers %v %v", callers, err)
	}

	// Revoking k2 ends g2 at the next pass; its row goes.
	e.put(t, meetID, guestPut("Sync", []string{k1}, nowWindow()))
	if err := e.srv.ReconcileGuests(ctx); err != nil {
		t.Fatal(err)
	}
	if callers, _ := api.GuestCallersForTest(e.srv, ctx); len(callers) != 1 || callers[g1] == "" {
		t.Fatalf("after revoke %v", callers)
	}
	if got := e.rooms.take(); !slices.Contains(got, "kick "+g2) {
		t.Fatalf("revoked guest not removed: %v", got)
	}
	if len(e.mas.ag.deactivated) != 1 {
		t.Fatalf("deactivated %v", e.mas.ag.deactivated)
	}

	// The window ends: g1 is ended too.
	e.put(t, meetID, guestPut("Sync", []string{k1}, [2]time.Time{time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Minute)}))
	if callers, _ := api.GuestCallersForTest(e.srv, ctx); len(callers) != 0 {
		t.Fatalf("calls outside the window: %v", callers)
	}
	api.ExpireGuestSessionsForTest(t, e.st)
	if err := e.srv.ReconcileGuests(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.mas.ag.deactivated) != 2 || len(e.mas.ag.revoked) != 2 {
		t.Fatalf("window end: %v %v", e.mas.ag.deactivated, e.mas.ag.revoked)
	}
	if a := strings.Join(e.guestAudits(t), "\n"); !strings.Contains(a, `reason="window_ended"`) || !strings.Contains(a, `reason="revoked"`) {
		t.Fatalf("expiry audit:\n%s", a)
	}
	if live := decode[map[string]any](t, adminDo(t, e.srv, e.admin, "GET", "/api/admin/meeting-guests", nil)); live["live"] != 0.0 {
		t.Fatalf("live %v", live)
	}
}

func TestGuestsOffEndsEverySession(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k := "guestkey_000000000001"
	tok := token(t, e.put(t, meetID, guestPut("Sync", []string{k}, nowWindow()))[k])
	if code, _ := e.join(t, tok, "Visitor", "192.0.2.7:1"); code != 200 {
		t.Fatal(code)
	}
	if w := adminDo(t, e.srv, e.admin, "POST", "/api/admin/meeting-guests/disable", nil); w.Code != 200 || strings.Contains(w.Body.String(), "not ended") {
		t.Fatalf("disable %d %s", w.Code, w.Body)
	}
	if len(e.mas.ag.deactivated) != 1 {
		t.Fatalf("guest not ended: %v", e.mas.ag.deactivated)
	}
	if callers, _ := api.GuestCallersForTest(e.srv, context.Background()); len(callers) != 0 {
		t.Fatalf("callers while off %v", callers)
	}
	if code, _ := e.join(t, tok, "Visitor", "192.0.2.8:1"); code != 404 {
		t.Fatalf("join while off: %d", code)
	}
	wantCode(t, e.call(t, "PUT", meetID, guestPut("Sync", []string{k}, nowWindow()), signOpts{}), 422, "guests_off")
}

// A joined guest out of the call longer than the departure timeout is removed, so it must
// knock again; one in the call stays.
func TestGuestDeparture(t *testing.T) {
	e := setupGuests(t)
	e.cfg.Matrix.RTCHost = "https://sfu.example.com"
	lk := &fakeLiveKit{participants: map[string][][2]string{}}
	lk.serve(t, e.srv)
	e.on(t)
	k1, k2 := "guestkey_000000000001", "guestkey_000000000002"
	urls := e.put(t, meetID, guestPut("Sync", []string{k1, k2}, nowWindow()))
	var gs []string
	for _, k := range []string{k1, k2} {
		e.rooms.take()
		if code, _ := e.join(t, token(t, urls[k]), "Visitor", "192.0.2.7:1"); code != 200 {
			t.Fatal(code)
		}
		for _, c := range e.rooms.take() {
			if l, ok := strings.CutPrefix(c, "mark "); ok {
				gs = append(gs, "@"+l+":example.com")
			}
		}
	}
	e.rooms.mu.Lock()
	e.rooms.member[meetRoomID][gs[0]], e.rooms.member[meetRoomID][gs[1]] = "join", "join"
	e.rooms.mu.Unlock()
	lk.mu.Lock()
	lk.rooms = [][2]string{{"call", meetRoomID}}
	lk.participants["call"] = [][2]string{{"id0", gs[0]}}
	lk.mu.Unlock()
	if err := e.srv.ReconcileGuests(context.Background()); err != nil {
		t.Fatal(err)
	}
	// gs[0] was in the call 30 seconds ago and left; gs[1] is in it now.
	lk.mu.Lock()
	lk.participants["call"] = [][2]string{{"id1", gs[1]}}
	lk.mu.Unlock()
	api.SetGuestSeenForTest(e.srv, gs[0], time.Now().Add(-30*time.Second), true)
	if err := e.srv.ReconcileGuests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.rooms.take(); !slices.Equal(got, []string{"kick " + gs[0]}) {
		t.Fatalf("calls %v", got)
	}
	if a := strings.Join(e.guestAudits(t), "\n"); !strings.Contains(a, "guest.leave "+meetID) {
		t.Fatalf("leave not audited:\n%s", a)
	}
	// A guest stays only in its own meeting's call: in another room's call, it is removed.
	e.rooms.fakeRooms.mu.Lock()
	e.rooms.fakeRooms.members[meetRoomID] = []string{gs[1]}
	e.rooms.fakeRooms.members["!other:example.com"] = []string{gs[1]}
	e.rooms.fakeRooms.mu.Unlock()
	lk.mu.Lock()
	lk.rooms = [][2]string{{"call", meetRoomID}, {"other", "!other:example.com"}}
	lk.participants["other"] = [][2]string{{"id1b", gs[1]}}
	lk.mu.Unlock()
	if err := e.srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lk.takeRemoved(); !slices.Contains(got, "other/id1b") || slices.Contains(got, "call/id1") {
		t.Fatalf("removed %v", got)
	}
}

// A PUT that shortens or drops the window an admitted guest joined in ends that guest at the
// next pass, though its session would run longer (review finding P2 on #135).
func TestShortenedWindowEndsTheGuest(t *testing.T) {
	e := setupGuests(t)
	e.on(t)
	k := "guestkey_000000000001"
	tok := token(t, e.put(t, meetID, guestPut("Sync", []string{k}, nowWindow()))[k])
	e.rooms.take()
	if code, _ := e.join(t, tok, "Visitor", "192.0.2.7:1"); code != 200 {
		t.Fatal(code)
	}
	var guest string
	for _, c := range e.rooms.take() {
		if l, ok := strings.CutPrefix(c, "mark "); ok {
			guest = "@" + l + ":example.com"
		}
	}
	e.rooms.mu.Lock()
	e.rooms.member[meetRoomID][guest] = "join"
	e.rooms.mu.Unlock()
	if err := e.srv.ReconcileGuests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.mas.ag.deactivated) != 0 {
		t.Fatal("ended inside its window")
	}
	// The window now ended a minute ago; the session's own end is untouched (50 minutes away).
	e.put(t, meetID, guestPut("Sync", []string{k}, [2]time.Time{time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)}))
	if err := e.srv.ReconcileGuests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.rooms.take(); !slices.Contains(got, "kick "+guest) {
		t.Fatalf("not removed: %v", got)
	}
	if len(e.mas.ag.deactivated) != 1 || len(e.mas.ag.revoked) != 1 {
		t.Fatalf("deactivated %v revoked %v", e.mas.ag.deactivated, e.mas.ag.revoked)
	}
	// Dropping every window ends a guest just the same.
	if code, _ := e.join(t, tok, "Visitor", "192.0.2.8:1"); code != 409 {
		t.Fatalf("join after the window: %d", code)
	}
}
