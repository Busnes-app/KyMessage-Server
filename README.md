# KyMessages

Private team conversations, on infrastructure you control.

KyMessages is moving to Matrix for chat, with suite identity and sealed server
backups. See the [Matrix platform design](docs/superpowers/specs/2026-10-01-matrix-platform-design.md).
It is not deployed or approved for private team use yet. The source-built application
currently provides the operator console and, once the Matrix stack below is configured,
an "Open chat" link for members; without it they see a page saying chat is not available.

- [Product scope and privacy contract](docs/PRODUCT.md)
- [Protocol and interoperability research](docs/KYMESSAGES-PROTOCOL-RESEARCH.md)
- [SQLite capsule restore runbook](docs/RESTORE.md)
- [Repository contracts](AGENTS.md)

## Build and inspect locally

Use the Go version in `go.mod` and a Node version supported by the pinned frontend
packages. The committed `web/dist` is embedded in the binary. Rebuild it after UI
changes:

```sh
make all
./kymessages version
```

To inspect the operator console with disposable data, choose a bootstrap password
without putting it in shell history, then bind the server to loopback:

```sh
read -r -s -p 'Bootstrap operator password: ' KY_ADMIN_PASSWORD
export KY_ADMIN_PASSWORD
KY_HOST=127.0.0.1 ./kymessages
```

Open `http://localhost:8080`. Replace the bootstrap password before privileged use.
The local administrator operates the service; member access requires
suite OIDC.
The default data directory is `./data`; keep its encryption key with the database.
`make clean` removes build artifacts, never runtime data or backups.

## Local container build

There is no published KyMessages image yet. Compose defaults to `kymessages:local`
with pulling disabled, so it cannot silently start the upstream server-base image.
Build this checkout with `docker-compose.build.yml`. In a new `.env`, set:

```dotenv
COMPOSE_FILE=docker-compose.yml:docker-compose.build.yml
```

For an existing `.env`, append the build overlay to its current `COMPOSE_FILE` chain;
keep any LAN-DNS/static-IP overlays. The build overlay contains an idempotent append
snippet. Protect `.env` with mode 0600, export `KY_ADMIN_PASSWORD` as above, then run
`docker compose up --build`. The published port binds `127.0.0.1` by default. Container
names, binary and local image are KyMessages-specific; the retained Go module path
and token-sealing label are internal compatibility identifiers.

Optional `docker-compose.static-ip.yml` requires `KY_CONTAINER_IP` and
`KY_NETWORK_SUBNET`; otherwise Compose chooses addressing. Optional
`docker-compose.lan-dns.yml` requires `KY_DNS`. Add either to the existing overlay
chain. Do not replace that chain with an unrelated `-f` list.

Behind a reverse proxy, add `docker-compose.proxy.yml` (needs `KY_APP_URL`, `KY_SESSION_SECRET`
and `KY_TRUSTED_PROXIES`; publishes no port; names the network `kymessages-net`). Setup, cloudflared and nginx are in
[docs/Reverse_Proxy_Networking.md](docs/Reverse_Proxy_Networking.md).

## Matrix chat

Chat is Matrix: Synapse, Matrix Authentication Service (MAS) and Element Web, with KyIdentity
as the only sign-in. It publishes no port and expects cloudflared in front (routes in
[docs/Reverse_Proxy_Networking.md](docs/Reverse_Proxy_Networking.md)). Registration and
federation are closed and MAS's compatibility (password) login is not served.

Encryption label: **End-to-end encrypted in Element (not independently audited)**. Element
always encrypts, but Synapse does not enforce it: a client or script that does not encrypt can
post plaintext into an encrypted room. The server does not stop it. Members should use Element.
Evidence: `docs/CHAT-PLATFORM-OPTIONS.md` section 7.

Setup, with the proxy overlay already working:

1. Export these for the next step (`matrix-init` reads the process environment, not `.env`;
   `docker compose` reads `.env`, so put the same values there too). All hosts are https
   origins without a path:
   `KY_MATRIX_SERVER_NAME` (for example `example.com`), `KY_MATRIX_HOST`,
   `KY_MATRIX_AUTH_HOST`, `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`, `KY_KYIDENTITY_ISSUER`,
   `KY_MATRIX_MAS_CLIENT_ID` and `KY_MATRIX_MAS_CLIENT_SECRET`. Choose the client ID and secret
   now; the secret goes in `.env` with mode 0600.
2. Run `./kymessages matrix-init` as an unprivileged user (`-dir` defaults to `./matrix`).
   It writes the configs and secrets, prints the KyIdentity registration values and prints
   `KY_MATRIX_UID` and `KY_MATRIX_GID`. Back up `matrix/secrets` and
   `matrix/synapse/signing.key`; they are never regenerated.
