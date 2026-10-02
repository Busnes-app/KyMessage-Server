# Matrix Console 5a (Users, Health, Audit) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A KyMessages admin lists Matrix users and ends their sessions, checks every component's health and pinned version, and reads the audit log from the console, with one "Confirm it's you" step-up prompt.

**Architecture:** The existing MAS admin client (`internal/matrixsync`) gains session list/read/finish, single-user read and version; one client is shared by the sweep and the API. A new `internal/health` package probes each component in parallel and compares versions with pins generated from `docker-compose.matrix.yml`. `internal/api` adds five routes; the store's audit listing gains a kind filter. The web console gets Users, Health and Audit tabs, a real Overview and an `adminFetch` helper that turns `reauthentication_required` into a prompt and retries.

**Tech Stack:** Go 1.26 (stdlib, existing `pgx` and `yaml.v3`), MAS 1.26.0 admin API, Synapse 1.162.0, Element Web 1.12.30, Postgres 17.6, React 19 + TypeScript + Vite + vitest, Playwright, bash acceptance harness.

**Spec:** `docs/superpowers/specs/2026-10-02-matrix-console-5a-design.md`

## Open questions for the controller

Each comes with a recommendation. The plan implements the recommendation; change the task if you decide otherwise.

1. **How the console admin authenticates in the acceptance harness.** The harness's KyMessages has no KyIdentity sign-in client, only the directory webhook. It already signs in the local bootstrap admin with `app_api` in the `backup` step. **Recommend:** a new `console` step takes over that sign-in (password sign-in, so the session is fresh for step-up), and `backup` signs in again as it does today. The KyIdentity `reauth_url` path stays covered by `sso_stepup_test.go`.
2. **Audit index.** `audit_records` has `(action, id)` (migration 13) and the primary key. The kind filter uses `substr(action, 1, n) = prefix`, because `_` in `admin.backup_` is a LIKE wildcard and the two engines differ on LIKE case rules. That predicate scans; ordering by `id DESC` uses the primary key. **Recommend:** no new index or migration. Row counts are small at the target team size. Add one only when a measured page is slow.
3. **IP and device display.** MAS gives `last_active_ip` and the user agent. **Recommend:** show the full IP and the client string in the session table, which is admin-only and `no-store`. Never log them and never put them in audit rows; the audit row records the acting admin's IP as every other row does.
4. **End all sessions.** The routes are fixed by the spec and include no bulk route. **Recommend:** the web calls the per-session finish route once per session, in the server's order (browser sessions first, so a live MAS web session cannot silently sign Element back in). Each call is audited. The loop stops on a refused step-up. This is sequential HTTP, fine for a person's handful of sessions.
5. **Version probes.** These are confirmed from upstream source and the pinned images. Synapse: `/health` and the unauthenticated `/_synapse/admin/v1/server_version`. MAS: discovery for liveness, because its `health` resource is not on any listener in `mas.yaml.tmpl`, plus `/api/admin/v1/version` with the admin token we already hold (returns `v1.26.0`). Element: `/version`, the plain text `1.12.30`. Postgres: `SHOW server_version` as `kybackup` (returns `17.6`). **Recommend:** when liveness passes but the version read fails, report "up, version unknown" with the cause. No new credentials.
6. **Probe targets.** **Recommend:** use the Compose service origins as constants (`http://synapse:8008`, `http://mas:8080`, `http://element:8080`) in `health.ComposeTargets`, with no new env vars. The overlay is the only shipped Matrix deployment, and the app already joins `default`. Postgres uses the existing `KY_MATRIX_DB_HOST`.
7. **"Confirm it's you" and the retry.** The KyIdentity callback ends with a redirect to `/`, so a full-page redirect would lose the pending request, and replaying a POST after a redirect is unsafe. **Recommend:** the prompt opens `reauth_url` in a new tab and retries when the admin clicks Retry. A local admin is told to sign out and sign in again in another tab, then retry; the shared cookie makes the retry fresh.
8. **Users tab with Matrix off.** **Recommend:** show the tab, with "Chat (Matrix) is not set up on this server." from the route's `matrix_disabled` 404. The browser regressions, which run without Matrix, then cover it.
9. **Audit `details`.** The spec lists who, what, target, outcome, when and IP. The session id lives in `details`, and the acceptance must find it. **Recommend:** also return `details`. It is already bounded and printable (`AuditSafe` at write) and admin-only.

## Global Constraints

- KyIdentity is the only access switch. The console has no lock or unlock. The Users page says "Access is controlled in KyIdentity, which the sync enforces" and links there.
- Routes, exactly:
  - `GET /api/admin/matrix/users` (admin, paged)
  - `GET /api/admin/matrix/users/{id}/sessions` (admin)
  - `POST /api/admin/matrix/sessions/{kind}/{id}/finish` (fresh admin)
  - `GET /api/admin/health` (admin)
  - `GET /api/admin/audit?offset&limit&kind` (admin)
- Matrix off: Matrix routes 404 (`code: matrix_disabled`). Health shows KyMessages and its database only.
- Ending a session is audited as `matrix.session_end`, resource the MXID, details `session=… kind=… outcome=…`, on success and failure. Ending an already-ended session is success (`already_ended`).
- Health is probed on each request, in parallel: 3 s per probe, 5 s for the request. There is no history. Error text never contains a secret.
- Pinned versions come from `docker-compose.matrix.yml` through `go generate ./internal/health`. A test fails on drift.
- The audit page is read-only, for any admin, newest first, paged, with kinds `auth`, `backup`, `matrix`, `scim`.
- `requireFreshAdmin`'s message is generic. The web has one helper, `adminFetch`, that turns `reauthentication_required` into "Confirm it's you" and retries.
- No new dependencies, Go or npm. Use the existing design system (`dr-*`, `panel`, `badge`, `ky-nav-item`) and themes. Do not edit `web/src/ky-ui/`.
- The network self-check moves from Settings to Health.
- IP and device data stay in the console. They are never logged.
- Every web task rebuilds `web/dist` and commits it, because CI diffs it.
- AGPL rule: official images only. Health links each component to the exact upstream source of its running version.
- The acceptance bound for a console-ended session is 30 seconds. If it is missed, record the cause and stop. Do not loosen it.
- Commits end with a blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **Two step-up refusals at once** (a double click, or End all racing a single End). Every waiting call must resume on the one answer; none may hang. Test in Task 5 (`ConfirmItsYou.test.tsx`, concurrent refusals).
2. **A session MAS finished between the list and the click.** MAS answers 400; the console must report `already_ended`, audit it, and not show an error. Tests in Task 1 (`TestFinishSessionIsIdempotent`) and Task 4 (second finish).
3. **A component that hangs** (a black-holed MAS admin listener, a stuck Postgres). Health must answer within its deadline, with that component down and the others reported. Test in Task 3 (`TestCheckIsParallelAndBounded`).
4. **An upstream answering odd text for its version** (Element's SPA HTML for `/version`, a `javascript:` source). It must never become a link or markup, and the component stays up with the version unknown. Tests in Task 3 (`TestResultRefusesOddVersionText`) and Task 6 (non-https source dropped).
5. **A Postgres or MAS failure whose text would carry a credential.** The `kybackup` password and the MAS admin secret must be redacted from every probe error. Tests in Task 3 (`TestResultRedactsSecrets`, `TestPostgresProbeNeverEchoesPassword`) and Task 4 (health body never contains the password).

---

### Task 1: MAS client — sessions, finish, user, version

**Files:**
- Modify: `internal/matrixsync/mas.go` (`call` at lines 75-111, `Users` at 140-192)
- Create: `internal/matrixsync/sessions_test.go`

**Interfaces:**
- Produces:
  - `type StatusError struct{ Method, Path string; Status int }`, with the same `Error()` text as today.
  - `type SessionKind string` and the constants `OAuth2Session = "oauth2"`, `CompatSession = "compat"`, `BrowserSession = "browser"`.
  - `func ParseSessionKind(s string) (SessionKind, bool)`.
  - `type Session struct{ Kind SessionKind; ID, UserID, Device, Client, IP string; CreatedAt time.Time; LastActiveAt, FinishedAt *time.Time }`.
  - Methods on `*Client`:
    - `User(ctx, id string) (User, error)`
    - `Sessions(ctx, userID string) ([]Session, error)`: active only, browser sessions first, then oauth2, then compat.
    - `Session(ctx, kind SessionKind, id string) (Session, error)`
    - `FinishSession(ctx, kind SessionKind, id string) (already bool, err error)`
    - `Version(ctx) (string, error)`: no leading `v`.

MAS facts this task relies on, from MAS v1.26.0 source (`crates/handlers/src/admin/v1/mod.rs`, `admin/model.rs`, `*/finish.rs`, `version.rs`):
- List routes are `oauth2-sessions`, `compat-sessions` and `user-sessions`, each with `filter[user]`, `filter[status]=active|finished` and `page[first]`. `links.next` carries `page[after]`.
- Attributes:
  - oauth2: `user_id` (nullable), `scope`, `user_agent`, `last_active_at`, `last_active_ip`, `human_name`, `created_at`, `finished_at`.
  - compat: `device_id`, `user_agent`, `last_active_ip`, `human_name`, `created_at`, `finished_at`.
  - user (browser): `user_agent`, `last_active_ip`, `created_at`, `finished_at`.
- `POST …/{id}/finish` returns 200, 400 for "already finished", and 404 when the id is unknown.
- `GET /api/admin/v1/version` returns `{"version":"v1.26.0"}` and needs the admin token.
- The oauth2 device comes from the scope token `urn:matrix:client:device:<id>`; older clients send `urn:matrix:org.matrix.msc2967.client:device:<id>`.

- [ ] **Step 1: Write the failing tests.** Create `internal/matrixsync/sessions_test.go`:

```go
package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// sessionFake is MAS's admin API for user U1 with one active session of each kind, plus N1,
// an admin-client session that belongs to no user.
type sessionFake struct {
	mu       sync.Mutex
	finished map[string]bool
	finishes []string
	srv      *httptest.Server
}

func newSessionFake(t *testing.T) *sessionFake {
	t.Helper()
	f := &sessionFake{finished: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sessionFake) client() *Client { return NewClient(f.srv.URL, "cid", "secret-value") }

var fakeSessions = map[string]struct{ path, attrs string }{
	"B1": {"user-sessions", `{"created_at":"2026-10-01T10:00:00Z","finished_at":null,"user_id":"U1","user_agent":"Firefox","last_active_at":"2026-10-02T09:00:00Z","last_active_ip":"192.0.2.7"}`},
	"O1": {"oauth2-sessions", `{"created_at":"2026-10-01T10:01:00Z","finished_at":null,"user_id":"U1","user_session_id":"B1","client_id":"C1","scope":"openid urn:matrix:client:api:* urn:matrix:client:device:DEVOAUTH","user_agent":"Element","last_active_at":null,"last_active_ip":null,"human_name":"Element on Firefox"}`},
	"C1": {"compat-sessions", `{"user_id":"U1","device_id":"DEVCOMPAT","user_session_id":null,"redirect_uri":null,"created_at":"2026-10-01T10:02:00Z","user_agent":"legacy-bot/1","last_active_at":null,"last_active_ip":"198.51.100.4","finished_at":null,"human_name":null}`},
	"N1": {"oauth2-sessions", `{"created_at":"2026-10-01T10:03:00Z","finished_at":null,"user_id":null,"client_id":"C2","scope":"urn:mas:admin","user_agent":null,"last_active_at":null,"last_active_ip":null,"human_name":null}`},
}

func (f *sessionFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/oauth2/token" {
		fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer","expires_in":300}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "bad bearer", http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/admin/v1/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	attrs := func(id string) string {
		a := fakeSessions[id].attrs
		if f.finished[id] {
			a = strings.Replace(a, `"finished_at":null`, `"finished_at":"2026-10-02T10:00:00Z"`, 1)
		}
		return a
	}
	parts := strings.Split(rest, "/")
	switch {
	case rest == "version":
		fmt.Fprint(w, `{"version":"v1.26.0"}`)
	case rest == "users/U1":
		fmt.Fprint(w, `{"data":{"type":"user","id":"U1","attributes":{"username":"alice","locked_at":null,"deactivated_at":null}}}`)
	case len(parts) == 1:
		q := r.URL.Query()
		if q.Get("filter[user]") != "U1" || q.Get("filter[status]") != "active" || q.Get("page[first]") != "100" {
			http.Error(w, "bad query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		var data []string
		for _, id := range []string{"B1", "O1", "C1", "N1"} {
			s := fakeSessions[id]
			if s.path == parts[0] && !f.finished[id] && strings.Contains(s.attrs, `"user_id":"U1"`) {
				data = append(data, fmt.Sprintf(`{"type":"session","id":%q,"attributes":%s}`, id, attrs(id)))
			}
		}
		fmt.Fprintf(w, `{"data":[%s],"links":{}}`, strings.Join(data, ","))
	default:
		s, known := fakeSessions[parts[1]]
		if !known || s.path != parts[0] {
			http.Error(w, `{"errors":[{"title":"not found"}]}`, http.StatusNotFound)
			return
		}
		if len(parts) == 3 && parts[2] == "finish" && r.Method == http.MethodPost {
			f.finishes = append(f.finishes, parts[0]+"/"+parts[1])
			if f.finished[parts[1]] {
				http.Error(w, `{"errors":[{"title":"session is already finished"}]}`, http.StatusBadRequest)
				return
			}
			f.finished[parts[1]] = true
		}
		fmt.Fprintf(w, `{"data":{"type":"session","id":%q,"attributes":%s}}`, parts[1], attrs(parts[1]))
	}
}

func TestSessionsListsEveryKindForTheUser(t *testing.T) {
	got, err := newSessionFake(t).client().Sessions(context.Background(), "U1")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind                         SessionKind
		id, user, device, client, ip string
	}
	var rows []row
	for _, s := range got {
		rows = append(rows, row{s.Kind, s.ID, s.UserID, s.Device, s.Client, s.IP})
	}
	want := []row{
		{BrowserSession, "B1", "U1", "", "Firefox", "192.0.2.7"},
		{OAuth2Session, "O1", "U1", "DEVOAUTH", "Element on Firefox", ""},
		{CompatSession, "C1", "U1", "DEVCOMPAT", "legacy-bot/1", "198.51.100.4"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %+v\nwant %+v", rows, want)
	}
	if got[0].LastActiveAt == nil || !got[0].LastActiveAt.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) || got[1].LastActiveAt != nil {
		t.Fatalf("last active: %v, %v", got[0].LastActiveAt, got[1].LastActiveAt)
	}
}

// Ending an ended session is success: MAS answers 400 and the read-back shows it finished.
func TestFinishSessionIsIdempotent(t *testing.T) {
	f := newSessionFake(t)
	c := f.client()
	for i, want := range []bool{false, true} {
		already, err := c.FinishSession(context.Background(), OAuth2Session, "O1")
		if err != nil || already != want {
			t.Fatalf("finish %d: already=%v err=%v", i, already, err)
		}
	}
	if !reflect.DeepEqual(f.finishes, []string{"oauth2-sessions/O1", "oauth2-sessions/O1"}) {
		t.Fatalf("finishes %v", f.finishes)
	}
}

func TestFinishSessionFailures(t *testing.T) {
	c := newSessionFake(t).client()
	// MAS has no compat session O1: a 404 is an error, never "already ended".
	_, err := c.FinishSession(context.Background(), CompatSession, "O1")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("wrong kind: %v", err)
	}
	if _, err := c.FinishSession(context.Background(), SessionKind("device"), "O1"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestParseSessionKind(t *testing.T) {
	for _, k := range []string{"oauth2", "compat", "browser"} {
		if got, ok := ParseSessionKind(k); !ok || string(got) != k {
			t.Errorf("%q refused", k)
		}
	}
	for _, k := range []string{"", "user-sessions", "OAUTH2", "oauth2-sessions"} {
		if _, ok := ParseSessionKind(k); ok {
			t.Errorf("%q accepted", k)
		}
	}
}

func TestSessionUserAndVersion(t *testing.T) {
	c := newSessionFake(t).client()
	ctx := context.Background()
	s, err := c.Session(ctx, OAuth2Session, "N1")
	if err != nil || s.UserID != "" || s.ID != "N1" {
		t.Fatalf("session N1: %+v %v", s, err)
	}
	u, err := c.User(ctx, "U1")
	if err != nil || u.Username != "alice" || u.Locked || u.Deactivated {
		t.Fatalf("user U1: %+v %v", u, err)
	}
	v, err := c.Version(ctx)
	if err != nil || v != "1.26.0" {
		t.Fatalf("version %q %v", v, err)
	}
}
```

- [ ] **Step 2: Run, expect failure.** `go test ./internal/matrixsync/ -run 'Session|Version|ParseSessionKind'` → compile errors (`Sessions`, `StatusError`, … undefined).

- [ ] **Step 3: Implement.** In `mas.go`:

1. Add after `adminPrefix`:

```go
// StatusError is a non-2xx answer from the admin API. MAS's body is not kept: callers that
// need more ask MAS again.
type StatusError struct {
	Method, Path string
	Status       int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("MAS %s %s: HTTP %d", e.Method, e.Path, e.Status)
}
```

2. In `call`, replace `return fmt.Errorf("MAS %s %s: HTTP %d", method, path, resp.StatusCode)` with `return &StatusError{Method: method, Path: path, Status: resp.StatusCode}`.

3. Replace the anonymous attributes struct in `Users` (lines 175-179) with `var a userAttrs`, and add:

```go
type userAttrs struct {
	Username      string  `json:"username"`
	LockedAt      *string `json:"locked_at"`
	DeactivatedAt *string `json:"deactivated_at"`
}

// one fetches a single resource from path.
func (c *Client) one(ctx context.Context, path string) (resource, error) {
	var doc struct {
		Data resource `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, path, nil, &doc)
	return doc.Data, err
}

