# Rootless Podman

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

Docker Engine is the release-gated runtime: CI and `make matrix-acceptance` run on it. Rootless
Podman works with the overlays below. They were checked once on Fedora 44 (Podman 5.8.7,
SELinux enforcing, Docker Compose v5.6 against the Podman socket): the console, the Matrix stack and
the meeting-notes bot all started, with every healthcheck passing. The matrix acceptance has
not been run under Podman.

Rootless Podman needs three changes to the Docker steps:

- **User IDs.** Rootless Podman maps a container's `--user` ID to a subordinate host ID, which
  cannot write the checkout. `--userns=keep-id` (`userns_mode: keep-id` in Compose) maps it to
  your own account, so `KY_UID` and `KY_MATRIX_UID` are what the Docker steps say they are.
- **SELinux.** On an enforcing host (Fedora, RHEL) a container cannot read a bind mount until
  it has the container label. `:z` applies the shared label, which every container of the
  stack may read; never `:Z` (private to one container, which locks out the others) and never
  `label=disable`.
- **Healthchecks.** Podman runs healthchecks from your systemd user manager (transient
  timers). Compose waits on them (`condition: service_healthy`), so where they cannot run, `up`
  never gets past Postgres. On a server you do not stay logged in to, run
  `loginctl enable-linger` once so your user manager keeps running.
  Check with `podman inspect --format '{{.State.Health.Status}}' <container>`
  (`docker compose ps` does not show Podman's health).

## Compose

Point Compose at the Podman socket: `systemctl --user enable --now podman.socket`, then either
`podman compose` (which runs Docker Compose or podman-compose) or `docker compose` with
`DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock`. Append the Podman overlays last
in `COMPOSE_FILE`, keeping the rest of the chain:

| With | Append |
| --- | --- |
| the console (and proxy, static-IP) | `docker-compose.podman.yml` |
| `docker-compose.matrix.yml` | also `docker-compose.matrix-podman.yml` |
| `docker-compose.notes.yml` | also `docker-compose.notes-podman.yml` |

For example: `COMPOSE_FILE=docker-compose.yml:docker-compose.proxy.yml:docker-compose.matrix.yml:docker-compose.podman.yml:docker-compose.matrix-podman.yml`.

They add only `userns_mode: keep-id` to each service that runs as `KY_UID`/`KY_MATRIX_UID`
(or chowns a volume to it) and the shared label to the app's `./data` and `./backups` and to
`restore-matrix`'s binds; every other setting, hardening included, is the Docker one.
`scripts/check-compose-podman.sh` holds them to that.

`docker-compose.proxy.yml`'s default subnet (`10.91.0.0/24`) must be free in Podman's networks
too; set `KY_NETWORK_SUBNET` (and `KY_NETWORK`) if it is taken.

## matrix-init

Create `./matrix` first and mount only it, labelled; files `matrix-init` writes there inherit
the label. Use this wherever [Chat setup](CHAT-SETUP.md) runs the `docker run` command:

```sh
mkdir -p -m 700 matrix
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" -v "$PWD/matrix:$PWD/matrix:z" -w "$PWD" \
  -e KY_MATRIX_SERVER_NAME -e KY_MATRIX_HOST -e KY_MATRIX_AUTH_HOST -e KY_MATRIX_CHAT_HOST \
  -e KY_MATRIX_RTC_HOST -e KY_MATRIX_MEDIA_IP -e KY_ADMIN_HOST -e KY_KYIDENTITY_ISSUER \
  -e KY_MATRIX_MAS_CLIENT_ID -e KY_MATRIX_CALLS -e KY_MATRIX_MEDIA_TCP_PORT -e KY_MATRIX_MEDIA_UDP_PORT \
  --entrypoint /app/kymessages \
  ghcr.io/busnes-app/kymessage-server@sha256:<digest> matrix-init -dir "$PWD/matrix"
```

For TURN, copy the certificate and key into a directory of their own (key mode 0600), mount
that with `:ro,z` and pass `KY_MATRIX_TURN_*` as container paths. Do not label the ACME
client's own directory: `:z` changes its label on the host too. If you ran a source-built `./kymessages matrix-init`
instead, label the directory once: `chcon -R -t container_file_t matrix`.

## Restore

[The restore runbook](RESTORE.md) works as written with the Podman overlays in `COMPOSE_FILE`:
`restore-matrix` relabels the restored `./data`, `./backups` and `./matrix` it reads, and the
app relabels `./data` and `./backups` when it starts.
