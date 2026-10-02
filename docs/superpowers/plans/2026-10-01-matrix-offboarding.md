# Matrix Offboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Disabling, unassigning or deleting a user in KyIdentity cuts their Matrix access within seconds and keeps it cut; re-enabling restores it.

**Architecture:** KyIdentity's OIDC back-channel logout ends MAS sessions (config only). KyMessages' existing signed webhook wakes a new `internal/matrixsync` sweep that locks, unlocks or deactivates MAS users through the MAS admin API, reached only on an internal Compose network. The acceptance harness measures the cut against real Element sessions.

**Tech Stack:** Go 1.26 (stdlib `net/http`, `encoding/json`), MAS 1.26.0 admin API, Docker Compose overlays, bash + Playwright acceptance harness.

**Spec:** `docs/superpowers/specs/2026-10-01-matrix-offboarding-design.md`

## Global Constraints

- Delete deactivates with `{"skip_erase": true}`, always; nothing in KyMessages may send `skip_erase: false` or omit it.
- A deactivated MAS user is never reactivated by KyMessages.
- Lock when the subject is inactive, unknown to KyMessages, or the MAS user has no KyIdentity link (fail closed).
- The MAS admin API listener binds only to host `mas-admin`, port 8081; `mas-admin` is a network alias on the internal network `matrix-admin`, which only `mas` and `app` join.
- MAS admin client: `client_auth_method: client_secret_basic`, secret written once 0600 by `matrix-init`, listed alone in `policy.data.admin_clients`.
- KyIdentity provider in MAS: `on_backchannel_logout: logout_all`. Back-channel URL `<auth host>/upstream/backchannel-logout/<provider id>`.
- With `KY_MATRIX_*` set and admin access missing or invalid, KyMessages refuses to start.
- Sweep every 5 minutes and once at start; a webhook wakes it without blocking.
- Audit actions are exactly `matrix.lock`, `matrix.unlock`, `matrix.deactivate`; never log or audit the admin secret or tokens.
- The acceptance bound for the cut is 30 seconds; if missed, record the cause and stop for the owner. Do not loosen it.
- AGPL rule: official MAS/Synapse/Element images, configured by files and HTTP APIs only.
- Commits end with a blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. A MAS admin page whose `links.next` points off the admin API (absolute URL, other path) — must be refused, not followed. Test in Task 4.
2. More or fewer than one upstream provider in MAS — the sweep must fail, not guess which links count. Test in Task 4.
3. A MAS user who is both locked and deleted in KyIdentity — must still be deactivated. Test in Task 4 (`Plan`).
4. A webhook burst (many events) — `Wake` must never block the webhook handler. Test in Task 5.
5. An admin secret file that is empty, a directory, or missing — refuse start with a message naming the variable. Test in Task 3.

---

### Task 1: `matrix-init` renders admin access and back-channel logout

**Files:**
- Modify: `internal/matrixinit/matrixinit.go`
- Modify: `internal/matrixinit/templates/mas.yaml.tmpl`
- Modify: `cmd/server/matrixinit.go`
- Test: `internal/matrixinit/matrixinit_test.go`, `cmd/server/matrixinit_test.go` (if it exists; else add the print assertion to the matrixinit test of `Registration`)

**Interfaces:**
- Produces: secrets `secrets/mas_admin_client_id` (ULID) and `secrets/mas_admin_client_secret` (hex); `Registration.BackchannelLogoutURI string`; `Result.AdminClientID string`. Compose (Task 2) mounts `matrix/secrets/mas_admin_client_secret`; operators set `KY_MATRIX_ADMIN_CLIENT_ID` from the printed value.

- [ ] **Step 1: Write failing tests.** In `TestMASConfigTrustsOnlyKyIdentity`, replace the "One listener" block with:

```go
	// The public listener serves no compat login and no admin API; the admin API is on its own
	// listener bound only to the internal matrix-admin network alias.
	listeners := mas["http"].(map[string]any)["listeners"].([]any)
	if len(listeners) != 2 {
		t.Fatalf("MAS has %d listeners, want web and admin", len(listeners))
	}
	for _, l := range listeners {
		lm := l.(map[string]any)
		var names []string
		for _, r := range lm["resources"].([]any) {
			names = append(names, r.(map[string]any)["name"].(string))
		}
		binds := lm["binds"].([]any)
		switch lm["name"] {
		case "web":
			if slices.Contains(names, "compat") || slices.Contains(names, "adminapi") {
				t.Errorf("public listener serves %v", names)
			}
		case "admin":
			if !slices.Equal(names, []string{"adminapi", "oauth"}) {
				t.Errorf("admin listener resources %v", names)
			}
			if len(binds) != 1 || binds[0].(map[string]any)["host"] != "mas-admin" || binds[0].(map[string]any)["port"] != 8081 {
				t.Errorf("admin listener binds %v, want only mas-admin:8081", binds)
			}
		default:
			t.Errorf("unexpected listener %v", lm["name"])
		}
	}
	if p["on_backchannel_logout"] != "logout_all" {
		t.Errorf("on_backchannel_logout = %v", p["on_backchannel_logout"])
	}
	clients := mas["clients"].([]any)
	admins := mas["policy"].(map[string]any)["data"].(map[string]any)["admin_clients"].([]any)
	c := clients[0].(map[string]any)
	if len(clients) != 1 || len(admins) != 1 || admins[0] != c["client_id"] || c["client_id"] != res.AdminClientID ||
		c["client_auth_method"] != "client_secret_basic" || len(c["client_secret"].(string)) != 64 {
		t.Errorf("admin client %v, admin_clients %v", clients, admins)
	}
	if want := "https://auth.example.com/upstream/backchannel-logout/" + p["id"].(string); res.Registration.BackchannelLogoutURI != want {
		t.Errorf("back-channel URI %q, want %q", res.Registration.BackchannelLogoutURI, want)
	}
```

Add `"slices"` to the imports. In `TestInitIsWriteOnceForSecrets`, add `secrets/mas_admin_client_id` and `secrets/mas_admin_client_secret` to whatever list of expected created secrets it checks (read the test; follow its pattern). If `res` is unused in this test today, it is now used.