// User fetches one MAS user. Subject is left empty; Users resolves links.
func (c *Client) User(ctx context.Context, id string) (User, error) {
	r, err := c.one(ctx, adminPrefix+"users/"+url.PathEscape(id))
	if err != nil {
		return User{}, err
	}
	var a userAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return User{}, err
	}
	return User{ID: r.ID, Username: a.Username, Locked: a.LockedAt != nil, Deactivated: a.DeactivatedAt != nil}, nil
}

// SessionKind names one of MAS's three session lists.
type SessionKind string

const (
	OAuth2Session  SessionKind = "oauth2"  // a Matrix client such as Element (native OIDC)
	CompatSession  SessionKind = "compat"  // a legacy Matrix login
	BrowserSession SessionKind = "browser" // the person's web session at MAS itself
)

var sessionPaths = map[SessionKind]string{
	OAuth2Session:  "oauth2-sessions",
	CompatSession:  "compat-sessions",
	BrowserSession: "user-sessions",
}

// ParseSessionKind accepts exactly the three kinds.
func ParseSessionKind(s string) (SessionKind, bool) {
	_, ok := sessionPaths[SessionKind(s)]
	return SessionKind(s), ok
}

// Session is one MAS session as the console shows it.
type Session struct {
	Kind       SessionKind
	ID, UserID string
	Device     string // Matrix device ID; empty for browser sessions
	Client     string // MAS's human name for the session, else the user agent
	IP         string
	CreatedAt  time.Time
	// LastActiveAt and FinishedAt are nil when MAS has none.
	LastActiveAt, FinishedAt *time.Time
}

type sessionAttrs struct {
	UserID       *string    `json:"user_id"`
	DeviceID     *string    `json:"device_id"`
	Scope        string     `json:"scope"`
	HumanName    *string    `json:"human_name"`
	UserAgent    *string    `json:"user_agent"`
	LastActiveIP *string    `json:"last_active_ip"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (a sessionAttrs) session(kind SessionKind, id string) Session {
	s := Session{Kind: kind, ID: id, UserID: deref(a.UserID), Client: deref(a.HumanName), IP: deref(a.LastActiveIP),
		CreatedAt: a.CreatedAt, LastActiveAt: a.LastActiveAt, FinishedAt: a.FinishedAt}
	if s.Client == "" {
		s.Client = deref(a.UserAgent)
	}
	switch kind {
	case CompatSession:
		s.Device = deref(a.DeviceID)
	case OAuth2Session:
		s.Device = scopeDevice(a.Scope)
	}
	return s
}

// scopeDevice is the Matrix device an OAuth 2.0 session's scope grants.
func scopeDevice(scope string) string {
	for _, tok := range strings.Fields(scope) {
		for _, p := range []string{"urn:matrix:client:device:", "urn:matrix:org.matrix.msc2967.client:device:"} {
			if d, ok := strings.CutPrefix(tok, p); ok {
				return d
			}
		}
	}
	return ""
}

// Sessions lists userID's active sessions: browser sessions first, because ending them first
// stops MAS from silently signing an app back in; then app and legacy sessions.
func (c *Client) Sessions(ctx context.Context, userID string) ([]Session, error) {
	var out []Session
	for _, kind := range []SessionKind{BrowserSession, OAuth2Session, CompatSession} {
		path := adminPrefix + sessionPaths[kind] + "?filter[user]=" + url.QueryEscape(userID) + "&filter[status]=active&page[first]=100"
		if err := c.list(ctx, path, func(r resource) error {
			var a sessionAttrs
			if err := json.Unmarshal(r.Attributes, &a); err != nil {
				return err
			}
			out = append(out, a.session(kind, r.ID))
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Session fetches one session.
func (c *Client) Session(ctx context.Context, kind SessionKind, id string) (Session, error) {
	p, ok := sessionPaths[kind]
	if !ok {
		return Session{}, fmt.Errorf("unknown session kind %q", kind)
	}
	r, err := c.one(ctx, adminPrefix+p+"/"+url.PathEscape(id))
	if err != nil {
		return Session{}, err
	}
	var a sessionAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return Session{}, err
	}
	return a.session(kind, r.ID), nil
}

// FinishSession ends one session. MAS answers 400 for one that has already ended; that is
// confirmed by reading it back and reported as already, not as an error.
func (c *Client) FinishSession(ctx context.Context, kind SessionKind, id string) (already bool, err error) {
	p, ok := sessionPaths[kind]
	if !ok {
		return false, fmt.Errorf("unknown session kind %q", kind)
	}
	err = c.call(ctx, http.MethodPost, adminPrefix+p+"/"+url.PathEscape(id)+"/finish", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		return false, err
	}
	if s, gerr := c.Session(ctx, kind, id); gerr != nil || s.FinishedAt == nil {
		return false, err
	}
	return true, nil
}

// Version is MAS's running version without the leading "v".
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, adminPrefix+"version", nil, &v); err != nil {
		return "", err
	}
	return strings.TrimPrefix(v.Version, "v"), nil
}
```

- [ ] **Step 4: Run.** `go test -race ./internal/matrixsync/ -v` → PASS, including the existing tests (`TestClientNeverLogsSecret` still holds: `StatusError` has no body).

- [ ] **Step 5: Commit.** `matrixsync: list, read and finish MAS sessions; MAS version`.

---

### Task 2: Audit listing filtered by kind, in insertion order

**Files:**
- Modify: `internal/store/store.go:119`, `internal/store/sqlstore.go:892-926`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `AuditStore.ListAuditRecords(ctx, offset, limit int, prefixes ...string) ([]*AuditRecord, int, error)`. Rows come newest first by `id`. With prefixes, only rows whose action starts with one of them are returned, and the count covers only those rows. Existing callers (12 in tests, none in production) compile unchanged.

- [ ] **Step 1: Failing test.** Append to `internal/store/store_test.go` (add `"reflect"` and `"time"` to the imports if missing):

```go
// Kinds filter by literal action prefix: "admin.backupXrun" would match LIKE 'admin.backup_%'
// because '_' is a wildcard there. Order is insertion order even when the clock runs back.
func TestAuditListingFiltersByKindNewestFirst(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, action := range []string{"auth.login", "admin.backup_run", "matrix.lock", "admin.backupXrun", "admin.backup_media", "scim.user.create"} {
		if err := st.Audit().LogAudit(ctx, &store.AuditRecord{Action: action, CreatedAt: now.Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(offset, limit int, prefixes ...string) ([]string, int) {
		t.Helper()
		recs, n, err := st.Audit().ListAuditRecords(ctx, offset, limit, prefixes...)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range recs {
			out = append(out, r.Action)
		}
		return out, n
	}
	if got, n := list(0, 10); n != 6 || !reflect.DeepEqual(got, []string{"scim.user.create", "admin.backup_media", "admin.backupXrun", "matrix.lock", "admin.backup_run", "auth.login"}) {
		t.Errorf("all: %v (%d)", got, n)
	}
	if got, n := list(0, 10, "backup.", "admin.backup_"); n != 2 || !reflect.DeepEqual(got, []string{"admin.backup_media", "admin.backup_run"}) {
		t.Errorf("backup kind: %v (%d)", got, n)
	}
	if got, n := list(1, 2); n != 6 || !reflect.DeepEqual(got, []string{"admin.backup_media", "admin.backupXrun"}) {
		t.Errorf("page: %v (%d)", got, n)
	}
	if got, n := list(0, 10, "nothing."); n != 0 || len(got) != 0 {
		t.Errorf("no match: %v (%d)", got, n)
	}
}
```

- [ ] **Step 2: Run, expect failure.** `go test ./internal/store/ -run TestAuditListing` → compile error (too many arguments).

- [ ] **Step 3: Implement.** In `store.go` replace the `ListAuditRecords` line with:

```go
	// ListAuditRecords pages rows newest first by insertion order. With prefixes, only rows
	// whose action starts with one of them; the count is of those rows.
	ListAuditRecords(ctx context.Context, offset, limit int, prefixes ...string) ([]*AuditRecord, int, error)
```

In `sqlstore.go` replace the function with:

```go
func (a *auditStore) ListAuditRecords(ctx context.Context, offset, limit int, prefixes ...string) ([]*AuditRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	// substr, not LIKE: '_' in "admin.backup_" is a LIKE wildcard, and the engines disagree
	// on LIKE's case rules.
	where, args := "", []any{}
	for i, p := range prefixes {
		if i == 0 {
			where = " WHERE "
		} else {
			where += " OR "
		}
		where += "substr(action, 1, ?) = ?"
		args = append(args, len(p), p)
	}
	var count int
	if err := a.store.db.QueryRowContext(ctx, a.store.rebind("SELECT COUNT(1) FROM audit_records"+where), args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	q := a.store.rebind("SELECT id, user_id, action, resource, details, ip_address, created_at FROM audit_records" +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?")
	rows, err := a.store.db.QueryContext(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var records []*AuditRecord
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.ID, &r.UserID, &r.Action, &r.Resource, &r.Details, &r.IPAddress, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		records = append(records, &r)
	}
	return records, count, rows.Err()
}
```

- [ ] **Step 4: Run.** `go test -race ./internal/store/ ./internal/api/ ./internal/matrixsync/ ./cmd/server/` → PASS on SQLite. Then `make test-postgres`, or `KY_TEST_DB` as `internal/testdb` documents, → PASS on Postgres. That run proves `substr(action, 1, $1) = $2` resolves on both engines.

- [ ] **Step 5: Commit.** `store: page audit records by kind prefix in insertion order`.

---

### Task 3: `internal/health` — probes and generated pins

**Files:**
- Create: `internal/health/health.go`, `internal/health/health_test.go`, `internal/health/pins.go` (generated), `internal/health/genpins/main.go`, `internal/health/genpins/main_test.go`, `internal/health/AGENTS.md`
- Modify: `AGENTS.md` (root Child DOX Index: one line)

**Interfaces:**
- Produces:
  - `type Component struct{ Name, Status, Version, Pinned string; Mismatch bool; Source, Error string }`, with JSON `name`, `status` (`up`|`down`), `version`, `pinned`, `mismatch`, `source` and `error`.
  - `type Probe struct{ Name string; Run func(context.Context) (string, error) }`
  - `func Check(ctx context.Context, timeout time.Duration, probes []Probe, secrets ...string) []Component`, which returns results in probe order.
  - `func NewHTTPClient() *http.Client`
  - `func Synapse(hc *http.Client, base string) func(context.Context) (string, error)`
  - `func MAS(hc *http.Client, base string, version func(context.Context) (string, error)) func(context.Context) (string, error)`
  - `func Element(hc *http.Client, base string) func(context.Context) (string, error)`
  - `func Postgres(host, password string) func(context.Context) (string, error)`
  - `type Targets struct{ Synapse, MAS, Element string }` and `var ComposeTargets`
  - `var Pins map[string]string`, keyed by Compose service: `element`, `mas`, `postgres`, `synapse`.

- [ ] **Step 1: Generator test (failing).** Create `internal/health/genpins/main_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"testing"
)

// The committed pins must be what go generate writes from the committed Compose file.
func TestPinsMatchCompose(t *testing.T) {
	compose, err := os.ReadFile("../../../docker-compose.matrix.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := render(compose)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../pins.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/health/pins.go is stale; run: go generate ./internal/health\n--- want\n%s", want)
	}
}

func TestTagVersion(t *testing.T) {
	for image, want := range map[string]string{
		"postgres:17.6-alpine@sha256:ef25":                         "17.6",
		"ghcr.io/element-hq/synapse:v1.162.0@sha256:6b84":          "1.162.0",
		"ghcr.io/element-hq/matrix-authentication-service:1.26.0": "1.26.0",
		"registry.local:5000/element-web:v1.12.30":                "1.12.30",
	} {
		if got, err := tagVersion(image); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", image, got, err, want)
		}
	}
	for _, image := range []string{"registry.local:5000/element-web", "synapse:latest", "synapse@sha256:6b84", ""} {
		if got, err := tagVersion(image); err == nil {
			t.Errorf("%s accepted as %q", image, got)
		}
	}
}

func TestRenderRequiresEveryService(t *testing.T) {
	if _, err := render([]byte("services:\n  synapse:\n    image: synapse:v1.0.0\n")); err == nil {
		t.Fatal("a Compose file without mas, element and postgres rendered")
	}
}
```

- [ ] **Step 2: Implement the generator.** Create `internal/health/genpins/main.go`:

```go
// Command genpins writes the versions docker-compose.matrix.yml pins as Go, so the health
// screen compares running versions with what Compose deploys. `go generate ./internal/health`
// runs it; TestPinsMatchCompose fails when the two drift.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var services = []string{"element", "mas", "postgres", "synapse"}

func main() {
	compose := flag.String("compose", "", "path to docker-compose.matrix.yml")
	out := flag.String("out", "", "Go file to write")
	flag.Parse()
	src, err := os.ReadFile(*compose)
	if err != nil {
		log.Fatal(err)
	}
	code, err := render(src)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, code, 0o644); err != nil {
		log.Fatal(err)
	}
}

// render returns pins.go for compose: each service's image tag without a leading "v" or a
// "-variant" suffix ("v1.162.0" → "1.162.0", "17.6-alpine" → "17.6").
func render(compose []byte) ([]byte, error) {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("// Code generated by genpins from docker-compose.matrix.yml; DO NOT EDIT.\n\npackage health\n\n")
	b.WriteString("// Pins are the versions docker-compose.matrix.yml deploys, by Compose service.\nvar Pins = map[string]string{\n")
	for _, name := range services {
		v, err := tagVersion(doc.Services[name].Image)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		fmt.Fprintf(&b, "\t%q: %q,\n", name, v)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

var release = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)

func tagVersion(image string) (string, error) {
	ref, _, _ := strings.Cut(image, "@")
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") {
		return "", fmt.Errorf("image %q has no tag", image)
	}
	v, _, _ := strings.Cut(strings.TrimPrefix(ref[i+1:], "v"), "-")
	if !release.MatchString(v) {
		return "", fmt.Errorf("image %q: tag is not a release version", image)
	}
	return v, nil
}
```

- [ ] **Step 3: Probe tests (failing).** Create `internal/health/health_test.go`:

```go
package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckIsParallelAndBounded(t *testing.T) {
	block := func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	probes := []Probe{{"a", block}, {"b", block}, {"c", block}, {"d", block}, {"e", block}}
	start := time.Now()
	got := Check(context.Background(), 200*time.Millisecond, probes)
	if d := time.Since(start); d > 700*time.Millisecond {
		t.Fatalf("took %s: probes ran in series or ignored the timeout", d)
	}
	for i, c := range got {
		if c.Name != probes[i].Name || c.Status != "down" || !strings.Contains(c.Error, "deadline exceeded") {
			t.Errorf("%+v", c)
		}
	}
}

func TestResultComparesWithPinsAndLinksSource(t *testing.T) {
	pin := Pins["synapse"]
	ok := result("synapse", "v"+pin, nil, nil)
	if ok.Status != "up" || ok.Version != pin || ok.Pinned != pin || ok.Mismatch || ok.Source != "https://github.com/element-hq/synapse/tree/v"+pin {
		t.Errorf("pinned: %+v", ok)
	}
	old := result("synapse", "1.0.0", nil, nil)
	if !old.Mismatch || old.Source != "https://github.com/element-hq/synapse/tree/v1.0.0" {
		t.Errorf("mismatch: %+v", old)
	}
	for name, want := range map[string]string{
		"mas":      "https://github.com/element-hq/matrix-authentication-service/tree/v1.26.0",
		"element":  "https://github.com/element-hq/element-web/tree/v1.26.0",
		"postgres": "https://github.com/postgres/postgres/tree/REL_1_26_0",
	} {
		if got := result(name, "1.26.0", nil, nil).Source; got != want {
			t.Errorf("%s source %q, want %q", name, got, want)
		}
	}
	app := result("kymessages", "0.1.0-dev", nil, nil)
	if app.Version != "0.1.0-dev" || app.Pinned != "" || app.Mismatch || app.Source != "" {
		t.Errorf("app: %+v", app)
	}
}

// An upstream may answer HTML or worse where a version belongs: it never becomes a version
// or a link.
func TestResultRefusesOddVersionText(t *testing.T) {
	for _, v := range []string{"<html><body>", "javascript:alert(1)", strings.Repeat("9", 80)} {
		c := result("element", v, nil, nil)
		if c.Version != "" || c.Source != "" || c.Status != "up" || c.Error != "unrecognised version string" || c.Mismatch {
			t.Errorf("%q: %+v", v, c)
		}
	}
}

func TestResultRedactsSecrets(t *testing.T) {
	c := result("postgres", "", errors.New(`password "hunter2" rejected for hunter2`), []string{"", "hunter2"})
	if c.Status != "down" || strings.Contains(c.Error, "hunter2") || !strings.Contains(c.Error, "[redacted]") {
		t.Errorf("%+v", c)
	}
	u := result("mas", "", versionUnknown{errors.New("MAS token: HTTP 401")}, nil)
	if u.Status != "up" || u.Error != "version unknown: MAS token: HTTP 401" {
		t.Errorf("version unknown: %+v", u)
	}
}

func serve(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if code, loc, redirect := strings.Cut(body, " -> "); redirect {
			w.Header().Set("Location", loc)
			w.WriteHeader(map[string]int{"302": 302, "503": 503}[code])
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSynapseProbe(t *testing.T) {
	ctx := context.Background()
	hc := NewHTTPClient()
	base := serve(t, map[string]string{"/health": "OK", "/_synapse/admin/v1/server_version": `{"server_version":"1.162.0"}`})
	if v, err := Synapse(hc, base)(ctx); err != nil || v != "1.162.0" {
		t.Errorf("healthy: %q %v", v, err)
	}
	down := serve(t, map[string]string{"/health": "503 -> /"})
	if _, err := Synapse(hc, down)(ctx); err == nil {
		t.Error("a 503 /health passed")
	}
	quiet := serve(t, map[string]string{"/health": "OK"})
	var vu versionUnknown
	if _, err := Synapse(hc, quiet)(ctx); !errors.As(err, &vu) {
		t.Errorf("no server_version: %v", err)
	}
	moved := serve(t, map[string]string{"/health": "302 -> https://elsewhere.example/"})
	if _, err := Synapse(hc, moved)(ctx); err == nil {
		t.Error("a redirect was followed or accepted")
	}
}

func TestMASAndElementProbes(t *testing.T) {
	ctx := context.Background()
	hc := NewHTTPClient()
	called := false
	version := func(context.Context) (string, error) { called = true; return "1.26.0", nil }
	up := serve(t, map[string]string{"/.well-known/openid-configuration": "{}"})
	if v, err := MAS(hc, up, version)(ctx); err != nil || v != "1.26.0" {
		t.Errorf("mas: %q %v", v, err)
	}
	called = false
	if _, err := MAS(hc, serve(t, nil), version)(ctx); err == nil || called {
		t.Errorf("mas without discovery: err=%v version called=%v", err, called)
	}
	el := serve(t, map[string]string{"/version": "1.12.30"})
	if v, err := Element(hc, el)(ctx); err != nil || v != "1.12.30" {
		t.Errorf("element: %q %v", v, err)
	}
}

func TestPostgresProbeNeverEchoesPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Postgres("postgres.invalid", "pw-hunter2")(ctx)
	if err == nil || strings.Contains(err.Error(), "pw-hunter2") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 4: Implement.** Create `internal/health/health.go`:

```go
// Package health probes each component of a KyMessages deployment once, in parallel, and
// compares running versions with the ones docker-compose.matrix.yml pins.
package health

//go:generate go run ./genpins -compose ../../docker-compose.matrix.yml -out pins.go

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Component is one probe's result. Error is probe text for the admin screen; secrets passed
// to Check are redacted from it.
type Component struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // "up" or "down"
	Version  string `json:"version"`
	Pinned   string `json:"pinned"`
	Mismatch bool   `json:"mismatch"`
	Source   string `json:"source"`
	Error    string `json:"error"`
}

