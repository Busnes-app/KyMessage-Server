# KyMessages

Private team conversations, on infrastructure you control.

KyMessages is being built for small teams: encrypted direct messages and
invitation-only rooms, with suite identity and sealed server backups. It is not
deployed or approved for private team use yet. The working MLS chat client remains
in [mls-proof/](mls-proof/README.md), outside the embedded UI and container image.
The source-built application currently provides the operator console and messaging
API. Its console states that encrypted chat is not included in that build.

The isolated client exercises real encrypted delivery, fingerprint verification,
multiple rooms, durable retries, live wakeups, device recovery, retention and local
data removal on Chromium and Firefox. Independent MLS/application-profile security
review, interoperability, production client integration, deployed KyIdentity/HTTPS
checks and declared deployment limits remain release gates.

- [First-release execution plan](docs/FIRST-RELEASE-PLAN.md)
- [Product scope and privacy contract](docs/PRODUCT.md)
- [Messaging API](docs/MESSAGING-API.md)
- [Browser prototype and disposable OIDC fixture](mls-proof/README.md)
- [MLS library evidence](docs/MLS-LIBRARY-RESEARCH.md)
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
The local administrator operates the service; messaging member access requires
suite OIDC. Generic OIDC, SAML and local passwords do not grant messaging access.
The default data directory is `./data`; keep its encryption key with the database.
`make clean` removes build artifacts, never runtime data or backups.

For chat, use the separate [prototype instructions](mls-proof/README.md#interactive-chat-prototype)
with synthetic accounts/messages. Do not expose its disposable sign-in fixture.

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

## Identity and recovery configuration

Deployment is still unverified. Prepare these settings for the eventual HTTPS
instance; the current operator-console preview is not an encrypted-chat release:

| Setting | Purpose |
|---|---|
| `KY_APP_NAME` | Defaults to `KyMessages`; also the capsule service name, pinned at pairing |
| `KY_APP_URL` | Exact public origin used for OIDC, browser Origin checks and WebSockets |
| `KY_ENV=production` | Requires a durable `KY_SESSION_SECRET` and an `https` `KY_APP_URL` (unless `KY_COOKIE_SECURE=false`) |
| `KY_COOKIE_SECURE` | Defaults to true for production or any `https` `KY_APP_URL`; also turns on HSTS |
| `KY_KYSIGNON_ISSUER`, `KY_KYSIGNON_CLIENT_ID`, `KY_KYSIGNON_SECRET` | Suite KyIdentity integration; inherited environment names remain supported |
| `KY_KYSIGNON_HMAC_SECRET` | Signing secret KyIdentity shows once when you pair a `suite_webhook` system; set that system's callback URL to `https://<host>/api/sso/kysignon/sync` |
| `KY_TRUSTED_PROXIES` | Only the reverse proxy's own addresses/CIDRs, not the whole container network |
| `KY_MESSAGING_IDENTITY_RESET_ENABLED` | Off until deployed fresh-authentication/callback behavior is verified |
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

Only SQLite has a supported capsule backup/restore path. Backups are two capsules:
people (accounts, settings, no messaging data) and opt-in messages (threads, devices
without tokens), with their own schedule, receipts and `<KY_BACKUP_DIR>/messages`
copies; the messages schedule is off until an admin sets it. `deposit -messages` and
`backup-drill -messages` select it on the CLI. Restore invalidates stale grants;
`restore-messages` then optionally brings threads back with devices suspended until
their owners resume them. Without it, recovery uses fresh identities and new rooms. See the
[restore runbook](docs/RESTORE.md) before relying on backups. If a prior test pairing
used the scaffold's `Busnes.app` service name, preserve that explicit `KY_APP_NAME`
for its existing token/capsules; changing the default does not change KyRecovery's pin.

## Verification

`make ci` runs formatting, module checks, race tests, frontend tests and the running
server smoke checks. `make test-postgres` needs a disposable PostgreSQL 17 instance.
See [web/AGENTS.md](web/AGENTS.md) for production-CSP, keyboard and responsive browser
checks and [mls-proof/README.md](mls-proof/README.md) for the isolated crypto suites.
The smoke CI job also runs `python3 scripts/backup-acceptance.py`, a three-minute
disposable test of actual scheduler ticks, local-copy failures and live schedule
changes. `scripts/restore-messages-rehearsal.sh` runs the full people + messages
backup and restore path with the built binary and throwaway shares (not in CI).
CI builds/runs the container, checks dependencies and verifies committed frontend
assets. It publishes no image while the first-release gates remain open.

## Messaging storage preview

The administrator's Overview shows read-only messaging storage totals, room limits
and up to 100 rooms, with rooms near capacity listed first. Retention is per thread (Off, 1, 7, 30 or 90 days, default 90,
changeable by the owner); purged messages are deleted with their retry receipts and
per-message audit rows. These counts exclude actual
database overhead, audit logs, backup copies and browser history. Errors remain
visible instead of showing zero usage. Refresh explicitly for a new snapshot.

The encrypted client remains isolated and under review. [Transport measurements](docs/MESSAGING-LOAD.md)
and [browser evidence](docs/BROWSER-EVIDENCE.md) describe the tested subset and open
release gates; they do not establish deployed production E2EE.
