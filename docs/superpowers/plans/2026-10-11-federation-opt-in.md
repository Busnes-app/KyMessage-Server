# Opt-in Federation (Console Allow-list) Plan

**Goal:** An operator picks a federation level in Settings, types another server's name
(`partner.example`) or ticks a vetted public server, and runs one restart command. Two
KyMessages installs federate once each allows the other. Closed by default; nothing changes for
installs that never change the level.

**Status:** draft plan, not yet a code-level task list. D1–D5 decided 2026-10-11. Facts marked
**unproven** are gated by Task 0 or the acceptance task.

## Levels

| Level | Who can federate | How a server gets on the list |
|---|---|---|
| 1. **Closed** (default) | nobody | n/a; federation paths 404, as today |
| 2. **Manual approval** | servers the operator added | operator types the name the other operator sent |
| 3. **Curated list + approval** | level 2, plus vetted public servers the operator ticked | as level 2, or tick a preset |

"Approval" always means the operator adds the server; there is no inbound request queue (D5).
Full open federation is not a level (D4).

## Shape

- Federation is an **allow-list only**. Both servers must list each other (Synapse checks both
  directions), so consent is mutual by construction.
- Effective allow-list = manual servers, plus ticked presets only at level 3, sorted and
  deduplicated. Level 1 renders an empty list. Going down a level keeps what was entered, so
  going back up restores it.
- The console owns one file, `./matrix/federation/federation.yaml`, rendered by one Go function
  shared with `matrix-init`. Synapse loads it as a second `--config-path`. It holds exactly two
  keys: `listeners` (with or without the `federation` resource) and
  `federation_domain_whitelist`.
- The settings table is the source of truth (`federation_level`, `federation_servers`,
  `federation_presets`). The app reconciles the file from it at start and after every change,
  same as `ReconcileBrand`.
- Synapse reads config only at start. The console shows the restart command
  (`KY_MATRIX_SYNAPSE_RESTART_HINT`, default `docker compose restart synapse`), and a status
  probe on each server shows whether the change is live.
- Discovery: the app serves `/.well-known/matrix/server` →
  `{"m.server":"<matrix host>:443"}` only at level 2 or 3. Federation traffic then reaches
  Synapse through the existing `matrix.example.com → synapse:8008` route; port 8448 never
  opens and cloudflared keeps working.

### Operator flow (the whole thing)

1. Settings → Federation → choose **Manual approval** or **Curated list + approval** (step-up
   confirm with the consequences listed, see "Warnings").
2. The panel shows **Your server: `example.com` [Copy]**. Send it to the other operator.
3. Type their server name, **Add**; at level 3, also tick any public servers. Repeat as needed.
4. Run the shown restart command once (it also covers several adds).
5. Each server shows **Connected**, **Waiting for them to allow you**, **Restart to apply**, or
   **Unreachable**.

One-time setup for installs made before this ships: re-run `matrix-init` (creates the file
closed), then `docker compose up -d` with the existing overlay chain, not `restart`: a restart
keeps the old Synapse command and the app's old mounts, so only recreating the containers
loads the new file. Then widen the apex route to `^/\.well-known/matrix/(client|server)$`.
New installs get all of it from the docs from the start.

## Facts this plan relies on