// Probe checks one component and returns its running version ("" when it has none).
type Probe struct {
	Name string
	Run  func(ctx context.Context) (string, error)
}

// Targets are the Matrix services' internal origins.
type Targets struct{ Synapse, MAS, Element string }

// ComposeTargets are the origins docker-compose.matrix.yml gives the app on its default network.
var ComposeTargets = Targets{Synapse: "http://synapse:8008", MAS: "http://mas:8080", Element: "http://element:8080"}

// versionUnknown marks a component that answered but would not say its version: it is up.
type versionUnknown struct{ err error }

func (v versionUnknown) Error() string { return "version unknown: " + v.err.Error() }

// Check runs every probe in parallel, each under its own timeout, and returns results in
// probe order.
func Check(ctx context.Context, timeout time.Duration, probes []Probe, secrets ...string) []Component {
	out := make([]Component, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			v, err := p.Run(pctx)
			out[i] = result(p.Name, v, err, secrets)
		})
	}
	wg.Wait()
	return out
}

var (
	versionText = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	release     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)
)

func result(name, version string, err error, secrets []string) Component {
	c := Component{Name: name, Status: "up", Pinned: Pins[name]}
	if err != nil {
		var vu versionUnknown
		if !errors.As(err, &vu) {
			c.Status = "down"
		}
		c.Error = clip(redact(err.Error(), secrets))
	}
	switch version = strings.TrimPrefix(strings.TrimSpace(version), "v"); {
	case version == "":
	case versionText.MatchString(version):
		c.Version = version
	case c.Error == "":
		c.Error = "unrecognised version string"
	}
	c.Mismatch = c.Pinned != "" && c.Version != "" && c.Version != c.Pinned
	c.Source = source(name, c.Version)
	return c
}

// source links a running version to its exact upstream source tree.
func source(name, version string) string {
	if !release.MatchString(version) {
		return ""
	}
	switch name {
	case "synapse":
		return "https://github.com/element-hq/synapse/tree/v" + version
	case "mas":
		return "https://github.com/element-hq/matrix-authentication-service/tree/v" + version
	case "element":
		return "https://github.com/element-hq/element-web/tree/v" + version
	case "postgres":
		return "https://github.com/postgres/postgres/tree/REL_" + strings.ReplaceAll(version, ".", "_")
	}
	return ""
}

func redact(s string, secrets []string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "[redacted]")
		}
	}
	return s
}

func clip(s string) string {
	if len(s) <= 300 {
		return s
	}
	return strings.ToValidUTF8(s[:300], "") + "…"
}

// NewHTTPClient is the probes' client: no proxy from the environment, no redirects.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// fetch GETs u and returns at most 64 KiB of its body when the answer is 200.
func fetch(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// Synapse: /health must answer OK; the version comes from the unauthenticated
// /_synapse/admin/v1/server_version, which the client listener serves.
func Synapse(hc *http.Client, base string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		body, err := fetch(ctx, hc, base+"/health")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(body)) != "OK" {
			return "", errors.New("/health did not answer OK")
		}
		var v struct {
			ServerVersion string `json:"server_version"`
		}
		body, err = fetch(ctx, hc, base+"/_synapse/admin/v1/server_version")
		if err == nil {
			err = json.Unmarshal(body, &v)
		}
		if err != nil {
			return "", versionUnknown{err}
		}
		return v.ServerVersion, nil
	}
}

// MAS: public discovery for liveness (no listener serves MAS's health resource), then the
// version from the admin API.
func MAS(hc *http.Client, base string, version func(context.Context) (string, error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if _, err := fetch(ctx, hc, base+"/.well-known/openid-configuration"); err != nil {
			return "", err
		}
		v, err := version(ctx)
		if err != nil {
			return "", versionUnknown{err}
		}
		return v, nil
	}
}

// Element serves its version as the plain-text file /version.
func Element(hc *http.Client, base string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		body, err := fetch(ctx, hc, base+"/version")
		return string(body), err
	}
}

// Postgres connects to the synapse database as the read-only kybackup role.
func Postgres(host, password string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		u := url.URL{Scheme: "postgres", User: url.UserPassword("kybackup", password),
			Host: net.JoinHostPort(host, "5432"), Path: "/synapse", RawQuery: "connect_timeout=3"}
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			return "", errors.New("postgres: bad connection settings") // the error may quote the DSN
		}
		defer db.Close()
		var v string
		err = db.QueryRowContext(ctx, "SHOW server_version").Scan(&v)
		return v, err
	}
}
```

Then run `go generate ./internal/health`, which writes `internal/health/pins.go`:

```go
// Code generated by genpins from docker-compose.matrix.yml; DO NOT EDIT.

package health

// Pins are the versions docker-compose.matrix.yml deploys, by Compose service.
var Pins = map[string]string{
	"element":  "1.12.30",
	"mas":      "1.26.0",
	"postgres": "17.6",
	"synapse":  "1.162.0",
}
```

- [ ] **Step 5: Run.** `go test -race ./internal/health/... -v` → PASS. Prove the drift test bites: change `v1.162.0` to `v1.162.1` in `docker-compose.matrix.yml`, run `go test ./internal/health/genpins/` → FAIL "pins.go is stale". Revert with `git checkout docker-compose.matrix.yml`.

- [ ] **Step 6: DOX.** Create `internal/health/AGENTS.md`:

```markdown
# Health

## Purpose
Probes KyMessages, its database and (with Matrix) Synapse, MAS, Element and Postgres once per
request, and compares running versions with the versions Compose pins.

## Ownership
Owns `Check`, the per-component probes, `ComposeTargets`, the source links and `pins.go`
(generated by `genpins` from `docker-compose.matrix.yml`). `internal/api` builds the probe list.

## Local Contracts
- Probes run in parallel, each under its own timeout; `Check` returns in probe order. No history.
- `up` means the component answered its liveness check. A version that cannot be read is "up,
  version unknown". Text that is not a version string is dropped and never linked.