- [ ] **Step 2: Run, expect failure.** `go test ./internal/matrixinit/ -run 'TestMASConfigTrustsOnlyKyIdentity|TestInitIsWriteOnceForSecrets' -v` → FAIL (2 listeners expected, missing clients).

- [ ] **Step 3: Implement.** In `secretSpecs` append:

```go
	{"mas_admin_client_id", newULID},     // KyMessages' MAS admin client; set KY_MATRIX_ADMIN_CLIENT_ID to it
	{"mas_admin_client_secret", hexSecret}, // mounted into KyMessages, never in env
```

Add `BackchannelLogoutURI string` to `Registration` and `AdminClientID string` to `Result`. In `Run`, set:

```go
	res.AdminClientID = s["mas_admin_client_id"]
	res.Registration = Registration{
		RedirectURI:          in.AuthHost + "/upstream/callback/" + s["upstream_provider_id"],
		BackchannelLogoutURI: in.AuthHost + "/upstream/backchannel-logout/" + s["upstream_provider_id"],
		Scopes:               []string{"openid", "profile", "email"},
		ClientType:           "confidential",
	}
```

In `mas.yaml.tmpl`, after the `web` listener add:

```yaml
    # Internal: the admin API (and its token endpoint) for KyMessages' offboarding sync. Bound
    # only to mas-admin, MAS's alias on the internal matrix-admin network; never proxied.
    - name: admin
      resources:
        - name: adminapi
        - name: oauth
      binds:
        - host: mas-admin
          port: 8081
```

After the `secrets:` block add:

```yaml
clients:
  - client_id: {{ q .S.mas_admin_client_id }}
    client_auth_method: client_secret_basic
    client_secret: {{ q .S.mas_admin_client_secret }}
policy:
  data:
    admin_clients:
      - {{ q .S.mas_admin_client_id }}
```

In the provider, after `token_endpoint_auth_method`, add `      on_backchannel_logout: logout_all`. Update the listener comment `# Public: no compat (legacy /login) resource and no admin API.` to stay true.

In `cmd/server/matrixinit.go`, after the redirect URI line print:

```go
	fmt.Fprintf(w, "  back-channel logout URI  %s\n", r.BackchannelLogoutURI)
```

and after the `KY_MATRIX_GID` line print `fmt.Fprintf(w, "  KY_MATRIX_ADMIN_CLIENT_ID=%s\n\n", res.AdminClientID)` (adjust the blank lines so output stays one block). `cmd/server/matrixinit_test.go:60` asserts that no file under `secrets/` except `upstream_provider_id` appears in the output: exempt `mas_admin_client_id` too (an identifier, not a secret), keep `mas_admin_client_secret` covered, and assert the output contains `back-channel logout URI` and `KY_MATRIX_ADMIN_CLIENT_ID=`.

- [ ] **Step 4: Run.** `go test ./internal/matrixinit/ ./cmd/server/ -v -run 'Matrix|Init|MAS'` → PASS. Then `go test ./...` → PASS.

- [ ] **Step 5: Commit.** `git add internal/matrixinit cmd/server/matrixinit.go cmd/server/*_test.go && git commit` — message `matrix-init: MAS admin client on an internal listener, back-channel logout`.

---

### Task 2: Compose joins KyMessages to MAS's admin network

**Files:**
- Modify: `docker-compose.matrix.yml`
- Modify: `scripts/check-compose-matrix.sh`

**Interfaces:**
- Consumes: `matrix/secrets/mas_admin_client_secret` (Task 1).
- Produces: app env `KY_MATRIX_ADMIN_URL=http://mas-admin:8081`, `KY_MATRIX_ADMIN_CLIENT_ID`, `KY_MATRIX_ADMIN_SECRET_FILE=/run/secrets/mas_admin_client_secret` (Task 3 reads them).

- [ ] **Step 1: Write failing checks.** In `scripts/check-compose-matrix.sh`, add the export `KY_MATRIX_ADMIN_CLIENT_ID=01J0000000000000000000ADMN` to the throwaway export line, then before the final exit add:

```bash
# Offboarding: only mas and app reach the admin API, on an internal network, by alias.
[ "$(jq -r '.networks["matrix-admin"].internal' <<<"$out")" = true ] || bad "matrix-admin is not internal"
members=$(jq -r '[.services | to_entries[] | select(.value.networks | has("matrix-admin")) | .key] | sort | join(",")' <<<"$out")
[ "$members" = app,mas ] || bad "matrix-admin members are $members, want app,mas"
[ "$(jq -c '.services.mas.networks["matrix-admin"].aliases' <<<"$out")" = '["mas-admin"]' ] || bad "mas lacks the mas-admin alias on matrix-admin"
for k in default matrix-db; do
  jq -e --arg k "$k" '.services.mas.networks[$k].aliases // [] | index("mas-admin") | not' <<<"$out" >/dev/null || bad "mas-admin alias leaks onto $k"
done
[ "$(jq -r '.services.app.environment.KY_MATRIX_ADMIN_URL' <<<"$out")" = http://mas-admin:8081 ] || bad "app admin URL"
[ "$(jq -r '.services.app.environment.KY_MATRIX_ADMIN_SECRET_FILE' <<<"$out")" = /run/secrets/mas_admin_client_secret ] || bad "app admin secret path"
[ "$(jq -r '.secrets.mas_admin_client_secret.file' <<<"$out")" = "$root/matrix/secrets/mas_admin_client_secret" ] || bad "admin secret source"
jq -e '.services.app.environment | has("KY_MATRIX_ADMIN_CLIENT_SECRET") | not' <<<"$out" >/dev/null || bad "admin secret in env"
```

Also confirm the script's existing loop over "ports published" and "images pinned" still covers every service. Run the static-IP overlay rendering the script already does, and assert `members` there too (app keeps its static address on `default`).

- [ ] **Step 2: Run, expect failure.** `bash scripts/check-compose-matrix.sh` → prints `matrix-admin is not internal` etc., exit 1.

- [ ] **Step 3: Implement.** In `docker-compose.matrix.yml`:

