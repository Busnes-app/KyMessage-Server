# KyMessages

Private team conversations, on infrastructure you control.

KyMessages is moving to Matrix for chat, with suite identity and sealed server
backups. See the [Matrix platform design](docs/superpowers/specs/2026-10-01-matrix-platform-design.md).
It is not deployed or approved for private team use yet. The source-built application
currently provides the operator console and, once the Matrix stack below is configured,
an "Open chat" link for members; without it they see a page saying chat is not available.

- [Product scope and privacy contract](docs/PRODUCT.md)
- [Protocol and interoperability research](docs/KYMESSAGES-PROTOCOL-RESEARCH.md)
- [Server restore runbook](docs/RESTORE.md)
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

Encryption label: **End-to-end encrypted in Element (not independently audited)**. In the
acceptance run, every message Element sent to an encrypted room (direct and group) was stored
encrypted and its text appeared nowhere in the database, but Synapse does not enforce
encryption: a client or script that does not encrypt can post plaintext into an encrypted room,
and the server does not stop it. Members should use Element. Even with encryption, the server
sees room names and topics, membership, timestamps, display names and other metadata in
plaintext. Evidence: `docs/CHAT-PLATFORM-OPTIONS.md` section 7.

Setup, with the proxy overlay already working. `matrix-init` runs twice: KyIdentity shows the
client secret once, when you register the client the first run describes.

1. Export these for `matrix-init` (it reads the process environment, not `.env`). Hosts are
   https origins without a path: `KY_MATRIX_SERVER_NAME` (for example `example.com`),
   `KY_MATRIX_HOST`, `KY_MATRIX_AUTH_HOST`, `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`,
   `KY_KYIDENTITY_ISSUER` and `KY_MATRIX_MAS_CLIENT_ID` (a name you choose; not secret).
2. Run `./kymessages matrix-init` as the unprivileged user that will own the files
   (`-dir` defaults to `./matrix`; it refuses root, and an existing directory that group or
   others can read). It writes every config except MAS's, prints the KyIdentity registration
   values and `KY_MATRIX_UID`/`KY_MATRIX_GID`. Back up `matrix/secrets` and
   `matrix/synapse/signing.key`; they are never regenerated.
3. In KyIdentity, register the printed confidential client (client ID, redirect URI, back-channel
   logout URI, scopes `openid profile email`) and assign the users who may chat. Unassigned
   users cannot sign in. The back-channel logout URI is what ends a disabled user's open
   Element sessions; without it they keep working until their token expires.
4. Save the client secret KyIdentity shows to `matrix/secrets/kyidentity_client_secret` with
   mode 0600 (for example `install -m 600 /dev/null matrix/secrets/kyidentity_client_secret`,
   then paste it in with an editor). It lives only in that file, never in env or `.env`;
   `matrix-init` refuses it if group or others can read it.
5. Run `./kymessages matrix-init` again. It keeps every secret and now writes
   `matrix/mas/config.yaml`. Until then, `docker compose up` refuses MAS and the app with
   "bind source path does not exist". Whenever you re-run it on a running stack, apply the configs
   with `docker compose restart synapse mas element app` (the app mounts each secret file, so
   it sees a replaced `kyidentity_client_secret` only after a restart).
6. Pair a `suite_webhook` system in KyIdentity (callback `https://<host>/api/sso/kyidentity/sync`)
   and link it to the MAS client's app. KyIdentity shows its signing secret once: that is
   `KY_KYIDENTITY_HMAC_SECRET`. Its directory events are what lock and deactivate users in MAS.
   If MAS and KyMessages resolve only to private addresses (LAN-only), set
   `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true` on KyIdentity so it may call them.
7. Add to `.env`: `KY_MATRIX_UID` and `KY_MATRIX_GID` as printed, plus `KY_MATRIX_SERVER_NAME`,
   `KY_MATRIX_HOST`, `KY_MATRIX_CHAT_HOST` (KyMessages serves discovery and the chat link
   from them), `KY_MATRIX_ADMIN_CLIENT_ID` (printed by `matrix-init`) and
   `KY_KYIDENTITY_HMAC_SECRET`. With Matrix enabled, KyMessages refuses to start with missing
   admin settings (client ID, readable non-empty secret file) or no webhook secret. A wrong
   admin secret still starts, but every offboarding sweep then fails; the failure is logged
   once per failure streak (`[MATRIX] offboarding sweep failing`), so check the logs after
   start (console health comes later). Postgres, Synapse and MAS run as that user, the owner
   of `./matrix`, so its 0600 secrets stay unreadable to every other account; Compose
   refuses to start without these. If the UID or GID ever changes, `chown -R` `./matrix` and
   the `matrix-postgres` and `matrix-media` volumes (prefixed with the Compose project name)
   to the new owner.
8. Append `docker-compose.matrix.yml` to `COMPOSE_FILE`, after the proxy overlay, keeping the
   rest of the chain, then `docker compose up -d`.
9. In KyIdentity, open the system's deliveries, resume any held one, resync the system and
   confirm every assigned user shows delivered before members rely on chat. The sweep locks a
   MAS user KyMessages has no record of; it unlocks when that user's delivery lands.

Upgrading from the Matrix setup before offboarding:

1. Re-run `./kymessages matrix-init`. It keeps every secret, adds the MAS admin client and
   its secret file, and prints `KY_MATRIX_ADMIN_CLIENT_ID` and the back-channel logout URI.
