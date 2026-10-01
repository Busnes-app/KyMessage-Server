# Matrix Stack and KyIdentity Sign-in Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A KyMessages deployment runs a closed, encrypted-by-default Matrix chat (Synapse + MAS + Element, unmodified) with KyIdentity as the only sign-in, proven by a repeatable acceptance test.

**Architecture:** A Go package `internal/matrixinit` renders and write-once-persists the Synapse, MAS, Element and Postgres-init configs from env; `kymessages matrix-init` drives it. A Compose overlay runs the pinned upstream images on `kymessages-net`. KyMessages serves `.well-known/matrix/client` and exposes the chat URL to the member page. `scripts/matrix-acceptance.sh` stands the whole stack up with a throwaway KyIdentity and Playwright-driven Element to reproduce the spike's plaintext message and prove the shipped settings.

**Tech Stack:** Go (text/template, crypto/rand, gopkg.in/yaml.v3 for test parsing), Docker Compose v2.24+, Synapse v1.162.0, MAS 1.26.0, Element Web v1.12.30, Postgres 17.6, Playwright (Node), KyIdentity built from `Busnes-app/KyIdentity-server`.

**Spec:** `docs/superpowers/specs/2026-10-01-matrix-stack-design.md` (parent `2026-10-01-matrix-platform-design.md`)

## Global Constraints

- AGPL rule: official upstream images only, pinned by tag **and** digest, never rebuilt or repackaged; configured only through config files and HTTP APIs; no code patches, no MAS/Synapse template overrides; Element branding through `config.json` only.
- Pinned images (from the spike, `.superpowers/sdd/2026-10-01-matrix-stack/spike-reference/evidence-image-versions.txt`):
  - `ghcr.io/element-hq/synapse:v1.162.0@sha256:6b84a7bbac36f080b2d2e51e0289cf1b08b349598ea44a558df38d558f2c2311`
  - `ghcr.io/element-hq/matrix-authentication-service:1.26.0@sha256:e089f1048a1d4a9a492ed17b9fe759100f1bd619407b001f5927928d88b780c4`
  - `ghcr.io/element-hq/element-web:v1.12.30@sha256:3e7dbd4424de9e11c812bc740acf2725b2abe66ea0d0802023b56fd85d3fa09a`
  - `postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94`
- Env inputs: `KY_MATRIX_SERVER_NAME`, `KY_MATRIX_HOST`, `KY_MATRIX_AUTH_HOST`, `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`, `KY_KYIDENTITY_ISSUER`, `KY_MATRIX_MAS_CLIENT_ID`, `KY_MATRIX_MAS_CLIENT_SECRET`; output dir flag `-dir` default `./matrix`.
- Secrets from `crypto/rand`, files 0600, directories 0700, written once and never overwritten.
- Synapse: auth delegated to MAS; registration off; federation off; `encryption_enabled_by_default_for_room_type: all`.
- MAS: KyIdentity sole upstream provider; local passwords and password registration off; scopes `openid profile email`; compatibility login off or unrouted.
- Nothing published to the host; all services on `kymessages-net`.
- E2EE label never ships until `scripts/matrix-acceptance.sh` passes.
- Commits end with a blank line then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. DOX: read and update AGENTS.md along each touched path.

## Reference material

The spike's working configs are in `.superpowers/sdd/2026-10-01-matrix-stack/spike-reference/` (git-ignored; contains throwaway secrets — never copy secret values): `homeserver.yaml`, `mas-config.yaml`, `element-config.json`, `pg-init.sh`, `compose.yml`, `e2e.mjs` (Playwright against Element), `kyid-admin.sh` (KyIdentity admin client/user/assignment calls), `nginx-hs.conf` (compat routing — used only to reproduce the plaintext case), `FINDINGS.md`. The spike ran loopback http and needed MAS `discovery_mode: insecure` and `allow_insecure_uris` plus a socat shim; shipped configs must not contain those — the acceptance harness supplies loopback-only overrides.

## Review Focus