- Synapse: `/health` then the unauthenticated `/_synapse/admin/v1/server_version`. MAS: discovery
  (MAS's health resource is on no listener) then the admin API version. Element: `/version`.
  Postgres: `SHOW server_version` as `kybackup` on database `synapse`.
- The HTTP client ignores proxy variables and follows no redirects. Errors carry no credential:
  `Check` redacts the secrets it is given; the Postgres DSN never appears in an error.
- `pins.go` is generated (`go generate ./internal/health`); never edit it by hand.
  `genpins.TestPinsMatchCompose` fails when it drifts from the Compose file.
- Source links point at the exact upstream tag of the running version (AGPL rule).

## Verification
- `go test -race ./internal/health/...`
```

In the root `AGENTS.md` Child DOX Index, after the `internal/matrixsync` line, add:
`- [internal/health/AGENTS.md](internal/health/AGENTS.md): Per-request component probes, version pins generated from docker-compose.matrix.yml, upstream source links.`

- [ ] **Step 7: Commit.** `health: parallel component probes and Compose-generated version pins`.

---

### Task 4: API routes, generic step-up and shared MAS client

**Files:**
- Create: `internal/api/matrix_handlers.go`, `internal/api/health_handlers.go`, `internal/api/audit_handlers.go`, `internal/api/console_test.go`
- Modify:
  - `internal/api/server.go` (fields at lines 33-54, `NewServer` at 151-177, `routes` at 242-296, `admin` at 326-332)
  - `internal/api/backup_handlers.go` (rename `auditBackup` → `audit`, line 97 and its 14 callers)
  - `internal/api/export_test.go`, `internal/api/authz_test.go`, `internal/api/sso_stepup_test.go`
  - `cmd/server/main.go:127-131`, `scripts/smoke-test.sh`

**Interfaces:**
- Consumes: Task 1's `matrixsync` client API; Task 2's `ListAuditRecords(..., prefixes...)`; Task 3's `health.Check`, `health.Synapse`, `health.MAS`, `health.Element`, `health.Postgres`, `health.ComposeTargets` and `health.NewHTTPClient`.
- Produces:
  - `type MatrixAdmin interface{ Users; User; Sessions; Session; FinishSession; Version }`, with the signatures from Task 1.
  - `func (s *Server) SetMatrixAdmin(m MatrixAdmin)`
  - Test-only `api.SetHealthTargetsForTest(s, health.Targets)`
  - JSON shapes the web relies on:
    - `GET /api/admin/matrix/users?search&offset&limit` → `{users:[{id,username,mxid,status,kyidentity}], total, offset, limit, directory_url}`. `status` is one of `active`, `locked`, `deactivated` or `not_linked`. `kyidentity` is the directory status, `unknown`, or `""` when not linked.
    - `GET /api/admin/matrix/users/{id}/sessions` → `{sessions:[{kind,id,device,client,ip,created_at,last_active_at|null}]}`
    - `POST /api/admin/matrix/sessions/{kind}/{id}/finish` → `{outcome:"ended"|"already_ended", mxid}`
    - `GET /api/admin/health` → `{matrix:bool, components:[health.Component]}`
    - `GET /api/admin/audit?kind&offset&limit` → `{records:[{id,at,actor,action,target,outcome,details,ip}], total, offset, limit}`
    - Matrix off: the three Matrix routes answer `404 {"error":"Chat (Matrix) is not set up on this server","code":"matrix_disabled"}`.
    - Step-up refusal: `403 {"error":"Confirm it's you: this change needs a sign-in from the last 10 minutes","code":"reauthentication_required"}`, plus `reauth_url` for KyIdentity admins.

- [ ] **Step 1: Failing tests.** Create `internal/api/console_test.go`:

```go
package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// MAS IDs are ULIDs: 26 Crockford base32 characters.
const (
	uAlice  = "01J9ZK8V6N3W4X5Y6Z7A8B9C0A"
	uBob    = "01J9ZK8V6N3W4X5Y6Z7A8B9C0B"
	uCarol  = "01J9ZK8V6N3W4X5Y6Z7A8B9C0C"
	uDave   = "01J9ZK8V6N3W4X5Y6Z7A8B9C0D"
	sOAuth  = "01J9ZK8V6N3W4X5Y6Z7A8B9C1A"
	sCompat = "01J9ZK8V6N3W4X5Y6Z7A8B9C1B"
	sNoUser = "01J9ZK8V6N3W4X5Y6Z7A8B9C1C"
)

type fakeMAS struct {
	mu        sync.Mutex
	users     []matrixsync.User
	usersErr  error
	sessions  map[string][]matrixsync.Session // by user ID; "" holds sessions without a user
	finished  map[string]bool
	finishErr error
	finishes  []string
}

func notFound(path string) error { return &matrixsync.StatusError{Method: "GET", Path: path, Status: http.StatusNotFound} }

func (f *fakeMAS) Users(context.Context) ([]matrixsync.User, error) { return f.users, f.usersErr }

func (f *fakeMAS) User(_ context.Context, id string) (matrixsync.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return matrixsync.User{}, notFound("/api/admin/v1/users/" + id)
}

func (f *fakeMAS) Sessions(_ context.Context, userID string) ([]matrixsync.Session, error) {
	list, ok := f.sessions[userID]
	if !ok {
		return nil, notFound("/api/admin/v1/user-sessions")
	}
	return list, nil
}

func (f *fakeMAS) Session(_ context.Context, kind matrixsync.SessionKind, id string) (matrixsync.Session, error) {
	for _, list := range f.sessions {
		for _, s := range list {
			if s.Kind == kind && s.ID == id {
				return s, nil
			}
		}
	}
	return matrixsync.Session{}, notFound("/api/admin/v1/sessions/" + id)
}

func (f *fakeMAS) FinishSession(_ context.Context, kind matrixsync.SessionKind, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes = append(f.finishes, string(kind)+"/"+id)
	if f.finishErr != nil {
		return false, f.finishErr
	}
	already := f.finished[id]
	f.finished[id] = true
	return already, nil
}

func (f *fakeMAS) Version(context.Context) (string, error) { return health.Pins["mas"], nil }

// setupMatrixServer: four MAS users (active, locked, deactivated, unlinked) and a KyIdentity
// directory in which sub-a is active, sub-b disabled and sub-c unknown.
func setupMatrixServer(t *testing.T) (*api.Server, store.Store, *config.Config, *fakeMAS) {
	t.Helper()
	srv, st, cfg := setupTestServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.SSO.KyIdentityIssuer = "https://id.example.com"
	f := &fakeMAS{
		users: []matrixsync.User{
			{ID: uAlice, Username: "alice", Subject: "sub-a"},
			{ID: uBob, Username: "bob", Subject: "sub-b", Locked: true},
			{ID: uCarol, Username: "carol", Subject: "sub-c", Deactivated: true},
			{ID: uDave, Username: "dave"},
		},
		sessions: map[string][]matrixsync.Session{
			uAlice: {
				{Kind: matrixsync.OAuth2Session, ID: sOAuth, UserID: uAlice, Device: "DEVA", Client: "Element", IP: "192.0.2.7", CreatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
				{Kind: matrixsync.CompatSession, ID: sCompat, UserID: uAlice, Device: "DEVB", Client: "bot"},
			},
			uDave: {},
			"":    {{Kind: matrixsync.OAuth2Session, ID: sNoUser}},
		},
		finished: map[string]bool{},
	}
	srv.SetMatrixAdmin(f)
	for _, u := range []store.User{
		{ID: "usr_dir_a", Username: "alice-ky", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-a"},
		{ID: "usr_dir_b", Username: "bob-ky", Role: "user", Status: "disabled", SSOProvider: "kyidentity", SSOSubject: "sub-b"},
	} {
		if err := st.Users().CreateUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	return srv, st, cfg, f
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type componentsBody struct {
	Matrix     bool
	Components []health.Component
}

func TestMatrixRoutesAre404WithoutMatrix(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/admin/matrix/users"},
		{"GET", "/api/admin/matrix/users/" + uAlice + "/sessions"},
		{"POST", "/api/admin/matrix/sessions/oauth2/" + sOAuth + "/finish"},
	} {
		w := adminDo(t, srv, admin, rt.method, rt.path, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"matrix_disabled"`) {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	h := decode[componentsBody](t, adminDo(t, srv, admin, "GET", "/api/admin/health", nil))
	if h.Matrix || len(h.Components) != 2 || h.Components[0].Name != "kymessages" || h.Components[1].Name != "database" ||
		h.Components[0].Status != "up" || h.Components[1].Status != "up" {
		t.Fatalf("health without Matrix: %+v", h)
	}
}

func TestMatrixUsersStatusesSearchAndPaging(t *testing.T) {
	srv, st, _, _ := setupMatrixServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type page struct {
		Users []struct {
			ID, Username, MXID, Status, KyIdentity string
		}
		Total, Offset, Limit int
		DirectoryURL         string `json:"directory_url"`
	}
	get := func(q string) page { return decode[page](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/users"+q, nil)) }
	all := get("")
	var rows []string
	for _, u := range all.Users {
		rows = append(rows, fmt.Sprintf("%s %s %s %s", u.MXID, u.Username, u.Status, u.KyIdentity))
	}
	want := []string{
		"@alice:example.com alice active active", "@bob:example.com bob locked disabled",
		"@carol:example.com carol deactivated unknown", "@dave:example.com dave not_linked ",
	}
	if strings.Join(rows, "|") != strings.Join(want, "|") || all.Total != 4 || all.Limit != 50 || all.DirectoryURL != "https://id.example.com" {
		t.Fatalf("all: %v total=%d limit=%d url=%q", rows, all.Total, all.Limit, all.DirectoryURL)
	}
	p := get("?search=A&limit=2&offset=1") // alice, carol, dave match "a"
	if p.Total != 3 || len(p.Users) != 2 || p.Users[0].Username != "carol" || p.Users[1].Username != "dave" {
		t.Fatalf("search page: %+v", p)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users?search=zzz", nil).Body.String(); !strings.Contains(body, `"users":[]`) {
		t.Errorf("empty page is not an empty array: %s", body)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?offset=-1", "?limit=x"} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

func TestMatrixUsersReportsMASFailure(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	f.usersErr = errors.New("MAS token: HTTP 401")
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/matrix/users", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "MAS token: HTTP 401") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestMatrixSessionsListed(t *testing.T) {
	srv, st, _, _ := setupMatrixServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type sessions struct {
		Sessions []struct {
			Kind, ID, Device, Client, IP string
			LastActiveAt                 *string `json:"last_active_at"`
		}
	}
	got := decode[sessions](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uAlice+"/sessions", nil))
	if len(got.Sessions) != 2 {
		t.Fatalf("%+v", got)
	}
	s := got.Sessions[0]
	if s.Kind != "oauth2" || s.ID != sOAuth || s.Device != "DEVA" || s.Client != "Element" || s.IP != "192.0.2.7" || s.LastActiveAt != nil {
		t.Errorf("%+v", s)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uDave+"/sessions", nil).Body.String(); !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("no sessions is not an empty array: %s", body)
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+uBob+"/sessions", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown to MAS: %d", w.Code)
	}
	for _, id := range []string{"not-a-ulid", "..%2F..%2Fusers", strings.ToLower(uAlice)} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/users/"+id+"/sessions", nil); w.Code != http.StatusBadRequest {
			t.Errorf("id %q: %d", id, w.Code)
		}
	}
}

func staleAdmin(t *testing.T, st store.Store) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	createLocalUser(t, st, "usr_old", "old-admin", "admin", "OldAdminPass123!")
	user, err := st.Users().GetUserByID(ctx, "usr_old")
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.RandomHex(32)
	if err := st.Sessions().CreateSession(ctx, &store.Session{
		TokenHash: crypto.SHA256Hex([]byte(raw)), UserID: "usr_old",
		CreatedAt: time.Now().UTC().Add(-11 * time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, user.PasswordHash); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: raw}
}

func TestSessionFinishNeedsFreshAdminAndIsAudited(t *testing.T) {
	srv, st, _, f := setupMatrixServer(t)
	path := "/api/admin/matrix/sessions/oauth2/" + sOAuth + "/finish"
	w := adminDo(t, srv, staleAdmin(t, st), "POST", path, nil)
	body := w.Body.String()
	if w.Code != http.StatusForbidden || !strings.Contains(body, `"code":"reauthentication_required"`) ||
		!strings.Contains(body, "Confirm it's you") || strings.Contains(body, "backup") || strings.Contains(body, "reauth_url") {
		t.Fatalf("stale local admin: %d %s", w.Code, body)
	}
	if len(f.finishes) != 0 {
		t.Fatalf("a stale session reached MAS: %v", f.finishes)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, want := range []string{"ended", "already_ended"} {
		w := adminDo(t, srv, admin, "POST", path, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"`+want+`"`) || !strings.Contains(w.Body.String(), `"mxid":"@alice:example.com"`) {
			t.Fatalf("want %s: %d %s", want, w.Code, w.Body.String())
		}
	}
	f.finishErr = errors.New("MAS POST /api/admin/v1/oauth2-sessions/x/finish: HTTP 500")
	if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("MAS failure: %d %s", w.Code, w.Body.String())
	}
	rows := auditRows(t, st, "matrix.session_end") // newest first
	if len(rows) != 3 {
		t.Fatalf("%d session_end rows", len(rows))
	}
	for i, outcome := range []string{`outcome="error: MAS POST`, `outcome="already_ended"`, `outcome="ended"`} {
		r := rows[i]
		if r.Resource != "@alice:example.com" || r.UserID != "usr_root" || r.IPAddress == "" ||
			!strings.Contains(r.Details, `session="`+sOAuth+`"`) || !strings.Contains(r.Details, `kind="oauth2"`) || !strings.Contains(r.Details, outcome) {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	// Refused before MAS: unknown kind, malformed ID.
	for _, p := range []string{"/api/admin/matrix/sessions/device/" + sOAuth + "/finish", "/api/admin/matrix/sessions/oauth2/nope/finish"} {
		if w := adminDo(t, srv, admin, "POST", p, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// MAS's own client sessions belong to no user: refused, audited, never finished.
	if w := adminDo(t, srv, admin, "POST", "/api/admin/matrix/sessions/oauth2/"+sNoUser+"/finish", nil); w.Code != http.StatusBadRequest {
		t.Errorf("no-user session: %d", w.Code)
	}
	rows = auditRows(t, st, "matrix.session_end")
	if len(rows) != 4 || !strings.Contains(rows[0].Details, `outcome="refused: no user"`) || len(f.finishes) != 3 {
		t.Errorf("rows %d newest %+v finishes %v", len(rows), rows[0], f.finishes)
	}
}

func TestHealthWithMatrixReportsEveryComponent(t *testing.T) {
	srv, st, cfg, _ := setupMatrixServer(t)
	mux := func(routes map[string]string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b, ok := routes[r.URL.Path]; ok {
				fmt.Fprint(w, b)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	api.SetHealthTargetsForTest(srv, health.Targets{
		Synapse: mux(map[string]string{"/health": "OK", "/_synapse/admin/v1/server_version": `{"server_version":"` + health.Pins["synapse"] + `"}`}),
		MAS:     mux(map[string]string{"/.well-known/openid-configuration": "{}"}),
		Element: mux(map[string]string{"/version": "1.0.0"}),
	})
	cfg.Matrix.DBHost = "postgres.invalid"
	cfg.Matrix.BackupDBPassword = "pw-hunter2"
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/health", nil)
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "pw-hunter2") {
		t.Fatalf("headers %v body %s", w.Header(), w.Body.String())
	}
	h := decode[componentsBody](t, w)
	by := map[string]health.Component{}
	var names []string
	for _, c := range h.Components {
		by[c.Name] = c
		names = append(names, c.Name)
	}
	if !h.Matrix || strings.Join(names, ",") != "kymessages,database,synapse,mas,element,postgres" {
		t.Fatalf("components %v", names)
	}
	if c := by["synapse"]; c.Status != "up" || c.Version != health.Pins["synapse"] || c.Mismatch || c.Source == "" {
		t.Errorf("synapse %+v", c)
	}
	if c := by["mas"]; c.Status != "up" || c.Version != health.Pins["mas"] || c.Mismatch {
		t.Errorf("mas %+v", c)
	}
	if c := by["element"]; c.Status != "up" || !c.Mismatch || c.Pinned != health.Pins["element"] {
		t.Errorf("element %+v", c)
	}
	if c := by["postgres"]; c.Status != "down" || c.Error == "" {
		t.Errorf("postgres %+v", c)
	}
}

func TestAuditListsNewestFirstByKind(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin") // writes auth.login
	ctx := context.Background()
	for _, r := range []store.AuditRecord{
		{UserID: "usr_root", Action: "admin.backup_run", Resource: "cap-1", Details: `outcome="success" trigger="admin"`, IPAddress: "192.0.2.9"},
		{Action: "matrix.lock", Resource: "@bob:example.com", Details: `subject="s" reason="r" outcome="ok"`},
		{UserID: "usr_gone", Action: "scim.user.create", Resource: "erin"},
	} {
		if err := st.Audit().LogAudit(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	type rec struct{ Action, Actor, Target, Outcome, Details, IP string }
	type page struct {
		Records []rec
		Total   int
	}
	get := func(q string) page { return decode[page](t, adminDo(t, srv, admin, "GET", "/api/admin/audit"+q, nil)) }
	all := get("")
	if len(all.Records) < 4 || all.Records[0].Action != "scim.user.create" || all.Records[1].Action != "matrix.lock" || all.Records[2].Action != "admin.backup_run" {
		t.Fatalf("order: %+v", all.Records)
	}
	if r := all.Records[0]; r.Actor != "usr_gone" || r.Target != "erin" || r.Outcome != "" {
		t.Errorf("unknown actor: %+v", r)
	}
	if r := all.Records[1]; r.Actor != "system" || r.Outcome != "ok" {
		t.Errorf("sweep row: %+v", r)
	}
	if r := all.Records[2]; r.Actor != "root" || r.Outcome != "success" || r.Target != "cap-1" || r.IP != "192.0.2.9" || !strings.Contains(r.Details, `trigger="admin"`) {
		t.Errorf("backup row: %+v", r)
	}
	if b := get("?kind=backup"); b.Total != 1 || b.Records[0].Action != "admin.backup_run" {
		t.Errorf("backup kind: %+v", b)
	}
	for _, r := range get("?kind=auth").Records {
		if !strings.HasPrefix(r.Action, "auth.") && !strings.HasPrefix(r.Action, "device.") {
			t.Errorf("auth kind returned %s", r.Action)
		}
	}
	if p := get("?limit=1&offset=1"); len(p.Records) != 1 || p.Records[0] != all.Records[1] || p.Total != all.Total {
		t.Errorf("page: %+v", p)
	}
	for _, bad := range []string{"?kind=messaging", "?limit=500", "?offset=x"} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/audit"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}
```

In `authz_test.go` `TestPrivilegedEndpointsRequireAdmin`, add these cases (Matrix is off there, so an admin gets 404 or 200, which passes):

```go
		{"GET", "/api/admin/matrix/users"},
		{"GET", "/api/admin/matrix/users/" + uAlice + "/sessions"},
		{"POST", "/api/admin/matrix/sessions/oauth2/" + sOAuth + "/finish"},
		{"GET", "/api/admin/health"},
		{"GET", "/api/admin/audit"},
```

In `sso_stepup_test.go`, add `{"POST", "/api/admin/matrix/sessions/oauth2/01J9ZK8V6N3W4X5Y6Z7A8B9C1A/finish"}` to `routes`. A stale KyIdentity admin must get `reauth_url` on it too.

- [ ] **Step 2: Run, expect failure.** `go test ./internal/api/ -run 'Matrix|Session|Health|Audit|Privileged|SSOStepUp'` → compile errors (`SetMatrixAdmin`, `SetHealthTargetsForTest` undefined).

- [ ] **Step 3: Server wiring.** In `server.go`:

1. Add the imports `"github.com/Busnes-app/ky_server_base/internal/health"` and `"github.com/Busnes-app/ky_server_base/internal/matrixsync"`.
2. Add the interface and fields:

```go
// MatrixAdmin is the MAS admin surface the console uses; *matrixsync.Client implements it.
type MatrixAdmin interface {
	Users(ctx context.Context) ([]matrixsync.User, error)
	User(ctx context.Context, id string) (matrixsync.User, error)
	Sessions(ctx context.Context, userID string) ([]matrixsync.Session, error)
	Session(ctx context.Context, kind matrixsync.SessionKind, id string) (matrixsync.Session, error)
	FinishSession(ctx context.Context, kind matrixsync.SessionKind, id string) (bool, error)
	Version(ctx context.Context) (string, error)
}
```

   In `Server`: `mas MatrixAdmin // nil when Matrix is off`, `matrixTargets health.Targets` and `probeHTTP *http.Client`. In `NewServer`'s literal: `matrixTargets: health.ComposeTargets, probeHTTP: health.NewHTTPClient(),`. Then:

```go
// SetMatrixAdmin enables the Matrix console routes with the client the offboarding sweep
// also uses, so they share one token and one path guard.
func (s *Server) SetMatrixAdmin(m MatrixAdmin) { s.mas = m }
```

3. In `routes()`, after the network-check line:

```go
	// Console. Reads are any admin; ending a session needs a recent sign-in and is audited.
	s.mux.HandleFunc("GET /api/admin/matrix/users", s.requireAdmin(s.handleMatrixUsers))
	s.mux.HandleFunc("GET /api/admin/matrix/users/{id}/sessions", s.requireAdmin(s.handleMatrixUserSessions))
	s.mux.HandleFunc("POST /api/admin/matrix/sessions/{kind}/{id}/finish", s.tracked(s.requireFreshAdmin(s.handleMatrixSessionFinish)))
	s.mux.HandleFunc("GET /api/admin/health", s.requireAdmin(s.handleHealth))
	s.mux.HandleFunc("GET /api/admin/audit", s.requireAdmin(s.handleAudit))
```

4. Replace the step-up body in `admin()` (lines 327-332) with:

```go
			body := map[string]string{"error": "Confirm it's you: this change needs a sign-in from the last 10 minutes", "code": "reauthentication_required"}
			if user.SSOProvider == "kyidentity" {
				// A plain SSO login may silently reuse the IdP session; this one forces credentials.
				body["reauth_url"] = reauthURL
			}
```

5. Rename `auditBackup` to `audit` in `backup_handlers.go`: `sed -i 's/\bauditBackup\b/audit/g' internal/api/backup_handlers.go`. Then fix its doc comment to "audit records an admin event against the acting admin…" and its log prefix `[BACKUP]` → `[AUDIT]`.

6. `export_test.go`: add

```go
// SetHealthTargetsForTest points the Matrix probes at test servers. Test-only.
func SetHealthTargetsForTest(s *Server, t health.Targets) { s.matrixTargets = t }
```

   Add the `health` import there too.

7. `cmd/server/main.go`: make the sweep and the API share one client:

```go
	if cfg.Matrix.Enabled() {
		mas := matrixsync.NewClient(cfg.Matrix.AdminURL, cfg.Matrix.AdminClientID, cfg.Matrix.AdminSecret)
		srv.SetMatrixAdmin(mas)
		syncer := matrixsync.New(mas, st, cfg.Matrix.ServerName)
		srv.OnDirectoryChange(syncer.Wake)
		go syncer.Run(ctx, 5*time.Minute, matrixDone)
	} else {
```

- [ ] **Step 4: Handlers.** Create `internal/api/matrix_handlers.go`:

```go
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

func (s *Server) mxid(username string) string { return "@" + username + ":" + s.config.Matrix.ServerName }

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
		client := x.Client
		if len(client) > 200 {
			client = strings.ToValidUTF8(client[:200], "")
		}
		out = append(out, sessionView{Kind: string(x.Kind), ID: x.ID, Device: x.Device, Client: client, IP: x.IP, CreatedAt: x.CreatedAt, LastActiveAt: x.LastActiveAt})
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
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
		s.audit(actx, actor, r, "matrix.session_end", mxid, fmt.Sprintf("session=%q kind=%q outcome=%q", id, kind, outcome))
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
```

Create `internal/api/health_handlers.go`:

```go
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/health"
)

const (
	probeTimeout  = 3 * time.Second
	healthTimeout = 5 * time.Second
)

// handleHealth probes every component now, in parallel. Without Matrix: KyMessages and its
// database only.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	probes := []health.Probe{
		{Name: "kymessages", Run: func(context.Context) (string, error) { return config.AppVersion, nil }},
		{Name: "database", Run: func(ctx context.Context) (string, error) { return "", s.store.Ping(ctx) }},
	}
	m := s.config.Matrix
	if s.mas != nil {
		t := s.matrixTargets
		probes = append(probes,
			health.Probe{Name: "synapse", Run: health.Synapse(s.probeHTTP, t.Synapse)},
			health.Probe{Name: "mas", Run: health.MAS(s.probeHTTP, t.MAS, s.mas.Version)},
			health.Probe{Name: "element", Run: health.Element(s.probeHTTP, t.Element)},
			health.Probe{Name: "postgres", Run: health.Postgres(m.DBHost, m.BackupDBPassword)},
		)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"matrix":     s.mas != nil,
		"components": health.Check(ctx, probeTimeout, probes, m.BackupDBPassword, m.AdminSecret),
	})
}
```

Create `internal/api/audit_handlers.go`:

```go
package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// auditKinds maps the console's filter to action prefixes.
var auditKinds = map[string][]string{
	"auth":   {"auth.", "device."},
	"backup": {"backup.", "admin.backup_", "restore."},
	"matrix": {"matrix."},
	"scim":   {"scim."},
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

var outcomeField = regexp.MustCompile(`(?:^|\s)outcome=("(?:[^"\\]|\\.)*"|\S+)`)

// auditOutcome is the outcome= field rows carry in their details, unquoted; "" when absent.
func auditOutcome(details string) string {
	m := outcomeField.FindStringSubmatch(details)
	if m == nil {
		return ""
	}
	if v, err := strconv.Unquote(m[1]); err == nil {
		return v
	}
	return m[1]
}

// handleAudit pages the audit log newest first, optionally by kind. Read-only, any admin.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	offset, limit, ok := pageParams(r)
	kind := r.URL.Query().Get("kind")
	prefixes, known := auditKinds[kind]
	if !ok || (kind != "" && !known) {
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
		out = append(out, auditView{ID: rec.ID, At: rec.CreatedAt, Actor: s.actorName(r.Context(), rec.UserID, names),
			Action: rec.Action, Target: rec.Resource, Outcome: auditOutcome(rec.Details), Details: rec.Details, IP: rec.IPAddress})
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
```

`scripts/smoke-test.sh`: after `check "anonymous cannot unpair" …` add:

```bash
check "anonymous cannot read the audit log" "$(status "$BASE/api/admin/audit")" "401"
check "anonymous cannot read health" "$(status "$BASE/api/admin/health")" "401"
```

After `check "bootstrap session cannot read backup state" …` add:

```bash
check "bootstrap session cannot read the audit log" "$(status -b "$WORK/cookies" "$BASE/api/admin/audit")" "403"
```

- [ ] **Step 5: Run.** `go test -race ./internal/api/ ./cmd/server/ -v` → PASS. Then `go vet ./...`, and `make ci` → PASS (the smoke test needs the built binary).

- [ ] **Step 6: Commit.** `api: console routes for Matrix users and sessions, health and audit; generic step-up`.

---

### Task 5: Web — `adminFetch`, "Confirm it's you", Backup migration, Users page

**Files:**
- Create: `web/src/dto.ts`, `web/src/test-setup.ts`, `web/src/components/ConfirmItsYou.tsx`, `web/src/components/ConfirmItsYou.test.tsx`, `web/src/pages/Users.tsx`, `web/src/pages/Users.test.tsx`
- Modify:
  - `web/src/api.ts`, `web/vite.config.ts` (`test.setupFiles`)
  - `web/src/pages/Backup.tsx` (lines 18, 40, 146-171, 195-199, 268, 402-406, and every `changeError(`)
  - `web/src/pages/Backup.test.tsx` (lines 94-112)
  - `web/src/components/AppHeader.tsx:14-19`, `web/src/components/AppHeader.test.tsx`, `web/src/App.tsx`
  - `web/src/styles/theme.css`, `web/dist/**`

**Interfaces:**
- Consumes: Task 4's JSON shapes.
- Produces:
  - `api.ts`:
    - `adminFetch(input: string, init?: RequestInit): Promise<Response>`
    - `onReauthRequired(prompt: (r: Reauth) => Promise<boolean>): () => void`
    - `errorMessage(res, fallback): Promise<string>`
    - `isMatrixDisabled(res): Promise<boolean>`
    - `interface Reauth { message: string; url: string | null }`
  - `dto.ts`: `obj`, `arr`, `str`, `bool`, `count`, `iso`, `oneOf`, `InvalidResponse`
  - `Users.tsx`: `Users`, `parseUsersPage`, `parseSessions`, and the types `MatrixUser`, `UsersPage`, `MatrixSession`
  - `Backup.tsx`: `export function backupAttempt`, used by Task 6.
  - `ConfirmItsYou` component, mounted once in the admin shell.

- [ ] **Step 1: Test setup and failing prompt tests.** `web/src/test-setup.ts`:

```ts
// jsdom has no modal dialogs: open and close by attribute so role queries see them.
HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) { this.setAttribute('open', ''); };
HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) { this.removeAttribute('open'); };
```

In `vite.config.ts`'s `test` block add `setupFiles: ['src/test-setup.ts'],`.

`web/src/components/ConfirmItsYou.test.tsx`:

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { ConfirmItsYou } from './ConfirmItsYou';
import { adminFetch } from '../api';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required', reauth_url: '/api/sso/kyidentity/login?fresh=1' };
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

/** fetch answers each path from its queue, repeating the last answer. */
function answers(byPath: Record<string, Response[]>) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    const q = byPath[url];
    return (q.length > 1 ? q.shift() : q[0])!.clone();
  }));
  return calls;
}

it('without a mounted prompt, returns the refusal', async () => {
  answers({ '/x': [json(STEP_UP, 403)] });
  expect((await adminFetch('/x', { method: 'POST' })).status).toBe(403);
});

it('passes through a 403 that is not a step-up', async () => {
  answers({ '/x': [json({ error: 'Administrator role required' }, 403)] });
  render(<ConfirmItsYou />);
  expect((await adminFetch('/x', { method: 'POST' })).status).toBe(403);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('retries the request once the admin has signed in again', async () => {
  const calls = answers({ '/x': [json(STEP_UP, 403), json({ ok: true })] });
  render(<ConfirmItsYou />);
  const pending = adminFetch('/x', { method: 'POST' });
  const dialog = await screen.findByRole('dialog', { name: "Confirm it's you" });
  const link = within(dialog).getByRole('link', { name: 'Sign in to KyIdentity again' });
  expect(link.getAttribute('href')).toBe('/api/sso/kyidentity/login?fresh=1');
  expect(link.getAttribute('target')).toBe('_blank');
  expect(link.getAttribute('rel')).toBe('noopener noreferrer');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Retry' }));
  expect((await pending).status).toBe(200);
  expect(calls).toEqual(['/x', '/x']);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('returns the refusal when the admin cancels', async () => {
  answers({ '/x': [json(STEP_UP, 403)] });
  render(<ConfirmItsYou />);
  const pending = adminFetch('/x', { method: 'POST' });
  fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));
  expect((await pending).status).toBe(403);
});

it('offers no link for a reauth_url off the suite sign-in route', async () => {
  answers({ '/x': [json({ ...STEP_UP, reauth_url: 'https://evil.example/' }, 403)] });
  render(<ConfirmItsYou />);
  void adminFetch('/x', { method: 'POST' });
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).queryByRole('link')).toBeNull();
  expect(dialog.textContent).toContain('sign out and sign in again');
});

it('answers concurrent refusals with one prompt', async () => {
  answers({ '/a': [json(STEP_UP, 403), json({ ok: 1 })], '/b': [json(STEP_UP, 403), json({ ok: 2 })] });
  render(<ConfirmItsYou />);
  const a = adminFetch('/a', { method: 'POST' });
  const b = adminFetch('/b', { method: 'POST' });
  await screen.findByRole('dialog');
  // Both refusals are mocked and parse within a few ticks; 50 ms lets the second join the open prompt.
  await new Promise((r) => setTimeout(r, 50));
  expect(screen.getAllByRole('dialog')).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  expect((await a).status).toBe(200);
  expect((await b).status).toBe(200);
});
```

- [ ] **Step 2: Run, expect failure.** `cd web && npx vitest run src/components/ConfirmItsYou.test.tsx` → FAIL (module not found).

- [ ] **Step 3: Implement the helper and the prompt.** Append to `web/src/api.ts`:

```ts
/** A step-up refusal. `url` is a same-origin suite sign-in that forces credentials; local
 * accounts get none and sign in again themselves. */
export interface Reauth { message: string; url: string | null }

let confirmItsYou: ((r: Reauth) => Promise<boolean>) | null = null;

/** Registers the "Confirm it's you" prompt; returns its unregister function. */
export function onReauthRequired(prompt: (r: Reauth) => Promise<boolean>): () => void {
  confirmItsYou = prompt;
  return () => { if (confirmItsYou === prompt) confirmItsYou = null; };
}

async function reauthNeeded(res: Response): Promise<Reauth | null> {
  if (res.status !== 403) return null;
  const body: unknown = await res.clone().json().catch(() => null);
  if (typeof body !== 'object' || body === null) return null;
  const { code, error, reauth_url: url } = body as { code?: unknown; error?: unknown; reauth_url?: unknown };
  if (code !== 'reauthentication_required') return null;
  return {
    message: typeof error === 'string' ? error : "Confirm it's you to continue.",
    url: typeof url === 'string' && url.startsWith('/api/sso/') ? url : null,
  };
}

/** secureFetch for admin calls: a step-up refusal opens "Confirm it's you" and, once the
 * admin has signed in again, retries the same request. Cancelling returns the refusal. */
export async function adminFetch(input: string, init: RequestInit = {}): Promise<Response> {
  for (;;) {
    const res = await secureFetch(input, { credentials: 'same-origin', ...init });
    const reauth = await reauthNeeded(res);
    if (!reauth || !confirmItsYou || !(await confirmItsYou(reauth))) return res;
  }
}

/** The server's JSON `error`, or `fallback (HTTP n)` when the body has none. */
export async function errorMessage(res: Response, fallback: string): Promise<string> {
  const body: unknown = await res.clone().json().catch(() => null);
  const error = typeof body === 'object' && body !== null ? (body as { error?: unknown }).error : undefined;
  return typeof error === 'string' && error ? error : `${fallback} (HTTP ${res.status})`;
}

/** True for the 404 the Matrix routes answer when chat is not set up. */
export async function isMatrixDisabled(res: Response): Promise<boolean> {
  if (res.status !== 404) return false;
  const body: unknown = await res.clone().json().catch(() => null);
  return typeof body === 'object' && body !== null && (body as { code?: unknown }).code === 'matrix_disabled';
}
```

`web/src/components/ConfirmItsYou.tsx`:

```tsx
import { useEffect, useRef, useState } from 'react';
import { ShieldCheck } from 'lucide-react';
import { onReauthRequired, type Reauth } from '../api';

type Pending = Reauth & { resolvers: ((retry: boolean) => void)[] };

/** The console's one step-up prompt, mounted once and opened by adminFetch. Refusals that
 * arrive while it is open wait on the same answer. */
export function ConfirmItsYou() {
  const [pending, setPending] = useState<Pending | null>(null);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => onReauthRequired((r) => new Promise<boolean>((resolve) => {
    setPending((p) => ({ ...r, resolvers: [...(p?.resolvers ?? []), resolve] }));
  })), []);
  useEffect(() => {
    if (pending && !dialog.current?.open) dialog.current?.showModal();
  }, [pending]);
  const answer = (retry: boolean) => {
    pending?.resolvers.forEach((resolve) => resolve(retry));
    dialog.current?.close();
    setPending(null);
  };
  if (!pending) return null;
  return (
    <dialog ref={dialog} className="modal-window" aria-labelledby="confirm-title"
      onCancel={(e) => { e.preventDefault(); answer(false); }}>
      <h3 id="confirm-title" style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '18px', marginBottom: '12px' }}>
        <ShieldCheck size={20} style={{ color: 'var(--accent)' }} />
        {"Confirm it's you"}
      </h3>
      <p style={{ color: 'var(--ink)', fontSize: '14px', marginBottom: '12px' }}>{pending.message}</p>
      {pending.url ? (
        <p style={{ fontSize: '14px', marginBottom: '16px' }}>
          <a href={pending.url} target="_blank" rel="noopener noreferrer">Sign in to KyIdentity again</a>
          {' '}in the new tab, then come back here and retry.
        </p>
      ) : (
        <p style={{ fontSize: '14px', marginBottom: '16px' }}>
          In another tab, sign out and sign in again, then come back here and retry.
        </p>
      )}
      <div className="dr-actions" style={{ justifyContent: 'flex-end' }}>
        <button type="button" className="btn-secondary" onClick={() => answer(false)}>Cancel</button>
        <button type="button" onClick={() => answer(true)}>Retry</button>
      </div>
    </dialog>
  );
}
```

`web/src/dto.ts`:

```ts
/** Boundary checks for admin DTOs: each returns the narrowed value or throws. */
export class InvalidResponse extends Error {
  constructor() { super('Invalid response from the server'); }
}
export function obj(v: unknown): Record<string, unknown> {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) throw new InvalidResponse();
  return v as Record<string, unknown>;
}
export function arr(v: unknown): unknown[] {
  if (!Array.isArray(v)) throw new InvalidResponse();
  return v;
}
export function str(v: unknown, max = 1024): string {
  if (typeof v !== 'string' || v.length > max) throw new InvalidResponse();
  return v;
}
export function bool(v: unknown): boolean {
  if (typeof v !== 'boolean') throw new InvalidResponse();
  return v;
}
export function count(v: unknown): number {
  if (typeof v !== 'number' || !Number.isSafeInteger(v) || v < 0) throw new InvalidResponse();
  return v;
}
export function iso(v: unknown): string {
  const s = str(v, 64);
  if (!Number.isFinite(Date.parse(s))) throw new InvalidResponse();
  return s;
}
export function oneOf<T extends string>(v: unknown, allowed: readonly T[]): T {
  if (typeof v !== 'string' || !(allowed as readonly string[]).includes(v)) throw new InvalidResponse();
  return v as T;
}
```

- [ ] **Step 4: Migrate Backup to the helper.** In `Backup.tsx`:
  1. Change the import to `import { adminFetch, errorMessage } from '../api';` and `function backupAttempt` to `export function backupAttempt`.
  2. Delete the `ReauthRequired` class.
  3. Replace `apiError` and `call` with:

```ts
async function apiError(res: Response, fallback: string): Promise<Error> {
  return new Error(await errorMessage(res, fallback));
}

async function call<T>(path: string, init: RequestInit, fallback: string): Promise<T> {
  const res = await adminFetch(path, init);
  if (!res.ok) throw await apiError(res, fallback);
  return (await res.json()) as T;
}
```

  4. Delete the `reauthUrl` state, `changeError`, and the `{reauthUrl && (…)}` banner. Replace every `changeError(` with `errorText(`.
  5. In `downloadCapsule`, use `adminFetch('/api/backup/export-capsule', { method: 'POST' })`.

In `Backup.test.tsx`, add `import { ConfirmItsYou } from '../components/ConfirmItsYou';`, add `waitFor, within` to the testing-library import, and replace the step-up test (lines 94-112) with:

```tsx
  it('asks the admin to confirm it is them, then retries the change', async () => {
    let saves = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith('/api/backup/status')) return new Response(JSON.stringify(PAIRED), { status: 200 });
      if (++saves === 1) {
        return new Response(JSON.stringify({ error: "Confirm it's you: this change needs a sign-in from the last 10 minutes",
          code: 'reauthentication_required', reauth_url: '/api/sso/kyidentity/login?fresh=1' }), { status: 403 });
      }
      return new Response(JSON.stringify({ interval_sec: 0 }), { status: 200 });
    }));
    render(<><ConfirmItsYou /><Backup /></>);
    await screen.findByText('https://recovery.example');
    fireEvent.change(screen.getByLabelText('Back up automatically'), { target: { value: '0' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    const dialog = await screen.findByRole('dialog', { name: "Confirm it's you" });
    expect(within(dialog).getByRole('link', { name: 'Sign in to KyIdentity again' }).getAttribute('href')).toBe('/api/sso/kyidentity/login?fresh=1');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(saves).toBe(2));
    expect(await screen.findByText('Automatic backups are off.')).toBeTruthy();
  });
```

- [ ] **Step 5: Users tests (failing).** `web/src/pages/Users.test.tsx`:

```tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Users } from './Users';
import { ConfirmItsYou } from '../components/ConfirmItsYou';

const A = '01J9ZK8V6N3W4X5Y6Z7A8B9C0A';
const PAGE = {
  users: [
    { id: A, username: 'alice', mxid: '@alice:example.com', status: 'active', kyidentity: 'active' },
    { id: '01J9ZK8V6N3W4X5Y6Z7A8B9C0D', username: 'dave', mxid: '@dave:example.com', status: 'not_linked', kyidentity: '' },
  ],
  total: 2, offset: 0, limit: 50, directory_url: 'https://id.example.com',
};
const SESSIONS = { sessions: [
  { kind: 'browser', id: 'S1', device: '', client: 'Firefox', ip: '192.0.2.7', created_at: '2026-10-01T10:00:00Z', last_active_at: null },
  { kind: 'oauth2', id: 'S2', device: 'DEVA', client: 'Element', ip: '192.0.2.7', created_at: '2026-10-01T10:00:00Z', last_active_at: '2026-10-02T09:00:00Z' },
] };
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required', reauth_url: '/api/sso/kyidentity/login?fresh=1' };
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${url}`);
    return res;
  }));
  return calls;
}
const users = (url: string) => (url.startsWith('/api/admin/matrix/users?') ? json(PAGE) : undefined);
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

async function openSessions() {
  fireEvent.click(await screen.findByRole('button', { name: 'Sessions for alice' }));
  const panel = await screen.findByRole('region', { name: 'Sessions for @alice:example.com' });
  await within(panel).findByText('DEVA');
  return panel;
}

describe('Users', () => {
  it('lists Matrix users with status, KyIdentity link and the access notice', async () => {
    const calls = serve(users);
    render(<Users />);
    expect(await screen.findByText('@alice:example.com')).toBeTruthy();
    expect(screen.getAllByText('Not linked')).toHaveLength(2);
    expect(screen.getByRole('note').textContent).toContain('Access is controlled in KyIdentity');
    expect(screen.getByRole('link', { name: /Open KyIdentity/ }).getAttribute('href')).toBe('https://id.example.com');
    expect(screen.queryByRole('button', { name: /lock/i })).toBeNull();
    expect(calls[0]).toBe('GET /api/admin/matrix/users?search=&offset=0&limit=50');
  });

  it('searches and pages', async () => {
    const calls = serve((url) => (url.startsWith('/api/admin/matrix/users?') ? json({ ...PAGE, total: 120 }) : undefined));
    render(<Users />);
    await screen.findByText('@alice:example.com');
    fireEvent.change(screen.getByLabelText('Search by username'), { target: { value: ' ali ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/users?search=ali&offset=0&limit=50'));
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/users?search=ali&offset=50&limit=50'));
  });

  it('says when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    render(<Users />);
    expect(await screen.findByText('Chat (Matrix) is not set up on this server.')).toBeTruthy();
  });

  it('shows why MAS is unreachable', async () => {
    serve(() => json({ error: 'Matrix admin API unavailable: MAS token: HTTP 401' }, 502));
    render(<Users />);
    expect((await screen.findByRole('alert')).textContent).toContain('MAS token: HTTP 401');
  });

  it('refuses a malformed page', async () => {
    serve(() => json({ ...PAGE, users: [{ ...PAGE.users[0], status: 'superuser' }] }));
    render(<Users />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
    expect(screen.queryByText('@alice:example.com')).toBeNull();
  });

  it('ends one session', async () => {
    let finished = false;
    const calls = serve((url, init) => {
      if (url === `/api/admin/matrix/users/${A}/sessions`) return json(finished ? { sessions: SESSIONS.sessions.slice(0, 1) } : SESSIONS);
      if (url === '/api/admin/matrix/sessions/oauth2/S2/finish' && init?.method === 'POST') {
        finished = true;
        return json({ outcome: 'ended', mxid: '@alice:example.com' });
      }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    expect(within(panel).getAllByText('192.0.2.7', { selector: 'td' })).toHaveLength(2);
    fireEvent.click(within(panel).getByRole('button', { name: 'End Matrix app session DEVA' }));
    expect(await within(panel).findByText('Ended 1 session.')).toBeTruthy();
    await waitFor(() => expect(within(panel).queryByText('DEVA')).toBeNull());
    expect(calls).toContain('POST /api/admin/matrix/sessions/oauth2/S2/finish');
  });

  it('ends all sessions, browser first, after the admin confirms it is them', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') {
        posts.push(url);
        return posts.length === 1 ? json(STEP_UP, 403) : json({ outcome: 'ended', mxid: '@alice:example.com' });
      }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End all sessions' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: "Confirm it's you" })).getByRole('button', { name: 'Retry' }));
    expect(await within(panel).findByText('Ended 2 sessions.')).toBeTruthy();
    expect(posts).toEqual([
      '/api/admin/matrix/sessions/browser/S1/finish',
      '/api/admin/matrix/sessions/browser/S1/finish',
      '/api/admin/matrix/sessions/oauth2/S2/finish',
    ]);
  });

  it('stops when the admin cancels the confirmation', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') { posts.push(url); return json(STEP_UP, 403); }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End all sessions' }));
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain("Confirm it's you");
    expect(posts).toHaveLength(1);
  });
});
```

Run `npx vitest run src/pages/Users.test.tsx` → FAIL (module not found).

- [ ] **Step 6: Implement Users.** `web/src/pages/Users.tsx`:

```tsx
import React, { useCallback, useEffect, useState } from 'react';
import { ExternalLink, Loader2, LogOut, Search, X } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, count, iso, obj, oneOf, str } from '../dto';

const STATUSES = ['active', 'locked', 'deactivated', 'not_linked'] as const;
const KINDS = ['oauth2', 'compat', 'browser'] as const;
export interface MatrixUser { id: string; username: string; mxid: string; status: (typeof STATUSES)[number]; kyidentity: string }
export interface UsersPage { users: MatrixUser[]; total: number; offset: number; limit: number; directory_url: string }
export interface MatrixSession {
  kind: (typeof KINDS)[number]; id: string; device: string; client: string; ip: string; created_at: string; last_active_at: string | null;
}

export function parseUsersPage(v: unknown): UsersPage {
  const p = obj(v);
  return {
    users: arr(p.users).map((x) => {
      const u = obj(x);
      return { id: str(u.id, 64), username: str(u.username, 255), mxid: str(u.mxid, 512), status: oneOf(u.status, STATUSES), kyidentity: str(u.kyidentity, 64) };
    }),
    total: count(p.total), offset: count(p.offset), limit: count(p.limit), directory_url: str(p.directory_url),
  };
}

export function parseSessions(v: unknown): MatrixSession[] {
  return arr(obj(v).sessions).map((x) => {
    const s = obj(x);
    return {
      kind: oneOf(s.kind, KINDS), id: str(s.id, 64), device: str(s.device, 255), client: str(s.client, 512), ip: str(s.ip, 64),
      created_at: iso(s.created_at), last_active_at: s.last_active_at === null ? null : iso(s.last_active_at),
    };
  });
}

const STATUS_LABEL: Record<MatrixUser['status'], string> = { active: 'Active', locked: 'Locked', deactivated: 'Deactivated', not_linked: 'Not linked' };
const KIND_LABEL: Record<MatrixSession['kind'], string> = { oauth2: 'Matrix app', compat: 'Legacy login', browser: 'Account web' };
const PAGE = 50;
const errorText = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback);
const when = (at: string | null) => (at ? new Date(at).toLocaleString() : '—');

const UserSessions: React.FC<{ user: MatrixUser; onClose: () => void }> = ({ user, onClose }) => {
  const [sessions, setSessions] = useState<MatrixSession[] | null>(null);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/admin/matrix/users/${encodeURIComponent(user.id)}/sessions`, { cache: 'no-store' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Could not list sessions'));
      setSessions(parseSessions(await res.json()));
    } catch (err) {
      setError(errorText(err, 'Could not list sessions'));
    }
  }, [user.id]);
  useEffect(() => { void load(); }, [load]);

  // In the server's order: browser sessions first, so MAS cannot sign an app straight back in.
  const end = async (targets: MatrixSession[]) => {
    setBusy(true);
    setMessage('');
    setError('');
    const failures: string[] = [];
    let ended = 0;
    try {
      for (const s of targets) {
        const res = await adminFetch(`/api/admin/matrix/sessions/${s.kind}/${encodeURIComponent(s.id)}/finish`, { method: 'POST' });
        if (res.ok) { ended++; continue; }
        failures.push(await errorMessage(res, 'Could not end the session'));
        if (res.status === 403) break; // the admin did not confirm it's them
      }
    } catch (err) {
      failures.push(errorText(err, 'Could not end the session'));
    }
    if (ended) setMessage(`Ended ${ended} session${ended === 1 ? '' : 's'}.`);
    if (failures.length) setError(failures.join(' '));
    setBusy(false);
    await load();
  };

  return (
    <section className="panel dr-section" aria-labelledby="sessions-title">
      <div className="panel-header">
        <h3 id="sessions-title">Sessions for {user.mxid}</h3>
        <div className="dr-actions">
          <button type="button" className="btn-danger" disabled={busy || !sessions?.length} onClick={() => sessions && void end(sessions)}>
            {busy ? <Loader2 size={14} className="animate-spin" /> : <LogOut size={14} />}
            <span>End all sessions</span>
          </button>
          <button type="button" className="btn-secondary" onClick={onClose} aria-label="Close sessions"><X size={14} /></button>
        </div>
      </div>
      <p className="dr-hint">Ending a session signs that device out. The person can sign in again unless KyIdentity refuses them.</p>
      {message && <div className="dr-alert dr-alert-success" role="status"><span>{message}</span></div>}
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {sessions === null && !error && <p role="status">Loading sessions…</p>}
      {sessions?.length === 0 && <p className="dr-hint">No active sessions.</p>}
      {sessions && sessions.length > 0 && (
        <div className="console-table-wrap">
          <table className="console-table">
            <thead>
              <tr><th scope="col">Kind</th><th scope="col">Client</th><th scope="col">Device</th><th scope="col">Last active</th><th scope="col">IP</th><th scope="col">Action</th></tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={`${s.kind}/${s.id}`}>
                  <td>{KIND_LABEL[s.kind]}</td>
                  <td>{s.client || '—'}</td>
                  <td className="dr-mono">{s.device || '—'}</td>
                  <td>{when(s.last_active_at)}</td>
                  <td className="dr-mono">{s.ip || '—'}</td>
                  <td>
                    <button type="button" className="btn-secondary" disabled={busy} onClick={() => void end([s])}
                      aria-label={`End ${KIND_LABEL[s.kind]} session ${s.device || s.id}`}>End</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
};

export const Users: React.FC = () => {
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<UsersPage | null>(null);
  const [error, setError] = useState('');
  const [disabled, setDisabled] = useState(false);
  const [selected, setSelected] = useState<MatrixUser | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ search, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/matrix/users?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return setDisabled(true);
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not list Matrix users'));
        setPage(parseUsersPage(await res.json()));
        setError('');
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) setError(errorText(err, 'Could not list Matrix users')); });
    return () => controller.abort();
  }, [search, offset]);

  if (disabled) {
    return (
      <div className="dr-page">
        <div className="dr-header"><h1>Users</h1></div>
        <p role="status">Chat (Matrix) is not set up on this server.</p>
      </div>
    );
  }
  const last = page ? Math.min(page.offset + page.users.length, page.total) : 0;
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Users</h1></div>
      <div className="dr-alert dr-alert-warn" role="note">
        <span>
          Access is controlled in KyIdentity, which the sync enforces: lock, unlock or remove people there.{' '}
          {page?.directory_url.startsWith('https://') && (
            <a href={page.directory_url} target="_blank" rel="noopener noreferrer">Open KyIdentity <ExternalLink size={12} /></a>
          )}
        </span>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      <form role="search" className="dr-row" onSubmit={(e) => { e.preventDefault(); setOffset(0); setSearch(query.trim()); }}>
        <label className="dr-field">
          Search by username
          <input type="search" value={query} maxLength={255} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <button type="submit" className="btn-secondary"><Search size={14} /><span>Search</span></button>
      </form>
      {page && (
        <section className="panel dr-section" aria-label="Matrix users">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead><tr><th scope="col">User</th><th scope="col">Status</th><th scope="col">KyIdentity</th><th scope="col">Sessions</th></tr></thead>
              <tbody>
                {page.users.map((u) => (
                  <tr key={u.id}>
                    <td className="dr-mono">{u.mxid}</td>
                    <td>{STATUS_LABEL[u.status]}</td>
                    <td>{u.kyidentity || 'Not linked'}</td>
                    <td>
                      <button type="button" className="btn-secondary" onClick={() => setSelected(u)} aria-label={`Sessions for ${u.username}`}>Sessions</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.users.length === 0 && <p className="dr-hint">No Matrix users{search ? ` match “${search}”` : ''}.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${last} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</button>
              <button type="button" className="btn-secondary" disabled={last >= page.total} onClick={() => setOffset(offset + PAGE)}>Next</button>
            </div>
          </div>
        </section>
      )}
      {selected && <UserSessions key={selected.id} user={selected} onClose={() => setSelected(null)} />}
    </div>
  );
};
```

Append to `web/src/styles/theme.css`, after the Backup & recovery block:

```css
/* ---------- Console tables (Users, Audit) ---------- */
.console-table-wrap { overflow-x: auto; margin-bottom: 12px; }
.console-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.console-table th { text-align: left; font-size: 12px; font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; color: var(--ink); padding: 8px 12px; border-bottom: 1px solid var(--line); }
.console-table td { padding: 10px 12px; border-bottom: 1px solid var(--line); color: var(--ink-strong); vertical-align: top; overflow-wrap: anywhere; }
```

`AppHeader.tsx`: import `MessageSquare` and insert `{ id: 'users', label: 'Users', icon: MessageSquare },` after `dashboard`. In `AppHeader.test.tsx` expect `['dashboard', 'users', 'scim', 'backup', 'settings']`.

`App.tsx`: add `import { Users } from './pages/Users';` and `import { ConfirmItsYou } from './components/ConfirmItsYou';`. Inside `app-shell` before `<main>` add `{isAdmin && <ConfirmItsYou />}`. In `<main>` add `{isAdmin && activeTab === 'users' && <Users />}`.

- [ ] **Step 7: Run.** `cd web && npm test` → PASS. Then `npm run build` → PASS (strict TS, no unused imports). Commit the rebuilt `web/dist`. `git -C .. status --short web/dist` shows the changes.

- [ ] **Step 8: Commit.** `web: Confirm it's you prompt, adminFetch, Users page with session ending`.

---

### Task 6: Web — Health, Audit, real Overview; network check moves

**Files:**
- Create: `web/src/pages/Health.tsx`, `web/src/pages/Health.test.tsx`, `web/src/pages/Audit.tsx`, `web/src/pages/Audit.test.tsx`, `web/src/pages/Dashboard.test.tsx`
- Modify:
  - `web/src/pages/Dashboard.tsx` (rewrite)
  - `web/src/pages/Settings.tsx` (drop `NetworkCheck`, lines 4 and 62)
  - `web/src/components/AppHeader.tsx`, `web/src/components/AppHeader.test.tsx`, `web/src/App.tsx`
  - `web/dist/**`

**Interfaces:**
- Consumes:
  - from Task 5: `errorMessage`, `isMatrixDisabled`, `dto.ts`, `parseUsersPage` and `backupAttempt`
  - from Task 4: the health and audit JSON
- Produces: `Health` and `parseHealth`; `Audit` and `parseAudit`; a `Dashboard` with three cards (Chat health, Matrix users, Backups) whose buttons call `onNavigate('health'|'users'|'backup')`; and the tab ids `health` and `audit`.

- [ ] **Step 1: Failing tests.** `web/src/pages/Health.test.tsx`:

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/react';
import { Health } from './Health';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const NET = { peer_ip: '10.0.0.1', client_ip: '203.0.113.9', forwarded_trusted: true, forwarded_proto: 'https', app_url_https: true, host_matches: true, trusted_proxies_narrow: true };
const c = (name: string, extra: Record<string, unknown> = {}) => ({ name, status: 'up', version: '', pinned: '', mismatch: false, source: '', error: '', ...extra });
const REPORT = { matrix: true, components: [
  c('kymessages', { version: '0.1.0-dev' }),
  c('database'),
  c('synapse', { version: '1.162.0', pinned: '1.162.0', source: 'https://github.com/element-hq/synapse/tree/v1.162.0' }),
  c('element', { version: '1.12.29', pinned: '1.12.30', mismatch: true, source: 'javascript:alert(1)' }),
  c('postgres', { status: 'down', pinned: '17.6', error: 'dial tcp: lookup postgres: no such host' }),
] };
function serve(health: Response) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => (String(input) === '/api/admin/health' ? health.clone() : json(NET))));
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('shows each component, its pin, its source and the network check', async () => {
  serve(json(REPORT));
  render(<Health />);
  const list = await screen.findByRole('region', { name: 'Components' });
  expect(within(list).getAllByText('Up')).toHaveLength(4);
  expect(within(list).getByText('Down')).toBeTruthy();
  expect(within(list).getByRole('link', { name: /Source for 1\.162\.0/ }).getAttribute('href')).toBe('https://github.com/element-hq/synapse/tree/v1.162.0');
  expect(within(list).getByText(/Compose pins 1\.12\.30/)).toBeTruthy();
  expect(within(list).queryByRole('link', { name: /Source for 1\.12\.29/ })).toBeNull();
  expect(within(list).getByText('dial tcp: lookup postgres: no such host')).toBeTruthy();
  expect(await screen.findByRole('heading', { name: 'Network path' })).toBeTruthy();
});