2. Add that back-channel logout URI to the existing KyIdentity client.
3. Pair and link the `suite_webhook` system (step 6) if you have not, and set
   `KY_MATRIX_ADMIN_CLIENT_ID` and `KY_KYIDENTITY_HMAC_SECRET` in `.env`.
4. `docker compose up -d`, then `docker compose restart synapse mas element` so they read
   the new configs.
5. Do step 9: confirm every assignee shows delivered before relying on chat.

After start:

- Add the cloudflared routes, then open the chat host. Signed-in members see an "Open chat"
  link in KyMessages.
- `curl https://<server name>/.well-known/matrix/client` must show your `KY_MATRIX_HOST`.

Offboarding, as measured by `make matrix-acceptance` (cut within 30 s, in practice 0.3-3 s):

- **Disable or unassign** in KyIdentity: open Element sessions end and sign-in is refused;
  KyMessages locks the MAS user (sessions are gone either way, history stays).
- **Re-enable**: MAS unlocks within seconds. Sign in again on a new device and restore keys from
  key backup with the security key to read earlier history; a device whose session ended
  stays ended.
- **Delete**: MAS deactivates the user without erasing; they leave their rooms and colleagues
  still read their messages. A deactivated user is never reactivated.
- Offboard in KyIdentity, not MAS. A lock applied by hand in MAS to someone active in
  KyIdentity is undone by the next sweep.
- MAS's admin API listens on `mas-admin:8081` on an internal network only KyMessages and MAS
  join; it is never routed.
- **After any KyMessages outage**, open the system in KyIdentity and check its deliveries. A
  disable sent while KyMessages was down is held as an uncertain write ("operator recovery
  required") and is not retried: resume it (allowed after 60 s), then resync the system.
  Until then that user has no sessions and cannot sign in, but stays unlocked in MAS. Other
  failed deliveries KyIdentity retries itself.

Backups:

- The scheduled server capsule includes Matrix: configs, secrets, signing key and `pg_dump`s of
  Synapse and MAS (one-time keys excluded). It leaves out the Postgres superuser password;
  restore recreates it with `matrix-init`.
- What this costs: the app joins `matrix-db` and reads `./matrix` to back it up. The configs
  there hold the `synapse` and `mas` database owner passwords and the MAS-Synapse shared
  secret, so a compromised app can write both databases and act as Synapse admin. It cannot
  decrypt end-to-end encrypted messages. Compose hides the superuser password from it.
- Media is mirrored, encrypted, to `KY_BACKUP_DIR/media` after each scheduled run or
  `kymessages deposit`, with a full archive each month. Copy that directory off the host.
  `deposit` exits non-zero if the media step fails after a good deposit.
- The backup screen shows the capsule size, a warning from 75% of the 256 MiB limit and the
  last media run. "Run now" seals the capsule only.
- Restore: see [docs/RESTORE.md](docs/RESTORE.md).

Upgrading a stack from before Matrix backups:

1. Re-run `./kymessages matrix-init` (adds `kybackup_db_password` and `postgres/kybackup-role.sql`).
2. Run the role file once:
   `docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql`.
3. Rebuild the app image (no image is published), keep `KY_BACKUP_DIR` set, and optionally set
   `KY_BACKUP_MEDIA_FULL_KEEP`.
4. `docker compose up -d`. The first scheduled run then backs up Matrix.

The admin console for the stack is not built yet. `make matrix-acceptance` proves encrypted
storage in Element, a closed server, offboarding, and backup then restore of a lost host (needs
Docker, node and a KyIdentity checkout; see [AGENTS.md](AGENTS.md)).

## Identity and recovery configuration

Deployment is still unverified. Prepare these settings for the eventual HTTPS
instance; the current operator-console preview is not a chat release (see `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`):

| Setting | Purpose |
|---|---|
| `KY_APP_NAME` | Defaults to `KyMessages`; also the capsule service name, pinned at pairing |
| `KY_APP_URL` | Exact public origin used for OIDC, browser Origin checks |
| `KY_ENV=production` | Requires a durable `KY_SESSION_SECRET` and an `https` `KY_APP_URL` (unless `KY_COOKIE_SECURE=false`) |
| `KY_COOKIE_SECURE` | Defaults to true for production or any `https` `KY_APP_URL`; also turns on HSTS for the `KY_APP_URL` host |
| `KY_KYIDENTITY_ISSUER`, `KY_KYIDENTITY_CLIENT_ID`, `KY_KYIDENTITY_SECRET` | Suite KyIdentity OIDC client; register `https://<host>/api/sso/kyidentity/callback` as its redirect URI. The former `KY_KYSIGNON_*` names stop startup |
| `KY_KYIDENTITY_HMAC_SECRET` | Signing secret KyIdentity shows once when you pair a `suite_webhook` system; set that system's callback URL to `https://<host>/api/sso/kyidentity/sync`; required with the Matrix stack |
| `KY_TRUSTED_PROXIES` | Only the reverse proxy's own addresses/CIDRs, not the whole container network |
| `KY_SCIM_TOKEN` | Stable provisioning credential when SCIM is used; no automatic SCIM-to-room mapping |
| `KY_BACKUP_DIR`, `KY_BACKUP_KEEP` | Optional local sealed copies; keep newest N (default 7) |
| `KY_BACKUP_MEDIA_FULL_KEEP` | Monthly full media archives kept, default 3; below 1 fails startup |
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

Only SQLite has a supported capsule backup/restore path. The backup is one server
capsule (accounts and settings, plus the Matrix stack when enabled). Restore invalidates stale grants. See the
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