1. Re-running `matrix-init` must never change or reveal an existing secret — Task 1 `TestInitIsWriteOnceForSecrets`.
2. A typo in a hostname (http, path, missing) must fail before anything is written — Task 1 `TestInitRefusesBadInputs`.
3. An unassigned KyIdentity user must be refused sign-in — Task 3 acceptance step 4.
4. A message in an encrypted room must never be stored as `m.room.message` with shipped settings — Task 3 acceptance step 3.
5. The overlay must not publish a port and must compose with the existing proxy/static-ip overlays — Task 2 compose check.

---

### Task 1: `internal/matrixinit` and `kymessages matrix-init`

**Files:**
- Create: `internal/matrixinit/matrixinit.go`, `internal/matrixinit/templates/{homeserver.yaml.tmpl,mas.yaml.tmpl,element.json.tmpl,pg-init.sql.tmpl}`, `internal/matrixinit/matrixinit_test.go`, `internal/matrixinit/AGENTS.md`
- Modify: `cmd/server/main.go` (new `matrix-init` subcommand case), root `AGENTS.md` (child index)

**Interfaces:**
- Produces:
  ```go
  type Input struct {
      ServerName, MatrixHost, AuthHost, ChatHost, AdminHost string // hosts are https URLs
      Issuer, ClientID, ClientSecret string
  }
  func InputFromEnv(getenv func(string) string) (Input, error)   // validates
  type Result struct { Dir string; Created []string; Kept []string; Registration Registration }
  type Registration struct { RedirectURI string; Scopes []string; ClientType string }
  func Run(in Input, dir string) (Result, error)
  ```
  Files written under `dir`: `synapse/homeserver.yaml`, `synapse/signing.key`, `mas/config.yaml`, `element/config.json`, `postgres/init.sql`, `secrets/` (one file per secret).

- [ ] **Step 1: Failing tests** in `internal/matrixinit/matrixinit_test.go`:

```go
package matrixinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func goodInput() Input {
	return Input{ServerName: "example.com", MatrixHost: "https://matrix.example.com",
		AuthHost: "https://auth.example.com", ChatHost: "https://chat.example.com",
		AdminHost: "https://admin.example.com", Issuer: "https://id.example.com",
		ClientID: "mas-client", ClientSecret: "s3cret"}
}

func TestInitRefusesBadInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Input){
		"http host":       func(i *Input) { i.ChatHost = "http://chat.example.com" },
		"host with path":  func(i *Input) { i.AuthHost = "https://auth.example.com/x" },
		"missing secret":  func(i *Input) { i.ClientSecret = "" },
		"bad server name": func(i *Input) { i.ServerName = "Not A Domain" },
		"http issuer":     func(i *Input) { i.Issuer = "http://id.example.com" },
	} {
		in := goodInput()
		mutate(&in)
		dir := t.TempDir()
		if _, err := Run(in, filepath.Join(dir, "m")); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "m")); !os.IsNotExist(err) {
			t.Errorf("%s: wrote output before refusing", name)
		}
	}
}

func TestInitIsWriteOnceForSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, filepath.Join(dir, "secrets"))
	key1, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	in := goodInput()
	in.ChatHost = "https://talk.example.com" // non-secret change
	if _, err := Run(in, dir); err != nil {
		t.Fatal(err)
	}
	after := readAll(t, filepath.Join(dir, "secrets"))
	key2, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("secrets changed in count: %d -> %d", len(before), len(after))
	}
	for name, v := range before {
		if after[name] != v {
			t.Errorf("secret %s changed on rerun", name)
		}
	}
	if string(key1) != string(key2) {
		t.Error("signing key changed on rerun")
	}
	el, _ := os.ReadFile(filepath.Join(dir, "element", "config.json"))
	if !strings.Contains(string(el), "talk.example.com") && !strings.Contains(string(el), "matrix.example.com") {
		t.Error("non-secret settings not reconciled")
	}
	for _, p := range []string{"secrets", "synapse/signing.key", "mas/config.yaml", "synapse/homeserver.yaml"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v readable by group/other", p, fi.Mode().Perm())
		}
	}
}

func TestSynapseConfigIsClosedAndEncrypted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	var hs map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "synapse", "homeserver.yaml"))
	if err := yaml.Unmarshal(b, &hs); err != nil {
		t.Fatal(err)
	}
	if hs["server_name"] != "example.com" || hs["enable_registration"] != false ||
		hs["encryption_enabled_by_default_for_room_type"] != "all" {
		t.Errorf("synapse settings wrong: %v", hs)
	}
	if fed, ok := hs["federation_domain_whitelist"].([]any); !ok || len(fed) != 0 {
		t.Errorf("federation not disabled: %v", hs["federation_domain_whitelist"])
	}
}

func TestMASConfigTrustsOnlyKyIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var mas map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
	if err := yaml.Unmarshal(b, &mas); err != nil {
		t.Fatal(err)
	}
	pw := mas["passwords"].(map[string]any)
	if pw["enabled"] != false {
		t.Error("local passwords enabled")
	}
	providers := mas["upstream_oauth2"].(map[string]any)["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["issuer"] != "https://id.example.com" {
		t.Errorf("providers: %v", providers)
	}
	if strings.Contains(string(b), "insecure") {
		t.Error("shipped MAS config contains an insecure relaxation")
	}
	if !strings.HasPrefix(res.Registration.RedirectURI, "https://auth.example.com/upstream/callback/") ||
		strings.Join(res.Registration.Scopes, " ") != "openid profile email" {
		t.Errorf("registration: %+v", res.Registration)
	}
}

func readAll(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}
```

