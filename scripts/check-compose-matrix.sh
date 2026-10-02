#!/usr/bin/env bash
# The Matrix overlay must publish nothing, pin every image by tag and digest, run the stateful
# services as the matrix-init owner, mount only each service's own ./matrix path read-only,
# pass the Postgres superuser password as a secret file and keep it out of the app's view, and
# keep composing with the proxy and static-IP overlays.
# The app's one writable ./matrix path is Element's config.json, whose brand the console sets.
# Uses throwaway values and ignores any local .env; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_SESSION_SECRET=check-only \
  KY_TRUSTED_PROXIES=10.91.0.10 KY_CONTAINER_IP=10.91.0.20 KY_MATRIX_UID=1234 KY_MATRIX_GID=5678 \
  KY_MATRIX_SERVER_NAME=example.com KY_MATRIX_HOST=https://matrix.example.com KY_MATRIX_CHAT_HOST=https://chat.example.com \
  KY_MATRIX_ADMIN_CLIENT_ID=01J0000000000000000000ADMN KY_KYIDENTITY_HMAC_SECRET=check-only
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
# Element stays stock: exactly its config.json at /app/config.json, read-only. No mask of its
# start script and no bind where nginx serves its copy (owner decision 2026-10-02).
[ "$(jq -r '.services.element.volumes[] | "\(.type) \(.source) \(.target) \(.read_only // false)"' <<<"$out")" = \
  "bind $root/matrix/element/config.json /app/config.json true" ] || bad "element does not mount exactly ./matrix/element/config.json at /app/config.json read-only"
jq -e '[.services.element.volumes[] | select(.source == "/dev/null" or (.target | startswith("/tmp/element-web-config")) or (.target | startswith("/docker-entrypoint")))] | length == 0' <<<"$out" >/dev/null \
  || bad "element's start-up is altered: a /dev/null, /tmp/element-web-config or /docker-entrypoint mount"
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
[ "$(jq -c '.services.synapse.networks | keys' <<<"$out")" = '["default","matrix-db"]' ] || bad "synapse is not on default and matrix-db"
[ "$(jq -c '.services.mas.networks | keys' <<<"$out")" = '["default","matrix-admin","matrix-db"]' ] || bad "mas is not on default, matrix-admin and matrix-db"
# MAS is probed through its public discovery resource; it has no internal port to reach.
jq -e '.services.synapse.healthcheck.test | index("--fail-early") and index("http://mas:8080/.well-known/openid-configuration")' <<<"$out" >/dev/null \
  || bad "synapse healthcheck does not probe MAS discovery with --fail-early"
# The admin port appears once: the app's URL. No service publishes or exposes it.
[ "$(grep -o 8081 <<<"$out" | wc -l)" = 1 ] || bad "MAS port 8081 is named more than once"
# Without the client secret matrix-init renders no MAS config; `up` must then refuse MAS, not
# let Docker create a directory in its place.
# Compose versions omit opposite defaults, so compare with this Compose's own rendering of false.
bindjson() { printf 'services: {p: {image: x, volumes: [{type: bind, source: /x, target: /x, bind: {create_host_path: %s}}]}}\n' "$1" \
  | docker compose --env-file /dev/null -f - config --format json | jq -c '.services.p.volumes[0].bind'; }
nocreate=$(bindjson false)
if [ -z "$nocreate" ] || [ "$nocreate" = "$(bindjson true)" ]; then bad "this docker compose cannot express create_host_path false"; fi
[ "$(jq -c '.services.mas.volumes' <<<"$out")" = "[{\"type\":\"bind\",\"source\":\"$root/matrix/mas/config.yaml\",\"target\":\"/config/config.yaml\",\"read_only\":true,\"bind\":$nocreate}]" ] \
  || bad "mas does not bind only ./matrix/mas/config.yaml read-only with create_host_path false: $(jq -c '.services.mas.volumes' <<<"$out")"
dep() { jq -r --arg s "$1" --arg d "$2" '.services[$s].depends_on[$d].condition // ""' <<<"$out"; }
[ "$(dep synapse postgres)" = service_healthy ] || bad "synapse does not wait for a healthy postgres"
[ "$(dep mas postgres)" = service_healthy ] || bad "mas does not wait for a healthy postgres"
[ "$(dep element synapse)" = service_healthy ] || bad "element does not wait for a healthy synapse"
[ "$(dep synapse synapse-media-owner)" = service_completed_successfully ] || bad "synapse starts before its media volume is owned"
# Docker re-copies the image's root-owned /media onto an empty volume at each mount without nocopy.
[ "$(jq -c '[.services.synapse.volumes[] | select(.source == "matrix-media") | .volume.nocopy]' <<<"$out")" = '[true]' ] \
  || bad "synapse mounts matrix-media without nocopy, which resets the media owner"
[ "$(jq -r '.services.postgres.environment.POSTGRES_PASSWORD_FILE' <<<"$out")" = /run/secrets/postgres_password ] \
  || bad "postgres superuser password is not read from its secret file"
[ "$(jq -r '.secrets.postgres_password.file' <<<"$out")" = "$root/matrix/secrets/postgres_password" ] \
  || bad "postgres_password secret is not ./matrix/secrets/postgres_password"
[ "$(jq -c '[.services[] | .secrets // [] | .[].source] | sort' <<<"$out")" = '["kybackup_db_password","mas_admin_client_secret","postgres_password"]' ] \
  || bad "secrets go to a service other than app (admin, backup) and postgres"

both=$(KY_NETWORK_SUBNET=10.91.0.0/24 render "${stack[@]}" -f "$root/docker-compose.static-ip.yml") || { echo "matrix + static-ip does not compose"; exit 1; }
[ "$(jq -r '.services.app.networks.default.ipv4_address' <<<"$both")" = 10.91.0.20 ] || bad "static IP lost with matrix overlay"
[ "$(jq -c '.services.app.environment | [.KY_MATRIX_SERVER_NAME, .KY_MATRIX_HOST, .KY_MATRIX_CHAT_HOST]' <<<"$out")" = '["example.com","https://matrix.example.com","https://chat.example.com"]' ] \
  || bad "app does not receive the Matrix locations"
# Offboarding: only mas and app reach the admin API, on an internal network, by alias.
admin_checks() {
  local j=$1
  [ "$(jq -r '.networks["matrix-admin"].internal' <<<"$j")" = true ] || bad "matrix-admin is not internal"
  members=$(jq -r '[.services | to_entries[] | select(.value.networks | has("matrix-admin")) | .key] | sort | join(",")' <<<"$j")
  [ "$members" = app,mas ] || bad "matrix-admin members are $members, want app,mas"
  [ "$(jq -c '.services.mas.networks["matrix-admin"].aliases' <<<"$j")" = '["mas-admin"]' ] || bad "mas lacks the mas-admin alias on matrix-admin"
}
admin_checks "$out"
admin_checks "$both"
[ "$(jq -c '.services.app.networks | keys' <<<"$both")" = '["default","matrix-admin","matrix-db"]' ] || bad "static-IP overlay drops an app network"
for k in default matrix-db; do
  jq -e --arg k "$k" '.services.mas.networks[$k].aliases // [] | index("mas-admin") | not' <<<"$out" >/dev/null || bad "mas-admin alias leaks onto $k"
done
[ "$(jq -r '.services.app.environment.KY_MATRIX_ADMIN_URL' <<<"$out")" = http://mas-admin:8081 ] || bad "app admin URL"
[ "$(jq -r '.services.app.environment.KY_MATRIX_ADMIN_SECRET_FILE' <<<"$out")" = /run/secrets/mas_admin_client_secret ] || bad "app admin secret path"
[ "$(jq -r '.secrets.mas_admin_client_secret.file' <<<"$out")" = "$root/matrix/secrets/mas_admin_client_secret" ] || bad "admin secret source"
jq -e '.services.app.environment | has("KY_MATRIX_ADMIN_CLIENT_SECRET") | not' <<<"$out" >/dev/null || bad "admin secret in env"
for v in KY_MATRIX_UID KY_MATRIX_GID KY_MATRIX_SERVER_NAME KY_MATRIX_HOST KY_MATRIX_CHAT_HOST KY_MATRIX_ADMIN_CLIENT_ID KY_KYIDENTITY_HMAC_SECRET; do
  err=$(env -u "$v" docker compose --env-file /dev/null --project-directory "$root" "${stack[@]}" config 2>&1 >/dev/null) \
    && { bad "matrix overlay accepted a missing $v"; continue; }
  grep -q "$v" <<<"$err" || bad "missing $v failed for another reason: $err"
done
# Backups: the app dumps on matrix-db as kybackup and reads ./matrix and the media store read-only.
[ "$(jq -c '.services.app.networks | keys' <<<"$out")" = '["default","matrix-admin","matrix-db"]' ] || bad "app is not on default, matrix-admin and matrix-db"
[ "$(jq -r '[.services | to_entries[] | select(.value.networks | has("matrix-db")) | .key] | sort | join(",")' <<<"$out")" = app,mas,postgres,synapse ] \
  || bad "matrix-db members are not app,mas,postgres,synapse"
for kv in KY_MATRIX_DIR=/matrix KY_MATRIX_MEDIA_DIR=/matrix-media KY_MATRIX_DB_HOST=postgres \
  KY_MATRIX_BACKUP_DB_PASSWORD_FILE=/run/secrets/kybackup_db_password KY_BACKUP_MEDIA_FULL_KEEP=3; do
  [ "$(jq -r --arg k "${kv%%=*}" '.services.app.environment[$k]' <<<"$out")" = "${kv#*=}" ] || bad "app ${kv%%=*} is not ${kv#*=}"
done
[ "$(jq -r '.secrets.kybackup_db_password.file' <<<"$out")" = "$root/matrix/secrets/kybackup_db_password" ] || bad "kybackup secret source"
appvol() { jq -r --arg t "$1" '.services.app.volumes[] | select(.target == $t) | [.type, .source, (.read_only // false)] | @tsv' <<<"$out"; }
# ./matrix piece by piece: each bind read-only at the same path under /matrix, never created by
# Docker, and none of them ./matrix, ./matrix/secrets or the Postgres superuser password.
# Element's config.json is the one exception, checked by rw_check below.
elementcfg="$root/matrix/element/config.json"
appmx=$(jq -c --arg r "$root/matrix" --arg e "$elementcfg" '[.services.app.volumes[] | select(.type == "bind" and .source != $e and (.source == $r or (.source | startswith($r + "/"))))]' <<<"$out")
jq -e --arg r "$root/matrix" --argjson nc "$nocreate" 'length > 0 and all(.[]; .target == "/matrix" + (.source | ltrimstr($r))
    and .read_only == true and .bind == $nc and .source != $r and .source != $r + "/secrets")' <<<"$appmx" >/dev/null \
  || bad "app ./matrix binds are not read-only, same-path, non-creating pieces: $appmx"
jq -e 'any(.[]; .source | test("postgres_password")) | not' <<<"$appmx" >/dev/null || bad "app can read the Postgres superuser password"
for d in synapse mas postgres; do
  jq -e --arg t "/matrix/$d" 'any(.[]; .target == $t)' <<<"$appmx" >/dev/null || bad "app does not mount ./matrix/$d"
done
# No app mount inside another: Docker cannot recreate a nested mountpoint inside a read-only
# bind, and `docker cp` from the app then fails.
nested=$(jq -c '[.services.app.volumes[].target] as $t | [$t[] as $a | $t[] | select(startswith($a + "/"))]' <<<"$out")
[ "$nested" = '[]' ] || bad "app mounts nested inside another app mount break docker cp: $nested"
# The app's only writable bind under ./matrix: Element's config.json (all it needs of
# ./matrix/element), never created by Docker. Element's own mount of it stays read-only (the
# per-service loop above).
rw_check() {
  local rw
  rw=$(jq -c --arg r "$root/matrix" '[.services.app.volumes[] | select(.type == "bind" and (.source == $r or (.source | startswith($r + "/"))) and (.read_only // false) == false)]' <<<"$1")
  jq -e --arg e "$elementcfg" --argjson nc "$nocreate" 'length == 1 and .[0].source == $e and .[0].target == "/matrix/element/config.json" and .[0].bind == $nc' <<<"$rw" >/dev/null \
    || bad "$2: the app's writable ./matrix binds must be exactly ./matrix/element/config.json at /matrix/element/config.json, create_host_path false: $rw"
}
rw_check "$out" "matrix overlay"
rw_check "$both" "matrix + static-ip"
[ "$(appvol /matrix-media)" = "$(printf 'volume\tmatrix-media\ttrue')" ] || bad "app does not mount matrix-media read-only at /matrix-media"
# pg_dump must match the server's major version: the image's client package against the postgres tag.
client=$(grep -E '^RUN apk .*postgresql[0-9]+-client' "$root/Dockerfile" | grep -oE 'postgresql[0-9]+-client' | grep -oE '[0-9]+')
server=$(jq -r '.services.postgres.image' <<<"$out" | sed -E 's/^postgres:([0-9]+).*/\1/')
{ [ -n "$client" ] && [ "$client" = "$server" ]; } || bad "Dockerfile installs postgresql${client}-client but postgres runs major $server"
# restore-matrix: a one-shot under the restore profile, as KY_MATRIX_UID:GID with no capability.
# The only writer of the media volume besides Synapse; everything else it mounts is read-only.
jq -e '.services | has("restore-matrix") | not' <<<"$out" >/dev/null || bad "restore-matrix runs without --profile restore"
rs=$(render "${stack[@]}" --profile restore) || { echo "restore profile does not compose"; exit 1; }
r=$(jq -c '.services["restore-matrix"]' <<<"$rs")
[ "$(jq -c '[.entrypoint, (.networks | keys), .user, .cap_drop, .cap_add, .security_opt, .profiles, .pull_policy, .environment,
    .depends_on.postgres.condition, .depends_on["synapse-media-owner"].condition]' <<<"$r")" \
  = '[["/app/kymessages","restore-matrix"],["matrix-db"],"1234:5678",["ALL"],null,["no-new-privileges:true"],["restore"],"never",null,"service_healthy","service_completed_successfully"]' ] \
  || bad "restore-matrix is not locked down: $r"
[ "$(jq -r .image <<<"$r")" = "$(jq -r .services.app.image <<<"$out")" ] || bad "restore-matrix does not run the app image"
want=$(printf '%s\n' "/app/backups	$root/backups	true" "/app/data	$root/data	true" "/matrix	$root/matrix	true" "/media	matrix-media	false")
[ "$(jq -r '.volumes[] | [.target, .source, (.read_only // false)] | @tsv' <<<"$r" | sort)" = "$want" ] \
  || bad "restore-matrix mounts: $(jq -c .volumes <<<"$r")"
[ "$(jq -c '[.volumes[] | select(.source == "matrix-media") | .volume.nocopy]' <<<"$r")" = '[true]' ] \
  || bad "restore-matrix mounts matrix-media without nocopy, which resets the media owner"
exit $fail