it('says when only KyMessages is checked', async () => {
  serve(json({ matrix: false, components: [c('kymessages', { version: '0.1.0-dev' }), c('database')] }));
  render(<Health />);
  expect(await screen.findByText(/only KyMessages and its database are checked/)).toBeTruthy();
});

it('refuses a malformed report', async () => {
  serve(json({ matrix: true, components: [c('synapse', { status: 'sideways' })] }));
  render(<Health />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
});
```

`web/src/pages/Audit.test.tsx`:

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Audit } from './Audit';

const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });
const rec = (id: number, action: string, extra: Record<string, unknown> = {}) =>
  ({ id, at: '2026-10-02T10:00:00Z', actor: 'root', action, target: '', outcome: '', details: '', ip: '192.0.2.9', ...extra });
const PAGE = { records: [
  rec(3, 'matrix.session_end', { target: '@alice:example.com', outcome: 'ended', details: 'session="S2" kind="oauth2" outcome="ended"' }),
  rec(2, 'admin.backup_run', { outcome: 'failure' }),
  rec(1, 'auth.login'),
], total: 120, offset: 0, limit: 50 };
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('lists rows newest first and filters by kind', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => { calls.push(String(input)); return json(PAGE); }));
  render(<Audit />);
  const cells = await screen.findAllByRole('cell', { name: /^(matrix\.session_end|admin\.backup_run|auth\.login)$/ });
  expect(cells.map((c) => c.textContent)).toEqual(['matrix.session_end', 'admin.backup_run', 'auth.login']);
  expect(screen.getByRole('cell', { name: 'failure' }).className).toContain('dr-danger');
  expect(calls[0]).toBe('/api/admin/audit?kind=&offset=0&limit=50');
  fireEvent.click(screen.getByRole('button', { name: 'Older' }));
  await waitFor(() => expect(calls).toContain('/api/admin/audit?kind=&offset=50&limit=50'));
  fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'backup' } });
  await waitFor(() => expect(calls).toContain('/api/admin/audit?kind=backup&offset=0&limit=50'));
});

it('refuses a malformed page', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...PAGE, records: [{ ...PAGE.records[0], id: 'x' }] })));
  render(<Audit />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
});
```

`web/src/pages/Dashboard.test.tsx`:

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { Dashboard } from './Dashboard';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const up = (name: string) => ({ name, status: 'up', version: '', pinned: '', mismatch: false, source: '', error: '' });
function serve(routes: Record<string, Response>) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const key = Object.keys(routes).find((k) => String(input).startsWith(k));
    if (!key) throw new Error(`unexpected ${String(input)}`);
    return routes[key].clone();
  }));
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('shows chat health, the user count and the last backup, each linking to its page', async () => {
  serve({
    '/api/admin/health': json({ matrix: true, components: [up('kymessages'), up('database'), { ...up('element'), mismatch: true }] }),
    '/api/admin/matrix/users': json({ users: [], total: 7, offset: 0, limit: 1, directory_url: '' }),
    '/api/backup/status': json({ last_run: { outcome: 'success', trigger: 'scheduled', recorded_at: '2026-10-02T03:00:00Z', capsule_id: 'c1' } }),
  });
  const onNavigate = vi.fn();
  render(<Dashboard settings={{ app_name: 'KyMessages' }} user={{ username: 'root' }} onNavigate={onNavigate} />);
  expect(await screen.findByText('3 of 3 components up, 1 off their pinned version')).toBeTruthy();
  expect(await screen.findByText('7 Matrix users')).toBeTruthy();
  expect(await screen.findByText(/^Last backup: Succeeded/)).toBeTruthy();
  expect(screen.queryByText(/not set up yet/)).toBeNull();
  fireEvent.click(within(screen.getByRole('region', { name: 'Chat health' })).getByRole('button'));
  expect(onNavigate).toHaveBeenCalledWith('health');
});