Adjust key names to MAS 1.26 / Synapse 1.162's real schema (read the reference configs and the official config references; cite the doc URLs in `internal/matrixinit/AGENTS.md`). If federation is disabled by a different, documented setting than `federation_domain_whitelist: []`, assert that setting instead and say why in a comment.

- [ ] **Step 2: Run and see failures.** `go test ./internal/matrixinit/ -count=1` → FAIL (package missing).
- [ ] **Step 3: Implement.** Embed the templates with `//go:embed templates/*`. `InputFromEnv` reads the Global Constraints env names. Validation: hosts parse as `https` URLs with no path/query; server name matches `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`; all fields non-empty. Validate fully before creating `dir`. Secrets: `ensureSecret(dir, name, n)` returns existing file content or writes `n` random bytes hex-encoded at 0600 with `O_EXCL`. Generate the Synapse signing key in Synapse's `ed25519 <keyid> <base64 seed>` format with `crypto/ed25519` (write once). Generate the MAS encryption secret (32 bytes hex) and an RSA or EC signing key in the PEM form MAS accepts (write once). Render templates to a temp file in the same dir and `os.Rename` into place, mode 0600. `Registration.RedirectURI` = `<AuthHost>/upstream/callback/<provider ulid>`, the provider id being a stable ULID stored as a secret-like file so it survives reruns.
  MAS username mapping: set the provider's claims import so the localpart is `preferred_username`, lowercased, with disallowed characters replaced by `_`, using MAS's template filters if they support it. If they cannot, render the plain `preferred_username` mapping, document the rule in `internal/matrixinit/AGENTS.md` ("KyIdentity usernames must already be valid Matrix localparts; others are refused at sign-in"), and Task 3 tests the refusal.
  Wire `case "matrix-init":` in `cmd/server/main.go` to parse `-dir`, call `InputFromEnv(os.Getenv)` and `Run`, then print created/kept files and the registration values (redirect URI, scopes, client type `confidential`). Never print secret values.
- [ ] **Step 4: Run.** `go test ./internal/matrixinit/ ./cmd/... -count=1` → pass; `go vet ./...`; `make ci` → passes.
- [ ] **Step 5: DOX + commit.** `internal/matrixinit/AGENTS.md` (Purpose, Ownership, Local Contracts: write-once secrets, no insecure settings, AGPL config-only rule, official doc URLs; Verification). Root `AGENTS.md` child index entry. Commit `matrixinit: generate Synapse, MAS and Element configs with write-once secrets`.

