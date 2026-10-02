# Matrix Console 5b (Rooms) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A KyMessages admin sees every Matrix room in the console and closes or permanently deletes a bad one. Every change needs a fresh sign-in and is audited. Synapse admin access runs through a dedicated MAS service account with 5-minute sessions.

**Architecture:**
- `internal/matrixsync` gains the console service account `kymessages-console` (ensured through the MAS admin API, exempt from the sweep by exact username) and `AsConsole`, which mints a 5-minute personal session, runs one action and revokes the session on a detached context.
- A new `internal/synapseadmin` package calls Synapse's admin API at `http://synapse:8008` with that token.
- `internal/api` adds the room routes and a `synapse-admin` Health probe.
- The web console gets a Rooms tab with typed-name confirmation and job polling.
- Task 1 proves the whole chain live in the acceptance harness before any room code is written.

**Tech Stack:** Go 1.27 (stdlib only), MAS 1.26.0 admin API, Synapse 1.162.0 admin API, React 19 + TypeScript + Vite + vitest, Playwright, the bash acceptance harness.

**Spec:** `docs/superpowers/specs/2026-10-02-matrix-console-5b-design.md` (context: `2026-10-02-matrix-console-5a-design.md`).

## Upstream facts this plan relies on

Read from source. MAS v1.26.0 is at `/tmp/mas-src`; Synapse v1.162.0 was read with `gh api 'repos/element-hq/synapse/contents/<path>?ref=v1.162.0'`. Each fact was read from source but has not been run live, except where Task 1 proves it.

**MAS**

| Endpoint or behaviour | What the source shows |
|---|---|
| `POST /api/admin/v1/users` (`crates/handlers/src/admin/v1/users/add.rs`) | Body `{username, skip_homeserver_check?}`. Answers 201 `data.id` and `data.attributes.{username, admin, locked_at, deactivated_at}`. An existing user gets 409. The username rule is `[a-z0-9=_\-./+]`, so `kymessages-console` is valid. The user is provisioned in Synapse immediately (`provision_user`, lines 178-181). |
| `GET /api/admin/v1/users/by-username/{username}` (`users/by_username.rs`) | 404 when the user does not exist. |
| `POST /users/{id}/set-admin` with `{"admin":true}` (`users/set_admin.rs`) | Sets `users.can_request_admin`. |
| `POST /api/admin/v1/personal-sessions` (`personal_sessions/add.rs:67-81`) | Body `{actor_user_id, human_name, scope, expires_in?}`. Answers 201 with `data.id` and `data.attributes.access_token` (`mpt_…`). A deactivated actor gets 410. A **locked actor is accepted**. No policy runs and `can_request_admin` is never checked. |
| `POST /personal-sessions/{id}/revoke` (`personal_sessions/revoke.rs`) | No body. 200; 409 when already revoked. |
| Introspection (`crates/handlers/src/oauth2/introspection.rs:656-743`) | A personal token whose actor is locked or deactivated introspects as inactive. The scope is returned as stored, and `device_id` is none. |
| Admin API registration (`mod.rs:95-120`) | Personal sessions need no feature flag. |
| Schema | `personal_sessions(actor_user_id, scope_list text[], revoked_at)`, `personal_access_tokens(created_at, expires_at, revoked_at)`, `users.can_request_admin`. |

**Synapse**

| Endpoint or behaviour | What the source shows |
|---|---|
| Admin authentication (`synapse/api/auth/mas.py`) | Admin is decided by `"urn:synapse:admin:*" in requester.scope` (line 274). Every token needs `urn:matrix:client:api:*` (lines 339-345). A device scope is optional (lines 368-406). The user must already exist in Synapse (lines 359-364). `/_synapse/admin` uses this same path. |
| `GET /_synapse/admin/v1/rooms` (`rest/admin/rooms.py:272-346`; `storage/databases/main/room.py:800-999`) | Params `from`, `limit`, `order_by`, `search_term`. An empty `search_term` gets 400. The search matches the name, an alias substring, or the exact room ID. Each row has `room_id, name, canonical_alias, joined_members, joined_local_members, creator, encryption, public, join_rules, state_events, …` and the page has `total_rooms`. **There is no byte size and no blocked flag.** |
| `GET /v1/rooms/{id}` and `GET /v1/rooms/{id}/members` | `{members:[user ids], total}`; 404 "Room not found". |
| `DELETE /_synapse/admin/v2/rooms/{id}` (`rooms.py:147-205`) | Body `{block, purge, force_purge}`. **`purge` defaults to true.** Answers `{delete_id}`. The work runs in the background. While a task is *active*, a second request gets 400 "Purge already in progress"; a *scheduled* task does not trip this. |
| `block=true, purge=false` (`handlers/room.py:2440-2581`) | Blocks the room (upsert into `blocked_rooms`). Every local member then **leaves as themselves**. The room stays in `/rooms`. |
| Rejoining a blocked room (`handlers/room_member.py:882-885`) | 403 `M_UNKNOWN` "This room has been blocked on this server". |
| `GET /v2/rooms/{id}/delete_status` (`rooms.py:217-269`) | `{results:[{delete_id, room_id, status, shutdown_room}]}` for active, complete and failed tasks only, not scheduled ones. **It carries no error text.** 404 when the room has no task. Finished tasks are kept for a week. |
| `GET /v1/rooms/{id}/block` | `{block: bool}`. |
| Purge (`storage/databases/main/purge_events.py`) | Deletes `events`, `event_json`, `state_events`, `current_state_events`, `room_memberships`, `rooms` and more. It keeps `blocked_rooms` and **deletes no media**. |
| `GET /_synapse/admin/v1/room/{id}/media` (`rest/admin/media.py:364-380`; `room.py:1206-1236`) | Lists only media referenced by a plaintext `url` in non-encrypted events. `DELETE /_synapse/admin/v1/media/{server}/{media_id}` (`media.py:415-437`) deletes local media and answers 404 when it is unknown. |

## Open questions for the controller

Each comes with a recommendation. The plan implements the recommendation; change the task if you decide otherwise.

1. **Reopen cannot work on this deployment; the plan drops it.**
   - The approved Close (`block=true, purge=false`) makes every local member leave (`handlers/room.py:2504-2515`).
   - With federation off, the server is then no longer in the room. A join to a room with no joined local member goes down the remote-join path (`room_member.py:984`, `1088-1131`), and there are no other servers to join through.
   - An invite needs a joined inviter. Synapse's admin `join` and `make_room_admin` both need a joined local admin in the room (`rest/admin/rooms.py:607-625`, `696-712`).
   - So after Close, unblocking lets nobody back in, public room or not, and "Reopen lets an invite work" cannot pass. This is derived from source and not proven live.
   - **Recommend:** ship Close as final ("history kept, nobody can rejoin; delete it when you no longer need the history"). Drop the `/reopen` route and the Reopen button, and amend the spec (Task 6).
   - If the owner wants reversibility, it needs a different Close: the console account takes room admin via `make_room_admin` before removing the others, and stays joined. Reopen then needs a console invite action, which is membership editing and out of 5b's scope. That would be a follow-up sub-project.
2. **Delete cannot purge most media.**
   - Synapse's room purge deletes no media.
   - The only media Synapse can attribute to a room is what plaintext events reference. Every room here is encrypted by default (`homeserver.yaml.tmpl`: `encryption_enabled_by_default_for_room_type: all`), so attachments sit inside ciphertext.
   - **Recommend:** before starting the purge, delete the local media that `/room/{id}/media` lists (typically the room avatar). Audit how many were deleted. Tell the admin plainly that attachments in encrypted rooms stay in the media store as encrypted files the server cannot attribute.
   - Alternative: skip media entirely and say so.
3. **Close is a background job too.** The approved Close is the same `DELETE /v2/rooms` call with `purge=false`, so it returns a `delete_id`. **Recommend:** both Close and Delete answer `{"outcome":"started","delete_id":…}`, and the page polls `delete-status` for both. The audit row records the start, as the spec says for Delete; completion is not audited.
4. **"Size".** Synapse reports no byte size. `state_events` is the only size-like count. **Recommend:** show it under the honest header "State events" and say why in the page hint.
5. **Blocked per row.** The room list has no blocked flag, so the route reads `/block` once per listed room (at most 100, on the internal network). **Recommend:** accept this at the target team size.
6. **The Health probe makes one admin read.** Minting and revoking a session proves nothing about Synapse. **Recommend:** the `synapse-admin` probe mints a session, reads `rooms?limit=1` and revokes. That is also Task 1's live proof.
7. **`set-admin` is not what grants Synapse admin.** MAS never checks `can_request_admin` for personal sessions, and Synapse decides by scope alone. **Recommend:** keep it as the spec says. It is cheap and marks the account as admin in MAS. Document the real boundaries:
   - The MAS admin client secret can already mint a session for any user, so the console account adds no privilege beyond it.
   - Locking `kymessages-console` in MAS cuts the console's room access: a locked actor's tokens introspect inactive, and the sweep never unlocks the account.
8. **The typed confirmation.** The admin types the name exactly as the console shows it: clipped to 200 bytes, as every console label is. **Recommend:** use the room ID instead when the name is empty or holds characters nobody can type (controls, bidi and other format characters). The detail response carries the exact `confirm_text`.
9. **A second delete while one is scheduled.** Neither Synapse's guard nor `delete_status` sees a scheduled task. **Recommend:** refuse while an *active* or listed task runs, and map Synapse's own 400 to 409. A queued duplicate shutdown is harmless (an idempotent block, then a purge of nothing). Document this.
10. **Where the acceptance runs rooms.** **Recommend:** a last `rooms` step on the restored stack, for two reasons. The restore step asserts that bob is still in the group room. And the start-up sweep after the restore runs with the console account present, which proves the exemption.

## Global Constraints

- **Service account.** MAS username exactly `kymessages-console` (MXID `@kymessages-console:<server name>`). Created through the MAS admin API. No password, no upstream link, MAS admin. Never created at start-up: only when a console room action or Health needs it.
- **Sweep exemption.** `matrixsync.Plan` exempts exactly that username, and only while it has no KyIdentity link and is not ambiguous. Every other unlinked user is still locked. The sweep never unlocks the console account.
- **Sessions.** Each action mints a personal session:
  - scope exactly `urn:matrix:client:api:* urn:synapse:admin:*`;
  - `expires_in` 300;
  - `human_name` `KyMessages console`.
  It runs one action, then revokes the session on a context detached from the request (10 s). A failed revoke is logged with the session ID, never the token. The session still expires in 5 minutes.
- **No fallback.** There is no other Synapse credential. If the account is locked, deactivated or linked, or Synapse refuses the token, Rooms and Health say "Synapse admin access is not working: <cause>".
- **Transport.** Synapse admin calls go to `health.ComposeTargets.Synapse` (`http://synapse:8008`). They ignore proxy variables and follow no redirects. The public tunnel keeps `/_synapse/admin` blocked (`docs/Reverse_Proxy_Networking.md`).
- **Routes:**
  - `GET /api/admin/matrix/rooms?search&offset&limit` (admin)
  - `GET /api/admin/matrix/rooms/{id}` (admin)
  - `GET /api/admin/matrix/rooms/{id}/delete-status` (admin)
  - `POST /api/admin/matrix/rooms/{id}/close` (fresh admin)
  - `POST /api/admin/matrix/rooms/{id}/delete` with `{"confirm":"<room name>"}` (fresh admin)

  There is no `/reopen` (question 1). With Matrix off, every room route answers 404 `matrix_disabled`.
- **Audit.** Actions `matrix.room_close` and `matrix.room_delete`. Resource is the room ID. Details are strict quoted tokens with outcome first, `outcome="…" [delete_id="…"] [media="…"]`, at most 200 bytes. Outcomes:
  - `started`
  - `already_closed`
  - `refused: confirmation does not match`
  - `refused: a close or delete is running`
  - `error: …`

  Written on success and failure, after input parsing, on a detached context.