```yaml
  app:
    environment:
      - KY_MATRIX_SERVER_NAME=${KY_MATRIX_SERVER_NAME:?Set KY_MATRIX_SERVER_NAME}
      - KY_MATRIX_HOST=${KY_MATRIX_HOST:?Set KY_MATRIX_HOST}
      - KY_MATRIX_CHAT_HOST=${KY_MATRIX_CHAT_HOST:?Set KY_MATRIX_CHAT_HOST}
      # Offboarding sync: MAS's admin API, reachable only on matrix-admin.
      - KY_MATRIX_ADMIN_URL=http://mas-admin:8081
      - KY_MATRIX_ADMIN_CLIENT_ID=${KY_MATRIX_ADMIN_CLIENT_ID:?Set KY_MATRIX_ADMIN_CLIENT_ID (printed by matrix-init)}
      - KY_MATRIX_ADMIN_SECRET_FILE=/run/secrets/mas_admin_client_secret
    secrets: [mas_admin_client_secret]
    networks:
      default: {}
      matrix-admin: {}
```

For `mas`, replace `networks: [default, matrix-db]` with:

```yaml
    networks:
      default: {}
      matrix-db: {}
      # MAS binds its admin listener to this alias, so the admin API exists only here.
      matrix-admin: {aliases: [mas-admin]}
```

Add the secret and network:

```yaml
secrets:
  postgres_password:
    file: ./matrix/secrets/postgres_password
  mas_admin_client_secret:
    file: ./matrix/secrets/mas_admin_client_secret

networks:
  matrix-db:
    internal: true
  # KyMessages to MAS's admin API only.
  matrix-admin:
    internal: true
```

If the static-IP overlay uses a map for `app.networks.default`, the map form above merges with it; if `docker compose config` errors, match the static-IP overlay's syntax. Update the header comment (lines 1–10): one sentence that the app reaches MAS's admin API on the internal `matrix-admin` network. The app runs as root in its container, so it can read the 0600 secret owned by `KY_MATRIX_UID`; do not loosen the file mode.

- [ ] **Step 4: Run.** `bash scripts/check-compose-matrix.sh && bash scripts/check-compose-proxy.sh && shellcheck scripts/*.sh` → all pass. Mutation check: change the alias to `[mas-admin2]`, re-run, expect failure, revert.

- [ ] **Step 5: Commit.** `compose: KyMessages reaches MAS's admin API on internal matrix-admin`.

---

### Task 3: Config requires admin access with the Matrix block