---

### Task 2: Compose overlay, `.well-known`, member chat link

**Files:**
- Create: `docker-compose.matrix.yml`
- Modify: `scripts/check-compose-proxy.sh` (or a new `scripts/check-compose-matrix.sh` called from CI/Makefile alongside it), `internal/api/server.go` (route `GET /.well-known/matrix/client`), `internal/api/*_test.go`, `internal/config/config.go` (optional `Matrix` block: server name, matrix host, chat host from the same env names), `internal/api` settings DTO (expose `chat_url` when configured), `web/src/pages/MemberHome.tsx` + test, `web/AGENTS.md`, `internal/api/AGENTS.md`, `internal/config/AGENTS.md`

**Interfaces:**
- Consumes: Task 1 output layout under `./matrix`.
- Produces: `GET /.well-known/matrix/client` → `{"m.homeserver":{"base_url":"<KY_MATRIX_HOST>"}}` with `Access-Control-Allow-Origin: *` and `Content-Type: application/json`, 404 when Matrix isn't configured. `/api/settings` gains `chat_url` (string, empty when unconfigured).

- [ ] **Step 1: Failing tests.**
  Go (`internal/api/wellknown_test.go`): configured → 200 with exactly the JSON above and the CORS header; unconfigured → 404 JSON. Note: `/.well-known/` is outside `/api/`, so register it explicitly before the SPA catch-all.
  Vitest (`MemberHome.test.tsx`): with `chatUrl="https://chat.example.com"` renders a link named "Open chat" to that URL with `rel="noopener noreferrer"`; without it, still shows `Chat isn't available yet.`
  Compose check: `docker compose -f docker-compose.yml -f docker-compose.proxy.yml -f docker-compose.matrix.yml config` renders services `postgres`, `synapse`, `mas`, `element`; none publishes ports; every image matches the pinned `name:tag@sha256:` from Global Constraints; each service mounts only its own `./matrix/<service>` path read-only; it still composes with `docker-compose.static-ip.yml`.
- [ ] **Step 2: Run and see failures.**
- [ ] **Step 3: Implement.** `docker-compose.matrix.yml`: the four services with pinned images, `restart: unless-stopped`, healthchecks (Postgres `pg_isready`; Synapse `curl -fs http://localhost:8008/health` if available in the image, else a documented alternative; MAS its health endpoint; Element HTTP 200), `depends_on` with `condition: service_healthy` (Postgres → MAS and Synapse → Element), volumes `matrix-postgres`, `matrix-media`, read-only config mounts, env for DB passwords read from the `matrix-init` secret files via Compose `secrets:` (not plain env). Route + config + settings field + MemberHome link as specified.
- [ ] **Step 4: Run.** Go tests, `cd web && npm test && npx tsc -b && npm run build`, compose check, `make ci`, console browser suite (`go build -o .browser/server ./cmd/server && cd web && npx playwright test`).
- [ ] **Step 5: DOX + commit.** Commit `compose: Matrix overlay; serve .well-known; members get an Open chat link`.

---

### Task 3: Acceptance harness — reproduce, prove, close

**Files:**
- Create: `scripts/matrix-acceptance.sh`, `scripts/matrix-acceptance/` (Node Playwright project: `package.json`, lockfile, `e2e.mjs`, KyIdentity admin helper, `overrides/` loopback-only config overrides), `docs/` update in `CHAT-PLATFORM-OPTIONS.md` section 7 with the root cause
- Modify: `.github/workflows/ci.yml` (new job `matrix-acceptance`), `Makefile` (target `matrix-acceptance`, not part of `ci`), root `AGENTS.md` Verification

**Interfaces:**
- Consumes: `kymessages matrix-init` (Task 1), `docker-compose.matrix.yml` (Task 2).

