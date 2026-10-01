#!/usr/bin/env bash
# The Matrix overlay must publish nothing, pin every image by tag and digest, run the stateful
# services as the matrix-init owner, mount only each service's own ./matrix path read-only,
# pass the Postgres superuser password as a secret file, and keep composing with the proxy and
# static-IP overlays. Uses throwaway values and ignores any local .env; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_SESSION_SECRET=check-only \
  KY_TRUSTED_PROXIES=10.91.0.10 KY_CONTAINER_IP=10.91.0.20 KY_MATRIX_UID=1234 KY_MATRIX_GID=5678 \
  KY_MATRIX_SERVER_NAME=example.com KY_MATRIX_HOST=https://matrix.example.com KY_MATRIX_CHAT_HOST=https://chat.example.com
unset KY_NETWORK_SUBNET KY_NETWORK
render() { docker compose --env-file /dev/null --project-directory "$root" "$@" config --format json; }
stack=(-f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml" -f "$root/docker-compose.matrix.yml")
fail=0
bad() { echo "$*"; fail=1; }
out=$(render "${stack[@]}") || exit 1

declare -A image=(
  [postgres]=postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94
  [synapse]=ghcr.io/element-hq/synapse:v1.162.0@sha256:6b84a7bbac36f080b2d2e51e0289cf1b08b349598ea44a558df38d558f2c2311
  [mas]=ghcr.io/element-hq/matrix-authentication-service:1.26.0@sha256:e089f1048a1d4a9a492ed17b9fe759100f1bd619407b001f5927928d88b780c4
  [element]=ghcr.io/element-hq/element-web:v1.12.30@sha256:3e7dbd4424de9e11c812bc740acf2725b2abe66ea0d0802023b56fd85d3fa09a
  [synapse-media-owner]=ghcr.io/element-hq/synapse:v1.162.0@sha256:6b84a7bbac36f080b2d2e51e0289cf1b08b349598ea44a558df38d558f2c2311
)
# The named volume each service may use; every bind mount must be its own ./matrix/<service> path.
declare -A named=([postgres]=matrix-postgres [synapse]=matrix-media [mas]="" [element]="" [synapse-media-owner]=matrix-media)
declare -A own=([postgres]=postgres [synapse]=synapse [mas]=mas [element]=element [synapse-media-owner]=none)

[ "$(jq -c '[.services | keys[] | select(. != "app")] | sort' <<<"$out")" = '["element","mas","postgres","synapse","synapse-media-owner"]' ] \
  || bad "unexpected Matrix services: $(jq -c '.services | keys' <<<"$out")"
[ "$(jq '[.services[] | .ports // [] | length] | add' <<<"$out")" = 0 ] || bad "a service publishes a port"
[ "$(jq -r '.networks.default.name' <<<"$out")" = kymessages-net ] || bad "network not named kymessages-net"
for s in "${!image[@]}"; do
  svc=$(jq -c --arg s "$s" '.services[$s]' <<<"$out")
  [ "$(jq -r .image <<<"$svc")" = "${image[$s]}" ] || bad "$s image is not ${image[$s]}"
  [ "$(jq -r '.network_mode // "default"' <<<"$svc")" != host ] || bad "$s uses host networking"
  [ "$(jq -c '.security_opt' <<<"$svc")" = '["no-new-privileges:true"]' ] || bad "$s lacks no-new-privileges"
  [ "$(jq -c '.cap_drop' <<<"$svc")" = '["ALL"]' ] || bad "$s keeps default capabilities"
  # Values, not just names: a password must never be a plain environment value.
  jq -e '.environment // {} | to_entries | any(.key | test("PASSWORD|SECRET|KEY|TOKEN") and (endswith("_FILE") | not))' <<<"$svc" >/dev/null \
    && bad "$s has a secret in plain env"
  while IFS=$'\t' read -r type src target ro; do
    case $type in
      volume) [ "$src" = "${named[$s]}" ] || bad "$s mounts volume $src" ;;
      bind)
        case $src in "$root/matrix/${own[$s]}" | "$root/matrix/${own[$s]}/"*) ;; *) bad "$s binds $src outside ./matrix/${own[$s]}" ;; esac
        [ "$ro" = true ] || bad "$s bind $src -> $target is writable" ;;
      *) bad "$s has a $type mount" ;;
    esac
  done < <(jq -r '.volumes // [] | .[] | [.type, .source, .target, (.read_only // false)] | @tsv' <<<"$svc")
done
for s in postgres synapse mas element; do
  [ "$(jq -r --arg s "$s" '.services[$s].restart' <<<"$out")" = unless-stopped ] || bad "$s restart is not unless-stopped"
