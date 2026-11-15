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
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const (
	postSecret  = "kypost-pair-secret-0123456789abcdefghij!"
	chatCalSec  = "kycalendar-pair-secret-0123456789abcdef!"
	chatPost    = "kypost.chat.open"
	chatCal     = "kycalendar.chat.open"
	chatIssuer  = "https://id.example.com"
	erinUserURL = "https://chat.example.com/#/user/@erin:example.com"
)

type chatEnv struct {
	srv *api.Server
	st  store.Store
	mas *fakeMAS
}

// setupChat pairs KyPost and KyCalendar as asked. alice (sub-a) and erin (sub-e) chat; bob's
// (sub-b) MAS account is locked; root (sub-r) is an administrator with a chat account; nora
// (sub-n) has a KyMessages row but no Matrix account; nobody has sub-x.
func setupChat(t *testing.T, kypost, kycal bool) chatEnv {
	t.Helper()
	_, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	cfg.SSO.Enabled = true
	cfg.SSO.KyIdentityIssuer = chatIssuer
	if kypost {
		cfg.KyPost = config.PairConfig{URL: "https://mail.example.com", Secret: []byte(postSecret)}
	}
	if kycal {
		cfg.KyCalendar = config.PairConfig{URL: "https://cal.example.com", Secret: []byte(chatCalSec)}
	}
	srv := api.NewServer(cfg, st)
	mas := &fakeMAS{finished: map[string]bool{}, users: []matrixsync.User{
		{ID: uAlice, Username: "alice", Subject: "sub-a"},
		{ID: uBob, Username: "bob", Subject: "sub-b", Locked: true},
		{ID: uCarol, Username: "erin", Subject: "sub-e"},
		{ID: uDave, Username: "root", Subject: "sub-r"},
	}}
	srv.SetMatrixAdmin(mas)
	for _, u := range []store.User{
		{ID: "usr_a", Username: "alice-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-a"},
		{ID: "usr_b", Username: "bob-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-b"},
		{ID: "usr_e", Username: "erin-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-e"},
		{ID: "usr_r", Username: "root-ky", Role: "admin", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-r"},
		{ID: "usr_n", Username: "nora-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-n"},
	} {
		if err := st.Users().CreateUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	return chatEnv{srv, st, mas}
}

type chatSign struct {
	secret, event, eventID string
	at                     time.Time
}

// call posts body signed as o says; empty fields sign as KyPost.
func (e chatEnv) call(t *testing.T, body string, o chatSign) *httptest.ResponseRecorder {
	t.Helper()
	if o.event == "" {
		o.event = chatPost
	}
	if o.secret == "" {
		o.secret = postSecret
		if o.event == chatCal {
			o.secret = chatCalSec
		}
	}
	if o.eventID == "" {
		o.eventID = fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}
	if o.at.IsZero() {
		o.at = time.Now()
	}
	h, err := syncauth.Sign([]byte(o.secret), o.at, o.event, o.eventID, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/suite/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Apply(req)
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	return w
}

func chatBody(sub, target string) string {
	b, _ := json.Marshal(map[string]string{"sub": sub, "issuer": chatIssuer, "target_sub": target})
	return string(b)
}

func (e chatEnv) audits(t *testing.T) []string {
	t.Helper()
	rows, _, err := e.st.Audit().ListAuditRecords(context.Background(), 0, 100, "chat.")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].Action+" "+rows[i].UserID+" "+rows[i].Resource+" "+rows[i].Details)
	}
	return out
}

func wantURL(t *testing.T, w *httptest.ResponseRecorder, url string) {
	t.Helper()
	if got := decode[map[string]string](t, w); w.Code != 200 || len(got) != 1 || got["url"] != url {
		t.Fatalf("got %d %s, want %s", w.Code, w.Body, url)
	}
}