**Files:**
- Modify: `internal/config/config.go:30-36, 255-285`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.MatrixConfig{ServerName, Host, ChatHost, AdminURL, AdminClientID string; AdminSecret string \`json:"-"\`}` and `func (m MatrixConfig) Enabled() bool { return m.ServerName != "" }`.

- [ ] **Step 1: Write failing tests** in `internal/config/config_test.go` (follow its existing env-setting helper; if none, use `t.Setenv`):

```go
func TestMatrixConfigRequiresAdminAccess(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(secret, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_CHAT_HOST": "https://chat.example.com", "KY_MATRIX_ADMIN_URL": "http://mas-admin:8081",
		"KY_MATRIX_ADMIN_CLIENT_ID": "01J0000000000000000000ADMN", "KY_MATRIX_ADMIN_SECRET_FILE": secret,
	}
	set := func(over map[string]string) {
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range over {
			t.Setenv(k, v)
		}
	}
	set(nil)
	m, err := matrixFromEnv()
	if err != nil || m.AdminSecret != "abc123" || m.AdminURL != "http://mas-admin:8081" || !m.Enabled() {
		t.Fatalf("good config: %+v %v", m, err)
	}
	for name, over := range map[string]map[string]string{
		"no admin URL":       {"KY_MATRIX_ADMIN_URL": ""},
		"no client ID":       {"KY_MATRIX_ADMIN_CLIENT_ID": ""},
		"no secret file":     {"KY_MATRIX_ADMIN_SECRET_FILE": ""},
		"missing file":       {"KY_MATRIX_ADMIN_SECRET_FILE": secret + ".nope"},
		"directory":          {"KY_MATRIX_ADMIN_SECRET_FILE": t.TempDir()},
		"admin URL path":     {"KY_MATRIX_ADMIN_URL": "http://mas-admin:8081/x"},
		"admin URL ftp":      {"KY_MATRIX_ADMIN_URL": "ftp://mas-admin:8081"},
		"admin URL userinfo": {"KY_MATRIX_ADMIN_URL": "http://u:p@mas-admin:8081"},
	} {
		set(over)
		if _, err := matrixFromEnv(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	empty := filepath.Join(t.TempDir(), "e")
	os.WriteFile(empty, []byte("\n"), 0o600)
	set(map[string]string{"KY_MATRIX_ADMIN_SECRET_FILE": empty})
	if _, err := matrixFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_ADMIN_SECRET_FILE") {
		t.Errorf("empty secret: %v", err)
	}
	for k := range base {
		t.Setenv(k, "")
	}
	if m, err := matrixFromEnv(); err != nil || m.Enabled() {
		t.Errorf("unset block: %+v %v", m, err)
	}
}
```

- [ ] **Step 2: Run, expect failure.** `go test ./internal/config/ -run TestMatrixConfigRequiresAdminAccess -v` → compile error (no `AdminSecret`).

- [ ] **Step 3: Implement.** Extend `MatrixConfig`:

```go
// MatrixConfig locates the optional Matrix stack: all set, or none. Hosts are https origins
// with no trailing slash. AdminURL reaches MAS's admin API on the internal network.
type MatrixConfig struct {
	ServerName    string `json:"server_name"`
	Host          string `json:"host"`
	ChatHost      string `json:"chat_host"`
	AdminURL      string `json:"admin_url"`
	AdminClientID string `json:"admin_client_id"`
	AdminSecret   string `json:"-"`
}

// Enabled reports whether the Matrix stack is configured.
func (m MatrixConfig) Enabled() bool { return m.ServerName != "" }
```

In `matrixFromEnv`, read `AdminURL`, `AdminClientID` with `getEnv`, keep the "all empty → return" check over every field including `KY_MATRIX_ADMIN_SECRET_FILE`, then after the host loop:

```go
	u, err := url.Parse(m.AdminURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_ADMIN_URL %q must be an http(s) origin with no path", m.AdminURL)
	}
	m.AdminURL = u.Scheme + "://" + u.Host
	if m.AdminClientID == "" {
		return MatrixConfig{}, errors.New("KY_MATRIX_ADMIN_CLIENT_ID is required with the Matrix stack (printed by matrix-init)")
	}
	path := getEnv("KY_MATRIX_ADMIN_SECRET_FILE", "")
	b, err := os.ReadFile(path)
	if v := strings.TrimSpace(string(b)); err != nil || v == "" {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_ADMIN_SECRET_FILE %q must name a readable, non-empty file: %v", path, err)
	} else {
		m.AdminSecret = v
	}
```

(`os.ReadFile` on a directory fails, covering that case.) Replace other `m == (MatrixConfig{})` / `cfg.Matrix.ServerName != ""` checks in the repo with `Enabled()` (`grep -rn "Matrix.ServerName\|MatrixConfig{}" --include=*.go`). Startup already fails on a `config.Load` error — verify `main.go` calls `log.Fatal` on it; that is the "refuses to start".

- [ ] **Step 4: Run.** `go test ./internal/config/ ./internal/api/ ./cmd/server/ -v` → PASS. Fix any existing test that sets the three Matrix vars without admin access by adding them.

- [ ] **Step 5: Commit.** `config: the Matrix block requires MAS admin access`.

---

### Task 4: `internal/matrixsync` — MAS client, `Plan`, `Syncer`

**Files:**
- Create: `internal/matrixsync/matrixsync.go` (types, `Plan`, `Syncer`), `internal/matrixsync/mas.go` (MAS client)
- Create: `internal/matrixsync/AGENTS.md`
- Modify: `internal/store/store.go` (interface), `internal/store/sqlstore.go`
- Test: `internal/matrixsync/matrixsync_test.go`, `internal/matrixsync/mas_test.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: `store.Store`, `config.MatrixConfig` (Task 3).
- Produces:
  - `store.UserStore.DirectoryStatuses(ctx context.Context, provider string) (map[string]string, error)` — subject → user status (`"active"`, `"inactive"`, …) or `"deleted"` for a subject with a `directory_sync_state` row and no user.
  - `matrixsync.NewClient(baseURL, clientID, secret string) *Client`
  - `matrixsync.New(mas MAS, st store.Store, serverName string) *Syncer`; `(*Syncer).Wake()`; `(*Syncer).Run(ctx context.Context, interval time.Duration, done chan<- struct{})`; `(*Syncer).Sweep(ctx context.Context) error`.

- [ ] **Step 1: Store test (failing).** Append to `internal/store/store_test.go`:

```go
func TestDirectoryStatuses(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mk := func(sub, status string, rev int64) *store.User {
		u := &store.User{ID: "usr_" + sub, Username: sub, Role: "user", Status: status, SSOProvider: "kyidentity", SSOSubject: sub}
		if _, err := st.Users().CreateDirectoryUser(ctx, u, store.DirectoryEvent{ID: "c-" + sub, Revision: rev}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	mk("on", "active", 1)
	mk("off", "inactive", 1)
	gone := mk("gone", "active", 1)
	if _, err := st.Users().DeleteDirectoryUser(ctx, gone, store.DirectoryEvent{ID: "d-gone", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	// A suite sign-in with no webhook yet: a user row and no order row.
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_oidc", Username: "oidc", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "oidc"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_local", Username: "local", Role: "user", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"on": "active", "off": "inactive", "gone": "deleted", "oidc": "active"}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
```

(Add `"maps"` import. If `CreateDirectoryUser` requires more fields, read `sqlstore.go` and fill them.) Run `go test ./internal/store/ -run TestDirectoryStatuses` → compile failure.

- [ ] **Step 2: Implement the store method.** Interface, next to `DeleteDirectoryUser` in `store.go`:

```go
	// DirectoryStatuses maps each subject of provider to its user's status, or to "deleted"
	// when the directory deleted it (an order row with no user).
	DirectoryStatuses(ctx context.Context, provider string) (map[string]string, error)
```

In `sqlstore.go`:

```go
func (u *userStore) DirectoryStatuses(ctx context.Context, provider string) (map[string]string, error) {
	rows, err := u.store.db.QueryContext(ctx, u.store.rebind(`SELECT sso_subject, status FROM users WHERE sso_provider = ? AND sso_subject <> ''
UNION ALL SELECT s.subject, 'deleted' FROM directory_sync_state s
WHERE s.provider = ? AND NOT EXISTS (SELECT 1 FROM users x WHERE x.sso_provider = s.provider AND x.sso_subject = s.subject)`), provider, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sub, status string
		if err := rows.Scan(&sub, &status); err != nil {
			return nil, err
		}
		out[sub] = status
	}
	return out, rows.Err()
}
```

Check the `users.sso_subject` column can be NULL (read the migration); if so use `COALESCE(sso_subject, '') <> ''`. Run `go test ./internal/store/ -run TestDirectoryStatuses -v` → PASS; with `KY_TEST_POSTGRES_DSN` (see `internal/testdb`) also on Postgres if available.

- [ ] **Step 3: `Plan` tests (failing).** `internal/matrixsync/matrixsync_test.go`:

```go
package matrixsync

import (
	"reflect"
	"testing"
)

func TestPlan(t *testing.T) {
	u := func(id, sub string, locked, deact bool) User {
		return User{ID: id, Username: id, Subject: sub, Locked: locked, Deactivated: deact}
	}
	dir := map[string]string{"a": "active", "i": "inactive", "d": "deleted"}
	got := Plan([]User{
		u("active-open", "a", false, false),    // nothing
		u("active-locked", "a", true, false),   // unlock
		u("inactive-open", "i", false, false),  // lock
		u("inactive-locked", "i", true, false), // nothing
		u("deleted-open", "d", false, false),   // deactivate
		u("deleted-locked", "d", true, false),  // deactivate
		u("unknown-open", "x", false, false),   // lock
		u("nolink-open", "", false, false),     // lock
		u("nolink-locked", "", true, false),    // nothing
		u("gone", "d", true, true),             // never touched again
		u("revived", "a", true, true),          // deactivated stays deactivated
	}, dir)
	want := []Action{
		{Unlock, u("active-locked", "a", true, false), "active in KyIdentity"},
		{Lock, u("inactive-open", "i", false, false), "inactive in KyIdentity"},
		{Deactivate, u("deleted-open", "d", false, false), "deleted in KyIdentity"},
		{Deactivate, u("deleted-locked", "d", true, false), "deleted in KyIdentity"},
		{Lock, u("unknown-open", "x", false, false), "not known to KyMessages"},
		{Lock, u("nolink-open", "", false, false), "no KyIdentity link"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
```

- [ ] **Step 4: Implement types and `Plan`** in `internal/matrixsync/matrixsync.go`:

```go
// Package matrixsync keeps MAS in step with KyIdentity's directory as KyMessages records it:
// inactive or unknown users are locked, deleted ones deactivated (never erased), re-enabled
// ones unlocked. KyIdentity's back-channel logout cuts sessions first; this makes it stick.
package matrixsync

// Kind is a MAS admin action.
type Kind string

const (
	Lock       Kind = "lock"
	Unlock     Kind = "unlock"
	Deactivate Kind = "deactivate"
)

// User is a MAS user with the KyIdentity subject of its upstream link ("" when none).
type User struct {
	ID, Username, Subject string
	Locked, Deactivated   bool
}

// Action is one change Plan wants in MAS.
type Action struct {
	Kind   Kind
	User   User
	Reason string
}

// Plan compares MAS with dir (subject → "active", "deleted" or another status) and returns
// the actions, failing closed: anything not known to be active is locked.
func Plan(users []User, dir map[string]string) []Action {
	var out []Action
	for _, u := range users {
		if u.Deactivated {
			continue // reactivation restores nothing; never undone here
		}
		status, known := dir[u.Subject]
		switch {
		case u.Subject == "":
			if !u.Locked {
				out = append(out, Action{Lock, u, "no KyIdentity link"})
			}
		case status == "active":
			if u.Locked {
				out = append(out, Action{Unlock, u, "active in KyIdentity"})
			}
		case status == "deleted":
			out = append(out, Action{Deactivate, u, "deleted in KyIdentity"})
		case !u.Locked:
			reason := "not known to KyMessages"
			if known {
				reason = "inactive in KyIdentity"
			}
			out = append(out, Action{Lock, u, reason})
		}
	}
	return out
}
```

Run `go test ./internal/matrixsync/ -run TestPlan -v` → PASS.

- [ ] **Step 5: MAS client tests (failing).** `internal/matrixsync/mas_test.go` with a fake MAS (`httptest.NewServer`) that:
  - `POST /oauth2/token`: checks Basic auth `id:secret`, form `grant_type=client_credentials`, `scope=urn:mas:admin`; returns `{"access_token":"tok<N>","token_type":"Bearer","expires_in":300}` and counts calls.
  - `GET /api/admin/v1/upstream-oauth-providers`: one provider `{"data":[{"type":"upstream-oauth-provider","id":"PROV"}],"links":{}}`.
  - `GET /api/admin/v1/upstream-oauth-links?filter[provider]=PROV&page[first]=100`: two pages; page 1 has `links.next` `/api/admin/v1/upstream-oauth-links?filter[provider]=PROV&page[first]=100&page[after]=L1`. Link attributes: `{"provider_id":"PROV","subject":"sub-a","user_id":"U1"}`.
  - `GET /api/admin/v1/users?page[first]=100`: users `U1` (`username alice`, `locked_at null`, `deactivated_at null`) and `U2` (`username bob`, `locked_at "2026-10-01T00:00:00Z"`).
  - `POST /api/admin/v1/users/{id}/lock|unlock|deactivate`: records path and body; returns `{"data":{"type":"user","id":"<id>","attributes":{}}}`.
  - Requires `Authorization: Bearer tok<N>` on every `/api/` call.

Tests:

```go
func TestClientListsUsersWithSubjects(t *testing.T)        // Users() → [{U1 alice sub-a false false} {U2 bob "" true false}], one token fetch for all pages
func TestClientDeactivateNeverErases(t *testing.T)        // Deactivate("U1") body is exactly {"skip_erase":true}; Lock/Unlock send no body or {}
func TestClientRefetchesTokenOn401(t *testing.T)          // fake rejects tok1 once with 401 → client fetches tok2 and the call succeeds; a second 401 is an error, no loop
func TestClientRefusesForeignNextLink(t *testing.T)       // links.next "https://evil.example/api/admin/v1/users" or "/elsewhere" → error, no request made to it
func TestClientRequiresExactlyOneProvider(t *testing.T)   // 0 or 2 providers → error
func TestClientNeverLogsSecret(t *testing.T)              // errors from a failing token endpoint (500 body echoing the request) do not contain "secret-value"
```

Write each fully, with the fake's handler as a `map`/`switch` on `r.URL.Path`. Run `go test ./internal/matrixsync/ -run TestClient` → compile failure.

- [ ] **Step 6: Implement the client** in `internal/matrixsync/mas.go`:

```go
package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const adminPrefix = "/api/admin/v1/"

// Client calls MAS's admin API with a client-credentials token, cached until shortly before
// it expires and refetched once on a 401.
type Client struct {
	base, id, secret string
	hc               *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewClient(baseURL, clientID, secret string) *Client {
	return &Client{base: strings.TrimSuffix(baseURL, "/"), id: clientID, secret: secret, hc: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"urn:mas:admin"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.id), url.QueryEscape(c.secret))
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("MAS token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MAS token: HTTP %d", resp.StatusCode) // body may echo credentials
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&t); err != nil || t.AccessToken == "" {
		return "", errors.New("MAS token: unusable response")
	}
	c.token, c.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second-30*time.Second)
	return c.token, nil
}

func (c *Client) dropToken() { c.mu.Lock(); c.token = ""; c.mu.Unlock() }

// call sends one admin request; path must start with adminPrefix.
func (c *Client) call(ctx context.Context, method, path string, body []byte, out any) error {
	if !strings.HasPrefix(path, adminPrefix) {
		return fmt.Errorf("refusing admin path %q", path)
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return fmt.Errorf("MAS %s %s: %w", method, path, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.dropToken()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("MAS %s %s: HTTP %d", method, path, resp.StatusCode)
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
}

type resource struct {
	ID         string          `json:"id"`
	Attributes json.RawMessage `json:"attributes"`
}

// list follows links.next from path; every page must stay on the admin API.
func (c *Client) list(ctx context.Context, path string, each func(resource) error) error {
	for path != "" {
		var page struct {
			Data  []resource `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if err := c.call(ctx, http.MethodGet, path, nil, &page); err != nil {
			return err
		}
		for _, r := range page.Data {
			if err := each(r); err != nil {
				return err
			}
		}
		path = page.Links.Next
	}
	return nil
}

// Users lists every MAS user with the subject of its KyIdentity link. MAS must have exactly
// one upstream provider: links from any other source would be guesses.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var providers []string
	if err := c.list(ctx, adminPrefix+"upstream-oauth-providers?page[first]=100", func(r resource) error {
		providers = append(providers, r.ID)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("MAS has %d upstream providers, want exactly KyIdentity", len(providers))
	}
	subjects := map[string]string{}
	if err := c.list(ctx, adminPrefix+"upstream-oauth-links?filter[provider]="+url.QueryEscape(providers[0])+"&page[first]=100", func(r resource) error {
		var a struct {
			Subject string  `json:"subject"`
			UserID  *string `json:"user_id"`
		}
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		if a.UserID != nil {
			subjects[*a.UserID] = a.Subject
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var users []User
	err := c.list(ctx, adminPrefix+"users?page[first]=100", func(r resource) error {
		var a struct {
			Username      string  `json:"username"`
			LockedAt      *string `json:"locked_at"`
			DeactivatedAt *string `json:"deactivated_at"`
		}
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		users = append(users, User{ID: r.ID, Username: a.Username, Subject: subjects[r.ID],
			Locked: a.LockedAt != nil, Deactivated: a.DeactivatedAt != nil})
		return nil
	})
	return users, err
}

func (c *Client) post(ctx context.Context, id, op string, body []byte) error {
	return c.call(ctx, http.MethodPost, adminPrefix+"users/"+url.PathEscape(id)+"/"+op, body, nil)
}

func (c *Client) Lock(ctx context.Context, id string) error   { return c.post(ctx, id, "lock", nil) }
func (c *Client) Unlock(ctx context.Context, id string) error { return c.post(ctx, id, "unlock", nil) }

// Deactivate ends the user's sessions and removes them from rooms; their messages stay.
func (c *Client) Deactivate(ctx context.Context, id string) error {
	return c.post(ctx, id, "deactivate", []byte(`{"skip_erase":true}`))
}
```

Verify with the MAS v1.26.0 OpenAPI spec (`gh api 'repos/element-hq/matrix-authentication-service/contents/docs/api/spec.json?ref=v1.26.0' -H 'Accept: application/vnd.github.raw'`) that `page[first]` accepts 100, that the providers list path is `/api/admin/v1/upstream-oauth-providers`, and that lock/unlock accept an empty body; adjust and note any difference in the report. Run `go test ./internal/matrixsync/ -v` → PASS.

- [ ] **Step 7: `Syncer` tests (failing).** In `matrixsync_test.go`, a `fakeMAS` implementing `MAS` (records calls; `err` field to fail) and a real store (`store.Open` on a temp SQLite file, as `cmd/server/maintenance_test.go` does):

```go
func TestSweepAppliesPlanAndAudits(t *testing.T)      // dir: sub-a active, sub-i inactive, sub-d deleted; fake users U1(a,locked) U2(i) U3(d) U4(no link) → calls unlock U1, lock U2, deactivate U3, lock U4; four audit rows with actions matrix.unlock/lock/deactivate/lock, Resource "@<username>:example.com", Details containing the subject and reason, and "outcome=ok"
func TestSweepAuditsFailuresAndContinues(t *testing.T) // Lock on U2 fails → U3 still deactivated; U2's audit row has outcome=error; Sweep returns an error
func TestWakeNeverBlocks(t *testing.T)                 // 1000 Wake() calls with no Run return within 100ms
func TestRunSweepsAtStartOnWakeAndClosesDone(t *testing.T) // Run with interval 1h: one sweep at start; Wake → second sweep; cancel → done closed
```

- [ ] **Step 8: Implement `Syncer`** (append to `matrixsync.go`; imports `context`, `errors`, `fmt`, `log`, `time`, `store`):

```go
// MAS is the admin surface the syncer needs; *Client implements it.
type MAS interface {
	Users(ctx context.Context) ([]User, error)
	Lock(ctx context.Context, id string) error
	Unlock(ctx context.Context, id string) error
	Deactivate(ctx context.Context, id string) error
}

// Syncer runs Plan against MAS on a timer and whenever Wake is called.
type Syncer struct {
	mas        MAS
	st         store.Store
	serverName string
	wake       chan struct{}
}

func New(mas MAS, st store.Store, serverName string) *Syncer {
	return &Syncer{mas: mas, st: st, serverName: serverName, wake: make(chan struct{}, 1)}
}

// Wake asks for a sweep soon; it never blocks, and wakes during a sweep coalesce into one.
func (s *Syncer) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run sweeps at start, every interval and on Wake. done closes between sweeps, so shutdown
// can wait for it before the store closes. A failure is logged once per streak.
func (s *Syncer) Run(ctx context.Context, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failing := false
	for {
		run, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := s.Sweep(run)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			log.Printf("[MATRIX] offboarding sweep failing (retrying every %s): %v", interval, err)
			failing = true
		case err == nil && failing:
			log.Printf("[MATRIX] offboarding sweep recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Sweep applies Plan once. Every action is audited, success or failure; one failed action
// does not stop the rest.
func (s *Syncer) Sweep(ctx context.Context) error {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return err
	}
	dir, err := s.st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range Plan(users, dir) {
		var err error
		switch a.Kind {
		case Lock:
			err = s.mas.Lock(ctx, a.User.ID)
		case Unlock:
			err = s.mas.Unlock(ctx, a.User.ID)
		case Deactivate:
			err = s.mas.Deactivate(ctx, a.User.ID)
		}
		outcome := "ok"
		if err != nil {
			outcome = "error: " + err.Error()
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.User.Username, err))
		}
		if aerr := s.st.Audit().LogAudit(ctx, &store.AuditRecord{
			Action:   "matrix." + string(a.Kind),
			Resource: "@" + a.User.Username + ":" + s.serverName,
			Details:  fmt.Sprintf("subject=%q reason=%q outcome=%q", a.User.Subject, a.Reason, outcome),
		}); aerr != nil {
			errs = append(errs, aerr)
		}
	}
	return errors.Join(errs...)
}
```

Run `go test -race ./internal/matrixsync/ -v` → PASS.

- [ ] **Step 9: AGENTS.md.** Create `internal/matrixsync/AGENTS.md` (Purpose, Ownership, Local Contracts, Verification) stating: the decision table, `skip_erase` always true, never reactivate, exactly one upstream provider, admin paths only under `/api/admin/v1/`, no secret or token in errors/logs/audit, sweep cadence. Verification: `go test -race ./internal/matrixsync/`.

- [ ] **Step 10: Commit.** `matrixsync: lock, unlock and deactivate MAS users from the directory`.

---

### Task 5: Wire the syncer into the server and the webhook

**Files:**
- Modify: `cmd/server/main.go:116-122`
- Modify: `internal/api/server.go` (field + setter), `internal/api/sso_handlers.go:109-138`
- Test: `internal/api/directory_webhook_test.go`, `cmd/server/maintenance_test.go` (or a new `cmd/server/matrixsync_test.go`)

**Interfaces:**
- Consumes: `matrixsync.NewClient`, `matrixsync.New`, `(*Syncer).Run/Wake` (Task 4), `cfg.Matrix.Enabled()` and admin fields (Task 3).
- Produces: `func (s *Server) OnDirectoryChange(fn func())` in `internal/api`.

- [ ] **Step 1: Failing test.** In `directory_webhook_test.go`, add `TestDirectoryWebhookWakesOffboarding`: build the server as `TestDirectoryWebhookRouteSpeaksKyIdentity` does, `woke := make(chan struct{}, 10); srv.OnDirectoryChange(func() { woke <- struct{}{} })`, send one valid signed `user.updated` with `active:false` (reuse that test's signing helper), assert 200 and exactly one value on `woke`; send a request with a bad signature, assert 401 and no further value on `woke`.

- [ ] **Step 2: Run, expect failure.** `go test ./internal/api/ -run TestDirectoryWebhookWakesOffboarding` → compile error.

- [ ] **Step 3: Implement.** In `Server` add `directoryChanged func()` and:

```go
// OnDirectoryChange registers fn to run after each applied-or-acknowledged directory event.
// fn must not block: the webhook's sender is waiting.
func (s *Server) OnDirectoryChange(fn func()) { s.directoryChanged = fn }
```

In `handleKyIdentitySyncWebhook`, just before `s.writeJSON(w, http.StatusOK, ...)`:

```go
	if s.directoryChanged != nil {
		s.directoryChanged()
	}
```

In `main.go` after `srv := api.NewServer(cfg, st)`:

```go
	matrixDone := make(chan struct{})
	if cfg.Matrix.Enabled() {
		syncer := matrixsync.New(matrixsync.NewClient(cfg.Matrix.AdminURL, cfg.Matrix.AdminClientID, cfg.Matrix.AdminSecret), st, cfg.Matrix.ServerName)
		srv.OnDirectoryChange(syncer.Wake)
		go syncer.Run(ctx, 5*time.Minute, matrixDone)
	} else {
		close(matrixDone)
	}
```

and extend `backgroundDone`'s goroutine to `<-backupDone; <-maintenanceDone; <-matrixDone`. Update the comment above `waitForBackupWork` / root AGENTS.md text that lists what the drain waits for (Task 7 does the docs; here only code comments).

- [ ] **Step 4: Run.** `go test -race ./internal/api/ ./cmd/server/ ./internal/matrixsync/ -v` → PASS; `make ci` → PASS.

- [ ] **Step 5: Commit.** `server: run the offboarding sweep; directory webhooks wake it`.

---

### Task 6: Acceptance — measure the cut

**Files:**
- Modify: `scripts/matrix-acceptance.sh`, `scripts/matrix-acceptance/e2e.mjs`, `scripts/matrix-acceptance/kyid-admin.sh` (only if a new call shape needs it)
- Create: `scripts/matrix-acceptance/overrides/app.yml` (harness-only)
- Modify: `.github/workflows/ci.yml` (only if the job needs more time or an image build)

**Interfaces:**
- Consumes: everything above; KyIdentity admin API at the pinned commit `c21445b` (`KYIDENTITY_SRC`).

Read first: the whole of `scripts/matrix-acceptance.sh` and `e2e.mjs`; in KyIdentity read `internal/api/server.go` routes for users (disable, enable, delete), clients (`backchannelLogoutUri` field in `internal/store/models.go:180`), and systems (`POST /api/admin/systems`, `systemType: suite_webhook`, `callbackUrl`; how a system is attached to an app registry record so the app's users are delivered — `internal/sync/sync.go` `CreateSystem` and `app_registry.system_id`).

- [ ] **Step 1: Run the app in the project.** The harness today never starts the base `app` service. Add `overrides/app.yml` that gives `app` an image built by the harness from this checkout (`docker build -t kymatrix-accept-app:$$ .` in the `build` step; `docker image rm` it in `cleanup`), the throwaway `KY_ADMIN_PASSWORD`/`KY_SESSION_SECRET`, `KY_KYIDENTITY_HMAC_SECRET` (from step 3), `KY_MATRIX_ADMIN_CLIENT_ID` from `matrix-init`'s output (`secrets/mas_admin_client_id`), and the harness CA if the app makes https calls. Add it to the harness's `dc` file list. The TLS proxy must route `https://<app host>` (use the existing `KY_APP_URL`/admin host) to `app:8080`; extend the proxy config the harness already writes.

- [ ] **Step 2: KyIdentity reaches MAS and the app.** Set `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true` and make the KyIdentity container trust the harness CA (`SSL_CERT_FILE` pointing at the mounted `ca.crt`, or append it to the system bundle — check what the KyIdentity image supports). Register the MAS client with `backchannelLogoutUri` set to the `back-channel logout URI` line `matrix-init` printed.

- [ ] **Step 3: Pair the webhook.** Create a `suite_webhook` system with `callbackUrl` `https://<app host>/api/sso/kyidentity/sync`, take the secret shown once, attach the MAS app registry record to it (or whatever KyIdentity requires so the assigned users are delivered), start `app`, and wait until the app has a directory row for each assigned user (`dc exec -T app` cannot run sqlite3 — instead wait for the first sweep's absence of `matrix.lock` for them, or poll KyIdentity's delivery status for the system).

- [ ] **Step 4: Capture a live token.** In `e2e.mjs`, add a command `token <user>` that signs the user in through Element exactly as the existing sign-in does, records the `Authorization: Bearer` header of the first `/_matrix/client/` request (`page.on('request')`), writes it to `state/<user>.token` (0600), and keeps the browser context alive only as long as needed to capture it. In bash:

```bash
# alive TOKEN: 0 while Synapse still accepts the token.
alive() { [ "$(status GET "$matrix_url/_matrix/client/v3/account/whoami" -H "Authorization: Bearer $1")" = 200 ]; }
# cut_within SECONDS TOKEN: prints the seconds until the token was refused; fails past the bound.
cut_within() {
	local bound=$1 tok=$2 t
	for ((t = 0; t <= bound; t++)); do
		alive "$tok" || { echo "$t"; return 0; }
		sleep 1
	done
	return 1
}
```

(One-second polling with `sleep 1` is the harness's existing pattern for deadlines.)

- [ ] **Step 5: Scenarios** — add steps `offboard-cut`, `offboard-reactivate`, `offboard-delete`, `offboard-missed`, `admin-isolation`, each with `step`/`pass`, using three new assigned users `carol`, `dave`, `erin` created like the existing ones:
  1. `offboard-cut`: carol in a room with alice (reuse e2e room helpers), `token carol`, `alive` is true, disable carol in KyIdentity, `secs=$(cut_within 30 "$(cat state/carol.token)")` or fail with `FAILED: carol's session survived 30s` and stop; record `ok "cut in ${secs}s"` into the summary. Then assert a fresh sign-in is refused (e2e sign-in expecting KyIdentity's refusal or MAS's "Account locked"). Assert MAS has carol locked within 60s: `sql mas "SELECT locked_at IS NOT NULL FROM users WHERE username='carol'"` → `t` (poll with a deadline).
  2. `offboard-reactivate`: enable carol; within 60s MAS `locked_at` is null; e2e signs carol in and reads the earlier room message.
  3. `offboard-delete`: dave sends a message to a room with alice, then is deleted in KyIdentity; within 60s `sql mas "SELECT deactivated_at IS NOT NULL FROM users WHERE username='dave'"` → `t`; Synapse membership `sql synapse "SELECT membership FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id='@dave:<server>' ORDER BY e.stream_ordering DESC LIMIT 1"` → `leave`; e2e alice still reads dave's message.
  4. `offboard-missed`: `token erin`; `dc stop app`; disable erin; `cut_within 30` passes (back-channel alone); MAS `locked_at` still null; `dc start app`; within 60s `locked_at` set.
  5. `admin-isolation`: the synapse service (on `default` and `matrix-db`, not `matrix-admin`) cannot reach the admin listener: `dc run --rm --no-deps --entrypoint curl synapse -sS -m 5 -o /dev/null http://mas:8081/` must exit non-zero, and the same for `http://mas-admin:8081/` (must not resolve). Reachability from the app is already proven by scenarios 1–4 succeeding.
  Append each measured cut time to the summary file.

- [ ] **Step 6: Run.** `make matrix-acceptance` → all steps PASS, including the earlier eight; summary shows the cut times. If any cut exceeds 30s: stop, record the timing and the cause (Synapse introspection cache — see `synapse/api/auth/msc3861_delegated.py` cache settings at v1.162.0; or MAS not storing `sid` — check `sql mas "SELECT count(*) FROM upstream_oauth_authorization_sessions WHERE ..."` for a non-null session id), and report BLOCKED. Do not raise the bound.

- [ ] **Step 7: CI.** If the job's `timeout-minutes` is now too tight (image build + scenarios), raise it with a comment giving the measured local duration. `shellcheck scripts/*.sh scripts/matrix-acceptance/*.sh` → clean.

- [ ] **Step 8: Commit.** `matrix acceptance: KyIdentity offboarding cuts Element sessions within 30s`.

---

### Task 7: Docs and DOX pass

**Files:**
- Modify: `README.md` (Matrix setup), `docs/Reverse_Proxy_Networking.md` (Matrix section), `AGENTS.md` (root), `internal/matrixinit/AGENTS.md`, `internal/config/AGENTS.md`, `internal/api/AGENTS.md`, `docs/CHAT-PLATFORM-OPTIONS.md` (section 7 open issue on disable)

- [ ] **Step 1: README.** In the Matrix setup: register the MAS client in KyIdentity with the printed back-channel logout URI as well as the redirect URI; set `KY_MATRIX_ADMIN_CLIENT_ID` from matrix-init's output; pair the `suite_webhook` system (already documented at the "pair a `suite_webhook`" paragraph — make it required with the Matrix stack); what happens on disable / re-enable / delete (lock, unlock, deactivate without erase); after a KyMessages outage longer than KyIdentity's retry window, press resync on the system in KyIdentity; a LAN-only MAS needs `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true` on KyIdentity.
- [ ] **Step 2: Reverse proxy doc.** State that `/upstream/backchannel-logout/*` on the auth host must reach `mas:8080` (it does with the documented route; say so) and that port 8081 is never routed.
- [ ] **Step 3: AGENTS.md.** Root: the compose bullet gains `matrix-admin` (app + mas only) and the admin secret; the `cmd/server` paragraph lists the offboarding syncer among the loops the shutdown drain waits for; the "Open:" list drops offboarding; Child DOX Index adds `internal/matrixsync/AGENTS.md`; the Verification `matrix-acceptance` bullet lists the offboarding scenarios and the 30s bound. `internal/matrixinit/AGENTS.md`: replace the stale "sub-project 3 re-adds the admin API" line with the shipped contract (admin listener on `mas-admin:8081`, admin client, `logout_all`). `internal/config/AGENTS.md` and `internal/api/AGENTS.md`: the new Matrix admin fields and `OnDirectoryChange`. `docs/CHAT-PLATFORM-OPTIONS.md` section 7: the disable open issue is resolved by back-channel logout plus lock; cite the measured cut.
- [ ] **Step 4: Verify.** `make ci` → PASS. `grep -rn "re-adds the admin API\|adminapi resource" AGENTS.md internal docs README.md` → no stale text.
- [ ] **Step 5: Commit.** `docs: Matrix offboarding setup, contracts and evidence`.