it('says when chat is not set up and when a source fails', async () => {
  serve({
    '/api/admin/health': json({ error: 'boom' }, 500),
    '/api/admin/matrix/users': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),
    '/api/backup/status': json({}),
  });
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  expect(await screen.findByText('Unavailable: HTTP 500')).toBeTruthy();
  expect(await screen.findByText('Chat (Matrix) is not set up')).toBeTruthy();
  expect(await screen.findByText('Last backup: none recorded')).toBeTruthy();
});
```

Run `npx vitest run src/pages/Health.test.tsx src/pages/Audit.test.tsx src/pages/Dashboard.test.tsx` → FAIL.

- [ ] **Step 2: Implement Health.** `web/src/pages/Health.tsx`:

```tsx
import React, { useCallback, useEffect, useState } from 'react';
import { ExternalLink, RefreshCw, XCircle } from 'lucide-react';
import { errorMessage } from '../api';
import { arr, bool, obj, oneOf, str } from '../dto';
import { NetworkCheck } from '../components/NetworkCheck';

export interface HealthComponent { name: string; status: 'up' | 'down'; version: string; pinned: string; mismatch: boolean; source: string; error: string }
export interface HealthReport { matrix: boolean; components: HealthComponent[] }

export function parseHealth(v: unknown): HealthReport {
  const h = obj(v);
  return {
    matrix: bool(h.matrix),
    components: arr(h.components).map((x) => {
      const c = obj(x);
      const source = str(c.source, 512);
      return {
        name: str(c.name, 64), status: oneOf(c.status, ['up', 'down'] as const), version: str(c.version, 64), pinned: str(c.pinned, 64),
        mismatch: bool(c.mismatch), source: source.startsWith('https://') ? source : '', error: str(c.error),
      };
    }),
  };
}