| Fact | Where | State |
|---|---|---|
| Stock `start.py` in run mode passes extra args through and adds `SYNAPSE_CONFIG_PATH` only when no `--config-path` is given, so `command: ["run","--config-path","/config/homeserver.yaml","--config-path","/federation.yaml"]` works without an entrypoint override | synapse v1.162.0 `docker/start.py:266-306` | read |
| Config files merge with a shallow `dict.update`, in order: a later file replaces a top-level key wholesale | synapse v1.162.0 `synapse/config/_base.py:975-997` | read |
| The forward-proxy doc lists outbound federation as proxied, which leaves room for an app-side egress gate later (not used here) | synapse v1.162.0 `docs/setup/forward_proxy.md:63` | read |
| The app already writes one Matrix file in place (Element's `config.json`) through a single-file rw bind, and shows a restart hint from config | `docker-compose.matrix.yml:56-66`, `internal/config/config.go:52,151,391` | read |
| A single-file rw bind must not sit inside a ro directory bind (`docker cp` breaks), hence the new `./matrix/federation/` directory | `docker-compose.matrix.yml:63-65` | read |
| Backups copy whole `./matrix` subdirectories from a fixed list | `internal/backup/matrix.go:42` | read |
| `matrix.example.com` already routes everything except `/_synapse/(admin|mas)/` to `synapse:8008`; the apex routes only `/.well-known/matrix/client` | `docs/Reverse_Proxy_Networking.md:104-115` | read |
| `federation_domain_whitelist` "does not stop a server from joining rooms that servers not on the whitelist are in"; it is "only useful to establish a private federation" | synapse v1.162.0 `docs/usage/configuration/config_documentation.md` | read |
| `lk-jwt-service` lets remote users join existing LiveKit rooms but only listed ("full-access") homeservers create them, and requires LiveKit `auto_create: false` | `element-hq/lk-jwt-service` README (main) | read |
| Our LiveKit already has `auto_create: false`; the app creates the room on each token request; the RTC endpoint already answers CORS `*` without credentials | `livekit.yaml.tmpl:12-13`, `rtc_handlers.go:144`, `server.go:441-444` | read |
| Synapse's default `ip_range_blacklist` (`DEFAULT_IP_RANGE_BLOCKLIST`, `synapse/config/server.py:143`) refuses private addresses for outbound federation; the client sees 502 `M_UNKNOWN`, as for an unreachable peer | Task 0 run, synapse v1.162.0 | **proven** |
| The stock entrypoint with two `--config-path` files works; the later file replaces `listeners` and `federation_domain_whitelist` wholesale; without the `federation` resource, `/_matrix/federation/v1/version` and `/_matrix/key/v2/server` are 404 | Task 0 run | **proven** |
| The alias lookup (`GET /_matrix/client/v3/directory/room/#…:<peer>`) cannot tell the states apart: both denials and unreachable are 502 `M_UNKNOWN` with the same text, and a peer with federation off answers 404 `M_NOT_FOUND` like a connected one | Task 0 run | **failed**; not used |
| The remote profile lookup (`GET /_matrix/client/v3/profile/@x:<peer>`) separates: connected 404 `M_NOT_FOUND`; either allow-list denying 403 `M_FORBIDDEN` (the two differ only in message text, not matched); unreachable or private address 502 `M_UNKNOWN`; peer federation off 404 `M_UNRECOGNIZED` | Task 0 run (three repeats, same results) | **proven**; the status probe |
| `/_matrix/federation/v1/openid/userinfo` answers any requester, even one off the peer's allow-list (`REQUIRE_AUTH = False`); 401 `M_UNKNOWN_TOKEN` for a bad token | Task 0 run | **proven**; the app's own allow-list is the gate for remote callers |
| No Synapse 1.162 option blocks invites from users who share no room. Per-user account data can: `m.invite_permission_config` `{"default_action":"block"}` (MSC4380, no flag) returned 403 `M_INVITE_BLOCKED` for local and federated invites; MSC4155 is behind `experimental_features.msc4155_enabled` | Task 0 run (MSC4380 run; MSC4155 read only) | **proven** / MSC4155 **unproven** |

## Decisions

- **D1 — Restart: manual hint.** (Decided 2026-10-11.) The console shows
  `KY_MATRIX_SYNAPSE_RESTART_HINT`; no code of ours runs in the Synapse container.
- **D2 — The app may write Synapse config.** (Accepted 2026-10-11.) A compromised app could put
  anything in `federation.yaml`, including a logging config whose factory runs code in Synapse.
  The app already reads every secret Synapse has and can act as Synapse admin; the gain is code
  execution in a second, unprivileged, `cap_drop: ALL` process. Record the cost in
  `docker-compose.matrix.yml` beside the existing one.
- **D3 — Remote users join calls through our LiveKit.** (Decided 2026-10-11.) Same split as
  Element's `lk-jwt-service`: local users start calls; remote users from allowed servers may
  only join a call that already exists in a room they have joined on this server. Task 3b.
- **D4 — Three levels; presets yes, full open no.** (Decided 2026-10-11.) Presets are level 3,
  Task 8. The allow-list cannot contain a public room: Synapse's docs say it "does not stop a
  server from joining rooms that servers not on the whitelist are in" and is "only useful to
  establish a private federation". Once a member joins a room on `matrix.org`, what they post
  there reaches every server in that room through `matrix.org`. The panel says presets suit DMs
  and small rooms. Full open would add only working large public rooms and no-admin contact
  with unknown servers, at the cost of remote media (including illegal content) cached on the
  operator's disk, database growth past the 256 MiB backup limit, spam from any server, and
  Synapse's federation API exposed to the whole internet. Not offered; revisit on demand.
- **D5 — "Approval" means the operator adds the name.** (Decided 2026-10-11.) No inbound
  pending queue: that would route federation through the app, and an unverified `X-Matrix`
  origin would let anyone fill the queue with forged names.

## Warnings shown before enabling (and in docs)

- Allowed servers see room names, topics, membership, display names and timestamps of rooms
  their users join. Chat keeps its label, "End-to-end encrypted in Element (not independently
  audited)", with its caveat: Synapse does not enforce encryption, so a client that does not
  encrypt can post plaintext. **Cross-server encryption is unproven until the acceptance task
  runs it.**
