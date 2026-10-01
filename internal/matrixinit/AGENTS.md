# Matrix init

## Purpose
Renders the configuration for the Matrix stack (Synapse, Matrix Authentication Service,
Element Web, Postgres init) from env, for `kymessages matrix-init`. KyIdentity is MAS's only
upstream sign-in.

## Ownership
Owns `matrixinit.go` and `templates/`. `cmd/server/matrixinit.go` is the CLI glue (flag,
env, printing); `docker-compose.matrix.yml` consumes the output layout.

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
  regenerated. `secrets/upstream_provider_id` is the MAS provider ULID, kept because
  KyIdentity's redirect URI embeds it. Configs are re-rendered on every run.
- Every rendered string goes through `q` (JSON quoting, a valid YAML scalar) or `sqlq`; a
  value cannot add keys.
- Container contract: Synapse reads `/config` (its dir) and writes media to `/media`;
  services reach each other as `postgres`, `synapse:8008`, `mas:8080`. MAS port 8081
  (`health`, `adminapi`) must never be routed. The public MAS listener has no `compat`
  resource, so password and legacy login are unreachable.
- Shipped templates never contain `discovery_mode: insecure`, `allow_insecure_uris` or
  anything else named insecure (`TestMASConfigTrustsOnlyKyIdentity`).
- Localpart: `preferred_username` lowercased, every character outside `[a-z0-9._=-]`
  replaced by `_` (minijinja loop, checked against MAS 1.26's environment); a missing claim
  renders empty and `action: require` refuses it; `on_conflict: fail` refuses collisions.
- AGPL rule: Synapse, MAS and Element are configured only through these files, never
  patched; no MAS template overrides; Element branding through `config.json` only.
- References used: MAS configuration
  https://element-hq.github.io/matrix-authentication-service/reference/configuration.html,
  MAS homeserver setup https://element-hq.github.io/matrix-authentication-service/setup/homeserver.html,
  MAS upstream providers and claims https://element-hq.github.io/matrix-authentication-service/setup/sso.html,
  MAS 1.26 template filters https://github.com/element-hq/matrix-authentication-service/blob/v1.26.0/crates/handlers/src/upstream_oauth2/template.rs,
  Synapse 1.162 config https://github.com/element-hq/synapse/blob/v1.162.0/docs/usage/configuration/config_documentation.md.

## Verification
- `go test ./internal/matrixinit/ ./cmd/server/` (write-once secrets, modes, refusal before
  writing, closed Synapse, KyIdentity-only MAS, no injection, no secret printed).