3. Add `KY_MATRIX_UID` and `KY_MATRIX_GID` to `.env`. Postgres, Synapse and MAS run as that
   user, the owner of `./matrix`, so its 0600 secrets stay unreadable to every other account.
   Compose refuses to start without them. Do not run `matrix-init` as root: the containers
   would run as root.
4. In KyIdentity, register the printed confidential client (client ID, redirect URI, scopes
   `openid profile email`) and assign the users who may chat. Unassigned users cannot sign in.
5. Append `docker-compose.matrix.yml` to `COMPOSE_FILE`, after the proxy overlay, keeping the
   rest of the chain, then `docker compose up -d`.
6. Add the cloudflared routes, then open the chat host. Signed-in members see an "Open chat"
   link in KyMessages.

Not built yet: automatic offboarding (disabling a user in KyIdentity does not end live Matrix
sessions), Matrix backups, and the admin console for the stack. `make matrix-acceptance`
proves encrypted storage in Element and a closed server (needs Docker, node and a KyIdentity
checkout; see [AGENTS.md](AGENTS.md)).

## Identity and recovery configuration

Deployment is still unverified. Prepare these settings for the eventual HTTPS
instance; the current operator-console preview is not a chat release (see `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`):

| Setting | Purpose |
|---|---|
| `KY_APP_NAME` | Defaults to `KyMessages`; also the capsule service name, pinned at pairing |
| `KY_APP_URL` | Exact public origin used for OIDC, browser Origin checks |
| `KY_ENV=production` | Requires a durable `KY_SESSION_SECRET` and an `https` `KY_APP_URL` (unless `KY_COOKIE_SECURE=false`) |
| `KY_COOKIE_SECURE` | Defaults to true for production or any `https` `KY_APP_URL`; also turns on HSTS |
| `KY_KYIDENTITY_ISSUER`, `KY_KYIDENTITY_CLIENT_ID`, `KY_KYIDENTITY_SECRET` | Suite KyIdentity OIDC client; register `https://<host>/api/sso/kyidentity/callback` as its redirect URI. The former `KY_KYSIGNON_*` names stop startup |
| `KY_KYIDENTITY_HMAC_SECRET` | Signing secret KyIdentity shows once when you pair a `suite_webhook` system; set that system's callback URL to `https://<host>/api/sso/kyidentity/sync` |
| `KY_TRUSTED_PROXIES` | Only the reverse proxy's own addresses/CIDRs, not the whole container network |
| `KY_SCIM_TOKEN` | Stable provisioning credential when SCIM is used; no automatic SCIM-to-room mapping |
| `KY_BACKUP_DIR`, `KY_BACKUP_KEEP` | Optional local sealed copies; keep newest N (default 7) |
| `KY_BACKUP_DEPOSIT_INTERVAL` | Initial schedule, default `24h`; `0` disables, otherwise at least `15m`; admin UI overrides without restart |
| `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | Explicit LAN KyRecovery opt-in, off by default; HTTPS remains mandatory and loopback is refused |

Pin the suite public key manually or pair with KyRecovery, and compare its fingerprint
with the ceremony record. A pinned key needs at least one destination: local directory
or KyRecovery. One backup run seals once for both destinations and checks the remote
receipt digest. The backup screen shows destinations, schedule and the latest recorded attempt,
including scheduled/local-only failures and partial-success warnings. The last
successful remote receipt stays separate. Unpairing
keeps the key pin and local copies; separately revoke the product token at KyRecovery.
Never put custodian shares into the running server.

Only SQLite has a supported capsule backup/restore path. The backup is one people
capsule (accounts and settings). Restore invalidates stale grants. See the
[restore runbook](docs/RESTORE.md) before relying on backups. If a prior test pairing
used the scaffold's `Busnes.app` service name, preserve that explicit `KY_APP_NAME`
for its existing token/capsules; changing the default does not change KyRecovery's pin.

## Verification

`make ci` runs formatting, module checks, race tests, frontend tests and the running
server smoke checks. `make test-postgres` needs a disposable PostgreSQL 17 instance.
See [web/AGENTS.md](web/AGENTS.md) for production-CSP, keyboard and responsive browser
checks.
The smoke CI job also runs `python3 scripts/backup-acceptance.py`, a three-minute
disposable test of actual scheduler ticks, local-copy failures and live schedule
changes.
CI builds/runs the container, checks dependencies and verifies committed frontend
assets. It publishes no image while release gates remain open (see `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`).