- Offboarding a user here does not remove what was already sent to other servers.
- **Close** removes this server from a room; other servers keep it. **Delete permanently**
  deletes only this server's copy.
- Users of allowed servers can join calls in rooms they share with your users; their media
  goes through your LiveKit and counts against your bandwidth. Only your users start calls.
- Public servers (presets): rooms on them can include servers you never allowed, and what your
  members post there reaches all of them. Servers with open sign-up let strangers invite your
  members.

## Tasks

### Task 0 — Prove the unproven facts (gate) — done 2026-10-11, results in the facts table
Run a scratch Synapse 1.162.0 pair (Compose, private CA via `federation_custom_ca_list` and
`ip_range_whitelist` for the Docker subnet, test-only). Record: the default
`ip_range_blacklist`, that the two-file merge works with the pinned image, and the exact
status/errcode of the alias probe for each of the four states. If two states can't be told
apart, merge them in the UI ("Not connected: check both servers allow each other, then
restart") instead of matching message text.

### Task 1 — `internal/matrixinit`: one renderer
(The renderer takes the effective allow-list; levels and presets are resolved in `internal/api`.)
- `FederationYAML(allow []string) []byte`: empty list → listener `[client, openid]` and
  `federation_domain_whitelist: []`; non-empty → `[client, openid, federation]` and the list.
  Sorted, deduplicated output.
- Names: the existing `ValidServerName` (a lowercase DNS name, at most 253 bytes, no port).
  Refuse IP literals, our own server name and wildcards. List capped at 100. Server names with
  an explicit port are valid Matrix but rare; they are refused with a clear message until
  someone needs one (YAGNI).
- `homeserver.yaml.tmpl`: keep the closed `listeners` and `federation_domain_whitelist: []`,
  pinned equal to `FederationYAML(nil)`; the second file replaces both. A Synapse started
  without the second file (a cluster manifest not yet updated) then still runs, closed. Keep
  `trusted_key_servers: []` (keys come straight from each origin).
- `matrix-init` creates `matrix/federation/federation.yaml` (closed) **only if missing**, owned
  by `KY_MATRIX_UID`, mode 0644.
- Tests: parse the output YAML; assert the closed form equals today's listener and allow-list
  (so the default is byte-for-byte the current policy); assert `allow_public_rooms_over_federation`
  and `ip_range_whitelist` are absent; table-test the validator.

### Task 2 — Compose and checks
- `synapse`: `command` with both `--config-path`s; ro bind
  `./matrix/federation/federation.yaml:/federation.yaml`.
- `app`: rw single-file bind of the same file at `/matrix/federation/federation.yaml`.
- `scripts/check-compose-matrix.sh`: allow exactly this second rw bind; assert Synapse's
  command and bind.
- `internal/backup/matrix.go`: add `"federation"` to `matrixDirs`; restore test covers it.
- `KY_MATRIX_SYNAPSE_RESTART_HINT` in config, default `docker compose restart synapse`.

### Task 3 — `internal/api`
- `GET /api/admin/federation` → `{level, server_name, servers:[{name,status}],
  presets:[{name,operator,open_signup,ticked,status}], restart_hint}`.
- `PUT /api/admin/federation` with `{level}` (`closed|manual|curated`), `POST`/`DELETE
  /api/admin/federation/servers`, and `PUT /api/admin/federation/presets/{name}` with
  `{ticked}`. Admin only. Raising the level, adding a server and ticking a preset require the
  existing step-up (`ConfirmItsYou`); lowering, removing and unticking do not, since they only
  narrow access. Each change writes an audit row and calls `ReconcileFederation`.
- Presets are ticked only at level 3 and only by name from the compiled list; a stored name a
  later release drops from the list is ignored and logged.
- `ReconcileFederation`: mutex, computes the effective list, renders, writes in place (same
  inode, `O_WRONLY|O_TRUNC`, as `branding.PatchElementBrand`). Runs at start and on each
  change.
- `/.well-known/matrix/server`: served only at level 2 or 3, 404 otherwise; same CORS/HSTS
  handling as the client file.
- Status: the profile probe (Task 0) through one `mas.AsConsole` session, on its own route
  (`GET /api/admin/federation/status`) so a dead peer never slows the page, all servers in
  parallel, 10 s each (Synapse's slowest observed failure was 6 s). The two denials merge into
  one `not_allowed` status. The app opens no outbound connection of its own: Synapse's SSRF
  rules apply.
- Self-check in `network_check.go` at level 2 or 3: fetch our public
  `https://<server name>/.well-known/matrix/server` and `https://<matrix host>/_matrix/key/v2/server`
  and name the fix (apex route, restart) when either fails.
- Tests: authz and step-up (required to widen, not to narrow), validator wiring, reconcile
  idempotence (run twice, same bytes), effective list per level (level 2 drops ticked presets,
  level 1 is empty, raising restores both), well-known per level, probe status mapping from
  Task 0's recorded responses.

### Task 3b — Remote users in calls (D3)
Today `rtc_handlers.go:89` refuses any `matrix_server_name` but ours, and `ReconcileCalls`
evicts anyone not active in MAS, which would evict every remote participant within 5 s.
- Token request from a remote user: refuse unless the server name is on the effective
  allow-list (so a caller can only steer the app's request to a listed server). Resolve the
  server with `/.well-known/matrix/server` (no SRV), then
  `GET https://<host>/_matrix/federation/v1/openid/userinfo?access_token=…` with a dedicated
  client: public addresses only (dial-time check, refuses loopback/private/link-local/CGNAT and
  IPv6 equivalents), 5 s timeout, no redirects, 4 KiB body cap, no proxy. `sub` must end in
  `:<that server name>`.
- Remote users never call `rtc.Create`: they get a join token only if the LiveKit room already
  exists (a local user started the call); otherwise 403 "Wait for someone on <server> to start
  the call". LiveKit keeps `auto_create: false`.
- Room membership check unchanged (`rooms.Members` includes remote members).
- `ReconcileCalls`: a remote participant stays while their server is on the effective
  allow-list and they are still a joined member; removing a server, unticking a preset or
  lowering the level evicts its participants on the next pass, without waiting for a Synapse
  restart.
- Tests: foreign server not listed → 403 with no outbound request; listed server whose
  well-known points at a private address → refused at dial; `sub` from another server → 401;
  no existing room → 403 and no `Create`; reconcile keeps listed and evicts delisted.
- Acceptance: a peer user joins a call a local user started (direct and relay-only ICE), and
  cannot start one.

### Task 4 — Console UI
- `web/src/components/FederationPanel.tsx` in Settings: a three-option level selector (radio
  group, one line of description each), "Your server" with copy, add field with inline
  validation, server list with status chips and remove, the "Public servers" list at level 3
  only (Task 8), and a restart banner with the copyable command after any change. Raising the
  level opens the confirm dialog with the warnings above; level 3 adds the public-server
  warning.
- Rooms page: when `joined_members > joined_local_members`, Close copy reads "Removes this
  server from the room; other servers keep it".
- Dashboard card only at level 2 or 3 when a server is not Connected.
- vitest for the panel; Playwright light/dark/mobile screenshots per `UI-VERIFICATION.md`.

### Task 5 — Acceptance (`scripts/matrix-acceptance.sh`)
- Default run unchanged: federation paths 404, no `/.well-known/matrix/server`.
- New stage with a peer Synapse (test-only CA and IP-range overlay mounted as a third
  `--config-path` in the acceptance overlay only):
  level 2 + add peer + restart → probe Connected; a peer user joins an invited room and a
  message crosses both ways; a server not on the list gets "Federation denied"; remove peer +
  restart → denied again; level 1 + restart → federation paths 404 again.
- Not in acceptance (decided 2026-10-12): a preset path with a test build of the list. A ticked
  preset joins the same effective allow-list as a typed server, so Synapse sees no difference
  from the manual path this stage proves; what is preset-specific (counted only at `curated`,
  dropped when unticked, the level lowered or a release drops it) is pure settings logic,
  covered by `TestFederationPresetsCountOnlyAtTheCuratedLevel`. A test build would add a
  build tag or env hook to the shipped binary for no new Synapse behaviour.
- Optional, required before the encryption label covers federated rooms: a second Element
  against the peer, with the existing "ciphertext only in the database" check on both servers.

### Task 6 — Docs and contracts
- README "Matrix chat": a Federation section with the five-step flow.
- `docs/Reverse_Proxy_Networking.md`: apex path `^/\.well-known/matrix/(client|server)$`, and
  the static-file variant for `server`.
- `docs/PRODUCT.md`: move federation from "not offered" to the three levels; full open stays
  "not offered" with D4's reasons; known risks.
- `docs/RESTORE.md`: the federation file is restored with the configs; after a restore,
  rooms shared with peers backfill on their own (state this only once acceptance shows it).
- `AGENTS.md` files: `internal/matrixinit`, `internal/api`, `internal/synapseadmin` (Close is
  final only for rooms with no remote members), `internal/backup`.

### Task 7 — `ky-kubernetes` (separate repo, controller-run)
Synapse args and file mount, the app's rw mount, `KY_MATRIX_SYNAPSE_RESTART_HINT`
(`kubectl -n ky-stack rollout restart deploy/synapse`), Synapse egress to public 443/8448
(private ranges denied), and the Cloudflare apex path rule.

### Task 8 — Public presets (level 3)
- A list compiled into the binary (`internal/api/federation_presets.go`), never fetched at
  runtime: name, operator, sign-up policy (open/closed), source URL, date checked. Releases
  update it.
- Vetting criteria, written into the file's header and applied per entry, with sources: run
  by an identifiable organisation, at least two years old, a published abuse/moderation contact
  and policy, serves federation over valid TLS, passes the Task 0 probe. Candidate list is a
  research task; nothing is listed unverified.
- UI (level 3 only): under "Add server", a "Public servers" list with a checkbox each and an
  "Open sign-up" badge where it applies. A ticked preset joins the same effective allow-list
  (not a separate mechanism), so status, removal and restart work as for any server.
- Research before shipping: whether Synapse 1.162 can stop invites from users who share no room
  with the invitee (MSC4155 invite filtering or equivalent), since open-sign-up servers bring
  strangers. If not, say so in the badge text.
- Tests: presets render, ticking adds and un-ticking removes, the list passes the validator,
  every entry has a source and date.

## Out of scope
Open federation (no allow-list; D4), an inbound approval queue (D5), room-level server ACL editing, moderation bots, remote users
starting calls, LAN-only peers (Synapse refuses private addresses by default), automatic
restart.
