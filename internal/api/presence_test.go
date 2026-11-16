package api_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

const (
	evBusy      = "kycalendar.presence.busy"
	evMe        = "kycalendar.presence.me"
	evOOO       = "kypost.presence.ooo"
	evLookupCal = "kycalendar.presence.lookup"
	evLookupPst = "kypost.presence.lookup"
)

var presenceRoutes = map[string]string{
	evBusy:      "/api/suite/presence/calendar",
	evMe:        "/api/suite/presence/me",
	evOOO:       "/api/suite/presence/out-of-office",
	evLookupCal: "/api/suite/presence/lookup",
	evLookupPst: "/api/suite/presence/lookup",
}

// presenceCall signs body as event under its pair's secret (secret overrides) and posts it
// to path (the event's route when empty).
func (e chatEnv) presenceCall(t *testing.T, event, path, secret, eventID, body string) *httptest.ResponseRecorder {
	t.Helper()
	if path == "" {
		path = presenceRoutes[event]
	}
	if secret == "" {
		secret = chatCalSec
		if strings.HasPrefix(event, "kypost.") {
			secret = postSecret
		}
	}
	if eventID == "" {
		eventID = fmt.Sprintf("p-%d", time.Now().UnixNano())
	}
	h, err := syncauth.Sign([]byte(secret), time.Now(), event, eventID, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Apply(req)
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	return w
}

func pbody(sub string, kv ...any) string {
	m := map[string]any{"sub": sub, "issuer": chatIssuer}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func block(from, to time.Duration) map[string]string {
	now := time.Now().UTC().Truncate(time.Second)
	return map[string]string{"start": now.Add(from).Format(time.RFC3339), "end": now.Add(to).Format(time.RFC3339)}
}

type personStatus struct {
	Sub   string     `json:"sub"`
	State string     `json:"state"`
	Until *time.Time `json:"until"`
}

func (e chatEnv) lookup(t *testing.T, caller string, subs ...string) map[string]personStatus {
	t.Helper()
	w := e.presenceCall(t, evLookupCal, "", "", "", pbody(caller, "subs", subs))
	if w.Code != 200 {
		t.Fatalf("lookup: %d %s", w.Code, w.Body)
	}
	var out struct{ People []personStatus }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.People) != len(subs) {
		t.Fatalf("lookup body %s: %v", w.Body, err)
	}
	m := map[string]personStatus{}
	for i, p := range out.People {
		if p.Sub != subs[i] {
			t.Fatalf("order: %s", w.Body)
		}
		m[p.Sub] = p
	}
	return m
}

func wantOutcome(t *testing.T, w *httptest.ResponseRecorder, outcome string) {
	t.Helper()
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got["outcome"] != outcome {
		t.Fatalf("got %d %s, want outcome %s", w.Code, w.Body, outcome)
	}
}

func TestPresenceFromCalendarAndOutOfOffice(t *testing.T) {
	e := setupChat(t, true, true)
	// Nothing known yet: unknown, never "available".
	if got := e.lookup(t, "sub-e", "sub-a"); got["sub-a"].State != "unknown" {
		t.Fatalf("before any push: %+v", got)
	}
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 2, "busy", []any{block(-30*time.Minute, 30*time.Minute), block(30*time.Minute, time.Hour)})), "applied")
	got := e.lookup(t, "sub-e", "sub-a")["sub-a"]
	if got.State != "in_a_meeting" || got.Until == nil || time.Until(*got.Until) < 59*time.Minute || time.Until(*got.Until) > time.Hour {
		t.Fatalf("in a meeting: %+v", got)
	}
	// An older revision changes nothing.
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 1, "busy", []any{})), "stale")
	// A newer one with no blocks frees her.
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 3, "busy", []any{})), "applied")
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "available" || got.Until != nil {
		t.Fatalf("free: %+v", got)
	}

	end := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	wantOutcome(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 10, "away", true,
		"start", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "end", end.Format(time.RFC3339))), "applied")
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "out_of_office" || got.Until == nil || !got.Until.Equal(end) {
		t.Fatalf("out of office: %+v", got)
	}
	wantOutcome(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 11, "away", false)), "applied")
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "available" {
		t.Fatalf("back: %+v", got)
	}
	// KyPost asks too, under its own pair.
	w := e.presenceCall(t, evLookupPst, "", "", "", pbody("sub-e", "subs", []string{"sub-a"}))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available"`) {
		t.Fatalf("kypost lookup: %d %s", w.Code, w.Body)
	}
	for _, a := range e.presenceAudits(t) {
		if strings.Contains(a, "sub-") || strings.Contains(a, "202") {
			t.Errorf("audit names a subject or a time: %s", a)
		}
	}
}

