# Development

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

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

Open `http://localhost:8080`. Replace the bootstrap password before privileged use, then
turn on two-factor sign-in under Settings > Account security. To create or reset the local
administrator from a shell, run `./kymessages init-admin [-username <name>]`; it reads the
password from stdin (no echo on a terminal), never from the command line.
The local administrator operates the service; member access requires
suite OIDC.
The default data directory is `./data`; keep its encryption key with the database.
`make clean` removes build artifacts, never runtime data or backups.

## Local container build

To run this checkout instead, build it with `docker-compose.build.yml`. Its image is
`kymessages:local`, never pulled, and wins over `KY_IMAGE`. In a new `.env`, set:

```dotenv
COMPOSE_FILE=docker-compose.yml:docker-compose.build.yml
```

For an existing `.env`, append the build overlay to its current `COMPOSE_FILE` chain;
keep any LAN-DNS/static-IP overlays. The build overlay contains an idempotent append
snippet. Then run `docker compose up --build`.

Optional `docker-compose.static-ip.yml` requires `KY_CONTAINER_IP` and
`KY_NETWORK_SUBNET`; otherwise Compose chooses addressing. Optional
`docker-compose.lan-dns.yml` requires `KY_DNS`. Add either to the existing overlay
chain. Do not replace that chain with an unrelated `-f` list.

Behind a reverse proxy, add `docker-compose.proxy.yml` (needs `KY_APP_URL`, `KY_SESSION_SECRET`
and `KY_TRUSTED_PROXIES`; publishes no port; names the network `kymessages-net`). Setup, cloudflared and nginx are in
[docs/Reverse_Proxy_Networking.md](Reverse_Proxy_Networking.md).

## Verification

`make ci` runs formatting, module checks, race tests, frontend tests and the running
server smoke checks. `make test-postgres` needs a disposable PostgreSQL 17 instance.
See [web/AGENTS.md](../web/AGENTS.md) for production-CSP, keyboard and responsive browser
checks.
The smoke CI job also runs `python3 scripts/backup-acceptance.py`, a three-minute
disposable test of actual scheduler ticks, local-copy failures and live schedule
changes.
CI builds/runs the container, checks dependencies and verifies committed frontend
assets; it publishes nothing. The version lives only in `internal/appversion/VERSION`
(`X.Y.Z`). Publishing a GitHub release `vX.Y.Z` or `vX.Y.Z.N` runs
`.github/workflows/release-image.yml`: it refuses a tag whose `X.Y.Z` disagrees with
`internal/appversion/VERSION` or a commit on which any CI job did not pass, then publishes
`:X.Y.Z[.N]` with that version compiled in, attests and verifies it, and moves `:stable`
unless the release is a prerelease or older than the newest one.
`.github/workflows/dependency-release.yml` publishes the next `vX.Y.Z.N` once a day when
Dependabot changes merged since the last release and CI on `master` is green; run it by hand
to release `master` immediately. A new line starts with a hand-published `vX.Y.Z` after
bumping `VERSION`.