func TestSuiteChatOpensUserView(t *testing.T) {
	e := setupChat(t, true, true)
	wantURL(t, e.call(t, chatBody("sub-a", "sub-e"), chatSign{}), erinUserURL)
	wantURL(t, e.call(t, chatBody("sub-a", "sub-e"), chatSign{event: chatCal}), erinUserURL)
	// A trailing slash on the issuer is the same issuer.
	b := `{"sub":"sub-e","issuer":"https://id.example.com/","target_sub":"sub-a"}`
	wantURL(t, e.call(t, b, chatSign{}), "https://chat.example.com/#/user/@alice:example.com")
	got := e.audits(t)
	want := []string{
		`chat.open usr_a usr_e outcome="opened" via="kypost"`,
		`chat.open usr_a usr_e outcome="opened" via="kycalendar"`,
		`chat.open usr_e usr_a outcome="opened" via="kypost"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s", strings.Join(got, "\n"))
	}
	// The console's Matrix filter lists them.
	w := adminDo(t, e.srv, loginAs(t, e.srv, e.st, "root", "admin"), "GET", "/api/admin/audit?kind=matrix", nil)
	if n := strings.Count(w.Body.String(), `"action":"chat.open"`); w.Code != 200 || n != 3 {
		t.Fatalf("matrix audit kind: %d, %d chat.open rows", w.Code, n)
	}
}

// Each event type verifies only under its own pair's secret, and no other event type is
// accepted on the route.
func TestSuiteChatRefusesTheOtherPairsSignature(t *testing.T) {
	e := setupChat(t, true, true)
	body := chatBody("sub-a", "sub-e")
	for name, o := range map[string]chatSign{
		"kypost type, kycalendar secret":  {event: chatPost, secret: chatCalSec},
		"kycalendar type, kypost secret":  {event: chatCal, secret: postSecret},
		"meeting type, kycalendar secret": {event: "kycalendar.meeting.put", secret: chatCalSec},
		"meeting type, kypost secret":     {event: "kycalendar.meeting.put", secret: postSecret},
		"wrong secret":                    {secret: strings.Repeat("z", 40)},
		"stale":                           {at: time.Now().Add(-10 * time.Minute)},
		"future":                          {at: time.Now().Add(10 * time.Minute)},
	} {
		t.Run(name, func(t *testing.T) { wantCode(t, e.call(t, body, o), 401, "bad_signature") })
	}
	// A chat event is not a meeting.
	w := (meetingEnv{srv: e.srv}).call(t, "PUT", meetID, putBody("sub-a", "Sync"), signOpts{secret: chatCalSec, event: chatCal})
	wantCode(t, w, 401, "bad_signature")
	// Tampered body.
	h, _ := syncauth.Sign([]byte(postSecret), time.Now(), chatPost, "tamper-1", []byte(body))
	req := httptest.NewRequest("POST", "/api/suite/chat", strings.NewReader(chatBody("sub-a", "sub-r")))
	h.Apply(req)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	wantCode(t, rec, 401, "bad_signature")
	// Over 64 KiB.
	big := `{"sub":"sub-a","issuer":"` + chatIssuer + `","target_sub":"sub-e","pad":"` + strings.Repeat("x", 64<<10) + `"}`
	wantCode(t, e.call(t, big, chatSign{}), 401, "bad_signature")
	if got := e.audits(t); len(got) != 0 {
		t.Fatalf("unverified requests audited: %v", got)
	}
}

func TestSuiteChatReplayIsDuplicate(t *testing.T) {
	e := setupChat(t, true, true)
	o := chatSign{eventID: "chat-replay-1"}
	wantURL(t, e.call(t, chatBody("sub-a", "sub-e"), o), erinUserURL)
	if w := e.call(t, chatBody("sub-a", "sub-e"), o); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"outcome":"duplicate"}` {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	// The guard is shared with meetings: the ID is spent on the other pair too.
	if w := e.call(t, chatBody("sub-a", "sub-e"), chatSign{eventID: "chat-replay-1", event: chatCal}); !strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("replay on the other pair: %d %s", w.Code, w.Body)
	}
}

func TestSuiteChatUnpaired(t *testing.T) {
	onlyCal := setupChat(t, false, true)
	wantCode(t, onlyCal.call(t, chatBody("sub-a", "sub-e"), chatSign{}), 404, "kypost_off")
	wantURL(t, onlyCal.call(t, chatBody("sub-a", "sub-e"), chatSign{event: chatCal}), erinUserURL)
	onlyPost := setupChat(t, true, false)
	wantCode(t, onlyPost.call(t, chatBody("sub-a", "sub-e"), chatSign{event: chatCal}), 404, "kycalendar_off")
	wantURL(t, onlyPost.call(t, chatBody("sub-a", "sub-e"), chatSign{}), erinUserURL)
	// An unknown event type is judged against KyPost.
	wantCode(t, onlyCal.call(t, chatBody("sub-a", "sub-e"), chatSign{event: "kydrive.chat.open", secret: chatCalSec}), 404, "kypost_off")
	wantCode(t, onlyPost.call(t, chatBody("sub-a", "sub-e"), chatSign{event: "kydrive.chat.open"}), 401, "bad_signature")
	// Without the Matrix admin client nothing is paired.
	_, st, cfg := setupTestServer(t)
	cfg.KyPost = config.PairConfig{URL: "https://mail.example.com", Secret: []byte(postSecret)}
	wantCode(t, (chatEnv{srv: api.NewServer(cfg, st)}).call(t, chatBody("sub-a", "sub-e"), chatSign{}), 404, "kypost_off")
}

func TestSuiteChatRefusesPeople(t *testing.T) {
	e := setupChat(t, true, true)
	for _, c := range []struct {
		name, body, code string
		status           int
	}{
		{"unknown caller", chatBody("sub-x", "sub-e"), "no_user", 404},
		{"administrator caller", chatBody("sub-r", "sub-e"), "no_user", 404},
		{"locked caller", chatBody("sub-b", "sub-e"), "no_user", 404},
		{"caller without Matrix", chatBody("sub-n", "sub-e"), "no_user", 404},
		{"other issuer", `{"sub":"sub-a","issuer":"https://evil.example.com","target_sub":"sub-e"}`, "no_user", 404},
		{"target is caller", chatBody("sub-a", "sub-a"), "no_target", 404},
		{"administrator target", chatBody("sub-a", "sub-r"), "no_target", 404},
		{"locked target", chatBody("sub-a", "sub-b"), "no_target", 404},
		{"target without Matrix", chatBody("sub-a", "sub-n"), "no_target", 404},
		{"unknown target", chatBody("sub-a", "sub-x"), "no_target", 404},
		{"not JSON", `{"sub":`, "bad_body", 400},
		{"no target", `{"sub":"sub-a","issuer":"https://id.example.com"}`, "bad_body", 400},
		{"control character", chatBody("sub-a", "sub-e\n"), "bad_body", 400},
		{"long subject", chatBody("sub-a", strings.Repeat("e", 256)), "bad_body", 400},
		{"not an object", `["sub-a"]`, "bad_body", 400},
	} {
		t.Run(c.name, func(t *testing.T) { wantCode(t, e.call(t, c.body, chatSign{}), c.status, c.code) })
	}
	for _, a := range e.audits(t) {
		if strings.Contains(a, "sub-") || strings.Contains(a, "evil") {
			t.Fatalf("audit names a subject: %s", a)
		}
	}
}

func TestSuiteChatRateLimit(t *testing.T) {
	e := setupChat(t, true, true)
	for i := range 60 {
		event := chatPost
		if i%2 == 1 {
			event = chatCal
		}
		if w := e.call(t, chatBody("sub-a", "sub-e"), chatSign{event: event}); w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	w := e.call(t, chatBody("sub-a", "sub-e"), chatSign{eventID: "chat-limited"})
	wantCode(t, w, 429, "rate_limited")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// Another caller has their own budget.
	wantURL(t, e.call(t, chatBody("sub-e", "sub-a"), chatSign{}), "https://chat.example.com/#/user/@alice:example.com")
}

// A MAS outage answers 503 and forgets the event, so the caller's retry is applied.
func TestSuiteChatMatrixOutage(t *testing.T) {
	e := setupChat(t, true, true)
	e.mas.usersErr = errors.New("mas down")
	o := chatSign{eventID: "chat-outage-1"}
	wantCode(t, e.call(t, chatBody("sub-a", "sub-e"), o), 503, "unavailable")
	e.mas.usersErr = nil
	wantURL(t, e.call(t, chatBody("sub-a", "sub-e"), o), erinUserURL)
}

func TestKyPostPairsThroughKyIdentity(t *testing.T) {
	d := &fakePeerDirectory{status: 200, url: "https://mail.example.com", secret: []byte(strings.Repeat("p", 32))}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := r.BasicAuth(); !ok || id != "kymessage" || secret != "client-secret" || r.URL.Path != "/api/suite/peer" || r.URL.Query().Get("client") != "kypost" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		w.WriteHeader(d.status)
		switch d.status {
		case 200:
			_ = json.NewEncoder(w).Encode(map[string]string{"client": "kypost", "url": d.url, "secret": base64.StdEncoding.EncodeToString(d.secret)})
		case 404:
			_, _ = w.Write([]byte(`{"error":"no_peer"}`))
		}
	}))
	defer hs.Close()
	_, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.SSO.Enabled, cfg.SSO.KyIdentityIssuer, cfg.SSO.KyIdentityClientID, cfg.SSO.KyIdentitySecret = true, hs.URL, "kymessage", "client-secret"
	srv := api.NewServer(cfg, st)
	srv.SetMatrixAdmin(&fakeMAS{finished: map[string]bool{}})
	admin := loginAs(t, srv, st, "root", "admin")
	status := func() map[string]any {
		return decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/kypost", nil))
	}
	if got := status(); got["connected"] != false || got["reason"] != "pending" {
		t.Fatalf("before lookup: %v", got)
	}
	e := chatEnv{srv: srv}
	ctx := context.Background()
	if !api.ResolveKyPostForTest(srv, ctx) || status()["source"] != "kyidentity" || status()["url"] != "https://mail.example.com" {
		t.Fatalf("paired: %v", status())
	}
	if strings.Contains(fmt.Sprint(status()), "pppp") {
		t.Fatal("status carries the secret")
	}
	// The KyCalendar pair is untouched by the KyPost lookup.
	if got := decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/kycalendar", nil)); got["connected"] != false {
		t.Fatalf("kycalendar: %v", got)
	}
	if w := e.call(t, chatBody("sub-a", "sub-e"), chatSign{secret: string(d.secret)}); !strings.Contains(w.Body.String(), `"no_user"`) {
		t.Fatalf("pair secret refused: %d %s", w.Code, w.Body)
	}
	d.set(500, "")
	if api.ResolveKyPostForTest(srv, ctx) || status()["connected"] != true {
		t.Fatalf("an outage keeps the pair: %v", status())
	}
	d.set(200, "https://mail.example.com/mail")
	if api.ResolveKyPostForTest(srv, ctx) || status()["reason"] != "invalid_peer" {
		t.Fatalf("a URL with a path: %v", status())
	}
	d.set(404, "")
	if api.ResolveKyPostForTest(srv, ctx) || status()["reason"] != "no_peer" {
		t.Fatalf("unpaired in KyIdentity: %v", status())
	}
	wantCode(t, e.call(t, chatBody("sub-a", "sub-e"), chatSign{secret: string(d.secret)}), 404, "kypost_off")
	d.set(401, "")
	if api.ResolveKyPostForTest(srv, ctx) || status()["reason"] != "unauthorized" {
		t.Fatalf("refused credentials: %v", status())
	}
	member := loginAs(t, srv, st, "mia", "user")
	if w := adminDo(t, srv, member, "GET", "/api/admin/kypost", nil); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
}

func TestKyPostPairMatrixOff(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	if got := decode[map[string]any](t, adminDo(t, srv, admin, "GET", "/api/admin/kypost", nil)); got["reason"] != "matrix_off" {
		t.Fatalf("got %v", got)
	}
}