// What leaves KyCalendar and KyPost is start, end and on/off: any other field is refused.
func TestPresenceRefusesTitlesAndReplyText(t *testing.T) {
	e := setupChat(t, true, true)
	titled := `{"sub":"sub-a","issuer":"` + chatIssuer + `","revision":1,"busy":[{"start":"` + block(0, time.Hour)["start"] + `","end":"` + block(0, time.Hour)["end"] + `","title":"Layoffs"}]}`
	wantCode(t, e.presenceCall(t, evBusy, "", "", "", titled), 400, "bad_body")
	wantCode(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 1, "busy", []any{}, "attendees", []string{"sub-e"})), 400, "bad_body")
	wantCode(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 1, "away", true, "start", block(0, time.Hour)["start"],
		"end", block(0, time.Hour)["end"], "comment_local", "On leave")), 400, "bad_body")
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "unknown" {
		t.Fatalf("a refused push was stored: %+v", got)
	}
}

func TestPresenceOptOutAndManualOverride(t *testing.T) {
	e := setupChat(t, true, true)
	w := e.presenceCall(t, evMe, "", "", "", pbody("sub-a"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"share":true`) || !strings.Contains(w.Body.String(), `"manual":null`) {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 1, "busy", []any{block(-time.Minute, time.Hour)})), "applied")

	// A manual status wins over the meeting until it expires.
	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	w = e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "manual", map[string]string{"state": "away", "until": until}))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"away"`) {
		t.Fatalf("set manual: %d %s", w.Code, w.Body)
	}
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "away" || got.Until != nil {
		t.Fatalf("manual: %+v", got)
	}
	wantCode(t, e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "manual", map[string]string{"state": "busy"})), 400, "bad_body")
	e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "manual", nil))
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "in_a_meeting" {
		t.Fatalf("manual cleared: %+v", got)
	}

	// Off: unknown everywhere, inputs dropped, a manual status refused.
	w = e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "share", false))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"share":false`) {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "unknown" || got.Until != nil {
		t.Fatalf("opted out: %+v", got)
	}
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 2, "busy", []any{block(0, time.Hour)})), "sharing_off")
	wantOutcome(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 1, "away", false)), "sharing_off")
	wantCode(t, e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "manual", map[string]string{"state": "busy", "until": until})), 409, "sharing_off")
	if p, _ := e.st.Presence().Presence(t.Context(), []string{"sub-a"}); len(p["sub-a"].Busy) != 0 {
		t.Fatalf("kept busy blocks while off: %+v", p)
	}
	// Back on: nothing old comes back.
	e.presenceCall(t, evMe, "", "", "", pbody("sub-a", "share", true))
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "unknown" {
		t.Fatalf("back on: %+v", got)
	}
}

// Guests, outsiders and administrators read nothing and are read as nothing.
func TestPresenceMembersOnly(t *testing.T) {
	e := setupChat(t, true, true)
	for _, sub := range []string{"sub-a", "sub-r"} {
		e.st.Presence().SetCalendar(t.Context(), sub, 1, nil)
	}
	got := e.lookup(t, "sub-e", "sub-a", "sub-r", "sub-x", "@guest-abc:example.com")
	if got["sub-a"].State != "available" {
		t.Fatalf("member: %+v", got)
	}
	for _, sub := range []string{"sub-r", "sub-x", "@guest-abc:example.com"} {
		if got[sub].State != "unknown" {
			t.Errorf("%s: %+v", sub, got[sub])
		}
	}
	// Callers: an administrator, an unknown subject, another issuer.
	for _, body := range []string{
		pbody("sub-r", "subs", []string{"sub-a"}),
		pbody("sub-x", "subs", []string{"sub-a"}),
		`{"sub":"sub-e","issuer":"https://other.example","subs":["sub-a"]}`,
	} {
		wantCode(t, e.presenceCall(t, evLookupCal, "", "", "", body), 404, "no_user")
	}
	wantCode(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-r", "revision", 1, "busy", []any{})), 404, "no_user")
	wantCode(t, e.presenceCall(t, evMe, "", "", "", pbody("sub-x")), 404, "no_user")
}

func TestPresenceRefusesUnverifiedRequests(t *testing.T) {
	e := setupChat(t, true, true)
	body := pbody("sub-a", "revision", 1, "busy", []any{})
	// Wrong secret, the other pair's secret, the wrong event type for the route.
	wantCode(t, e.presenceCall(t, evBusy, "", "wrong-secret-wrong-secret-wrong-secret!", "", body), 401, "bad_signature")
	wantCode(t, e.presenceCall(t, evBusy, "", postSecret, "", body), 401, "bad_signature")
	wantCode(t, e.presenceCall(t, evMe, presenceRoutes[evBusy], "", "", body), 401, "bad_signature")
	wantCode(t, e.presenceCall(t, evOOO, presenceRoutes[evBusy], postSecret, "", body), 401, "bad_signature")
	wantCode(t, e.presenceCall(t, evLookupPst, "", chatCalSec, "", pbody("sub-e", "subs", []string{"sub-a"})), 401, "bad_signature")
	wantCode(t, e.presenceCall(t, "kycalendar.chat.open", presenceRoutes[evLookupCal], "", "", pbody("sub-e", "subs", []string{"sub-a"})), 401, "bad_signature")
	// Oversize.
	big := pbody("sub-a", "revision", 1, "busy", []any{}, "pad", strings.Repeat("x", 70<<10))
	wantCode(t, e.presenceCall(t, evBusy, "", "", "", big), 401, "bad_signature")

	// Replay: the second delivery changes nothing.
	first := pbody("sub-a", "revision", 5, "busy", []any{block(0, time.Hour)})
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "evt-1", first), "applied")
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "evt-2", pbody("sub-a", "revision", 6, "busy", []any{})), "applied")
	wantOutcome(t, e.presenceCall(t, evBusy, "", "", "evt-1", first), "duplicate")
	if got := e.lookup(t, "sub-e", "sub-a")["sub-a"]; got.State != "available" {
		t.Fatalf("a replay changed the status: %+v", got)
	}
	if n := len(e.presenceAudits(t)); n != 3 { // two pushes and one lookup; refusals before verification are not audited
		t.Errorf("audits: %d %v", n, e.presenceAudits(t))
	}
}

func TestPresenceUnpaired(t *testing.T) {
	e := setupChat(t, false, false)
	wantCode(t, e.presenceCall(t, evBusy, "", "", "", pbody("sub-a", "revision", 1, "busy", []any{})), 404, "kycalendar_off")
	wantCode(t, e.presenceCall(t, evMe, "", "", "", pbody("sub-a")), 404, "kycalendar_off")
	wantCode(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 1, "away", false)), 404, "kypost_off")
	wantCode(t, e.presenceCall(t, evLookupCal, "", "", "", pbody("sub-e", "subs", []string{})), 404, "kycalendar_off")
	wantCode(t, e.presenceCall(t, evLookupPst, "", "", "", pbody("sub-e", "subs", []string{})), 404, "kypost_off")
	// One pair up does not open the other's routes.
	e = setupChat(t, false, true)
	wantCode(t, e.presenceCall(t, evOOO, "", "", "", pbody("sub-a", "revision", 1, "away", false)), 404, "kypost_off")
}

func TestPresenceLookupCapAndRateLimit(t *testing.T) {
	e := setupChat(t, true, true)
	subs := make([]string, 51)
	for i := range subs {
		subs[i] = fmt.Sprintf("sub-%d", i)
	}
	wantCode(t, e.presenceCall(t, evLookupCal, "", "", "", pbody("sub-e", "subs", subs)), 400, "too_many")
	if got := e.lookup(t, "sub-e", subs[:50]...); len(got) != 50 {
		t.Fatalf("at the cap: %d", len(got))
	}
	for i := range 59 { // 60 an hour; one spent above is not this route's
		if w := e.presenceCall(t, evMe, "", "", "", pbody("sub-a")); w.Code != 200 {
			t.Fatalf("call %d: %d %s", i, w.Code, w.Body)
		}
	}
	e.presenceCall(t, evMe, "", "", "", pbody("sub-a"))
	w := e.presenceCall(t, evMe, "", "", "evt-limited", pbody("sub-a"))
	wantCode(t, w, 429, "rate_limited")
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// Another user's budget is their own.
	if w := e.presenceCall(t, evMe, "", "", "", pbody("sub-e")); w.Code != 200 {
		t.Fatalf("other user: %d", w.Code)
	}
}

func (e chatEnv) presenceAudits(t *testing.T) []string {
	t.Helper()
	rows, _, err := e.st.Audit().ListAuditRecords(t.Context(), 0, 500, "presence.")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Action+" "+r.UserID+" "+r.Resource+" "+r.Details)
	}
	return out
}
