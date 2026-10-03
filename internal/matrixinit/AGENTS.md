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
  `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`, `KY_KYIDENTITY_ISSUER`, `KY_MATRIX_MAS_CLIENT_ID`;
  `-dir` defaults to `./matrix` (git- and docker-ignored). `KY_ADMIN_HOST` is validated but
  not rendered into any config yet. The CLI refuses uid 0 (`runMatrixInit` takes the uid).
- KyIdentity generates the MAS client secret and shows it once, so it is never an env var:
  the operator saves it to `secrets/kyidentity_client_secret` (`ClientSecretFile`). Absent:
  the run renders everything but `mas/config.yaml`, sets `ClientSecretMissing`, and the CLI
  prints where to save it and exits 0 (two-pass setup). Present: one line (a trailing newline
  is dropped), non-empty, no control characters, 0600 or stricter, else refused.
- Hosts are https origins (no path, query, fragment or credentials); the issuer is https and
  kept byte for byte. Never add an http escape hatch: loopback overrides belong to the
  acceptance harness's scratch copy. Everything is validated before `dir` is created.
- Layout: `secrets/<name>` (write-once), `synapse/{homeserver.yaml,signing.key}`,
  `mas/config.yaml`, `element/config.json`, `postgres/init.sql`, `postgres/kybackup-role.sql`. Directories 0700; files
  0600 except `element/config.json` (0644, no secrets). Only directories it creates are
  chmodded; an existing one with group or other bits is refused (never lock down `-dir .`).
  A kept secret or signing key looser than 0600 is refused, not tightened: the operator
  should know it was readable.
- Secrets come from `crypto/rand` and are published with a hard link from a temp file, so
  they are never overwritten or half-written. An empty secret file is an error, never
  regenerated. `secrets/postgres_password` (Postgres superuser) is never rendered; Compose
  passes it as a secret file to postgres only. The app mounts `./matrix` piece by piece, every
  other secret included (`TestComposeMountsEverySecretButTheSuperusers`; add a new secret there). The capsule leaves it out, so after a
  restore `matrix-init` creates it and keeps every restored secret
  (`TestInitRegeneratesOnlyAMissingSecret`). `secrets/kybackup_db_password` is the read-only `kybackup` backup role's password;
  `kybackup-role.sql` is idempotent, sorts after `init.sql` for the entrypoint, and operators run it once
  on an existing stack (printed on the second pass). `secrets/upstream_provider_id` is the MAS provider ULID, kept because
  KyIdentity's redirect URI embeds it. Configs are re-rendered on every run: `element/config.json` in place (`O_TRUNC` on the
  existing inode, then `fchmod 0644`; created by rename only when absent; anything but a
  regular file is refused), because Compose binds that single file into Element and the app
  and a running container keeps the inode it was given; every other file by temp file and rename.
- Every rendered string goes through `q` (JSON quoting, a valid YAML scalar) or `sqlq`; a
  value cannot add keys.
- Container contract (`docker-compose.matrix.yml`, checked by `scripts/check-compose-matrix.sh`):
  Postgres, Synapse and MAS run as `KY_MATRIX_UID:KY_MATRIX_GID` so the 0600 files stay
  private; Postgres and Synapse mount only their own `./matrix/<service>` read-only, MAS only
  the file `./matrix/mas/config.yaml` (`create_host_path: false`), Element only the single 0644
  `config.json` (nginx cannot enter the 0700 dir) at the image's stock `/app/config.json`;
  the image serves a copy made at start, so a rename shows after `docker compose restart element`. The app's only read-write path under `./matrix` is `./matrix/element/config.json`
  (`create_host_path: false`), its only mount of `./matrix/element`, so the console can
  set `brand` (`internal/branding`); the file belongs to `KY_MATRIX_UID`, and the root app
  writes it through Docker's default `CAP_DAC_OVERRIDE` (a `cap_drop: [ALL]` on the app would
  make the write fail, which Settings then shows). Synapse reads `/config` (its dir) and writes media to `/media`;
  services reach each other as `postgres` (internal `matrix-db` network only), `synapse:8008`,
  `mas:8080`. MAS has a public `web` listener (no `compat`, so password and legacy login are
  unreachable; no `adminapi`) and an `admin` listener (`adminapi`, `oauth`) bound only to
  `mas-admin:8081`, an alias on the internal `matrix-admin` network; never proxied.
  `secrets/mas_admin_client_id` (ULID, printed as `KY_MATRIX_ADMIN_CLIENT_ID`) and
  `secrets/mas_admin_client_secret` (`client_secret_basic`) define the one MAS client, which is
  alone in `policy.data.admin_clients`. The KyIdentity provider sets
  `on_backchannel_logout: logout_all`; the URL for KyIdentity is `Registration.BackchannelLogoutURI`.
- Shipped templates never contain `discovery_mode: insecure`, `allow_insecure_uris` or
  anything else named insecure (`TestMASConfigTrustsOnlyKyIdentity`).
- Localpart (owner decision 2026-10-02): the `email` claim's local part (before the first
  `@`, minijinja `split`/`first`), lowercased, every character outside `[a-z0-9._=-]`
  replaced by `_`. The exact template was rendered with MAS 1.26's environment (minijinja and
  minijinja-contrib 2.21.0, `upstream_oauth2/template.rs` `environment()`) and is pinned in
  `TestMASConfigTrustsOnlyKyIdentity`. No email, an email without `@` or with an empty local
  part render empty, which `action: require` refuses (`RequiredAttributeEmpty`);
  `on_conflict: fail` refuses collisions (the same local part on another domain). The
  `email` import is `force`, not `require`, so the refusal is the localpart's.
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