- [ ] **Step 1: Write the harness** (it is the test). `scripts/matrix-acceptance.sh`, `set -euo pipefail`, its own Compose project name `kymatrix-accept-$$`, a `trap` that runs `docker compose -p <that project> down -v` only, and a `mktemp -d` scratch dir. Sequence:
  1. Build KyIdentity from `Busnes-app/KyIdentity-server` (CI: `actions/checkout` with `repository:` into a sibling path; locally: `KYIDENTITY_SRC` env pointing at a checkout, default `../KyIdentity-server`). Start it on the project network with throwaway secrets. Create two users (`alice`, `bob`) and an unassigned user (`mallory`), create the MAS OIDC client with the redirect URI `matrix-init` printed, assign alice and bob (port the calls from `spike-reference/kyid-admin.sh`).
  2. Run `kymessages matrix-init` into the scratch dir with loopback hosts. Apply loopback-only overrides from `scripts/matrix-acceptance/overrides/` (MAS `discovery_mode: insecure`, `allow_insecure_uris`, the socat shim) **in the scratch copy only** — assert afterwards that the shipped templates contain no `insecure` string (Task 1 already pins it; repeat as a guard).
  3. **Reproduce:** with `MATRIX_ACCEPT_REPRODUCE=1` (CI runs it), enable the compatibility login route (port `spike-reference/nginx-hs.conf`), sign alice in through it, send a message in an encrypted room, and record whether the stored event type is `m.room.message`. Print the finding; then investigate the cause (Element compat session lacking crypto setup, key backup/cross-signing not bootstrapped, or room encryption state) and write it into `docs/CHAT-PLATFORM-OPTIONS.md` section 7 with the evidence. If it no longer reproduces on the pinned versions, record that instead — do not fake it.
  4. **Prove:** with shipped settings (compat unrouted), alice and bob sign in through Element's native OIDC (port `spike-reference/e2e.mjs`), complete Element's key setup, exchange messages in a DM and a group room. Query Postgres: `SELECT count(*) FROM events WHERE type = 'm.room.message' AND room_id IN (SELECT room_id FROM current_state_events WHERE type = 'm.room.encryption')` must be 0, and the count of `m.room.encrypted` events in those rooms must be ≥ the number of messages sent.
  5. **Closed server:** `POST /_matrix/client/v3/register` refused; `GET /_matrix/client/v3/login` offers no `m.login.password`; the federation port/paths are not served; mallory's sign-in ends in `access_denied` with no MAS account created.
  6. Exit non-zero on any failed assertion; print a one-line PASS/FAIL per step.
- [ ] **Step 2: Run locally until green.** Expect step 3 to show its finding; steps 4–5 must pass. Record timings.
- [ ] **Step 3: CI job** `matrix-acceptance` in `ci.yml`: checks out both repos, sets up Go and Node, installs Playwright Chromium, runs `bash scripts/matrix-acceptance.sh`, uploads Playwright traces on failure. Runs on every PR, parallel to other jobs.
- [ ] **Step 4: DOX + commit.** Root `AGENTS.md` Verification bullet for the job. Commit `matrix: acceptance harness proves encrypted storage and a closed server`.

---

### Task 4: Docs

**Files:**
- Modify: `docs/Reverse_Proxy_Networking.md` (Matrix section: four hostnames, cloudflared ingress rules `matrix.`→`http://synapse:8008`, `auth.`→`http://mas:8080`, `chat.`→`http://element:80`, `admin.`→`http://kymessages:8080`; `.well-known` on the server-name host), `README.md` (setup: set env, run `kymessages matrix-init`, register the printed values in KyIdentity and assign users, add `docker-compose.matrix.yml` to `COMPOSE_FILE`, start), root `AGENTS.md` (Matrix paragraph replacing "Matrix chat integration remain open" with what exists and what remains: offboarding, backups, console)

- [ ] **Step 1:** Write the docs; verify every command and setting against Tasks 1–3 code.
- [ ] **Step 2:** `make ci`; docs-only, no new tests.
- [ ] **Step 3: Commit** `docs: run KyMessages' Matrix stack behind cloudflared`.
