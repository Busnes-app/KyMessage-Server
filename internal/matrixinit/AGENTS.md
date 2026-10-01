# Matrix init

## Purpose
Renders the configuration for the Matrix stack (Synapse, Matrix Authentication Service,
Element Web, Postgres init) from env, for `kymessages matrix-init`. KyIdentity is MAS's only
upstream sign-in.

## Ownership
Owns `matrixinit.go` and `templates/`. `cmd/server/matrixinit.go` is the CLI glue (flag,
env, printing); `docker-compose.matrix.yml` consumes the output layout. `ValidServerName`
and `Origin` are also `internal/config`'s Matrix validators, so both refuse the same input.

## Local Contracts
- Inputs: `KY_MATRIX_SERVER_NAME`, `KY_MATRIX_HOST`, `KY_MATRIX_AUTH_HOST`,
  `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`, `KY_KYIDENTITY_ISSUER`, `KY_MATRIX_MAS_CLIENT_ID`,
  `KY_MATRIX_MAS_CLIENT_SECRET`; `-dir` defaults to `./matrix` (git- and docker-ignored).
  `KY_ADMIN_HOST` is validated but not rendered into any config yet.
- Hosts are https origins (no path, query, fragment or credentials); the issuer is https and
  kept byte for byte. Never add an http escape hatch: loopback overrides belong to the
  acceptance harness's scratch copy. Everything is validated before `dir` is created.
- Layout: `secrets/<name>` (write-once), `synapse/{homeserver.yaml,signing.key}`,
  `mas/config.yaml`, `element/config.json`, `postgres/init.sql`. Directories 0700; files
  0600 except `element/config.json` (0644, no secrets).
- Secrets come from `crypto/rand` and are published with a hard link from a temp file, so
  they are never overwritten or half-written. An empty secret file is an error, never
  regenerated. `secrets/postgres_password` (Postgres superuser) is never rendered; Compose
  passes it as a secret file. `secrets/upstream_provider_id` is the MAS provider ULID, kept because
  KyIdentity's redirect URI embeds it. Configs are re-rendered on every run.
- Every rendered string goes through `q` (JSON quoting, a valid YAML scalar) or `sqlq`; a
  value cannot add keys.
- Container contract (`docker-compose.matrix.yml`, checked by `scripts/check-compose-matrix.sh`):
  Postgres, Synapse and MAS run as `KY_MATRIX_UID:KY_MATRIX_GID` so the 0600 files stay
  private; each mounts only its own `./matrix/<service>` read-only (Element the single 0644
  `config.json`, since nginx cannot enter the 0700 dir). Synapse reads `/config` (its dir) and writes media to `/media`;
  services reach each other as `postgres`, `synapse:8008`, `mas:8080`. MAS port 8081
  (`health`, `adminapi`) must never be routed. The public MAS listener has no `compat`
  resource, so password and legacy login are unreachable.
- Shipped templates never contain `discovery_mode: insecure`, `allow_insecure_uris` or
  anything else named insecure (`TestMASConfigTrustsOnlyKyIdentity`).
- Localpart: `preferred_username` lowercased, every character outside `[a-z0-9._=-]`
  replaced by `_` (minijinja loop, checked against MAS 1.26's environment); a missing claim
  renders empty and `action: require` refuses it; `on_conflict: fail` refuses collisions.
  A mapped localpart starting with `_` (e.g. a name beginning with a non-ASCII letter) is
  refused by Synapse's `check_username`, which MAS calls through `is_localpart_available`:
  sign-in fails closed. Never set `allow_underscore_prefixed_localpart`.
- Element contacts no third party: integrations are null, `element_call.disable`,
  `UIFeature.voip`/`UIFeature.widgets` off, `jitsi.preferred_domain` is `jitsi.invalid`
  (Element keeps its `meet.element.io` default for an object set to null, so the
  unresolvable name makes any Jitsi start fail closed), help links and the logo
  (`/app-icon.png`) point at the admin host, desktop-build promotion is off. No rendered
  value may name `element.io` or `vector.im`.
- Element themes "Busnes Light"/"Busnes Dark" copy `web/src/ky-ui/tokens.css`; keep them
  in step. `default_theme` is the light one, but Element's device-only `use_system_theme`
  (default on, not settable by config) makes browsers with a colour-scheme preference start
  on built-in light/dark; the Busnes themes are one click away in Appearance.
- AGPL rule: Synapse, MAS and Element are configured only through these files, never
  patched; no MAS template overrides; Element branding through `config.json` only.
- References used: MAS configuration
  https://element-hq.github.io/matrix-authentication-service/reference/configuration.html,
  MAS homeserver setup https://element-hq.github.io/matrix-authentication-service/setup/homeserver.html,
  MAS upstream providers and claims https://element-hq.github.io/matrix-authentication-service/setup/sso.html,
  MAS 1.26 template filters https://github.com/element-hq/matrix-authentication-service/blob/v1.26.0/crates/handlers/src/upstream_oauth2/template.rs,
  Element v1.12.30 config https://github.com/element-hq/element-web/blob/v1.12.30/docs/config.md,
  its defaults `apps/web/src/SdkConfig.ts`, theming `docs/theming.md`,
  Synapse 1.162 config https://github.com/element-hq/synapse/blob/v1.162.0/docs/usage/configuration/config_documentation.md.

## Verification
- `go test ./internal/matrixinit/ ./cmd/server/` (write-once secrets, modes, refusal before
  writing, closed Synapse, KyIdentity-only MAS, no injection, no secret printed).