const LABEL: Record<string, string> = {
  kymessages: 'KyMessages', database: 'KyMessages database', synapse: 'Synapse',
  mas: 'Matrix Authentication Service', element: 'Element Web', postgres: 'PostgreSQL (Matrix)',
};

export const Health: React.FC = () => {
  const [report, setReport] = useState<HealthReport | null>(null);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(true);
  const check = useCallback(async (signal?: AbortSignal) => {
    setChecking(true);
    try {
      const res = await fetch('/api/admin/health', { signal, cache: 'no-store' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Health check failed'));
      setReport(parseHealth(await res.json()));
      setError('');
    } catch (err) {
      if (!signal?.aborted) setError(err instanceof Error && err.message ? err.message : 'Health check failed');
    } finally {
      if (!signal?.aborted) setChecking(false);
    }
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void check(controller.signal);
    return () => controller.abort();
  }, [check]);

  return (
    <div className="dr-page">
      <div className="dr-header">
        <h1>Health</h1>
        <button type="button" className="btn-secondary" onClick={() => void check()} disabled={checking}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : ''} />
          <span>Check again</span>
        </button>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><XCircle size={16} /><span>{error}</span></div>}
      {report && !report.matrix && <p className="dr-hint">Chat (Matrix) is not set up: only KyMessages and its database are checked.</p>}
      {report && (
        <section className="dr-facts" aria-label="Components">
          {report.components.map((c) => (
            <div key={c.name} className="dr-fact">
              <div className="dr-fact-label">
                <span>{LABEL[c.name] ?? c.name}</span>
                <span className={c.status === 'up' ? 'badge badge-success' : 'badge badge-danger'}>{c.status === 'up' ? 'Up' : 'Down'}</span>
              </div>
              <div className="dr-fact-value dr-mono">{c.version || (c.name === 'database' ? '' : 'Version unknown')}</div>
              {c.mismatch && <div className="dr-fact-note dr-danger">Compose pins {c.pinned}; this is not the pinned version.</div>}
              {!c.mismatch && c.pinned && c.version && <div className="dr-fact-note">Pinned in Compose</div>}
              {c.source && (
                <a className="dr-fact-note" href={c.source} target="_blank" rel="noopener noreferrer">
                  Source for {c.version} <ExternalLink size={12} />
                </a>
              )}
              {c.error && <div className="dr-fact-note dr-danger">{c.error}</div>}
            </div>
          ))}
        </section>
      )}
      <section className="panel dr-section"><NetworkCheck /></section>
    </div>
  );
};
```

- [ ] **Step 3: Implement Audit.** `web/src/pages/Audit.tsx`:

```tsx
import React, { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { arr, count, iso, obj, str } from '../dto';

export interface AuditRecord { id: number; at: string; actor: string; action: string; target: string; outcome: string; details: string; ip: string }
export interface AuditPage { records: AuditRecord[]; total: number; offset: number; limit: number }

export function parseAudit(v: unknown): AuditPage {
  const p = obj(v);
  return {
    records: arr(p.records).map((x) => {
      const r = obj(x);
      return { id: count(r.id), at: iso(r.at), actor: str(r.actor, 255), action: str(r.action, 128), target: str(r.target),
        outcome: str(r.outcome), details: str(r.details, 4096), ip: str(r.ip, 64) };
    }),
    total: count(p.total), offset: count(p.offset), limit: count(p.limit),
  };
}

const KINDS = [['', 'All'], ['auth', 'Sign-in'], ['backup', 'Backup'], ['matrix', 'Matrix'], ['scim', 'SCIM']] as const;
const PAGE = 50;
const bad = (outcome: string) => /^(error|failure|refused)/.test(outcome);

export const Audit: React.FC = () => {
  const [kind, setKind] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<AuditPage | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ kind, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/audit?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the audit log'));
        setPage(parseAudit(await res.json()));
        setError('');
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error && err.message ? err.message : 'Could not read the audit log');
      });
    return () => controller.abort();
  }, [kind, offset]);

  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Audit log</h1></div>
      <p className="dr-hint">Read-only. Newest first.</p>
      <div className="dr-row">
        <label className="dr-field dr-narrow" style={{ flex: '0 0 12rem' }}>
          Kind
          <select value={kind} onChange={(e) => { setKind(e.target.value); setOffset(0); }}>
            {KINDS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {page && (
        <section className="panel dr-section" aria-label="Audit records">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead>
                <tr><th scope="col">When</th><th scope="col">Who</th><th scope="col">What</th><th scope="col">Target</th><th scope="col">Outcome</th><th scope="col">Details</th><th scope="col">IP</th></tr>
              </thead>
              <tbody>
                {page.records.map((r) => (
                  <tr key={r.id}>
                    <td>{new Date(r.at).toLocaleString()}</td>
                    <td>{r.actor}</td>
                    <td className="dr-mono">{r.action}</td>
                    <td className="dr-mono">{r.target || '—'}</td>
                    <td className={bad(r.outcome) ? 'dr-danger' : ''}>{r.outcome || '—'}</td>
                    <td className="dr-mono" style={{ fontSize: '12px', color: 'var(--ink)' }}>{r.details || '—'}</td>
                    <td className="dr-mono">{r.ip || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.records.length === 0 && <p className="dr-hint">No audit records.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${page.offset + page.records.length} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Newer</button>
              <button type="button" className="btn-secondary" disabled={page.offset + page.records.length >= page.total} onClick={() => setOffset(offset + PAGE)}>Older</button>
            </div>
          </div>
        </section>
      )}
    </div>
  );
};
```

- [ ] **Step 4: Overview, Settings, navigation.** Replace `web/src/pages/Dashboard.tsx` with:

```tsx
import React, { useEffect, useState } from 'react';
import { Activity, Archive, ArrowRight, MessageSquare } from 'lucide-react';
import { isMatrixDisabled } from '../api';
import { parseHealth } from './Health';
import { parseUsersPage } from './Users';
import { backupAttempt } from './Backup';

interface DashboardProps {
  settings: { app_name?: string } | null;
  user: { display_name?: string; username?: string } | null;
  onNavigate: (tab: string) => void;
}
interface Card { text: string; tone: 'success' | 'danger' | 'muted' }
const OUTCOME = { success: 'Succeeded', warning: 'Needs attention', failure: 'Failed', unknown: 'Outcome unavailable' } as const;
const checking: Card = { text: 'Checking…', tone: 'muted' };

export const Dashboard: React.FC<DashboardProps> = ({ settings, user, onNavigate }) => {
  const [health, setHealth] = useState<Card>(checking);
  const [users, setUsers] = useState<Card>(checking);
  const [backup, setBackup] = useState<Card>(checking);

  useEffect(() => {
    const controller = new AbortController();
    const get = (path: string) => fetch(path, { signal: controller.signal, cache: 'no-store' });
    const settle = (set: (c: Card) => void, work: Promise<Card>) =>
      work.then(set, (err: unknown) => {
        if (!controller.signal.aborted) set({ text: `Unavailable: ${err instanceof Error ? err.message : 'request failed'}`, tone: 'danger' });
      });
    void settle(setHealth, get('/api/admin/health').then(async (res): Promise<Card> => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const { matrix, components } = parseHealth(await res.json());
      const down = components.filter((c) => c.status === 'down').length;
      const off = components.filter((c) => c.mismatch).length;
      const text = `${components.length - down} of ${components.length} components up` +
        (off ? `, ${off} off their pinned version` : '') + (matrix ? '' : ' (chat not set up)');
      return { text, tone: down || off ? 'danger' : 'success' };
    }));
    void settle(setUsers, get('/api/admin/matrix/users?limit=1').then(async (res): Promise<Card> => {
      if (await isMatrixDisabled(res)) return { text: 'Chat (Matrix) is not set up', tone: 'muted' };
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const { total } = parseUsersPage(await res.json());
      return { text: `${total} Matrix user${total === 1 ? '' : 's'}`, tone: 'muted' };
    }));
    void settle(setBackup, get('/api/backup/status').then(async (res): Promise<Card> => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const body: unknown = await res.json();
      const last = backupAttempt(typeof body === 'object' && body !== null ? (body as { last_run?: unknown }).last_run : undefined);
      if (!last) return { text: 'Last backup: none recorded', tone: 'muted' };
      return {
        text: `Last backup: ${OUTCOME[last.outcome]} ${new Date(last.recorded_at).toLocaleString()}`,
        tone: last.outcome === 'success' ? 'success' : last.outcome === 'unknown' ? 'muted' : 'danger',
      };
    }));
    return () => controller.abort();
  }, []);

  const cards = [
    { title: 'Chat health', card: health, Icon: Activity, tab: 'health', action: 'Open Health' },
    { title: 'Matrix users', card: users, Icon: MessageSquare, tab: 'users', action: 'Open Users' },
    { title: 'Backups', card: backup, Icon: Archive, tab: 'backup', action: 'Open Backup & recovery' },
  ];
  return (
    <div style={{ maxWidth: '1080px', margin: '0 auto', padding: '32px 20px' }}>
      <div style={{ marginBottom: '32px' }}>
        <h1 style={{ fontSize: '26px', fontWeight: 'bold', marginBottom: '6px' }}>Welcome, {user?.display_name || user?.username}!</h1>
        <p style={{ color: 'var(--ink)', fontSize: '15px' }}>{settings?.app_name || 'KyMessages'} operator console.</p>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 280px), 1fr))', gap: '20px' }}>
        {cards.map(({ title, card, Icon, tab, action }) => (
          <section key={tab} className="panel" aria-label={title} style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
            <div>
              <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '12px' }}>
                <div style={{ padding: '8px', background: 'var(--accent-soft)', borderRadius: '6px', color: 'var(--accent)' }}><Icon size={20} /></div>
                <h3 style={{ fontSize: '16px' }}>{title}</h3>
              </div>
              <p role="status" className={card.tone === 'danger' ? 'dr-danger' : card.tone === 'success' ? 'dr-ok' : ''} style={{ fontSize: '14px' }}>{card.text}</p>
            </div>
            <div style={{ marginTop: '20px', borderTop: '1px solid var(--line)', paddingTop: '12px' }}>
              <button type="button" className="btn-secondary" style={{ width: '100%', justifyContent: 'space-between', fontSize: '13px' }} onClick={() => onNavigate(tab)}>
                <span>{action}</span>
                <ArrowRight size={14} />
              </button>
            </div>
          </section>
        ))}
      </div>
    </div>
  );
};
```

`Settings.tsx`: delete `import { NetworkCheck } …` and `<NetworkCheck />`.

`AppHeader.tsx`: import `Activity, ScrollText`. `adminItems` becomes:
- dashboard (`Overview`)
- users (`Users`)
- `{ id: 'health', label: 'Health', icon: Activity }`
- `{ id: 'audit', label: 'Audit', icon: ScrollText }`
- scim
- backup
- settings

`AppHeader.test.tsx` expects `['dashboard', 'users', 'health', 'audit', 'scim', 'backup', 'settings']`.

`App.tsx`: import `Health` and `Audit`. Add `{isAdmin && activeTab === 'health' && <Health />}` and `{isAdmin && activeTab === 'audit' && <Audit />}`.

- [ ] **Step 5: Run.** `cd web && npm test` → PASS (`NetworkCheck.test.tsx` unchanged). Then `npm run build` → PASS. Commit `web/dist`.

- [ ] **Step 6: Commit.** `web: Health and Audit pages, real Overview; network check moves to Health`.

---

### Task 7: Acceptance and browser regressions

**Files:**
- Modify: `scripts/matrix-acceptance.sh` (lines 486-505 and a new step), `web/browser/ui.spec.mjs`, `.github/workflows/ci.yml` (only if the matrix job's `timeout-minutes` is now too tight)

**Interfaces:**
- Consumes:
  - the Task 4 routes, called through the harness's `app_api`, which sends the cookie jar and the CSRF header
  - `e2e token USER` (`e2e.mjs:321-333`), which writes `state/USER.token`
  - `cut_within` and `now_ms` (`matrix-acceptance.sh:356-371`)
  - the Task 5 and 6 UI

- [ ] **Step 1: Move the operator sign-in into a `console` step.** In `matrix-acceptance.sh`, move the `jar=`, `app_api()` and `kyb()` definitions (lines 486-494) above the `admin-isolation` step's closing `pass`, so they are defined before the new step. Move these lines out of `step backup` into the new step: `app_api POST /api/auth/login … $KY_ADMIN_PASSWORD`, `admin_pass=$(openssl rand -hex 16)`, and the `change-password` call. `step backup` keeps its third line, `app_api POST /api/auth/login … $admin_pass`, so pinning still has a fresh session. Insert this new step before `step backup`:

```bash
# ---------------------------------------------------------------------------------------
# The operator console: Health reports every component on its pinned version, and ending a
# session there cuts a live Element token like offboarding does, audited.
step console
app_api POST /api/auth/login "$(jq -n --arg p "$KY_ADMIN_PASSWORD" '{username: "admin", password: $p}')" >/dev/null
admin_pass=$(openssl rand -hex 16)
app_api POST /api/auth/change-password "$(jq -n --arg c "$KY_ADMIN_PASSWORD" --arg n "$admin_pass" '{current_password: $c, new_password: $n}')" >/dev/null
app_api POST /api/auth/login "$(jq -n --arg p "$admin_pass" '{username: "admin", password: $p}')" >/dev/null
ok "operator signed in to the console and replaced the bootstrap password"
app_api GET /api/admin/health >"$state/health.json"
expect "$(jq -r '[.components[].name] | join(",")' "$state/health.json")" kymessages,database,synapse,mas,element,postgres "health checks every component"
expect "$(jq -r '[.components[] | select(.status != "up") | .name] | join(",")' "$state/health.json")" "" "every component up"
dc config --format json >"$state/compose.json"
for svc in synapse mas element postgres; do
	# The pin, read here from the resolved Compose file, independently of the Go generator.
	pin=$(jq -r --arg s "$svc" '.services[$s].image | split("@")[0] | split(":") | last | ltrimstr("v") | split("-")[0]' "$state/compose.json")
	expect "$(jq -r --arg s "$svc" '.components[] | select(.name == $s) | "\(.version) \(.pinned) \(.mismatch)"' "$state/health.json")" \
		"$pin $pin false" "$svc runs the pinned $pin"