- **Delete confirmation.** Rechecked server side against `confirm_text` (question 8). A mismatch is 400 and audited.
- **What is shown.** Messages are never fetched or shown. Member lists and room names are admin-only and `no-store`.
- **Copy.** Never label a room "end-to-end encrypted": show "Encryption on" or "Encryption off" (the suite's E2EE claim has its own fixed wording).
- **Dependencies and design.** No new Go or npm dependencies. Use the existing design system (`dr-*`, `panel`, `console-table`, `badge`). Do not edit `web/src/ky-ui/`.
- **`web/dist`.** Every web task rebuilds `web/dist` and commits it, because CI diffs it.
- **Commits.** Each ends with a blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **A person whose KyIdentity username maps to `kymessages-console`, signed in before the console ever ran.** The console must refuse that linked account: never make it admin, never mint for it. The sweep must judge it as a person. Tests: Task 1 `TestEnsureConsoleUserRefusesBrokenAccounts` (linked) and `TestPlanExemptsOnlyTheUnlinkedConsoleAccount` (`x3`).
2. **A room name the admin cannot type** (bidi override, zero-width, control characters) **or one longer than 200 bytes.** Delete must stay possible: the confirmation falls back to the room ID, or matches the clipped name the page shows. Test: Task 3 `TestRoomDetailShowsMembersAndConfirmText`.
3. **The request dies mid-action** (the admin closes the tab, or Health's deadline passes). The console session must still be revoked, and Health must still answer on time. Tests: Task 1 `TestAsConsoleRevokesWhenTheCallFailsOrIsCancelled` and `TestHealthSynapseAdminProbeCannotHang`.
4. **A double click on Delete, or two admins at once.** The second request is refused while a job runs, never queued silently. Tests: Task 3 `TestRoomChangeRefusedWhileAJobRuns` (our pre-check and Synapse's own 400), and Task 4 (buttons disabled while pending).
5. **Synapse fails a job without saying why, or never lists it.** The page must say so and stop polling, not spin forever. Tests: Task 4 "reports a failed job" and "stops polling a job Synapse never lists".

---

### Task 1: Live proof — console account, personal sessions, Synapse admin probe

The gate for the whole plan. When this task's acceptance run passes, the chain is proven live: the service account, a personal session with exactly the two scopes, Synapse accepting it as admin, and the revoke. **If `synapse-admin` is not `up` in the acceptance, stop. Record its `error` text and report BLOCKED. Do not start Task 2.**

**Files:**
- Modify: `internal/matrixsync/mas.go` (`userAttrs` at lines 224-228; new code after `Version`, line 395)
- Modify: `internal/matrixsync/matrixsync.go` (`Plan`, lines 41-72)
- Create: `internal/matrixsync/console_test.go`
- Create: `internal/synapseadmin/synapseadmin.go`, `internal/synapseadmin/synapseadmin_test.go`, `internal/synapseadmin/AGENTS.md`
- Modify: `internal/api/server.go` (`MatrixAdmin` at lines 35-43; the `Server` struct; `NewServer`)
- Modify: `internal/api/health_handlers.go`
- Modify: `internal/api/export_test.go`
- Modify: `internal/api/console_test.go` (`fakeMAS`, `setupMatrixServer`, `TestHealthWithMatrixReportsEveryComponent`)
- Create: `internal/api/rooms_test.go`
- Modify: `scripts/matrix-acceptance.sh` (`step console`, lines 500-535)
- Modify: `AGENTS.md` (root Child DOX Index)

**Interfaces:**
- Produces, in `matrixsync`:
  - `const ConsoleUsername = "kymessages-console"`
  - `func (c *Client) EnsureConsoleUser(ctx context.Context) (string, error)`: returns the MAS user ID.
  - `func (c *Client) AsConsole(ctx context.Context, fn func(ctx context.Context, token string) error) error`
  - `userAttrs` gains `Admin bool`.
- Produces, in `synapseadmin`:
  - `func New(base string) *Client`
  - `type Error struct{ Method, Path string; Status int; Errcode, Message string }`
  - `type Room struct{ ID, Name, Alias, Creator string; Members, LocalMembers int; Encryption string; Public bool; JoinRule string; StateEvents int }`
  - `type RoomQuery struct{ Offset, Limit int; Search string }`
  - `type RoomPage struct{ Rooms []Room; Total int }`
  - `func (c *Client) Rooms(ctx, token string, q RoomQuery) (RoomPage, error)`
- Produces, in `api`:
  - `MatrixAdmin` gains `AsConsole`.
  - `type RoomAdmin interface{ Rooms(...) }`. Task 3 extends it.
  - `Server.rooms RoomAdmin`, defaulting to `synapseadmin.New(health.ComposeTargets.Synapse)`.
  - `SetRoomAdminForTest(s *Server, r RoomAdmin)`, test-only.
  - A Health component named `synapse-admin`.

- [ ] **Step 1: Failing matrixsync tests.** Create `internal/matrixsync/console_test.go`:

```go
package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const consoleID = "01J9ZK8V6N3W4X5Y6Z7A8B9C9C"

// consoleFake is MAS's admin API as the console account sees it. user is nil until created.
type consoleFake struct {
	mu           sync.Mutex
	user         map[string]any
	hideOnce     bool // the first by-username read misses a user that exists (a create race)
	linked       bool
	created      []string
	setAdmin     []string
	minted       []map[string]any
	revoked      []string
	revokeStatus int // non-zero: revoke answers this
	srv          *httptest.Server
}

func newConsoleFake(t *testing.T) *consoleFake {
	t.Helper()
	f := &consoleFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *consoleFake) client() *Client { return NewClient(f.srv.URL, "cid", "secret-value") }

func (f *consoleFake) handle(w http.ResponseWriter, r *http.Request) {
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
	body, _ := io.ReadAll(r.Body)
	userDoc := func() string {
		a, _ := json.Marshal(f.user)
		return fmt.Sprintf(`{"data":{"type":"user","id":%q,"attributes":%s}}`, consoleID, a)
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/admin/v1/users/by-username/kymessages-console":
		if f.user == nil || f.hideOnce {
			f.hideOnce = false
			http.Error(w, `{"errors":[{"title":"User not found"}]}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodPost && p == "/api/admin/v1/users":
		f.created = append(f.created, string(body))
		if f.user != nil {
			http.Error(w, `{"errors":[{"title":"User already exists"}]}`, http.StatusConflict)
			return
		}
		f.user = map[string]any{"username": "kymessages-console", "admin": false, "locked_at": nil, "deactivated_at": nil}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodGet && p == "/api/admin/v1/upstream-oauth-links":
		if r.URL.Query().Get("filter[user]") != consoleID {
			http.Error(w, "bad filter", http.StatusBadRequest)
			return
		}
		data := ""
		if f.linked {
			data = `{"type":"upstream-oauth-link","id":"L1","attributes":{"subject":"s","user_id":"` + consoleID + `"}}`
		}
		fmt.Fprintf(w, `{"data":[%s],"links":{}}`, data)
	case r.Method == http.MethodPost && p == "/api/admin/v1/users/"+consoleID+"/set-admin":
		f.setAdmin = append(f.setAdmin, string(body))
		f.user["admin"] = true
		fmt.Fprint(w, userDoc())
	case r.Method == http.MethodPost && p == "/api/admin/v1/personal-sessions":
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.minted = append(f.minted, m)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"data":{"type":"personal-session","id":"PS%d","attributes":{"access_token":"mpt_secret%d"}}}`, len(f.minted), len(f.minted))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/api/admin/v1/personal-sessions/") && strings.HasSuffix(p, "/revoke"):
		f.revoked = append(f.revoked, strings.TrimSuffix(strings.TrimPrefix(p, "/api/admin/v1/personal-sessions/"), "/revoke"))
		if f.revokeStatus != 0 {
			http.Error(w, `{"errors":[{"title":"no"}]}`, f.revokeStatus)
			return
		}
		fmt.Fprint(w, `{"data":{"type":"personal-session","id":"PS1","attributes":{}}}`)
	default:
		http.NotFound(w, r)
	}
}

func TestEnsureConsoleUserCreatesOnceAndGrantsAdmin(t *testing.T) {
	f := newConsoleFake(t)
	c := f.client()
	for range 2 {
		id, err := c.EnsureConsoleUser(context.Background())
		if err != nil || id != consoleID {
			t.Fatalf("ensure: %q %v", id, err)
		}
	}
	if !reflect.DeepEqual(f.created, []string{`{"username":"kymessages-console"}`}) {
		t.Errorf("created %v", f.created)
	}
	if !reflect.DeepEqual(f.setAdmin, []string{`{"admin":true}`}) {
		t.Errorf("set-admin %v", f.setAdmin)
	}
}

func TestEnsureConsoleUserSurvivesACreateRace(t *testing.T) {
	f := newConsoleFake(t)
	f.user = map[string]any{"username": "kymessages-console", "admin": true, "locked_at": nil, "deactivated_at": nil}
	f.hideOnce = true
	id, err := f.client().EnsureConsoleUser(context.Background())
	if err != nil || id != consoleID || len(f.created) != 1 || len(f.setAdmin) != 0 {
		t.Fatalf("id %q err %v created %v set-admin %v", id, err, f.created, f.setAdmin)
	}
}

// A locked, deactivated or linked account is refused, and no session is minted for it: a
// linked one is a person, never the console.
func TestEnsureConsoleUserRefusesBrokenAccounts(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*consoleFake)
		want string
	}{
		"locked":      {func(f *consoleFake) { f.user["locked_at"] = "2026-10-02T10:00:00Z" }, "console account is locked in MAS"},
		"deactivated": {func(f *consoleFake) { f.user["deactivated_at"] = "2026-10-02T10:00:00Z" }, "console account is deactivated in MAS"},
		"linked":      {func(f *consoleFake) { f.linked = true }, "console account is linked to an upstream identity"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newConsoleFake(t)
			f.user = map[string]any{"username": "kymessages-console", "admin": false, "locked_at": nil, "deactivated_at": nil}
			tc.edit(f)
			called := false
			err := f.client().AsConsole(context.Background(), func(context.Context, string) error { called = true; return nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) || called || len(f.minted) != 0 || len(f.setAdmin) != 0 {
				t.Fatalf("err %v called %v minted %v set-admin %v", err, called, f.minted, f.setAdmin)
			}
		})
	}
}

func TestAsConsoleMintsExactScopeAndRevokes(t *testing.T) {
	f := newConsoleFake(t)
	var got string
	if err := f.client().AsConsole(context.Background(), func(_ context.Context, tok string) error { got = tok; return nil }); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"actor_user_id": consoleID, "human_name": "KyMessages console",
		"scope": "urn:matrix:client:api:* urn:synapse:admin:*", "expires_in": float64(300)}
	if got != "mpt_secret1" || len(f.minted) != 1 || !reflect.DeepEqual(f.minted[0], want) {
		t.Fatalf("token %q minted %v", got, f.minted)
	}
	if !reflect.DeepEqual(f.revoked, []string{"PS1"}) {
		t.Errorf("revoked %v", f.revoked)
	}
}

func TestAsConsoleRevokesWhenTheCallFailsOrIsCancelled(t *testing.T) {
	f := newConsoleFake(t)
	c := f.client()
	boom := errors.New("synapse said no")
	if err := c.AsConsole(context.Background(), func(context.Context, string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error not returned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := c.AsConsole(ctx, func(ctx context.Context, _ string) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if !reflect.DeepEqual(f.revoked, []string{"PS1", "PS2"}) {
		t.Fatalf("revoked %v", f.revoked)
	}
}

// A refused revoke does not turn a done action into a failure; the session expires on its
// own. 409 means it was already revoked. Neither the token nor the secret reaches the log.
func TestAsConsoleRevokeFailureIsLoggedNotReturned(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, status := range []int{http.StatusConflict, http.StatusInternalServerError} {
		f := newConsoleFake(t)
		f.revokeStatus = status
		if err := f.client().AsConsole(context.Background(), func(context.Context, string) error { return nil }); err != nil {
			t.Errorf("revoke %d: %v", status, err)
		}
	}
	out := buf.String()
	if strings.Count(out, "not revoked") != 1 || strings.Contains(out, "mpt_secret") || strings.Contains(out, "secret-value") {
		t.Fatalf("log %q", out)
	}
}

func TestPlanExemptsOnlyTheUnlinkedConsoleAccount(t *testing.T) {
	got := Plan([]User{
		{ID: "c", Username: ConsoleUsername},                       // exempt
		{ID: "x1", Username: ConsoleUsername + "2"},                // lock: not the exact name
		{ID: "x2", Username: "kymessages"},                         // lock
		{ID: "x3", Username: ConsoleUsername, Subject: "i"},        // lock: linked, so a person
		{ID: "x4", Username: ConsoleUsername, Ambiguous: true},     // lock: linked twice
	}, map[string]string{"i": "inactive"})
	var locked []string
	for _, a := range got {
		if a.Kind != Lock {
			t.Fatalf("unexpected %+v", a)
		}
		locked = append(locked, a.User.ID)
	}
	if strings.Join(locked, ",") != "x1,x2,x3,x4" {
		t.Fatalf("locked %v", locked)
	}
}
```

- [ ] **Step 2: Run, expect failure.** Run `go test ./internal/matrixsync/ -run 'Console|PlanExempts'`. Expected: compile errors (`EnsureConsoleUser`, `AsConsole`, `ConsoleUsername` undefined).

- [ ] **Step 3: Implement in `mas.go`.** Add `"log"` to the imports. Change `userAttrs` to:

```go
type userAttrs struct {
	Username      string  `json:"username"`
	Admin         bool    `json:"admin"`
	LockedAt      *string `json:"locked_at"`
	DeactivatedAt *string `json:"deactivated_at"`
}
```

Append after `Version`:

```go
// ConsoleUsername is the MAS account the console acts as on Synapse's admin API. It has no
// password and no upstream link; the sweep leaves it alone (Plan).
const ConsoleUsername = "kymessages-console"

const (
	// consoleScope: Synapse requires the client API scope of every token; the admin scope is
	// what makes it an admin. No device scope: Synapse does not need one.
	consoleScope      = "urn:matrix:client:api:* urn:synapse:admin:*"
	consoleSessionTTL = 5 * time.Minute
)

// EnsureConsoleUser returns the console account's MAS ID, creating it and granting MAS admin
// when needed. It refuses an account that is locked, deactivated or linked to an upstream
// identity: a linked one belongs to a person.
func (c *Client) EnsureConsoleUser(ctx context.Context) (string, error) {
	path := adminPrefix + "users/by-username/" + ConsoleUsername
	r, err := c.one(ctx, path)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		var doc struct {
			Data resource `json:"data"`
		}
		err = c.call(ctx, http.MethodPost, adminPrefix+"users", []byte(`{"username":"`+ConsoleUsername+`"}`), &doc)
		r = doc.Data
		if errors.As(err, &se) && se.Status == http.StatusConflict {
			r, err = c.one(ctx, path) // created meanwhile
		}
	}
	if err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	var a userAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	switch {
	case a.Username != ConsoleUsername:
		return "", errors.New("console account: MAS answered with another user")
	case a.DeactivatedAt != nil:
		return "", errors.New("console account is deactivated in MAS")
	case a.LockedAt != nil:
		return "", errors.New("console account is locked in MAS")
	}
	var links struct {
		Data []resource `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, adminPrefix+"upstream-oauth-links?filter[user]="+url.QueryEscape(r.ID)+"&page[first]=1", nil, &links); err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	if len(links.Data) > 0 {
		return "", errors.New("console account is linked to an upstream identity; refusing to act as a person")
	}
	if !a.Admin {
		if err := c.post(ctx, r.ID, "set-admin", []byte(`{"admin":true}`)); err != nil {
			return "", fmt.Errorf("console account: %w", err)
		}
	}
	return r.ID, nil
}

// AsConsole runs fn with a fresh 5-minute console session and revokes the session afterwards,
// also when fn fails or ctx ends. A failed revoke is logged; the session then expires itself.
func (c *Client) AsConsole(ctx context.Context, fn func(ctx context.Context, token string) error) error {
	userID, err := c.EnsureConsoleUser(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"actor_user_id": userID, "human_name": "KyMessages console",
		"scope": consoleScope, "expires_in": int(consoleSessionTTL / time.Second)})
	if err != nil {
		return err
	}
	var doc struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				AccessToken string `json:"access_token"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodPost, adminPrefix+"personal-sessions", body, &doc); err != nil {
		return fmt.Errorf("console session: %w", err)
	}
	if doc.Data.ID == "" || doc.Data.Attributes.AccessToken == "" {
		return errors.New("console session: MAS returned no token")
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := c.call(rctx, http.MethodPost, adminPrefix+"personal-sessions/"+url.PathEscape(doc.Data.ID)+"/revoke", nil, nil)
		var se *StatusError
		if err != nil && !(errors.As(err, &se) && se.Status == http.StatusConflict) {
			log.Printf("[MATRIX] console session %s not revoked; it expires within %s: %v", doc.Data.ID, consoleSessionTTL, err)
		}
	}()
	return fn(ctx, doc.Data.Attributes.AccessToken)
}
```

- [ ] **Step 4: The sweep exemption.** In `matrixsync.go`'s `Plan`, directly after the `if u.Deactivated { continue }` block, add:

```go
		if u.Username == ConsoleUsername && u.Subject == "" && !u.Ambiguous {
			continue // the console's service account; linked, it is a person and judged as one
		}
```

- [ ] **Step 5: Run.** Run `go test -race ./internal/matrixsync/ -v`. Expected: PASS, including `TestPlan`, `TestClientNeverLogsSecret` and every 5a test.

- [ ] **Step 6: Failing synapseadmin tests.** Create `internal/synapseadmin/synapseadmin_test.go`:

