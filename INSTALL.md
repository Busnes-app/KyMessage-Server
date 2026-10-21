# KyMessage standalone installation

## Before you start

These steps are for a **new, independent installation** from this repository. Use the
README’s upgrade/restore procedure for an existing instance. Keep its volumes, keys,
configuration and Compose overlays; do not replace its environment files with examples.

Use Linux with Docker Engine and Docker Compose 2.24 or newer, Git, and permission to use Docker.
Rootless Podman is not supported yet: it maps the `--user` and `KY_UID` IDs these steps use to
subordinate host IDs that cannot write the checkout ([#75](https://github.com/Busnes-app/KyMessage-Server/issues/75)).
Choose a release/revision and use its matching Compose files and documentation. Image tags
can move: follow the README/Compose attestation and digest-pinning procedure before relying
on a published image. Source builds use the separate build overlay where provided.

Clone this repository, enter its directory, then run the product steps below:

```sh
git clone https://github.com/Busnes-app/KyMessage-Server.git
cd KyMessage-Server
```

Each service needs its own directory and persistent storage. Several products use host
port 8080 by default; install them on separate hosts or configure distinct host ports and
matching public origins. For remote users, provide a TLS reverse proxy and a hostname you
control. Trust only the proxy’s actual peer address, and forward the original scheme/client
address according to the product’s proxy guide.

## Install the operator console

The base service can run independently for operator/admin use. **It does not include
member chat.** Full chat requires the separately configured Matrix stack, KyIdentity,
HTTPS routes and Element. This is still a qualification deployment; read the
[product definition](docs/PRODUCT.md) and [Matrix setup](docs/CHAT-SETUP.md#matrix-chat) before
planning private team use.

In a new checkout, create a private Compose file as your non-root account:

```sh
mkdir -p data backups
[ -e .env ] || (umask 077; printf 'KY_UID=%s
KY_GID=%s
' "$(id -u)" "$(id -g)" > .env)
```

Set a generated `KY_ADMIN_PASSWORD` in the protected `.env` or export it privately before
Compose commands; keep it out of shell history. Review `KY_APP_URL` and the environment.
For a same-host preview, leave `http://localhost:8080` and the loopback host bind.

```sh
chmod 600 .env
docker compose config --quiet
docker compose pull
docker compose up -d
```

Open `http://localhost:8080`, sign in as `admin`, replace the bootstrap password and enable
MFA in **Settings → Account security**. Data and backups must belong to `KY_UID:KY_GID`.
Keep the persistent encryption key with the database.

For chat, follow the README’s complete Matrix initialization and proxy instructions,
including the two-pass MAS client-secret setup, closed registration, user assignment,
client key/device verification and recovery. A local administrator is an operator, not a
member chat account. Plain `docker compose up` and an “Open chat” link do not qualify an
encrypted conversation, calls, push, key recovery or federation policy.

Source builds retain `docker-compose.build.yml` alongside any Matrix/proxy overlays in
`COMPOSE_FILE`; use `docker compose up -d --build`. Recovery:
[server restore runbook](docs/RESTORE.md). Server backups do not replace users’ Matrix
recovery keys. `make ci` omits the separate `make matrix-acceptance` gate.

## Keep it recoverable

Preserve the database **and deployment keys**, not just the image. Configure recovery using
the linked restore runbook, retain an independent copy, and complete a scratch restore with
custodian cards before treating recovery as verified. A synthetic drill uses a throwaway
key and does not prove that the real cards are available or correct.

For upgrades, back up first, read the chosen release’s migration notes, and reuse the same
Compose project, volume mounts and overlay selection for every command. `docker compose
pull` followed by `docker compose up -d` updates a published-image installation; a digest pin
must be deliberately changed after verification. A source install must keep its build
overlay and rebuild. `docker compose down -v` deletes managed volumes: it is not an upgrade.

## What verification means

The startup checks above prove only the checks they perform. Use the README’s development
section and the repository CI workflow for source/build tests; a passing health response or
screenshot is not a substitute. Record the revision, commands, environment and skips when
reporting results. Design plans and dated evidence remain records of their stated scope.