done
for s in postgres synapse mas; do
  [ "$(jq -r --arg s "$s" '.services[$s].user' <<<"$out")" = 1234:5678 ] || bad "$s does not run as KY_MATRIX_UID:KY_MATRIX_GID"
done
for s in postgres synapse element; do
  [ "$(jq -r --arg s "$s" '.services[$s].healthcheck.test[0] // ""' <<<"$out")" = CMD ] || bad "$s has no exec healthcheck"
done
# The media chown is the only root step: no network, only CAP_CHOWN, one fixed command.
owner=$(jq -c '.services["synapse-media-owner"]' <<<"$out")
[ "$(jq -c '[.cap_drop, .cap_add, .network_mode, .read_only, .entrypoint, .command]' <<<"$owner")" = '[["ALL"],["CHOWN"],"none",true,["chown","1234:5678","/media"],null]' ] \
  || bad "synapse-media-owner is not locked down: $owner"
# Postgres sits only on an internal network; Synapse and MAS bridge to it.
[ "$(jq -c '.services.postgres.networks | keys' <<<"$out")" = '["matrix-db"]' ] || bad "postgres is not only on matrix-db"
[ "$(jq -r '.networks["matrix-db"].internal' <<<"$out")" = true ] || bad "matrix-db is not internal"
for s in synapse mas; do
  [ "$(jq -c --arg s "$s" '.services[$s].networks | keys' <<<"$out")" = '["default","matrix-db"]' ] || bad "$s is not on default and matrix-db"
done
# MAS is probed through its public discovery resource; it has no internal port to reach.
jq -e '.services.synapse.healthcheck.test | index("--fail-early") and index("http://mas:8080/.well-known/openid-configuration")' <<<"$out" >/dev/null \
  || bad "synapse healthcheck does not probe MAS discovery with --fail-early"
grep -q 8081 <<<"$out" && bad "the stack still names MAS port 8081"
# Without the client secret matrix-init renders no MAS config; `up` must then refuse MAS, not
# let Docker create a directory in its place.
[ "$(jq -c '.services.mas.volumes' <<<"$out")" = "[{\"type\":\"bind\",\"source\":\"$root/matrix/mas/config.yaml\",\"target\":\"/config/config.yaml\",\"read_only\":true,\"bind\":{\"create_host_path\":false}}]" ] \
  || bad "mas does not bind only ./matrix/mas/config.yaml read-only with create_host_path false: $(jq -c '.services.mas.volumes' <<<"$out")"
dep() { jq -r --arg s "$1" --arg d "$2" '.services[$s].depends_on[$d].condition // ""' <<<"$out"; }
[ "$(dep synapse postgres)" = service_healthy ] || bad "synapse does not wait for a healthy postgres"
[ "$(dep mas postgres)" = service_healthy ] || bad "mas does not wait for a healthy postgres"
[ "$(dep element synapse)" = service_healthy ] || bad "element does not wait for a healthy synapse"
[ "$(dep synapse synapse-media-owner)" = service_completed_successfully ] || bad "synapse starts before its media volume is owned"
[ "$(jq -r '.services.postgres.environment.POSTGRES_PASSWORD_FILE' <<<"$out")" = /run/secrets/postgres_password ] \
  || bad "postgres superuser password is not read from its secret file"
[ "$(jq -r '.secrets.postgres_password.file' <<<"$out")" = "$root/matrix/secrets/postgres_password" ] \
  || bad "postgres_password secret is not ./matrix/secrets/postgres_password"
[ "$(jq -c '[.services[] | .secrets // [] | .[].source]' <<<"$out")" = '["postgres_password"]' ] \
  || bad "a service other than postgres receives a secret"

both=$(KY_NETWORK_SUBNET=10.91.0.0/24 render "${stack[@]}" -f "$root/docker-compose.static-ip.yml") || { echo "matrix + static-ip does not compose"; exit 1; }
[ "$(jq -r '.services.app.networks.default.ipv4_address' <<<"$both")" = 10.91.0.20 ] || bad "static IP lost with matrix overlay"
[ "$(jq -c '.services.app.environment | [.KY_MATRIX_SERVER_NAME, .KY_MATRIX_HOST, .KY_MATRIX_CHAT_HOST]' <<<"$out")" = '["example.com","https://matrix.example.com","https://chat.example.com"]' ] \
  || bad "app does not receive the Matrix locations"
for v in KY_MATRIX_UID KY_MATRIX_GID KY_MATRIX_SERVER_NAME KY_MATRIX_HOST KY_MATRIX_CHAT_HOST; do
  err=$(env -u "$v" docker compose --env-file /dev/null --project-directory "$root" "${stack[@]}" config 2>&1 >/dev/null) \
    && { bad "matrix overlay accepted a missing $v"; continue; }
  grep -q "$v" <<<"$err" || bad "missing $v failed for another reason: $err"
done
exit $fail