```go
package synapseadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeSynapse refuses any token but "tok" as Synapse does an inactive one, then runs h.
func fakeSynapse(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Token is not active"}`)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestRoomsListsByNameWithSearch(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/_synapse/admin/v1/rooms" || q.Get("from") != "50" || q.Get("limit") != "25" ||
			q.Get("order_by") != "name" || q.Get("search_term") != "team chat" {
			http.Error(w, "bad request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"rooms":[
			{"room_id":"!a:example.com","name":"Team chat","canonical_alias":"#team:example.com","joined_members":3,"joined_local_members":3,"version":"10","creator":"@alice:example.com","encryption":"m.megolm.v1.aes-sha2","federatable":true,"public":false,"join_rules":"invite","guest_access":null,"history_visibility":"shared","state_events":17,"room_type":null},
			{"room_id":"!b:example.com","name":null,"canonical_alias":null,"joined_members":1,"joined_local_members":1,"creator":"@bob:example.com","encryption":null,"public":true,"join_rules":"public","state_events":5}
		],"offset":50,"total_rooms":77}`)
	})
	got, err := c.Rooms(context.Background(), "tok", RoomQuery{Offset: 50, Limit: 25, Search: "team chat"})
	want := RoomPage{Total: 77, Rooms: []Room{
		{ID: "!a:example.com", Name: "Team chat", Alias: "#team:example.com", Creator: "@alice:example.com", Members: 3, LocalMembers: 3,
			Encryption: "m.megolm.v1.aes-sha2", JoinRule: "invite", StateEvents: 17},
		{ID: "!b:example.com", Creator: "@bob:example.com", Members: 1, LocalMembers: 1, Public: true, JoinRule: "public", StateEvents: 5},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// Synapse answers 400 to an empty search_term, so no search sends none.
func TestRoomsOmitsAnEmptySearch(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("search_term") {
			http.Error(w, `{"errcode":"M_INVALID_PARAM"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"rooms":[],"offset":0,"total_rooms":0}`)
	})
	if p, err := c.Rooms(context.Background(), "tok", RoomQuery{Limit: 1}); err != nil || p.Total != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestErrorsNameTheCauseNeverTheToken(t *testing.T) {
	c := fakeSynapse(t, func(http.ResponseWriter, *http.Request) {})
	_, err := c.Rooms(context.Background(), "mpt_secret_value", RoomQuery{Limit: 1})
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized || se.Errcode != "M_UNKNOWN_TOKEN" || se.Message != "Token is not active" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "mpt_secret_value") || !strings.Contains(err.Error(), "Token is not active") {
		t.Fatalf("error text %q", err.Error())
	}
}

func TestRefusesRedirectsAndForeignPaths(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/steal", http.StatusFound)
	})
	_, err := c.Rooms(context.Background(), "tok", RoomQuery{Limit: 1})
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Fatalf("redirect followed or misreported: %v", err)
	}
	if err := c.do(context.Background(), "tok", http.MethodGet, "/_matrix/client/v3/account/whoami", nil, nil); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("foreign path: %v", err)
	}
}
```

- [ ] **Step 7: Implement synapseadmin.** Create `internal/synapseadmin/synapseadmin.go`:

```go
// Package synapseadmin calls Synapse's admin API on its internal listener. Every call carries
// a console session token minted for one action (matrixsync.Client.AsConsole); no error here
// carries the token.
package synapseadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const prefix = "/_synapse/admin/"

// Error is a non-2xx answer with Synapse's errcode and message (clipped).
type Error struct {
	Method, Path     string
	Status           int
	Errcode, Message string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("Synapse %s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Errcode != "" {
		s += " " + e.Errcode
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Client is safe for concurrent use.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for Synapse's internal origin. It ignores proxy variables and follows
// no redirects, so the token goes to base or nowhere.
func New(base string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Client{base: strings.TrimSuffix(base, "/"), hc: &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// do sends one admin request; path must start with prefix.
func (c *Client) do(ctx context.Context, token, method, path string, body, out any) error {
	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("refusing admin path %q", path)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("Synapse %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e := &Error{Method: method, Path: path, Status: resp.StatusCode}
		var b struct {
			Errcode string `json:"errcode"`
			Error   string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b) == nil {
			e.Errcode, e.Message = clip(b.Errcode, 64), clip(b.Error, 200)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// Room is one room as Synapse's admin API reports it. Synapse sends null for an unset name,
// alias, encryption or join rule; those read as "".
type Room struct {
	ID           string `json:"room_id"`
	Name         string `json:"name"`
	Alias        string `json:"canonical_alias"`
	Creator      string `json:"creator"`
	Members      int    `json:"joined_members"`
	LocalMembers int    `json:"joined_local_members"`
	Encryption   string `json:"encryption"` // the algorithm; "" when not encrypted
	Public       bool   `json:"public"`
	JoinRule     string `json:"join_rules"`
	StateEvents  int    `json:"state_events"` // the only size-like count Synapse reports
}

type RoomQuery struct {
	Offset, Limit int
	Search        string // name, alias substring or exact room ID; "" lists all
}

type RoomPage struct {
	Rooms []Room
	Total int
}

// Rooms lists rooms ordered by name.
func (c *Client) Rooms(ctx context.Context, token string, q RoomQuery) (RoomPage, error) {
	v := url.Values{"from": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}, "order_by": {"name"}}
	if q.Search != "" {
		v.Set("search_term", q.Search)
	}
	var page struct {
		Rooms []Room `json:"rooms"`
		Total int    `json:"total_rooms"`
	}
	if err := c.do(ctx, token, http.MethodGet, prefix+"v1/rooms?"+v.Encode(), nil, &page); err != nil {
		return RoomPage{}, err
	}
	return RoomPage{Rooms: page.Rooms, Total: page.Total}, nil
}
```

Run `go test -race ./internal/synapseadmin/ -v`. Expected: PASS.

- [ ] **Step 8: Failing API tests.** In `internal/api/console_test.go`:
  - Add three fields to `fakeMAS`: `consoleErr error`, `consoleHang chan struct{}` and `consoleRuns int`.
  - Add this method:

```go
// AsConsole stands in for the console session: fn gets a fixed token unless consoleErr is set.
// consoleHang blocks it, ignoring ctx, as a stuck MAS would.
func (f *fakeMAS) AsConsole(ctx context.Context, fn func(context.Context, string) error) error {
	f.mu.Lock()
	f.consoleRuns++
	err, hang := f.consoleErr, f.consoleHang
	f.mu.Unlock()
	if hang != nil {
		<-hang
	}
	if err != nil {
		return err
	}
	return fn(ctx, "console-token")
}
```

  - In `setupMatrixServer`, directly after `srv.SetMatrixAdmin(f)`, add `api.SetRoomAdminForTest(srv, newFakeRooms())`.
  - In `TestHealthWithMatrixReportsEveryComponent`, change the expected names to `"kymessages,database,synapse,mas,element,postgres,synapse-admin"`, and add:

```go
	if c := by["synapse-admin"]; c.Status != "up" || c.Version != "" || c.Pinned != "" || c.Error != "" {
		t.Errorf("synapse-admin %+v", c)
	}
```

Create `internal/api/rooms_test.go`:

```go
package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// fakeRooms stands in for Synapse's admin API. Task 3 grows it.
type fakeRooms struct {
	mu     sync.Mutex
	rooms  []synapseadmin.Room
	err    error
	tokens []string
}

func newFakeRooms() *fakeRooms { return &fakeRooms{} }

func (f *fakeRooms) Rooms(_ context.Context, tok string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tok)
	if f.err != nil {
		return synapseadmin.RoomPage{}, f.err
	}
	n := len(f.rooms)
	return synapseadmin.RoomPage{Rooms: f.rooms[min(q.Offset, n):min(q.Offset+q.Limit, n)], Total: n}, nil
}

// setupRoomsServer is setupMatrixServer with its Synapse admin fake in hand.
func setupRoomsServer(t *testing.T) (*api.Server, store.Store, *fakeMAS, *fakeRooms) {
	t.Helper()
	srv, st, _, f := setupMatrixServer(t)
	rooms := newFakeRooms()
	api.SetRoomAdminForTest(srv, rooms)
	return srv, st, f, rooms
}

// synapseAdminHealth is the synapse-admin component of one Health request.
func synapseAdminHealth(t *testing.T, srv *api.Server, admin *http.Cookie) health.Component {
	t.Helper()
	h := decode[componentsBody](t, adminDo(t, srv, admin, "GET", "/api/admin/health", nil))
	for _, c := range h.Components {
		if c.Name == "synapse-admin" {
			return c
		}
	}
	t.Fatalf("no synapse-admin in %+v", h.Components)
	return health.Component{}
}

func TestHealthProvesSynapseAdminAccess(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	get := func() health.Component { return synapseAdminHealth(t, srv, admin) }
	if c := get(); c.Status != "up" || c.Error != "" || len(rooms.tokens) != 1 || rooms.tokens[0] != "console-token" || f.consoleRuns != 1 {
		t.Fatalf("working: %+v tokens %v runs %d", c, rooms.tokens, f.consoleRuns)
	}
	rooms.err = &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms", Status: 401, Errcode: "M_UNKNOWN_TOKEN", Message: "Token is not active"}
	if c := get(); c.Status != "down" || !strings.Contains(c.Error, "Token is not active") {
		t.Errorf("token refused: %+v", c)
	}
	f.consoleErr = errors.New("console account is locked in MAS")
	if c := get(); c.Status != "down" || c.Error != "console account is locked in MAS" {
		t.Errorf("locked: %+v", c)
	}
}

// A MAS that never answers (and ignores the context) cannot hold Health past its deadline.
func TestHealthSynapseAdminProbeCannotHang(t *testing.T) {
	srv, st, f, _ := setupRoomsServer(t)
	f.consoleHang = make(chan struct{})
	t.Cleanup(func() { close(f.consoleHang) })
	admin := loginAs(t, srv, st, "root", "admin")
	start := time.Now()
	c := synapseAdminHealth(t, srv, admin)
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("health took %s", d)
	}
	if c.Status != "down" || !strings.Contains(c.Error, "timed out") {
		t.Fatalf("%+v", c)
	}
}
```

Add to `internal/api/export_test.go`:

```go
// SetRoomAdminForTest replaces the Synapse admin client. Test-only.
func SetRoomAdminForTest(s *Server, r RoomAdmin) { s.rooms = r }
```

Run `go test ./internal/api/ -run 'Health'`. Expected: compile errors (`RoomAdmin`, the field `rooms` undefined).

- [ ] **Step 9: Wire the API.** In `server.go`:
  - Import `github.com/Busnes-app/ky_server_base/internal/synapseadmin`.
  - Add to the end of the `MatrixAdmin` interface:

```go
	// AsConsole runs fn with a 5-minute console session token for Synapse's admin API.
	AsConsole(ctx context.Context, fn func(ctx context.Context, token string) error) error
```

  - Below the `MatrixAdmin` interface, add:

```go
// RoomAdmin is the Synapse admin surface the console uses; *synapseadmin.Client implements it.
type RoomAdmin interface {
	Rooms(ctx context.Context, token string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error)
}
```

  - Add the field `rooms RoomAdmin` after `mas` in the `Server` struct.
  - In `NewServer`'s literal, add `rooms: synapseadmin.New(health.ComposeTargets.Synapse),`.

In `health_handlers.go`, append to the Matrix probes, after `postgres`:

```go
			health.Probe{Name: "synapse-admin", Run: s.synapseAdminProbe},
```

and add:

```go
// synapseAdminProbe proves the console can use Synapse's admin API: a console session, one
// admin read, revoked. It has no version.
func (s *Server) synapseAdminProbe(ctx context.Context) (string, error) {
	return "", s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		_, err := s.rooms.Rooms(ctx, tok, synapseadmin.RoomQuery{Limit: 1})
		return err
	})
}
```

with the `synapseadmin` import.

- [ ] **Step 10: Run.** Run `go test -race ./internal/api/ ./internal/matrixsync/ ./internal/synapseadmin/ ./cmd/server/`. Expected: PASS. `cmd/server` needs no change: `*matrixsync.Client` now satisfies the wider `MatrixAdmin`, and the default `rooms` client is built in `NewServer`. Then run `go vet ./...`.

- [ ] **Step 11: DOX for the new package.** Create `internal/synapseadmin/AGENTS.md`:

```markdown
# Synapse admin client

## Purpose
Calls Synapse's admin API for the operator console with a short-lived console session token.

## Ownership
Owns `Client`, `Error` and the room types. `internal/api` builds requests from it through
`api.RoomAdmin`; `matrixsync.Client.AsConsole` supplies the token.

## Local Contracts
- Only paths under `/_synapse/admin/`; anything else is refused before a request is made.
- The transport ignores proxy variables and follows no redirects: the token goes to the
  configured origin (`health.ComposeTargets.Synapse`, `http://synapse:8008`) or nowhere.
- Errors carry method, path, status and Synapse's errcode and message (clipped to 200 bytes),
  never the token.
- `Rooms` orders by name and omits an empty search (Synapse refuses an empty `search_term`).
  `StateEvents` is the only size-like count Synapse reports; there is no byte size.

## Verification
- `go test -race ./internal/synapseadmin/`
```

In the root `AGENTS.md` Child DOX Index, after the `internal/matrixsync` line, add:

```markdown
- [internal/synapseadmin/AGENTS.md](internal/synapseadmin/AGENTS.md): Synapse admin API client for the console, used with 5-minute console sessions.
```

- [ ] **Step 12: The live proof in the acceptance harness.** In `scripts/matrix-acceptance.sh`, `step console`:
  - Replace the expected component list with `kymessages,database,synapse,mas,element,postgres,synapse-admin`.
  - Directly after the `"every component up"` line, insert:

```bash
# Synapse admin access, proven by the probe above: the console account exists, is MAS admin,
# unlocked and unlinked, and every session it used carried exactly the two scopes, expired
# within 5 minutes and was revoked.
expect "$(mas_user kymessages-console 'can_request_admin AND locked_at IS NULL AND deactivated_at IS NULL')" t "the console account exists, admin and unlocked"
expect "$(sql mas "SELECT count(*) FROM upstream_oauth_links l JOIN users u USING (user_id) WHERE u.username = 'kymessages-console'")" 0 "the console account has no KyIdentity link"
expect "$(sql mas "SELECT string_agg(DISTINCT array_to_string(s.scope_list, ' '), '|') FROM personal_sessions s JOIN users u ON u.user_id = s.actor_user_id WHERE u.username = 'kymessages-console'")" \
	'urn:matrix:client:api:* urn:synapse:admin:*' "console sessions carry exactly the client API and Synapse admin scopes"
expect "$(sql mas "SELECT count(*) FROM personal_sessions WHERE revoked_at IS NULL")" 0 "every console session was revoked"
expect "$(sql mas "SELECT bool_and(expires_at <= created_at + interval '5 minutes') FROM personal_access_tokens")" t "console tokens expire within 5 minutes"
```

  - Add one line to the header comment's console sentence: "Health proves Synapse admin access through the console's service account."

- [ ] **Step 13: Run the live proof.** Run `make matrix-acceptance`. Expected: every step PASSes, and `console` prints the five new `ok:` lines.
  - **If `synapse-admin` is down, stop and report BLOCKED with:**
    - its `error` from `$state/health.json` (the artifacts keep `compose.log`);
    - whether MAS created the account (`SELECT username, can_request_admin, locked_at FROM users`);
    - Synapse's log line for the request.
  - If MAS stores more scopes than the two (for example the unstable alias), stop and report. Do not loosen the check.
  - Then run `shellcheck scripts/*.sh`. Expected: clean.

- [ ] **Step 14: Commit.** Message: `console: Synapse admin access through a 5-minute MAS console session, proven live in Health`.

---

### Task 2: Synapse admin client — rooms, members, close, delete, jobs, media

**Files:**
- Modify: `internal/synapseadmin/synapseadmin.go` (append)
- Create: `internal/synapseadmin/rooms_test.go`
- Modify: `internal/synapseadmin/AGENTS.md`

**Interfaces:**
- Consumes: `Client.do`, `Error`, `Room` (Task 1).
- Produces, on `*Client`:
  - `Room(ctx, token, id string) (Room, error)`
  - `Members(ctx, token, id string) ([]string, error)`
  - `Blocked(ctx, token, id string) (bool, error)`
  - `Close(ctx, token, id string) (deleteID string, err error)`
  - `Delete(ctx, token, id string) (deleteID string, err error)`
  - `DeleteJobs(ctx, token, id string) ([]DeleteJob, error)`
  - `RoomMedia(ctx, token, id string) ([]Media, error)`
  - `DeleteMedia(ctx, token string, m Media) error`
- Produces the types `type DeleteJob struct{ ID, Status string }` and `type Media struct{ Server, ID string }`.

- [ ] **Step 1: Failing tests.** Create `internal/synapseadmin/rooms_test.go`:

```go
package synapseadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const grp = "!grp:example.com"

// synapseFake knows one room, !grp:example.com, and records changes.
type synapseFake struct {
	mu      sync.Mutex
	bodies  []string
	deleted []string
}

func newSynapseFake(t *testing.T) (*synapseFake, *Client) {
	t.Helper()
	f := &synapseFake{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, New(srv.URL)
}

func (f *synapseFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Token is not active"}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	notFound := func(msg string) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"errcode":"M_NOT_FOUND","error":%q}`, msg)
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp:
		fmt.Fprint(w, `{"room_id":"!grp:example.com","name":"Team chat","canonical_alias":null,"creator":"@alice:example.com","joined_members":2,"joined_local_members":2,"encryption":"m.megolm.v1.aes-sha2","public":false,"join_rules":"invite","state_events":12,"topic":"plans","avatar":null}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp+"/members":
		fmt.Fprint(w, `{"members":["@alice:example.com","@bob:example.com"],"total":2}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp+"/block":
		fmt.Fprint(w, `{"block":true,"user_id":"@kymessages-console:example.com"}`)
	case r.Method == http.MethodDelete && p == "/_synapse/admin/v2/rooms/"+grp:
		f.bodies = append(f.bodies, string(body))
		fmt.Fprintf(w, `{"delete_id":"D%d"}`, len(f.bodies))
	case r.Method == http.MethodGet && p == "/_synapse/admin/v2/rooms/"+grp+"/delete_status":
		fmt.Fprint(w, `{"results":[
			{"delete_id":"D1","room_id":"!grp:example.com","status":"complete","shutdown_room":{"kicked_users":["@bob:example.com"],"failed_to_kick_users":[],"local_aliases":[],"new_room_id":null}},
			{"delete_id":"D2","room_id":"!grp:example.com","status":"active","shutdown_room":null}]}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/room/"+grp+"/media":
		fmt.Fprint(w, `{"local":["mxc://example.com/AVATAR1","mxc://example.com/Pic_2-b"],"remote":["mxc://elsewhere.org/X"]}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/room/!odd:example.com/media":
		fmt.Fprint(w, `{"local":["https://evil.example/x"],"remote":[]}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/_synapse/admin/v1/media/example.com/"):
		id := strings.TrimPrefix(p, "/_synapse/admin/v1/media/example.com/")
		if id == "GONE" {
			notFound("Unknown media")
			return
		}
		f.deleted = append(f.deleted, id)
		fmt.Fprintf(w, `{"deleted_media":[%q],"total":1}`, id)
	case strings.Contains(p, "/delete_status"):
		notFound("No delete task for room_id found")
	default:
		notFound("Room not found")
	}
}

func TestRoomDetailMembersAndBlock(t *testing.T) {
	_, c := newSynapseFake(t)
	ctx := context.Background()
	r, err := c.Room(ctx, "tok", grp)
	want := Room{ID: grp, Name: "Team chat", Creator: "@alice:example.com", Members: 2, LocalMembers: 2,
		Encryption: "m.megolm.v1.aes-sha2", JoinRule: "invite", StateEvents: 12}
	if err != nil || r != want {
		t.Fatalf("room %+v %v", r, err)
	}
	m, err := c.Members(ctx, "tok", grp)
	if err != nil || !reflect.DeepEqual(m, []string{"@alice:example.com", "@bob:example.com"}) {
		t.Fatalf("members %v %v", m, err)
	}
	if b, err := c.Blocked(ctx, "tok", grp); err != nil || !b {
		t.Fatalf("blocked %v %v", b, err)
	}
}

func TestUnknownRoomIs404(t *testing.T) {
	_, c := newSynapseFake(t)
	for _, call := range []func() error{
		func() error { _, err := c.Room(context.Background(), "tok", "!nope:example.com"); return err },
		func() error { _, err := c.Members(context.Background(), "tok", "!nope:example.com"); return err },
	} {
		var se *Error
		if err := call(); !errors.As(err, &se) || se.Status != http.StatusNotFound || se.Errcode != "M_NOT_FOUND" {
			t.Errorf("%v", err)
		}
	}
}

// Synapse's purge defaults to true: Close must say false, every time.
func TestCloseAndDeleteAlwaysSendBlockAndPurge(t *testing.T) {
	f, c := newSynapseFake(t)
	ctx := context.Background()
	if id, err := c.Close(ctx, "tok", grp); err != nil || id != "D1" {
		t.Fatalf("close %q %v", id, err)
	}
	if id, err := c.Delete(ctx, "tok", grp); err != nil || id != "D2" {
		t.Fatalf("delete %q %v", id, err)
	}
	if want := []string{`{"block":true,"purge":false}`, `{"block":true,"purge":true}`}; !reflect.DeepEqual(f.bodies, want) {
		t.Fatalf("bodies %v", f.bodies)
	}
}

func TestDeleteJobs(t *testing.T) {
	_, c := newSynapseFake(t)
	jobs, err := c.DeleteJobs(context.Background(), "tok", grp)
	if err != nil || !reflect.DeepEqual(jobs, []DeleteJob{{ID: "D1", Status: "complete"}, {ID: "D2", Status: "active"}}) {
		t.Fatalf("%+v %v", jobs, err)
	}
	none, err := c.DeleteJobs(context.Background(), "tok", "!gone:example.com")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no task must be an empty list: %#v %v", none, err)
	}
}

func TestRoomMediaListsLocalOnlyAndDeleteIsIdempotent(t *testing.T) {
	f, c := newSynapseFake(t)
	ctx := context.Background()
	media, err := c.RoomMedia(ctx, "tok", grp)
	if err != nil || !reflect.DeepEqual(media, []Media{{"example.com", "AVATAR1"}, {"example.com", "Pic_2-b"}}) {
		t.Fatalf("%+v %v", media, err)
	}
	for _, m := range []Media{{"example.com", "AVATAR1"}, {"example.com", "GONE"}} {
		if err := c.DeleteMedia(ctx, "tok", m); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
	}
	if !reflect.DeepEqual(f.deleted, []string{"AVATAR1"}) {
		t.Errorf("deleted %v", f.deleted)
	}
	if _, err := c.RoomMedia(ctx, "tok", "!odd:example.com"); err == nil {
		t.Error("a non-mxc media URI was accepted")
	}
}
```

- [ ] **Step 2: Run, expect failure.** Run `go test ./internal/synapseadmin/`. Expected: compile errors (`Room` method, `DeleteJob` and others undefined).

- [ ] **Step 3: Implement.** Add `"errors"` and `"regexp"` to the imports, then append to `synapseadmin.go`:

```go
func roomPath(version, id string) string { return prefix + version + "/rooms/" + url.PathEscape(id) }

// Room reads one room; *Error with Status 404 when Synapse does not know it.
func (c *Client) Room(ctx context.Context, token, id string) (Room, error) {
	var r Room
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id), nil, &r)
	return r, err
}

// Members are the room's joined members (user IDs).
func (c *Client) Members(ctx context.Context, token, id string) ([]string, error) {
	var m struct {
		Members []string `json:"members"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id)+"/members", nil, &m)
	return m.Members, err
}

// Blocked reports whether joining the room is refused.
func (c *Client) Blocked(ctx context.Context, token, id string) (bool, error) {
	var b struct {
		Block bool `json:"block"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id)+"/block", nil, &b)
	return b.Block, err
}

// Close starts Synapse's shutdown without a purge: the room is blocked, every local member
// leaves, history stays. It returns the background job's delete_id.
func (c *Client) Close(ctx context.Context, token, id string) (string, error) {
	return c.shutdown(ctx, token, id, false)
}

// Delete starts a shutdown that also purges the room's events and state; the room stays
// blocked. Media is not purged (see RoomMedia).
func (c *Client) Delete(ctx context.Context, token, id string) (string, error) {
	return c.shutdown(ctx, token, id, true)
}

func (c *Client) shutdown(ctx context.Context, token, id string, purge bool) (string, error) {
	var r struct {
		DeleteID string `json:"delete_id"`
	}
	// Synapse's purge defaults to true: always send it.
	if err := c.do(ctx, token, http.MethodDelete, roomPath("v2", id), map[string]bool{"block": true, "purge": purge}, &r); err != nil {
		return "", err
	}
	if r.DeleteID == "" {
		return "", errors.New("Synapse started no delete job")
	}
	return r.DeleteID, nil
}

// DeleteJob is one close or delete job. Synapse gives no reason when one fails.
type DeleteJob struct{ ID, Status string }

// DeleteJobs lists the room's jobs that have started (active, complete, failed); Synapse does
// not list one still waiting to start. None is an empty list.
func (c *Client) DeleteJobs(ctx context.Context, token, id string) ([]DeleteJob, error) {
	var r struct {
		Results []struct {
			DeleteID string `json:"delete_id"`
			Status   string `json:"status"`
		} `json:"results"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v2", id)+"/delete_status", nil, &r)
	var se *Error
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return []DeleteJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]DeleteJob, 0, len(r.Results))
	for _, j := range r.Results {
		out = append(out, DeleteJob{ID: j.DeleteID, Status: j.Status})
	}
	return out, nil
}

// Media is one piece of local media.
type Media struct{ Server, ID string }

var mxcURI = regexp.MustCompile(`^mxc://([A-Za-z0-9.:-]+)/([A-Za-z0-9_-]+)$`)

// RoomMedia is the local media Synapse can attribute to the room: what non-encrypted events
// reference by URL. Attachments in encrypted rooms are referenced only inside ciphertext and
// are never listed.
func (c *Client) RoomMedia(ctx context.Context, token, id string) ([]Media, error) {
	var r struct {
		Local []string `json:"local"`
	}
	if err := c.do(ctx, token, http.MethodGet, prefix+"v1/room/"+url.PathEscape(id)+"/media", nil, &r); err != nil {
		return nil, err
	}
	out := make([]Media, 0, len(r.Local))
	for _, u := range r.Local {
		m := mxcURI.FindStringSubmatch(u)
		if m == nil {
			return nil, fmt.Errorf("Synapse listed unusable media %q", clip(u, 100))
		}
		out = append(out, Media{Server: m[1], ID: m[2]})
	}
	return out, nil
}

// DeleteMedia removes one piece of local media; already gone counts as done.
func (c *Client) DeleteMedia(ctx context.Context, token string, m Media) error {
	err := c.do(ctx, token, http.MethodDelete, prefix+"v1/media/"+url.PathEscape(m.Server)+"/"+url.PathEscape(m.ID), nil, nil)
	var se *Error
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Run.** Run `go test -race ./internal/synapseadmin/ -v`. Expected: PASS.

- [ ] **Step 5: DOX.** Append to `internal/synapseadmin/AGENTS.md` Local Contracts:

```markdown
- `Close` and `Delete` are `DELETE /_synapse/admin/v2/rooms/{id}` with `block` true and
  `purge` sent explicitly (false, true): Synapse's purge defaults to true. Both start a
  background job and return its `delete_id`. Close makes every local member leave; with
  federation off the server is then out of the room and nobody can rejoin, so Close is final.
- `DeleteJobs` lists started jobs only (Synapse omits scheduled ones and gives no failure
  reason); no job is an empty list.
- `RoomMedia` lists only media non-encrypted events reference; `DeleteMedia` treats 404 as
  done. Encrypted rooms' attachments cannot be attributed and are never deleted.
- Messages are never read: no method fetches events.
```

- [ ] **Step 6: Commit.** Message: `synapseadmin: room detail, members, block, close, delete, jobs and media`.

---

### Task 3: API — room routes, audit, delete confirmation

**Files:**
- Create: `internal/api/room_handlers.go`
- Modify: `internal/api/server.go` (the `RoomAdmin` interface; `routes()`, console block at lines 299-304)
- Modify: `internal/api/matrix_handlers.go` (`sessionEndDetails`, lines 46-57)
- Modify: `internal/api/rooms_test.go` (replace `fakeRooms` and `newFakeRooms`; append tests)
- Modify: `internal/api/console_test.go` (`TestMatrixRoutesAre404WithoutMatrix` route list)
- Modify: `internal/api/authz_test.go` (`TestPrivilegedEndpointsRequireAdmin` case list)

**Interfaces:**
- Consumes:
  - `MatrixAdmin.AsConsole` (Task 1).
  - Every `synapseadmin` method and type from Tasks 1 and 2.
  - `matrixOn`, `masTimeout`, `pageParams`, `clip200`, `s.audit`, `s.actorID`.
- Produces:
  - `RoomAdmin` with the nine methods below.
  - `func auditFields(outcome string, kv ...string) string`; `sessionEndDetails` now delegates to it.
  - The routes, returning these JSON bodies:
    - **Rooms list:** `{"rooms":[roomView], "total", "offset", "limit"}`
    - **Room detail:** `{"room": roomView, "members": [string], "members_total": n, "confirm_text": string, "jobs": [{"delete_id", "status"}]}`
    - **Delete status:** `{"jobs": [...]}`
    - **Close and delete:** `{"outcome": "started"|"already_closed", "delete_id": string}`
  - `roomView` is `{"id", "name", "alias", "creator", "members", "encrypted", "public", "join_rule", "state_events", "closed"}`. `join_rule` is one of `public|invite|knock|restricted|knock_restricted|private|other|""`. Job `status` is one of `scheduled|active|complete|failed|cancelled|unknown`.

- [ ] **Step 1: Grow the fake.** In `internal/api/rooms_test.go`, add `"fmt"` and `"net/url"` to the imports. Replace the Task 1 `fakeRooms` type, its `Rooms` method and `newFakeRooms` with:

```go
const (
	rGroup = "!grp:example.com"
	rDM    = "!dm:example.com"
)

func roomPath(id string) string { return "/api/admin/matrix/rooms/" + url.PathEscape(id) }

// fakeRooms stands in for Synapse's admin API: an encrypted group room "Team chat" and an
// unnamed DM, both with alice and bob; the group room's avatar is local media.
type fakeRooms struct {
	mu        sync.Mutex
	rooms     map[string]*synapseadmin.Room
	order     []string
	members   map[string][]string
	blocked   map[string]bool
	jobs      map[string][]synapseadmin.DeleteJob
	media     map[string][]synapseadmin.Media
	err       error // every call
	changeErr error // Close and Delete
	mediaErr  error // DeleteMedia
	tokens    []string
	changes   []string // "media <id>", "close <room>", "delete <room>", in order
}

func newFakeRooms() *fakeRooms {
	enc := "m.megolm.v1.aes-sha2"
	both := func() []string { return []string{"@alice:example.com", "@bob:example.com"} }
	return &fakeRooms{
		rooms: map[string]*synapseadmin.Room{
			rGroup: {ID: rGroup, Name: "Team chat", Creator: "@alice:example.com", Members: 2, LocalMembers: 2, Encryption: enc, JoinRule: "invite", StateEvents: 12},
			rDM:    {ID: rDM, Creator: "@alice:example.com", Members: 2, LocalMembers: 2, Encryption: enc, JoinRule: "invite", StateEvents: 9},
		},
		order:   []string{rGroup, rDM},
		members: map[string][]string{rGroup: both(), rDM: both()},
		blocked: map[string]bool{},
		jobs:    map[string][]synapseadmin.DeleteJob{},
		media:   map[string][]synapseadmin.Media{rGroup: {{Server: "example.com", ID: "AVATAR1"}}},
	}
}

// use records the token and returns the injected failure; the caller holds mu.
func (f *fakeRooms) use(tok string) error { f.tokens = append(f.tokens, tok); return f.err }

func roomNotFound(id string) error {
	return &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms/" + id, Status: http.StatusNotFound, Errcode: "M_NOT_FOUND", Message: "Room not found"}
}

func (f *fakeRooms) Rooms(_ context.Context, tok string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return synapseadmin.RoomPage{}, err
	}
	var all []synapseadmin.Room
	for _, id := range f.order {
		r, ok := f.rooms[id]
		if ok && (q.Search == "" || r.ID == q.Search || strings.Contains(strings.ToLower(r.Name), strings.ToLower(q.Search))) {
			all = append(all, *r)
		}
	}
	n := len(all)
	return synapseadmin.RoomPage{Rooms: all[min(q.Offset, n):min(q.Offset+q.Limit, n)], Total: n}, nil
}

func (f *fakeRooms) Room(_ context.Context, tok, id string) (synapseadmin.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return synapseadmin.Room{}, err
	}
	r, ok := f.rooms[id]
	if !ok {
		return synapseadmin.Room{}, roomNotFound(id)
	}
	return *r, nil
}

func (f *fakeRooms) Members(_ context.Context, tok, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return nil, err
	}
	if _, ok := f.rooms[id]; !ok {
		return nil, roomNotFound(id)
	}
	return f.members[id], nil
}

func (f *fakeRooms) Blocked(_ context.Context, tok, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[id], f.use(tok)
}

func (f *fakeRooms) change(tok, kind, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return "", err
	}
	if f.changeErr != nil {
		return "", f.changeErr
	}
	f.changes = append(f.changes, kind+" "+id)
	job := fmt.Sprintf("%s-%d", kind, len(f.changes))
	f.jobs[id] = append(f.jobs[id], synapseadmin.DeleteJob{ID: job, Status: "complete"})
	f.blocked[id] = true
	if kind == "delete" {
		delete(f.rooms, id)
	} else {
		f.members[id] = nil
		f.rooms[id].Members, f.rooms[id].LocalMembers = 0, 0
	}
	return job, nil
}

func (f *fakeRooms) Close(_ context.Context, tok, id string) (string, error)  { return f.change(tok, "close", id) }
func (f *fakeRooms) Delete(_ context.Context, tok, id string) (string, error) { return f.change(tok, "delete", id) }

func (f *fakeRooms) DeleteJobs(_ context.Context, tok, id string) ([]synapseadmin.DeleteJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]synapseadmin.DeleteJob{}, f.jobs[id]...), f.use(tok)
}

func (f *fakeRooms) RoomMedia(_ context.Context, tok, id string) ([]synapseadmin.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.media[id], f.use(tok)
}

func (f *fakeRooms) DeleteMedia(_ context.Context, tok string, m synapseadmin.Media) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.use(tok); err != nil {
		return err
	}
	if f.mediaErr != nil {
		return f.mediaErr
	}
	f.changes = append(f.changes, "media "+m.ID)
	return nil
}
```

- [ ] **Step 2: Failing route tests.** Append to `internal/api/rooms_test.go` (add `"unicode/utf8"` to the imports):

```go
type roomRow struct {
	ID, Name, Alias, Creator  string
	Members                   int
	Encrypted, Public, Closed bool
	JoinRule                  string `json:"join_rule"`
	StateEvents               int    `json:"state_events"`
}

type roomsPage struct {
	Rooms                []roomRow
	Total, Offset, Limit int
}

func TestRoomsListPagedSearchedWithClosedFlag(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	rooms.blocked[rDM] = true
	admin := loginAs(t, srv, st, "root", "admin")
	w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms", nil)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", w.Header())
	}
	all := decode[roomsPage](t, w)
	if all.Total != 2 || all.Limit != 50 || len(all.Rooms) != 2 {
		t.Fatalf("%+v", all)
	}
	g, d := all.Rooms[0], all.Rooms[1]
	if g.ID != rGroup || g.Name != "Team chat" || g.Members != 2 || !g.Encrypted || g.Public || g.JoinRule != "invite" ||
		g.StateEvents != 12 || g.Closed || g.Creator != "@alice:example.com" {
		t.Errorf("group %+v", g)
	}
	if d.ID != rDM || d.Name != "" || !d.Closed {
		t.Errorf("dm %+v", d)
	}
	if p := decode[roomsPage](t, adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms?search=team&limit=1", nil)); p.Total != 1 || p.Rooms[0].ID != rGroup {
		t.Errorf("search %+v", p)
	}
	if body := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms?search=zzz", nil).Body.String(); !strings.Contains(body, `"rooms":[]`) {
		t.Errorf("empty page is not an empty array: %s", body)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?offset=-1", "?search=" + strings.Repeat("x", 256)} {
		if w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/rooms"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	for _, tok := range rooms.tokens {
		if tok != "console-token" {
			t.Fatalf("a Synapse call carried %q", tok)
		}
	}
	if f.consoleRuns == 0 {
		t.Fatal("no console session was used")
	}
}

func TestRoomDetailShowsMembersAndConfirmText(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	type detail struct {
		Room         struct{ ID, Name string }
		Members      []string
		MembersTotal int    `json:"members_total"`
		ConfirmText  string `json:"confirm_text"`
		Jobs         []struct{ DeleteID, Status string }
	}
	get := func(id string) detail { return decode[detail](t, adminDo(t, srv, admin, "GET", roomPath(id), nil)) }
	g := get(rGroup)
	if g.Room.ID != rGroup || strings.Join(g.Members, ",") != "@alice:example.com,@bob:example.com" || g.MembersTotal != 2 ||
		g.ConfirmText != "Team chat" || g.Jobs == nil || len(g.Jobs) != 0 {
		t.Fatalf("group %+v", g)
	}
	if d := get(rDM); d.ConfirmText != rDM {
		t.Errorf("an unnamed room confirms with its ID: %q", d.ConfirmText)
	}
	// Names nobody can type fall back to the room ID; a long name confirms as shown (clipped).
	for name, want := range map[string]string{
		"Evil‮gnp.exe": rGroup,
		"zero​width":   rGroup,
		"tab\tname":         rGroup,
	} {
		rooms.rooms[rGroup].Name = name
		if got := get(rGroup).ConfirmText; got != want {
			t.Errorf("%q: confirm %q, want %q", name, got, want)
		}
	}
	rooms.rooms[rGroup].Name = strings.Repeat("é", 300)
	long := get(rGroup)
	if len(long.Room.Name) > 200 || !utf8.ValidString(long.Room.Name) || long.ConfirmText != long.Room.Name {
		t.Errorf("long name %d bytes, confirm %d bytes", len(long.Room.Name), len(long.ConfirmText))
	}
	if w := adminDo(t, srv, admin, "GET", roomPath("!nope:example.com"), nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown room: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{"nope", "!", "!a/b:example.com", "!" + strings.Repeat("a", 255), "!a:b:c:d"} {
		if w := adminDo(t, srv, admin, "GET", roomPath(id), nil); w.Code != http.StatusBadRequest {
			t.Errorf("id %q: %d", id, w.Code)
		}
	}
}

func TestRoomCloseNeedsFreshAdminIsAuditedAndIdempotent(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	path := roomPath(rGroup) + "/close"
	if w := adminDo(t, srv, staleAdmin(t, st), "POST", path, nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale: %d %s", w.Code, w.Body.String())
	}
	if len(rooms.changes) != 0 || len(rooms.tokens) != 0 {
		t.Fatalf("a stale session reached Synapse: %v", rooms.changes)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, want := range []string{`{"delete_id":"close-1","outcome":"started"}`, `{"delete_id":"","outcome":"already_closed"}`} {
		if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("want %s: %d %s", want, w.Code, w.Body.String())
		}
	}
	if strings.Join(rooms.changes, ",") != "close "+rGroup {
		t.Fatalf("changes %v", rooms.changes)
	}
	rooms.err = &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms/x", Status: 500, Message: "db down"}
	if w := adminDo(t, srv, admin, "POST", path, nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Synapse admin access is not working") {
		t.Fatalf("Synapse failure: %d %s", w.Code, w.Body.String())
	}
	rows := auditRows(t, st, "matrix.room_close") // newest first
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, want := range []string{`outcome="error: Synapse GET`, `outcome="already_closed"`, `outcome="started" delete_id="close-1"`} {
		r := rows[i]
		if r.Resource != rGroup || r.UserID != "usr_root" || r.IPAddress == "" || !strings.HasPrefix(r.Details, want) {
			t.Errorf("row %d: %+v", i, r)
		}
	}
}

// A double click or a second admin: refused while a job runs, by our check or Synapse's.
func TestRoomChangeRefusedWhileAJobRuns(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	rooms.jobs[rGroup] = []synapseadmin.DeleteJob{{ID: "d0", Status: "active"}}
	for _, p := range []string{"/close", "/delete"} {
		w := adminDo(t, srv, admin, "POST", roomPath(rGroup)+p, map[string]string{"confirm": "Team chat"})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "still running") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if len(rooms.changes) != 0 {
		t.Fatalf("changes %v", rooms.changes)
	}
	// Refused before any media is counted, so the row carries the outcome alone.
	if r := auditRows(t, st, "matrix.room_delete")[0]; r.Details != `outcome="refused: a close or delete is running"` {
		t.Errorf("delete row %q", r.Details)
	}
	rooms.jobs[rGroup] = nil
	rooms.changeErr = &synapseadmin.Error{Method: "DELETE", Path: "/_synapse/admin/v2/rooms/x", Status: 400, Errcode: "M_UNKNOWN", Message: "Purge already in progress for " + rGroup}
	if w := adminDo(t, srv, admin, "POST", roomPath(rGroup)+"/close", nil); w.Code != http.StatusConflict {
		t.Errorf("Synapse's own refusal: %d %s", w.Code, w.Body.String())
	}
}

func TestRoomDeleteChecksConfirmationServerSide(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	path := roomPath(rGroup) + "/delete"
	for _, body := range []any{map[string]string{"confirm": "team chat"}, map[string]string{"confirm": ""}, map[string]string{}} {
		if w := adminDo(t, srv, admin, "POST", path, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "exactly") {
			t.Fatalf("%v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(rooms.changes) != 0 {
		t.Fatalf("a refused delete changed something: %v", rooms.changes)
	}
	if w := adminDo(t, srv, admin, "POST", path, "not an object"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: %d", w.Code)
	}
	if n := len(auditRows(t, st, "matrix.room_delete")); n != 3 {
		t.Errorf("%d delete rows; the malformed body must not be audited", n)
	}
	w := adminDo(t, srv, admin, "POST", path, map[string]string{"confirm": "Team chat"})
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"delete_id":"delete-2","outcome":"started"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Join(rooms.changes, ",") != "media AVATAR1,delete "+rGroup {
		t.Fatalf("media first, then the purge: %v", rooms.changes)
	}
	if r := auditRows(t, st, "matrix.room_delete")[0]; r.Details != `outcome="started" delete_id="delete-2" media="1"` || r.Resource != rGroup {
		t.Errorf("row %+v", r)
	}
	if w := adminDo(t, srv, admin, "POST", roomPath(rDM)+"/delete", map[string]string{"confirm": rDM}); w.Code != http.StatusOK {
		t.Errorf("unnamed room by ID: %d %s", w.Code, w.Body.String())
	}
	if w := adminDo(t, srv, admin, "POST", path, map[string]string{"confirm": "Team chat"}); w.Code != http.StatusNotFound {
		t.Errorf("deleting a deleted room: %d", w.Code)
	}
}

func TestRoomDeleteStopsWhenMediaDeletionFails(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	rooms.mediaErr = errors.New("Synapse DELETE /_synapse/admin/v1/media/example.com/AVATAR1: " + strings.Repeat("\U0001F4A5", 120))
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "POST", roomPath(rGroup)+"/delete", map[string]string{"confirm": "Team chat"})
	if w.Code != http.StatusBadGateway || len(rooms.changes) != 0 {
		t.Fatalf("%d %v", w.Code, rooms.changes)
	}
	r := auditRows(t, st, "matrix.room_delete")[0]
	if len(r.Details) > 200 || !strings.HasSuffix(r.Details, `media="0"`) || !strings.HasPrefix(api.DetailOutcomeForTest(r.Details), "error: Synapse DELETE") {
		t.Fatalf("details %q", r.Details)
	}
}

func TestRoomDeleteStatusListsJobs(t *testing.T) {
	srv, st, _, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	rooms.jobs["!purged:example.com"] = []synapseadmin.DeleteJob{{ID: "d1", Status: "active"}, {ID: "d2", Status: "shiny"}}
	body := adminDo(t, srv, admin, "GET", roomPath("!purged:example.com")+"/delete-status", nil).Body.String()
	if strings.TrimSpace(body) != `{"jobs":[{"delete_id":"d1","status":"active"},{"delete_id":"d2","status":"unknown"}]}` {
		t.Errorf("%s", body)
	}
	if body := adminDo(t, srv, admin, "GET", roomPath(rGroup)+"/delete-status", nil).Body.String(); strings.TrimSpace(body) != `{"jobs":[]}` {
		t.Errorf("no jobs: %s", body)
	}
}

func TestBrokenConsoleAccountReachesRooms(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	f.consoleErr = errors.New("console account is locked in MAS")
	admin := loginAs(t, srv, st, "root", "admin")
	for _, rt := range []struct{ method, path string }{{"GET", "/api/admin/matrix/rooms"}, {"GET", roomPath(rGroup)}, {"POST", roomPath(rGroup) + "/close"}} {
		w := adminDo(t, srv, admin, rt.method, rt.path, nil)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Synapse admin access is not working: console account is locked in MAS") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	if len(rooms.tokens) != 0 {
		t.Fatal("Synapse was called without a console session")
	}
	if r := auditRows(t, st, "matrix.room_close"); len(r) != 1 || r[0].Details != `outcome="error: console account is locked in MAS"` {
		t.Errorf("rows %+v", r)
	}
}
```

`adminDo` marshals `"not an object"` as a JSON string: a valid JSON value that is not an object, so decoding it into the request struct fails. That is the malformed case the test wants.

In `console_test.go` `TestMatrixRoutesAre404WithoutMatrix`, add these to the route list:

```go
		{"GET", "/api/admin/matrix/rooms"},
		{"GET", "/api/admin/matrix/rooms/%21grp:example.com"},
		{"GET", "/api/admin/matrix/rooms/%21grp:example.com/delete-status"},
		{"POST", "/api/admin/matrix/rooms/%21grp:example.com/close"},
		{"POST", "/api/admin/matrix/rooms/%21grp:example.com/delete"},
```

Add the same five method/path pairs to `authz_test.go` `TestPrivilegedEndpointsRequireAdmin`. Matrix is off there, so an admin gets 404, which passes.

- [ ] **Step 3: Run, expect failure.** Run `go test ./internal/api/ -run 'Room|MatrixRoutes|Privileged'`. Expected: it compiles, because the Task 1 `RoomAdmin` needs only `Rooms`. The new tests FAIL: the room routes do not exist yet, so they answer 404 `Unknown API endpoint`.

- [ ] **Step 4: Widen `RoomAdmin` and register the routes.** In `server.go`, replace the `RoomAdmin` interface with:

```go
// RoomAdmin is the Synapse admin surface the console uses; *synapseadmin.Client implements it.
type RoomAdmin interface {
	Rooms(ctx context.Context, token string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error)
	Room(ctx context.Context, token, id string) (synapseadmin.Room, error)
	Members(ctx context.Context, token, id string) ([]string, error)
	Blocked(ctx context.Context, token, id string) (bool, error)
	Close(ctx context.Context, token, id string) (string, error)
	Delete(ctx context.Context, token, id string) (string, error)
	DeleteJobs(ctx context.Context, token, id string) ([]synapseadmin.DeleteJob, error)
	RoomMedia(ctx context.Context, token, id string) ([]synapseadmin.Media, error)
	DeleteMedia(ctx context.Context, token string, m synapseadmin.Media) error
}
```

In `routes()`, after the `audit` line of the console block, add:

```go
	// Rooms act through a 5-minute console session on Synapse's admin API. Close and delete
	// need a recent sign-in, run detached and are audited. Close is final: there is no reopen.
	s.mux.HandleFunc("GET /api/admin/matrix/rooms", s.requireAdmin(s.handleRooms))
	s.mux.HandleFunc("GET /api/admin/matrix/rooms/{id}", s.requireAdmin(s.handleRoom))
	s.mux.HandleFunc("GET /api/admin/matrix/rooms/{id}/delete-status", s.requireAdmin(s.handleRoomJobs))
	s.mux.HandleFunc("POST /api/admin/matrix/rooms/{id}/close", s.tracked(s.requireFreshAdmin(s.handleRoomClose)))
	s.mux.HandleFunc("POST /api/admin/matrix/rooms/{id}/delete", s.tracked(s.requireFreshAdmin(s.handleRoomDelete)))
```

- [ ] **Step 5: Generalise the audit details.** In `matrix_handlers.go`, replace `sessionEndDetails` with:

```go
// auditFields formats outcome first, then key/value pairs, all quoted. It shortens the outcome
// by whole runes until the details fit the audit cap, so the cut can never take a closing quote.
func auditFields(outcome string, kv ...string) string {
	r := []rune(outcome)
	for {
		var b strings.Builder
		fmt.Fprintf(&b, "outcome=%q", string(r))
		for i := 0; i+1 < len(kv); i += 2 {
			fmt.Fprintf(&b, " %s=%q", kv[i], kv[i+1])
		}
		if b.Len() <= auditDetailsMax || len(r) == 0 {
			return b.String()
		}
		r = r[:len(r)-1]
	}
}

func sessionEndDetails(outcome, id string, kind matrixsync.SessionKind) string {
	return auditFields(outcome, "session", id, "kind", string(kind))
}
```

- [ ] **Step 6: Handlers.** Create `internal/api/room_handlers.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// A room ID is "!" and an opaque part, plus ":server" before room version 12. Never "/", so a
// path value cannot reach another admin route.
var roomIDPattern = regexp.MustCompile(`^![0-9A-Za-z._~=+-]+(?::[0-9A-Za-z.-]+(?::[0-9]{1,5})?)?$`)

func validRoomID(id string) bool { return len(id) <= 255 && roomIDPattern.MatchString(id) }

var (
	errRoomBusy = errors.New("a close or delete of this room is still running")
	errConfirm  = errors.New("confirmation does not match")
)

const maxMembersShown = 1000

var (
	joinRules   = map[string]bool{"public": true, "invite": true, "knock": true, "restricted": true, "knock_restricted": true, "private": true}
	jobStatuses = map[string]bool{"scheduled": true, "active": true, "complete": true, "failed": true, "cancelled": true}
)

type roomView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Creator     string `json:"creator"`
	Members     int    `json:"members"`
	Encrypted   bool   `json:"encrypted"`
	Public      bool   `json:"public"`
	JoinRule    string `json:"join_rule"`
	StateEvents int    `json:"state_events"`
	Closed      bool   `json:"closed"`
}

func roomViewOf(r synapseadmin.Room, closed bool) roomView {
	rule := r.JoinRule
	if rule != "" && !joinRules[rule] {
		rule = "other"
	}
	return roomView{ID: r.ID, Name: clip200(r.Name), Alias: clip200(r.Alias), Creator: clip200(r.Creator), Members: r.Members,
		Encrypted: r.Encryption != "", Public: r.Public, JoinRule: rule, StateEvents: r.StateEvents, Closed: closed}
}

type jobView struct {
	DeleteID string `json:"delete_id"`
	Status   string `json:"status"`
}

func jobViews(jobs []synapseadmin.DeleteJob) []jobView {
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		st := j.Status
		if !jobStatuses[st] {
			st = "unknown"
		}
		out = append(out, jobView{DeleteID: clip200(j.ID), Status: st})
	}
	return out
}

// confirmText is what the admin types to delete a room: its name as the console shows it, or
// the room ID when the name is empty or holds characters nobody can type (controls, bidi and
// other format characters).
func confirmText(r synapseadmin.Room) string {
	name := clip200(r.Name)
	if name == "" || strings.IndexFunc(name, func(c rune) bool { return !unicode.IsPrint(c) || unicode.Is(unicode.Cf, c) }) >= 0 {
		return r.ID
	}
	return name
}

// roomError reports a failed room call. Anything but a missing room or a running job means
// Synapse admin access is not working; the cause is shown (no error here carries a token).
func (s *Server) roomError(w http.ResponseWriter, err error) {
	var se *synapseadmin.Error
	isSynapse := errors.As(err, &se)
	switch {
	case errors.Is(err, errRoomBusy), isSynapse && se.Status == http.StatusBadRequest && strings.Contains(se.Message, "already in progress"):
		s.writeError(w, http.StatusConflict, "A close or delete of this room is still running")
	case isSynapse && se.Status == http.StatusNotFound:
		s.writeError(w, http.StatusNotFound, "No such room")
	default:
		s.writeError(w, http.StatusBadGateway, "Synapse admin access is not working: "+err.Error())
	}
}

// handleRooms lists rooms by name. Synapse's list has no blocked flag, so each listed room's is
// read too (at most 100, on the internal network).
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	offset, limit, ok := pageParams(r)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if !ok || len(search) > 255 {
		s.writeError(w, http.StatusBadRequest, "Invalid paging or search")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var total int
	views := []roomView{}
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		page, err := s.rooms.Rooms(ctx, tok, synapseadmin.RoomQuery{Offset: offset, Limit: limit, Search: search})
		if err != nil {
			return err
		}
		total = page.Total
		for _, rm := range page.Rooms {
			blocked, err := s.rooms.Blocked(ctx, tok, rm.ID)
			if err != nil {
				return err
			}
			views = append(views, roomViewOf(rm, blocked))
		}
		return nil
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"rooms": views, "total": total, "offset": offset, "limit": limit})
}

// handleRoom shows one room: its facts, members (never messages), the delete confirmation text
// and its close or delete jobs.
func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var body map[string]any
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		members, err := s.rooms.Members(ctx, tok, id)
		if err != nil {
			return err
		}
		blocked, err := s.rooms.Blocked(ctx, tok, id)
		if err != nil {
			return err
		}
		jobs, err := s.rooms.DeleteJobs(ctx, tok, id)
		if err != nil {
			return err
		}
		shown := make([]string, 0, min(len(members), maxMembersShown))
		for _, m := range members[:min(len(members), maxMembersShown)] {
			shown = append(shown, clip200(m))
		}
		body = map[string]any{"room": roomViewOf(room, blocked), "members": shown, "members_total": len(members),
			"confirm_text": confirmText(room), "jobs": jobViews(jobs)}
		return nil
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, body)
}

// handleRoomJobs lists the room's close and delete jobs. It does not need the room to exist:
// a purged room's job stays listed for a week.
func (s *Server) handleRoomJobs(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), masTimeout)
	defer cancel()
	var jobs []synapseadmin.DeleteJob
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		var err error
		jobs, err = s.rooms.DeleteJobs(ctx, tok, id)
		return err
	})
	if err != nil {
		s.roomError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobViews(jobs)})
}

// idle refuses a change while one of the room's jobs is listed as running. Synapse does not
// list a job still waiting to start; a duplicate queued shutdown is harmless.
func (s *Server) idle(ctx context.Context, tok, id string) error {
	jobs, err := s.rooms.DeleteJobs(ctx, tok, id)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Status == "scheduled" || j.Status == "active" {
			return errRoomBusy
		}
	}
	return nil
}

type roomResult struct{ outcome, deleteID, media string }

// roomChange runs one audited room change on a context detached from the request, so a dropped
// connection cannot start a job without its audit row. Every outcome is audited.
func (s *Server) roomChange(w http.ResponseWriter, r *http.Request, action, id string, change func(ctx context.Context, tok string, res *roomResult) error) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), masTimeout)
	defer cancel()
	var res roomResult
	err := s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error { return change(ctx, tok, &res) })
	switch {
	case errors.Is(err, errConfirm):
		res.outcome = "refused: confirmation does not match"
	case errors.Is(err, errRoomBusy):
		res.outcome = "refused: a close or delete is running"
	case err != nil:
		res.outcome = "error: " + err.Error()
	}
	var kv []string
	if res.deleteID != "" {
		kv = append(kv, "delete_id", res.deleteID)
	}
	if res.media != "" {
		kv = append(kv, "media", res.media)
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	s.audit(actx, actor, r, action, id, auditFields(res.outcome, kv...))
	acancel()
	switch {
	case errors.Is(err, errConfirm):
		s.writeError(w, http.StatusBadRequest, "Type the room's name exactly as shown to delete it")
	case err != nil:
		s.roomError(w, err)
	default:
		s.writeJSON(w, http.StatusOK, map[string]string{"outcome": res.outcome, "delete_id": res.deleteID})
	}
}

// handleRoomClose removes every member and blocks the room; history stays. Closing a closed,
// empty room succeeds as already_closed.
func (s *Server) handleRoomClose(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	if !validRoomID(id) {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID")
		return
	}
	s.roomChange(w, r, "matrix.room_close", id, func(ctx context.Context, tok string, res *roomResult) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		if err := s.idle(ctx, tok, id); err != nil {
			return err
		}
		blocked, err := s.rooms.Blocked(ctx, tok, id)
		if err != nil {
			return err
		}
		if blocked && room.LocalMembers == 0 {
			res.outcome = "already_closed"
			return nil
		}
		if res.deleteID, err = s.rooms.Close(ctx, tok, id); err != nil {
			return err
		}
		res.outcome = "started"
		return nil
	})
}

// handleRoomDelete deletes the media Synapse can attribute to the room, then starts the purge.
// The confirmation is checked here against confirmText, never trusted from the page.
func (s *Server) handleRoomDelete(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !validRoomID(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req) != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid room ID or request")
		return
	}
	s.roomChange(w, r, "matrix.room_delete", id, func(ctx context.Context, tok string, res *roomResult) error {
		room, err := s.rooms.Room(ctx, tok, id)
		if err != nil {
			return err
		}
		if req.Confirm != confirmText(room) {
			return errConfirm
		}
		if err := s.idle(ctx, tok, id); err != nil {
			return err
		}
		media, err := s.rooms.RoomMedia(ctx, tok, id)
		if err != nil {
			return err
		}
		res.media = "0"
		for i, m := range media {
			if err := s.rooms.DeleteMedia(ctx, tok, m); err != nil {
				return err
			}
			res.media = strconv.Itoa(i + 1)
		}
		if res.deleteID, err = s.rooms.Delete(ctx, tok, id); err != nil {
			return err
		}
		res.outcome = "started"
		return nil
	})
}
```

Notes for the implementer:
- Deleting a room that is already gone: `Room` answers 404 before the confirmation check, so the response is 404 "No such room". This is audited as `error: …`, like every other failure after parsing.
- In `TestRoomCloseNeedsFreshAdminIsAuditedAndIdempotent` the expected JSON bodies are the ones `writeJSON` produces for `map[string]string`: Go sorts the keys, so `delete_id` comes before `outcome`.

- [ ] **Step 7: Run.** Run `go test -race ./internal/api/ ./cmd/server/ -v -run 'Room|Health|Matrix|Session|Audit|Privileged'`, then `go test -race ./...` and `go vet ./...`. Expected: PASS. Then `make ci`. Expected: PASS (the smoke test needs the built binary).

- [ ] **Step 8: Commit.** Message: `api: console room routes (list, detail, close, delete with typed confirmation, job status), audited`.

---

### Task 4: Web — Rooms tab, typed confirmation, job polling; Health label

**Files:**
- Create: `web/src/pages/Rooms.tsx`, `web/src/pages/Rooms.test.tsx`
- Modify: `web/src/App.tsx` (imports; the page switch at lines 116-122)
- Modify: `web/src/components/AppHeader.tsx` (`adminItems`, lines 14-22; the lucide import)
- Modify: `web/src/components/AppHeader.test.tsx`
- Modify: `web/src/pages/Health.tsx` (`LABEL`; the version line)
- Modify: `web/src/pages/Health.test.tsx`
- Modify: `web/dist/` (rebuilt)

**Interfaces:**
- Consumes:
  - The Task 3 routes and bodies.
  - `adminFetch`, `errorMessage`, `isMatrixDisabled` (`api.ts`).
  - `arr`, `bool`, `count`, `obj`, `oneOf`, `str` (`dto.ts`).
  - `ConfirmItsYou`.
- Produces:
  - `export const Rooms: React.FC<{ pollMs?: number }>`
  - `parseRoomsPage`, `parseRoomDetail`, `parseJobs`
  - Navigation id `rooms`, labelled "Rooms", between Users and Health.

- [ ] **Step 1: Failing tests.** Create `web/src/pages/Rooms.test.tsx`:

```tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Rooms } from './Rooms';
import { ConfirmItsYou } from '../components/ConfirmItsYou';

const GRP = '!grp:example.com';
const ENC = encodeURIComponent(GRP);
const ROOM = { id: GRP, name: 'Team chat', alias: '', creator: '@alice:example.com', members: 2, encrypted: true, public: false, join_rule: 'invite', state_events: 12, closed: false };
const DM = { ...ROOM, id: '!dm:example.com', name: '', closed: true };
const PAGE = { rooms: [ROOM, DM], total: 2, offset: 0, limit: 50 };
const DETAIL = { room: ROOM, members: ['@alice:example.com', '@bob:example.com'], members_total: 2, confirm_text: 'Team chat', jobs: [] };
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required' };
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
const list = (url: string) => (url.startsWith('/api/admin/matrix/rooms?') ? json(PAGE) : undefined);
const detail = (url: string) => (url === `/api/admin/matrix/rooms/${ENC}` ? json(DETAIL) : undefined);
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

async function openRoom() {
  fireEvent.click(await screen.findByRole('button', { name: 'Details for Team chat' }));
  const panel = await screen.findByRole('region', { name: 'Team chat' });
  await within(panel).findByRole('list', { name: 'Members' });
  return panel;
}

describe('Rooms', () => {
  it('lists rooms with encryption, access, size and status', async () => {
    const calls = serve(list);
    render(<Rooms />);
    const table = await screen.findByRole('table');
    expect(within(table).getByText('Team chat')).toBeTruthy();
    expect(within(table).getByText('Unnamed room')).toBeTruthy();
    expect(within(table).getAllByText('Encryption on')).toHaveLength(2);
    expect(within(table).getAllByText('Invite only')).toHaveLength(2);
    expect(within(table).getByText('Closed')).toBeTruthy();
    expect(within(table).getByRole('columnheader', { name: 'State events' })).toBeTruthy();
    expect(screen.getByText(/Messages are never shown here/)).toBeTruthy();
    expect(calls[0]).toBe('GET /api/admin/matrix/rooms?search=&offset=0&limit=50');
  });

  it('searches and pages', async () => {
    const calls = serve((url) => (url.startsWith('/api/admin/matrix/rooms?') ? json({ ...PAGE, total: 120 }) : undefined));
    render(<Rooms />);
    await screen.findByText('Team chat');
    fireEvent.change(screen.getByLabelText('Search by name or room ID'), { target: { value: ' team ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/rooms?search=team&offset=0&limit=50'));
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/rooms?search=team&offset=50&limit=50'));
  });

  it('says when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    render(<Rooms />);
    expect(await screen.findByText('Chat (Matrix) is not set up on this server.')).toBeTruthy();
  });

  it('says why Synapse admin access is not working', async () => {
    serve(() => json({ error: 'Synapse admin access is not working: console account is locked in MAS' }, 502));
    render(<Rooms />);
    expect((await screen.findByRole('alert')).textContent).toContain('console account is locked in MAS');
  });

  it('refuses a malformed page', async () => {
    serve(() => json({ ...PAGE, rooms: [{ ...ROOM, closed: 'yes' }] }));
    render(<Rooms />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
    expect(screen.queryByText('Team chat')).toBeNull();
  });

  it('shows a hostile room name as text', async () => {
    const name = '<img src=x onerror=alert(1)>';
    serve(() => json({ ...PAGE, rooms: [{ ...ROOM, name }] }));
    render(<Rooms />);
    expect(await screen.findByText(name)).toBeTruthy();
    expect(document.querySelector('img')).toBeNull();
  });

  it('closes a room after confirming, then polls until Synapse finishes', async () => {
    let polls = 0;
    const calls = serve((url, init) => {
      if (url === `/api/admin/matrix/rooms/${ENC}/close` && init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D1' });
      if (url === `/api/admin/matrix/rooms/${ENC}/delete-status`) {
        polls++;
        return json({ jobs: [{ delete_id: 'D1', status: polls < 5 ? 'active' : 'complete' }] });
      }
      return detail(url) ?? list(url);
    });
    render(<><ConfirmItsYou /><Rooms pollMs={20} /></>);
    const panel = await openRoom();
    expect(within(panel).getByRole('list', { name: 'Members' }).textContent).toContain('@bob:example.com');
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    const confirm = within(panel).getByRole('group', { name: 'Confirm close' });
    expect(confirm.textContent).toContain('cannot be reopened');
    fireEvent.click(within(confirm).getByRole('button', { name: 'Close room' }));
    expect(await within(panel).findByText('Closing…')).toBeTruthy();
    expect(await screen.findByText(/Room closed: everyone was removed/)).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'Team chat' })).toBeNull();
    expect(polls).toBe(5);
    expect(calls.filter((c) => c.startsWith('GET /api/admin/matrix/rooms?'))).toHaveLength(2);
  });

  it('retries a close after the admin confirms it is them', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (init?.method === 'POST') { posts.push(url); return posts.length === 1 ? json(STEP_UP, 403) : json({ outcome: 'already_closed', delete_id: '' }); }
      return detail(url) ?? list(url);
    });
    render(<><ConfirmItsYou /><Rooms pollMs={20} /></>);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: "Confirm it's you" })).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('The room was already closed.')).toBeTruthy();
    expect(posts).toHaveLength(2);
  });

  it('deletes only after the exact room name is typed, and sends it for the server to check', async () => {
    const bodies: string[] = [];
    serve((url, init) => {
      if (url === `/api/admin/matrix/rooms/${ENC}/delete` && init?.method === 'POST') { bodies.push(String(init.body)); return json({ outcome: 'started', delete_id: 'D2' }); }
      if (url === `/api/admin/matrix/rooms/${ENC}/delete-status`) return json({ jobs: [{ delete_id: 'D2', status: 'complete' }] });
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    const button = within(panel).getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement;
    const input = within(panel).getByLabelText(/to delete this room permanently/);
    expect(within(panel).getByText(/encrypted rooms cannot be found by the server/)).toBeTruthy();
    expect(button.disabled).toBe(true);
    fireEvent.change(input, { target: { value: 'team chat' } });
    expect(button.disabled).toBe(true);
    fireEvent.change(input, { target: { value: 'Team chat' } });
    expect(button.disabled).toBe(false);
    fireEvent.click(button);
    expect(await screen.findByText('Room deleted permanently.')).toBeTruthy();
    expect(bodies).toEqual(['{"confirm":"Team chat"}']);
  });

  it('shows the server refusing a delete', async () => {
    serve((url, init) => {
      if (init?.method === 'POST') return json({ error: "Type the room's name exactly as shown to delete it" }, 400);
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    fireEvent.change(within(panel).getByLabelText(/to delete this room permanently/), { target: { value: 'Team chat' } });
    fireEvent.click(within(panel).getByRole('button', { name: 'Delete permanently' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain('exactly as shown');
  });

  it('disables changes while a job already runs and follows it', async () => {
    serve((url) => {
      if (url === `/api/admin/matrix/rooms/${ENC}`) return json({ ...DETAIL, jobs: [{ delete_id: 'D0', status: 'active' }] });
      if (url.endsWith('/delete-status')) return json({ jobs: [{ delete_id: 'D0', status: 'active' }] });
      return list(url);
    });
    render(<Rooms pollMs={10000} />);
    const panel = await openRoom();
    expect(within(panel).getByText('A close or delete of this room is running…')).toBeTruthy();
    expect((within(panel).getByRole('button', { name: 'Close room…' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(panel).getByLabelText(/to delete this room permanently/), { target: { value: 'Team chat' } });
    expect((within(panel).getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('reports a failed job', async () => {
    serve((url, init) => {
      if (init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D3' });
      if (url.endsWith('/delete-status')) return json({ jobs: [{ delete_id: 'D3', status: 'failed' }] });
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain('Synapse reports the job as failed');
    expect(within(panel).queryByText('Closing…')).toBeNull();
  });

  it('stops polling a job Synapse never lists', async () => {
    let polls = 0;
    serve((url, init) => {
      if (init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D4' });
      if (url.endsWith('/delete-status')) { polls++; return json({ jobs: [] }); }
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={1} />);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    expect((await within(panel).findByRole('alert', {}, { timeout: 3000 })).textContent).toContain('has not finished yet');
    const stopped = polls;
    await new Promise((r) => setTimeout(r, 30));
    expect(polls).toBe(stopped);
    expect(stopped).toBe(90);
  });
});
```

In `AppHeader.test.tsx`, change the expected ids to `['dashboard', 'users', 'rooms', 'health', 'audit', 'scim', 'backup', 'settings']`.

Append to `Health.test.tsx`:

```tsx
it('names Synapse admin access and shows why it is down, without a version', async () => {
  serve(json({ matrix: true, components: [c('synapse-admin', { status: 'down', error: 'console account is locked in MAS' })] }));
  render(<Health />);
  const list = await screen.findByRole('region', { name: 'Components' });
  expect(within(list).getByText('Synapse admin access (console)')).toBeTruthy();
  expect(within(list).getByText('console account is locked in MAS')).toBeTruthy();
  expect(within(list).queryByText('Version unknown')).toBeNull();
});
```

- [ ] **Step 2: Run, expect failure.** Run `cd web && npx vitest run src/pages/Rooms.test.tsx src/components/AppHeader.test.tsx src/pages/Health.test.tsx`. Expected: FAIL (`./Rooms` not found; the nav ids; the Health label).

- [ ] **Step 3: Implement Rooms.** Create `web/src/pages/Rooms.tsx`:

```tsx
import React, { useCallback, useEffect, useState } from 'react';
import { Loader2, Lock, Search, Trash2, X } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, bool, count, obj, oneOf, str } from '../dto';

const JOB_STATUSES = ['scheduled', 'active', 'complete', 'failed', 'cancelled', 'unknown'] as const;
const JOIN_RULES = ['public', 'invite', 'knock', 'restricted', 'knock_restricted', 'private', 'other', ''] as const;
export interface RoomRow {
  id: string; name: string; alias: string; creator: string; members: number; encrypted: boolean;
  public: boolean; join_rule: (typeof JOIN_RULES)[number]; state_events: number; closed: boolean;
}
export interface RoomsPage { rooms: RoomRow[]; total: number; offset: number; limit: number }
export interface RoomJob { delete_id: string; status: (typeof JOB_STATUSES)[number] }
export interface RoomDetail { room: RoomRow; members: string[]; members_total: number; confirm_text: string; jobs: RoomJob[] }

function parseRoom(v: unknown): RoomRow {
  const r = obj(v);
  return {
    id: str(r.id, 255), name: str(r.name, 255), alias: str(r.alias, 255), creator: str(r.creator, 255),
    members: count(r.members), encrypted: bool(r.encrypted), public: bool(r.public),
    join_rule: oneOf(r.join_rule, JOIN_RULES), state_events: count(r.state_events), closed: bool(r.closed),
  };
}
export function parseRoomsPage(v: unknown): RoomsPage {
  const p = obj(v);
  return { rooms: arr(p.rooms).map(parseRoom), total: count(p.total), offset: count(p.offset), limit: count(p.limit) };
}
export function parseJobs(v: unknown): RoomJob[] {
  return arr(v).map((x) => {
    const j = obj(x);
    return { delete_id: str(j.delete_id, 255), status: oneOf(j.status, JOB_STATUSES) };
  });
}
export function parseRoomDetail(v: unknown): RoomDetail {
  const d = obj(v);
  return {
    room: parseRoom(d.room), members: arr(d.members).map((m) => str(m, 255)), members_total: count(d.members_total),
    confirm_text: str(d.confirm_text, 255), jobs: parseJobs(d.jobs),
  };
}

const PAGE = 50;
// Synapse lists a job only once it has started; give up waiting after this many polls.
const MAX_POLLS = 90;
const RUNNING: readonly RoomJob['status'][] = ['scheduled', 'active'];
const errorText = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback);
const roomPath = (id: string) => `/api/admin/matrix/rooms/${encodeURIComponent(id)}`;
const label = (r: RoomRow) => r.name || 'Unnamed room';
const access = (r: RoomRow) => (r.public ? 'Public' : r.join_rule === 'invite' ? 'Invite only' : r.join_rule || '—');

type Kind = 'close' | 'delete' | 'job';
const WORKING: Record<Kind, string> = { close: 'Closing…', delete: 'Deleting…', job: 'A close or delete of this room is running…' };
const DONE: Record<Kind, string> = {
  close: 'Room closed: everyone was removed and nobody can rejoin. Its history stays until you delete it.',
  delete: 'Room deleted permanently.',
  job: 'The close or delete that was running has finished.',
};

const RoomPanel: React.FC<{ id: string; pollMs: number; onClose: () => void; onDone: (message: string) => void }> = ({ id, pollMs, onClose, onDone }) => {
  const [detail, setDetail] = useState<RoomDetail | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [typed, setTyped] = useState('');
  const [pending, setPending] = useState<{ deleteId: string; kind: Kind } | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetch(roomPath(id), { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the room'));
        const d = parseRoomDetail(await res.json());
        setDetail(d);
        const run = d.jobs.find((j) => RUNNING.includes(j.status));
        if (run) setPending({ deleteId: run.delete_id, kind: 'job' });
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) setError(errorText(err, 'Could not read the room')); });
    return () => controller.abort();
  }, [id]);

  // Close and delete are Synapse background jobs: poll until this one ends.
  useEffect(() => {
    if (!pending) return;
    let stopped = false;
    let polls = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const res = await fetch(`${roomPath(id)}/delete-status`, { cache: 'no-store' });
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the job status'));
        const job = parseJobs(obj(await res.json()).jobs).find((j) => j.delete_id === pending.deleteId);
        if (stopped) return;
        if (job?.status === 'complete') { onDone(DONE[pending.kind]); return; }
        if (job && !RUNNING.includes(job.status)) {
          setPending(null);
          setError(`Synapse reports the job as ${job.status} and gives no reason; check Synapse's log.`);
          return;
        }
        if (++polls >= MAX_POLLS) {
          setPending(null);
          setError('Synapse has not finished yet. Open the room again later to see its state.');
          return;
        }
        timer = setTimeout(() => void tick(), pollMs);
      } catch (err) {
        if (!stopped) { setPending(null); setError(errorText(err, 'Could not read the job status')); }
      }
    };
    timer = setTimeout(() => void tick(), pollMs);
    return () => { stopped = true; clearTimeout(timer); };
  }, [pending, id, pollMs, onDone]);

  const act = async (kind: 'close' | 'delete') => {
    setBusy(true);
    setError('');
    try {
      const init: RequestInit = kind === 'delete'
        ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: typed }) }
        : { method: 'POST' };
      const res = await adminFetch(`${roomPath(id)}/${kind}`, init);
      if (!res.ok) throw new Error(await errorMessage(res, kind === 'close' ? 'Could not close the room' : 'Could not delete the room'));
      const b = obj(await res.json());
      if (oneOf(b.outcome, ['started', 'already_closed'] as const) === 'already_closed') { onDone('The room was already closed.'); return; }
      setConfirmClose(false);
      setPending({ deleteId: str(b.delete_id, 255), kind });
    } catch (err) {
      setError(errorText(err, 'The change failed'));
    } finally {
      setBusy(false);
    }
  };

  const room = detail?.room;
  const locked = busy || pending !== null;
  return (
    <section className="panel dr-section" aria-labelledby="room-title">
      <div className="panel-header">
        <h3 id="room-title">{room ? label(room) : 'Room'}</h3>
        <button type="button" className="btn-secondary" disabled={busy} onClick={onClose} aria-label="Close room details"><X size={14} /></button>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {pending && <p role="status"><Loader2 size={14} className="animate-spin" /> {WORKING[pending.kind]}</p>}
      {!detail && !error && <p role="status">Loading room…</p>}
      {detail && room && (
        <>
          <p className="dr-hint dr-mono">{room.id}{room.alias ? ` · ${room.alias}` : ''}</p>
          <p className="dr-hint">
            {room.encrypted ? 'Encryption on' : 'Encryption off'} · {access(room)} · {room.closed ? 'Closed' : 'Open'} · {room.state_events} state events
          </p>
          <h4>Members ({detail.members_total})</h4>
          {detail.members.length === 0 && <p className="dr-hint">Nobody is in this room.</p>}
          <ul aria-label="Members" className="dr-mono">
            {detail.members.map((m) => <li key={m}>{m}</li>)}
          </ul>
          {detail.members_total > detail.members.length && <p className="dr-hint">Showing the first {detail.members.length}.</p>}
          <div className="dr-section">
            {!confirmClose ? (
              <button type="button" className="btn-secondary" disabled={locked} onClick={() => setConfirmClose(true)}>
                <Lock size={14} /><span>Close room…</span>
              </button>
            ) : (
              <div role="group" aria-label="Confirm close" className="dr-alert dr-alert-warn">
                <span>Everyone is removed and nobody can rejoin: a closed room cannot be reopened. Its history stays on the server until you delete the room.</span>
                <div className="dr-actions">
                  <button type="button" className="btn-danger" disabled={locked} onClick={() => void act('close')}>Close room</button>
                  <button type="button" className="btn-secondary" disabled={busy} onClick={() => setConfirmClose(false)}>Cancel</button>
                </div>
              </div>
            )}
          </div>
          <div className="dr-section">
            <label className="dr-field">
              <span>Type <code className="dr-mono">{detail.confirm_text}</code> to delete this room permanently</span>
              <input value={typed} maxLength={255} autoComplete="off" spellCheck={false} onChange={(e) => setTyped(e.target.value)} />
            </label>
            <p className="dr-hint">
              Deletes the room's history from the server for everyone. Attachments in encrypted rooms cannot be found by the
              server and stay in its media store as encrypted files. This cannot be undone.
            </p>
            <button type="button" className="btn-danger" disabled={locked || typed !== detail.confirm_text} onClick={() => void act('delete')}>
              <Trash2 size={14} /><span>Delete permanently</span>
            </button>
          </div>
        </>
      )}
    </section>
  );
};

export const Rooms: React.FC<{ pollMs?: number }> = ({ pollMs = 2000 }) => {
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<RoomsPage | null>(null);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [disabled, setDisabled] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ search, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/matrix/rooms?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return setDisabled(true);
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not list rooms'));
        setPage(parseRoomsPage(await res.json()));
        setError('');
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) { setPage(null); setError(errorText(err, 'Could not list rooms')); } });
    return () => controller.abort();
  }, [search, offset, reload]);

  const done = useCallback((m: string) => { setSelected(null); setMessage(m); setReload((n) => n + 1); }, []);

  if (disabled) {
    return (
      <div className="dr-page">
        <div className="dr-header"><h1>Rooms</h1></div>
        <p role="status">Chat (Matrix) is not set up on this server.</p>
      </div>
    );
  }
  const last = page ? Math.min(page.offset + page.rooms.length, page.total) : 0;
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Rooms</h1></div>
      <p className="dr-hint">
        Room names and members are shown to admins only. Messages are never shown here. State events is the closest
        measure of a room's size Synapse reports.
      </p>
      {message && <div className="dr-alert dr-alert-success" role="status"><span>{message}</span></div>}
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      <form role="search" className="dr-row" onSubmit={(e) => { e.preventDefault(); setOffset(0); setSearch(query.trim()); }}>
        <label className="dr-field">
          Search by name or room ID
          <input type="search" value={query} maxLength={255} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <button type="submit" className="btn-secondary"><Search size={14} /><span>Search</span></button>
      </form>
      {page && (
        <section className="panel dr-section" aria-label="Rooms">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead>
                <tr>
                  <th scope="col">Room</th><th scope="col">Members</th><th scope="col">Encryption</th><th scope="col">Access</th>
                  <th scope="col">Creator</th><th scope="col">State events</th><th scope="col">Status</th><th scope="col">Details</th>
                </tr>
              </thead>
              <tbody>
                {page.rooms.map((r) => (
                  <tr key={r.id}>
                    <td><div>{label(r)}</div><div className="dr-mono dr-hint">{r.id}</div></td>
                    <td>{r.members}</td>
                    <td>{r.encrypted ? 'Encryption on' : 'Encryption off'}</td>
                    <td>{access(r)}</td>
                    <td className="dr-mono">{r.creator || '—'}</td>
                    <td>{r.state_events}</td>
                    <td>{r.closed ? 'Closed' : 'Open'}</td>
                    <td>
                      <button type="button" className="btn-secondary" onClick={() => { setMessage(''); setSelected(r.id); }}
                        aria-label={`Details for ${label(r)}`}>Details</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.rooms.length === 0 && <p className="dr-hint">No rooms{search ? ` match “${search}”` : ''}.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${last} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</button>
              <button type="button" className="btn-secondary" disabled={last >= page.total} onClick={() => setOffset(offset + PAGE)}>Next</button>
            </div>
          </div>
        </section>
      )}
      {selected && <RoomPanel key={selected} id={selected} pollMs={pollMs} onClose={() => setSelected(null)} onDone={done} />}
    </div>
  );
};
```

Notes:
- The "Members" list is always rendered (empty when nobody is in the room), so `openRoom()` can wait on it.
- The table's "Closed" cell text is unique in the list test: the panel is not open there.

- [ ] **Step 4: Navigation and Health.**
  - `AppHeader.tsx`: add `Hash` to the lucide import, and add `{ id: 'rooms', label: 'Rooms', icon: Hash },` after the `users` item.
  - `App.tsx`: add `import { Rooms } from './pages/Rooms';` and `{isAdmin && activeTab === 'rooms' && <Rooms />}` after the Users line.
  - `Health.tsx`: add `'synapse-admin': 'Synapse admin access (console)',` to `LABEL`, add `const VERSIONLESS = new Set(['database', 'synapse-admin']);` below it, and change the version line to:

```tsx
              <div className="dr-fact-value dr-mono">{c.version || (VERSIONLESS.has(c.name) ? '' : 'Version unknown')}</div>
```

- [ ] **Step 5: Run.** Run `cd web && npm test`. Expected: PASS. Then run `npm run build`. Expected: PASS (strict TS, no unused imports), and `git -C .. status --short web/dist` shows the rebuilt bundle. Then `node src/ky-ui/check-vendor.mjs`. Expected: PASS.

- [ ] **Step 6: Commit** (include `web/dist`). Message: `web: Rooms tab with close, typed-name delete and job polling; Synapse admin access in Health`.

---

### Task 5: Acceptance rooms step and browser regressions

**Files:**
- Modify: `scripts/matrix-acceptance.sh` (header comment; a new last step after `step no-username`, line 718-724)
- Modify: `web/browser/ui.spec.mjs` (after the Users check, around line 107)
- Modify: `.github/workflows/ci.yml` (`matrix-acceptance` `timeout-minutes` comment, only if the measured duration requires it)

**Interfaces:**
- Consumes:
  - The Task 3 routes.
  - The harness helpers `app_api`, `app_login`, `answer`, `status`, `hcurl`, `sql`, `mas_user`, `eventually`, `e2e token`, `token_status`, and `$group` from `step prove`.
  - The Rooms tab (Task 4).
- Produces: acceptance step `rooms`, and browser coverage of the Rooms tab.

The step runs last, on the restored stack (question 10). By then two things are true. `step restore` has already asserted that bob is still in the group room. And the app's start-up sweep after the restore ran with `kymessages-console` present, created by Task 1's Health probe before the backup, which proves the exemption.

- [ ] **Step 1: The rooms step.** Append at the end of `scripts/matrix-acceptance.sh`:

```bash
# ---------------------------------------------------------------------------------------
# Rooms in the console, on the restored stack: the group room is listed encrypted with both
# members; Close removes bob and refuses his rejoin; Delete permanently of a throwaway room
# leaves none of its rows; both audited. The start-up sweep after the restore ran with the
# console account present and never touched it.
step rooms
uri() { jq -rn --arg v "$1" '$v | @uri'; }
# job_status ROOM DELETE_ID: the console's status for that job ("absent" until Synapse starts it).
job_status() { app_api GET "/api/admin/matrix/rooms/$(uri "$1")/delete-status" | jq -r --arg d "$2" '[.jobs[] | select(.delete_id == $d) | .status] | first // "absent"'; }
latest_membership() { sql synapse "SELECT membership FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '$1' AND m.room_id = '$2' ORDER BY e.stream_ordering DESC LIMIT 1"; }
bob_id="@bob:$KY_MATRIX_SERVER_NAME"
app_login "$admin_pass"
app_api GET /api/admin/health | jq -e '.components[] | select(.name == "synapse-admin" and .status == "up")' >/dev/null ||
	{ echo "  FAILED: Synapse admin access is not up on the restored stack" >&2; false; }
ok "Synapse admin access works on the restored stack"
app_api GET '/api/admin/matrix/rooms?search=kymatrix-group' >"$state/rooms.json"
expect "$(jq -r --arg r "$group" '.rooms[] | select(.id == $r) | "\(.encrypted) \(.closed) \(.members)"' "$state/rooms.json")" \
	"true false 2" "the console lists the group room: encrypted, open, two members"
expect "$(app_api GET "/api/admin/matrix/rooms/$(uri "$group")" | jq -r '.members | sort | join(",")')" \
	"@alice.q_ky:$KY_MATRIX_SERVER_NAME,$bob_id" "its members are alice and bob"
e2e token bob
expect "$(token_status bob)" 200 "bob's captured Element token is live"

# Close needs a sign-in from the last 10 minutes.
app_login "$admin_pass"
read -r outcome job < <(app_api POST "/api/admin/matrix/rooms/$(uri "$group")/close" | jq -r '"\(.outcome) \(.delete_id)"')
expect "$outcome" started "the console started closing the group room"
eventually 120 complete "Synapse finished closing the group room" job_status "$group" "$job"
expect "$(latest_membership "$bob_id" "$group")" leave "bob was removed from the group room"
expect "$(answer POST "https://matrix.kymatrix.test/_matrix/client/v3/join/$(uri "$group")" \
	-H "Authorization: Bearer $(cat "$state/bob.token")" -H 'Content-Type: application/json' -d '{}')" '403 M_UNKNOWN' "bob's rejoin is refused"
expect "$(jq -r .error "$state/answer.json")" "This room has been blocked on this server" "refused because the room is blocked"
expect "$(app_api POST "/api/admin/matrix/rooms/$(uri "$group")/close" | jq -r .outcome)" already_closed "closing it again succeeds"
expect "$(app_api GET "/api/admin/matrix/rooms/$(uri "$group")" | jq -r '"\(.room.closed) \(.members | length)"')" "true 0" "the console shows it closed and empty"

# A throwaway room bob creates, with one non-message event (no plaintext m.room.message:
# earlier steps assert there is none).
throwaway_name=kymatrix-throwaway-$$
throwaway=$(hcurl -fsS -X POST -H "Authorization: Bearer $(cat "$state/bob.token")" -H 'Content-Type: application/json' \
	-d "$(jq -n --arg n "$throwaway_name" '{name: $n, preset: "private_chat"}')" \
	https://matrix.kymatrix.test/_matrix/client/v3/createRoom | jq -re .room_id)
hcurl -fsS -X PUT -H "Authorization: Bearer $(cat "$state/bob.token")" -H 'Content-Type: application/json' -d '{"probe": true}' \
	"https://matrix.kymatrix.test/_matrix/client/v3/rooms/$(uri "$throwaway")/send/org.kymatrix.probe/1" >/dev/null
events=$(sql synapse "SELECT count(*) FROM events WHERE room_id = '$throwaway'")
((events > 0)) || { echo "  FAILED: the throwaway room holds no events" >&2; false; }
ok "throwaway room $throwaway holds $events events"
app_login "$admin_pass"
csrf=$(awk '$6 == "ky_csrf" { print $7 }' "$jar")
expect "$(status POST "$KY_APP_URL/api/admin/matrix/rooms/$(uri "$throwaway")/delete" -b "$jar" -H "Origin: $KY_APP_URL" \
	-H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" -d '{"confirm":"not the name"}')" 400 "a delete with the wrong room name is refused"
read -r outcome job < <(app_api POST "/api/admin/matrix/rooms/$(uri "$throwaway")/delete" "$(jq -n --arg n "$throwaway_name" '{confirm: $n}')" |
	jq -r '"\(.outcome) \(.delete_id)"')
expect "$outcome" started "the console started deleting the throwaway room"
eventually 180 complete "Synapse finished deleting the throwaway room" job_status "$throwaway" "$job"
for table in events event_json state_events current_state_events room_memberships rooms; do
	expect "$(sql synapse "SELECT count(*) FROM $table WHERE room_id = '$throwaway'")" 0 "no $table rows left for the throwaway room"
done
expect "$(status GET "$KY_APP_URL/api/admin/matrix/rooms/$(uri "$throwaway")" -b "$jar")" 404 "the deleted room is gone from the console"

app_api GET '/api/admin/audit?kind=matrix&limit=100' >"$state/audit.json"
expect "$(jq -r --arg r "$group" '[.records[] | select(.action == "matrix.room_close" and .target == $r and .actor == "admin") | .outcome] | join(",")' "$state/audit.json")" \
	already_closed,started "the audit shows both closes, newest first"
expect "$(jq -r --arg r "$throwaway" '[.records[] | select(.action == "matrix.room_delete" and .target == $r) | .outcome] | join(",")' "$state/audit.json")" \
	"started,refused: confirmation does not match" "the audit shows the delete and the refused one"
expect "$(jq --arg c "@kymessages-console:$KY_MATRIX_SERVER_NAME" '[.records[] | select(.target == $c)] | length' "$state/audit.json")" 0 \
	"the sweep never acted on the console account"
expect "$(mas_user kymessages-console 'locked_at IS NULL AND deactivated_at IS NULL')" t "the console account is unlocked after the start-up sweep"
expect "$(sql mas "SELECT count(*) FROM personal_sessions WHERE revoked_at IS NULL")" 0 "every console session was revoked"
pass
```

Also extend the header comment's console sentence: "On the restored stack it closes the group room (bob removed, rejoin refused) and permanently deletes a throwaway room, audited."

- [ ] **Step 2: Run the acceptance.** Run `make matrix-acceptance`. Expected: every step PASSes, including `rooms`.
  - If bob's rejoin answers anything but `403 M_UNKNOWN` "This room has been blocked on this server", stop and report the body. Do not loosen the check: it is the proof that Close blocks.
  - If a purge leaves rows in a listed table, stop and report the table and count.
  - Compare the run's total duration with the `matrix-acceptance` comment in `.github/workflows/ci.yml` (314 s measured, `timeout-minutes: 45`). Update the measured figure in the comment; raise the timeout only if the new duration needs it.
  - Then run `shellcheck scripts/*.sh`. Expected: clean.

- [ ] **Step 3: Browser regressions.** In `web/browser/ui.spec.mjs`, directly after the Users block (the `fits(page)` after `'Chat (Matrix) is not set up on this server.'`), insert:

```js
  // Rooms, Matrix off: the same notice; reached from Users by keyboard.
  await nav.getByRole('button', { name: 'Users', exact: true }).focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button', { name: 'Rooms', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(nav.getByRole('button', { name: 'Rooms', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'Rooms', level: 1 })).toBeVisible();
  await expect(page.getByText('Chat (Matrix) is not set up on this server.')).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('rooms.png'), fullPage: true });
```

The existing Health → Tab → Audit keyboard check still holds: Rooms sits before Health.

- [ ] **Step 4: Run.** Build first: `cd web && npm run build && cd .. && go build -o .browser/server ./cmd/server`. Then run `cd web && npm run test:browser`. Expected: all six projects PASS (Chromium light and dark at 390 and 1280, Firefox light-1280 and dark-390).

- [ ] **Step 5: Commit.** Message: `acceptance: console closes and deletes rooms on the restored stack; browser regressions cover Rooms`.

---

### Task 6: Docs, spec amendment and DOX pass

**Files:**
- Modify: `README.md` ("Operator console", lines 192-206)
- Modify: `docs/superpowers/specs/2026-10-02-matrix-console-5b-design.md`
- Modify: `AGENTS.md` (root Verification paragraph on `scripts/matrix-acceptance.sh`; the closing status paragraph)
- Modify: `internal/api/AGENTS.md`, `internal/matrixsync/AGENTS.md`, `internal/health/AGENTS.md`, `internal/synapseadmin/AGENTS.md`
- Modify: `web/AGENTS.md`, `web/browser/AGENTS.md`

**Interfaces:**
- Consumes: everything shipped in Tasks 1-5.
- Produces: docs that match the shipped behaviour.

- [ ] **Step 1: README.** In "Operator console":
  - After the Users bullet, add:

```markdown
- **Rooms** lists every room (name, members, encryption, public or invite-only, creator, state
  events, open or closed) with search and paging, and a room's members. It never shows
  messages. **Close** removes everyone and blocks the room; nobody can rejoin and it cannot be
  reopened, but its history stays. **Delete permanently** purges the room's history; you type
  the room's name (or its ID when it has none) to confirm. Attachments in encrypted rooms cannot
  be found by the server and stay in the media store as encrypted files. Both need a sign-in
  from the last 10 minutes and are audited as `matrix.room_close` and `matrix.room_delete`.
- The console reaches Synapse's admin API as the MAS account `kymessages-console`, created the
  first time it is needed: no password, no KyIdentity link, sessions of at most 5 minutes,
  revoked after each action. The offboarding sweep leaves exactly that account alone, and it is
  listed under Users as not linked. To cut the console's room access, lock it in MAS; Health
  then shows "Synapse admin access" down. Keep `/_synapse/admin` unrouted publicly
  ([Reverse proxy](docs/Reverse_Proxy_Networking.md)).
```

  - Add "Synapse admin access" to the Health bullet's component list.
  - Change "Rooms and settings are not built yet." to "Settings are not built yet.", and add "room close and delete" to the list of what `make matrix-acceptance` proves.

- [ ] **Step 2: Spec amendment.** In `docs/superpowers/specs/2026-10-02-matrix-console-5b-design.md`:
  - Decision 2 becomes: "**Close** (`block=true`, `purge=false`): final. Synapse makes every local member leave, and with federation off the server is then out of the room, so nobody can rejoin or be invited, even if unblocked. There is no Reopen. **Delete permanently** (purge; typed room-name confirmation)."
  - In Section 1:
    - Remove the Reopen bullet and `/reopen` from Routes.
    - Add to Delete: "deletes the local media Synapse can attribute to the room (non-encrypted references only); encrypted rooms' attachments stay as ciphertext."
    - Add to Close: "a background job like Delete; the page polls status for both."
    - The audit actions are `matrix.room_close` and `matrix.room_delete`.
  - In Acceptance, replace "Reopen lets an invite work" with "closing again succeeds as already closed".
  - In Section 2, add: "A second close or delete is refused while one is listed as running."

- [ ] **Step 3: Root `AGENTS.md`.**
  - In the Verification paragraph on `scripts/matrix-acceptance.sh`, after the console sentence, add: "Health proves Synapse admin access through the `kymessages-console` MAS account (exactly two scopes, sessions revoked, at most 5 minutes). On the restored stack the console closes the group room (bob removed, rejoin refused as blocked) and permanently deletes a throwaway room (no rows left), both audited, and the start-up sweep leaves the console account unlocked."
  - In the closing paragraph, change "Open: the console and removal of the custom messaging stack." to "Open: console settings (5c) and removal of the custom messaging stack."

- [ ] **Step 4: `internal/api/AGENTS.md`.** Append to the Matrix console routes bullet:

```markdown
  Rooms (`room_handlers.go`, admin-only, `no-store`, Synapse via `RoomAdmin` with one
  `AsConsole` session per request): `GET /api/admin/matrix/rooms?search&offset&limit` (Synapse's
  list plus one `/block` read per row; `closed` is the block flag), `GET
  /api/admin/matrix/rooms/{id}` (room, members capped at 1000 with `members_total`,
  `confirm_text`, jobs), `GET .../{id}/delete-status` (jobs; does not need the room to exist),
  `POST .../{id}/close` and `POST .../{id}/delete` with `{"confirm"}` (`requireFreshAdmin`,
  `tracked`, detached context). Room IDs must match `roomIDPattern` (no `/`, at most 255
  bytes). Close answers `already_closed` for a blocked room with no local members; there is no
  reopen. Delete checks `confirm` against `confirmText` (the name clipped to 200 bytes, or the
  room ID when the name is empty or holds non-printable or format characters), deletes the
  media `/room/{id}/media` lists, then starts the purge. A change is refused (409) while a job
  is listed as scheduled or active, and Synapse's own "already in progress" 400 maps to 409.
  Audit `matrix.room_close` / `matrix.room_delete`, resource the room ID, details from
  `auditFields`: `outcome="started|already_closed|refused: …|error: …"` then `delete_id` and
  `media` when set, within 200 bytes. A failure other than a missing room or a running job is
  502 "Synapse admin access is not working: <cause>". Health adds `synapse-admin` (console
  session, `rooms?limit=1`, revoke; no version).
```

- [ ] **Step 5: `internal/matrixsync/AGENTS.md`.** In Ownership, add "the console service account and sessions (`EnsureConsoleUser`, `AsConsole`)". In Local Contracts, add:

```markdown
- `Plan` skips exactly `ConsoleUsername` (`kymessages-console`) while it has no upstream link
  and is not ambiguous; a linked account with that name is judged as a person. The sweep
  never unlocks the console account, so a lock in MAS is the console's off switch.
- `EnsureConsoleUser` finds or creates the account (409 on create means it exists; read it
  again), refuses it when locked, deactivated or linked, and grants MAS admin. MAS does not
  check that flag for personal sessions and Synapse decides admin by scope; the boundary is
  the MAS admin secret, which can already mint for any user.
- `AsConsole` mints a personal session (`human_name` `KyMessages console`, scope exactly
  `urn:matrix:client:api:* urn:synapse:admin:*`, `expires_in` 300), runs one action and
  revokes the session on a 10-second context detached from the caller's, also when the action
  fails or the caller is gone. A refused revoke (other than 409, already revoked) is logged
  with the session ID, never the token; the session expires in 5 minutes.
```

- [ ] **Step 6: `internal/health/AGENTS.md`.** In Ownership, add: "`internal/api` adds a `synapse-admin` probe (console session, one admin read, revoke) through `Check`; it has no pin and no version."

- [ ] **Step 7: `internal/synapseadmin/AGENTS.md`.** Re-read it against the shipped code; it should already hold the Task 1 and Task 2 contracts. Add under Ownership: "No reopen: `PUT .../block` is deliberately not wrapped (Close is final; see the 5b spec)."

- [ ] **Step 8: `web/AGENTS.md` and `web/browser/AGENTS.md`.**
  - In `web/AGENTS.md`, add:

```markdown
- `Rooms.tsx` lists rooms from `/api/admin/matrix/rooms` (search by name or room ID, paging 50)
  and opens one room's members. It never shows messages and labels encryption only as
  "Encryption on/off". Close asks for confirmation and says it is final; Delete permanently is
  enabled only when the typed text equals the server's `confirm_text`, which the server checks
  again. Both are background jobs: the page polls `delete-status` (every 2 s, at most 90 times)
  until the job completes, fails (shown without a reason, as Synapse gives none) or never
  appears. Changes use `adminFetch`. With Matrix off it says chat is not set up.
- `Health.tsx` labels `synapse-admin` "Synapse admin access (console)" and shows no version for it.
```

  - In `web/browser/AGENTS.md`, change the coverage bullet to: "The suite also covers Users and Rooms (Matrix off, Rooms reached by keyboard), Health, the Audit kind filter with keyboard navigation and the Overview, at every project."

- [ ] **Step 9: Verify.** Run `make ci`. Expected: PASS. Then run:

```bash
grep -rn "reopen\|Reopen" --include='*.go' --include='*.ts*' --include='*.md' . | grep -v node_modules | grep -v web/dist | grep -v docs/superpowers/plans/
```

Expected: only the deliberate mentions (Close is final, no reopen) in code comments, AGENTS files, README and the spec. Then run `grep -n "Rooms and settings are not built" README.md`. Expected: no output.

- [ ] **Step 10: Commit.** Message: `docs: console rooms, the console service account, spec amendment and DOX pass`.