done
alice='Alice.Q@Ky'
e2e token "$alice"
expect "$(token_status "$alice")" 200 "alice's captured Element token is live"
device=$(hcurl -fsS -H "Authorization: Bearer $(cat "$state/$alice.token")" https://matrix.kymatrix.test/_matrix/client/v3/account/whoami | jq -re .device_id)
alice_id=$(app_api GET '/api/admin/matrix/users?search=alice.q_ky' |
	jq -re --arg m "@alice.q_ky:$KY_MATRIX_SERVER_NAME" '.users[] | select(.mxid == $m and .status == "active") | .id')
session=$(app_api GET "/api/admin/matrix/users/$alice_id/sessions" | jq -re --arg d "$device" \
	'[.sessions[] | select(.kind == "oauth2" and .device == $d)] | if length == 1 then .[0].id else error("want one session for \($d), got \(length)") end')
start=$(now_ms)
expect "$(app_api POST "/api/admin/matrix/sessions/oauth2/$session/finish" | jq -r .outcome)" ended "the console ended alice's Element session $session"
secs=$(cut_within 30 "$alice" "$start")
ok "alice's open Element session refused ${secs}s after the console ended it (bound 30s)" | tee -a "$summary"
expect "$(app_api POST "/api/admin/matrix/sessions/oauth2/$session/finish" | jq -r .outcome)" already_ended "ending it again succeeds"
expect "$(app_api GET '/api/admin/audit?kind=matrix&limit=20' | jq -r --arg s "$session" --arg m "@alice.q_ky:$KY_MATRIX_SERVER_NAME" \
	'[.records[] | select(.action == "matrix.session_end" and .target == $m and .actor == "admin" and (.details | contains($s))) | .outcome] | join(",")')" \
	already_ended,ended "the audit API shows both session-end rows, newest first"
pass
```

Alice's `prove-alice` profile is a different device, and it is untouched: `e2e media` in `backup` still uses it. Update the header comment at the top of the script to list the console proof.

- [ ] **Step 2: Run the acceptance.** `make matrix-acceptance` → every step PASS, and the summary shows alice's console cut time. If the cut exceeds 30 s, stop. Record the time and the cause (compare with `offboard-cut`'s time; both depend on Synapse's introspection of MAS) and report BLOCKED. Do not raise the bound. If the job's total duration grows past its `timeout-minutes` in `.github/workflows/ci.yml`, raise that value and add a comment giving the measured local duration. Then run `shellcheck scripts/*.sh scripts/matrix-acceptance/*.sh` → clean.

- [ ] **Step 3: Browser regressions.** In `web/browser/ui.spec.mjs`, after the backup screenshot (line 104) and before `expect(violations).toEqual([])`, add:

```js
  // Console pages. Matrix is off here: Users says so, Health checks the app and its database
  // (with the network check moved from Settings), Audit lists the backup that just ran.
  await nav.getByRole('button', { name: 'Users', exact: true }).click();
  await expect(page.getByText('Chat (Matrix) is not set up on this server.')).toBeVisible();
  await fits(page);
  await nav.getByRole('button', { name: 'Health', exact: true }).click();
  const components = page.getByRole('region', { name: 'Components' });
  await expect(components.getByText('Up', { exact: true })).toHaveCount(2);
  await expect(page.getByRole('heading', { name: 'Network path' })).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('health.png'), fullPage: true });
  await nav.getByRole('button', { name: 'Health', exact: true }).focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button', { name: 'Audit', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(nav.getByRole('button', { name: 'Audit', exact: true })).toHaveAttribute('aria-current', 'page');
  await page.getByLabel('Kind').selectOption('backup');
  await expect(page.getByRole('cell', { name: 'admin.backup_run', exact: true }).first()).toBeVisible();
  await expect(page.getByRole('cell', { name: 'auth.login', exact: true })).toHaveCount(0);
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('audit.png'), fullPage: true });
  await nav.getByRole('button', { name: 'Overview', exact: true }).click();
  await expect(page.getByText(/^Last backup: Succeeded/)).toBeVisible();
  await expect(page.getByText(/not set up yet/)).toHaveCount(0);
  await fits(page);
```

The Settings step (line 61-64) still finds its heading; it no longer shows the network check. No extra login is spent, so the login budget in `web/browser/AGENTS.md` holds.

- [ ] **Step 4: Run.** Build first: `cd web && npm run build && cd .. && go build -o .browser/server ./cmd/server`. Then `cd web && npm run test:browser` → all six projects PASS (Chromium light and dark at 390 and 1280, Firefox light-1280 and dark-390).

- [ ] **Step 5: Commit.** `acceptance: console ends a live Element session within 30s; browser regressions cover the console`.

---

### Task 8: Docs and DOX pass

**Files:**
- Modify:
  - `README.md` ("Matrix chat" section)
  - `AGENTS.md` (root)
  - `internal/api/AGENTS.md`, `internal/matrixsync/AGENTS.md`, `internal/store/AGENTS.md`
  - `web/AGENTS.md`, `web/browser/AGENTS.md`
  - `docs/PRODUCT.md` (only if it still promises a console it now describes wrongly)

- [ ] **Step 1: README.** In "Matrix chat", add a short "Operator console" subsection:
  - **Users** lists Matrix users with their KyIdentity link. It ends one session or all of a person's sessions. Access itself is changed in KyIdentity.
  - **Health** probes each component on load and compares its version with the Compose pin. It links each component to its upstream source and shows the network check.
  - **Audit** is the read-only log, filtered by kind.
  - Ending a session needs a sign-in from the last 10 minutes; the console asks "Confirm it's you".
  - Session IPs and devices are shown to admins only.

- [ ] **Step 2: Root `AGENTS.md`.**
  - In the closing paragraph, change "Open: the console and removal of the custom messaging stack" to state that console 5a (users, health, audit) shipped. 5b (rooms) and 5c (settings) are open, and so is removing the custom messaging stack.
  - Extend the `scripts/matrix-acceptance.sh` Verification bullet with: "Console: Health shows every component up on its Compose pin; a console admin ends alice's Element session, her live token is refused within 30s, and the audit API shows the `matrix.session_end` rows."
  - In the `cmd/server` paragraph, say that the offboarding syncer and the API share one `matrixsync.Client`.
  - Confirm that Task 3's index line is present.

- [ ] **Step 3: `internal/api/AGENTS.md`.**
  - Add a console contract bullet: the five routes and their trust levels, `matrix_disabled` 404 when Matrix is off, `SetMatrixAdmin`, ULID-only path IDs, paging limit at most 100, and `no-store`.
  - Add: `matrix.session_end` audit details (`session`, `kind`, `outcome` = `ended`, `already_ended`, `error: …` or `refused: no user`). The route runs detached and `tracked`.
  - Add: the audit kinds map (`auth` = `auth.`, `device.`; `backup` = `backup.`, `admin.backup_`, `restore.`; `matrix`; `scim`), actor resolution (`system` for server rows), and `outcome` parsed from details.
  - Rewrite the step-up bullet to give the generic message and say it applies to every fresh-admin route, not only backup.
  - Add the five routes to the route table, or a second table.
  - Rename `auditBackup` to `audit` where the doc names it.
  - Under Verification, mention `console_test.go`.

- [ ] **Step 4: `internal/matrixsync/AGENTS.md`.**
  - Ownership: the client also serves the console through `api.MatrixAdmin` (sessions, finish, user, version), and `cmd/server` shares one client.
  - Local Contracts:
    - `FinishSession` treats MAS's 400 for an ended session as `already`, after reading the session back.
    - `Sessions` returns browser sessions first.
    - The oauth2 device comes from the scope.
  - Reword the audit bullet: the sweep's actions are exactly `matrix.lock`, `matrix.unlock` and `matrix.deactivate`. `matrix.session_end` is written by `internal/api`.

- [ ] **Step 5: `internal/store/AGENTS.md`.** Add: `ListAuditRecords(offset, limit, prefixes...)` pages newest first by `id`. It filters with `substr(action, 1, n) = prefix`, because LIKE treats `_` as a wildcard and the engines differ. No index serves the filter, deliberately, at the target size.

- [ ] **Step 6: `web/AGENTS.md` and `web/browser/AGENTS.md`.**
  - In `web/AGENTS.md`:
    - Replace "states that chat (Matrix) is not set up yet" with the Overview's three live cards.
    - Move the `NetworkCheck.tsx` contract to the Health page.
    - Add contracts for `adminFetch`, `ConfirmItsYou` and `dto.ts`, Users (no lock or unlock; the KyIdentity notice; End all goes in server order and stops on a refused confirmation), Health (a non-https source is dropped), and Audit (read-only).
    - Replace the Backup `reauth_url` link bullet with "step-up goes through `adminFetch`".
    - Add `src/test-setup.ts` (the jsdom dialog stub) to Verification.
  - In `web/browser/AGENTS.md`, add that the suite also covers Users (Matrix off), Health, the Audit kind filter with keyboard navigation, and the Overview, at every project.

- [ ] **Step 7: Verify.** `make ci` → PASS. Then `grep -rn "auditBackup\|not set up yet\|backup changes need a sign-in" --include='*.go' --include='*.md' --include='*.ts*' . | grep -v node_modules | grep -v web/dist` → no stale hits.

- [ ] **Step 8: Commit.** `docs: console 5a contracts, operator guide and DOX pass`.
