#!/usr/bin/env bash
# Rootless Podman overlays (docs/PODMAN.md): every service that runs as KY_UID or KY_MATRIX_UID
# maps that ID to the same host user (userns_mode keep-id), the app's ./data and ./backups and
# restore-matrix's binds carry the shared SELinux label (:z, never :Z, which would lock out the
# other containers), nothing turns SELinux labelling off, and the overlays change nothing else:
# hardening, ports, networks and mounts stay as the Docker chains render them.
# Uses throwaway values and ignores any local .env; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_SESSION_SECRET=check-only \
  KY_TRUSTED_PROXIES=10.91.0.10 KY_CONTAINER_IP=10.91.0.20 KY_MATRIX_UID=1234 KY_MATRIX_GID=5678 KY_UID=1234 KY_GID=5678 \
  KY_MATRIX_SERVER_NAME=example.com KY_MATRIX_HOST=https://matrix.example.com KY_MATRIX_CHAT_HOST=https://chat.example.com \
  KY_MATRIX_MEDIA_BIND_IP=192.0.2.10 KY_MATRIX_RTC_HOST=https://sfu.example.com KY_MATRIX_ADMIN_CLIENT_ID=01J0000000000000000000ADMN KY_KYIDENTITY_HMAC_SECRET=check-only
unset KY_NETWORK_SUBNET KY_NETWORK
render() { docker compose --env-file /dev/null --project-directory "$root" "$@" config --format json; }
fail=0
bad() { echo "$*"; fail=1; }
for f in docker-compose.podman.yml docker-compose.matrix-podman.yml docker-compose.notes-podman.yml; do
  [ -f "$root/$f" ] || { echo "$f is missing"; exit 1; }
done
o() { for x; do printf -- '-f %s/docker-compose.%s.yml ' "$root" "$x"; done; }
base="-f $root/docker-compose.yml" proxy=$(o proxy) matrix=$(o proxy matrix) notes=$(o notes) static=$(o static-ip)
pod=$(o podman) mpod=$(o podman matrix-podman) npod=$(o notes-podman)
# chain name, Docker chain, the same chain with the Podman overlays appended.
chains=(
  "console|$base|$base $pod"
  "proxy|$base $proxy|$base $proxy $pod"
  "matrix|$base $matrix|$base $matrix $mpod"
  "matrix+notes|$base $matrix $notes|$base $matrix $notes $mpod $npod"
  "matrix+static-ip|$base $matrix $static|$base $matrix $static $mpod"
)
for c in "${chains[@]}"; do
  IFS='|' read -r name dock podm <<<"$c"
  # shellcheck disable=SC2086 # the chains are word lists of -f arguments
  d=$(KY_NETWORK_SUBNET=10.91.0.0/24 render $dock --profile restore) || { bad "$name: Docker chain does not compose"; continue; }
  # shellcheck disable=SC2086
  p=$(KY_NETWORK_SUBNET=10.91.0.0/24 render $podm --profile restore) || { bad "$name: Podman chain does not compose"; continue; }
  # Every service running as the host user (KY_UID or KY_MATRIX_UID, or root chowning a volume to
  # it) maps it unchanged; nothing else gains a user-namespace setting.
  want=$(jq -c '[.services | to_entries[] | select((.value.user // "") == "1234:5678" or ((.value.entrypoint // []) | .[0:2] == ["chown", "1234:5678"])) | .key] | sort' <<<"$d")
  got=$(jq -c '[.services | to_entries[] | select(.value.userns_mode == "keep-id") | .key] | sort' <<<"$p")
  [ "$want" = "$got" ] || bad "$name: keep-id services are $got, want $want"
  jq -e '[.services[] | .userns_mode // empty | select(. != "keep-id")] | length == 0' <<<"$p" >/dev/null || bad "$name: a userns_mode other than keep-id"
  # SELinux stays on: only shared labels, never label=disable or a private :Z.
  jq -e '[.services[] | (.security_opt // [])[] | select(test("label"))] | length == 0' <<<"$p" >/dev/null || bad "$name: SELinux labelling altered"
  jq -e '[.services[] | (.volumes // [])[] | .bind.selinux // empty | select(. != "z")] | length == 0' <<<"$p" >/dev/null || bad "$name: a bind uses a label other than shared z"
  # Without the Podman-only keys and labels, the Podman chain renders exactly the Docker chain.
  strip='del(.services[].userns_mode) | (.services[].volumes // empty) |= map(del(.bind.selinux) | if .bind == {} then del(.bind) else . end)'
  norm='(.services[].volumes // empty) |= map(if .bind == {} then del(.bind) else . end)'
  [ "$(jq -S "$strip" <<<"$p")" = "$(jq -S "$norm" <<<"$d")" ] || bad "$name: the Podman overlays change more than userns_mode and SELinux labels: $(diff <(jq -S "$norm" <<<"$d") <(jq -S "$strip" <<<"$p") | head -20)"
  # ./data and ./backups get the shared label wherever they are mounted.
  jq -e --arg r "$root" '[.services[] | (.volumes // [])[] | select(.type == "bind" and (.source == $r + "/data" or .source == $r + "/backups"))] | length > 0 and all(.bind.selinux == "z")' <<<"$p" >/dev/null \
    || bad "$name: ./data or ./backups is mounted without the shared SELinux label"
done
# restore-matrix reads a restored ./matrix the host wrote, so it relabels it too, read-only.
# shellcheck disable=SC2086
r=$(render $base $matrix $mpod --profile restore | jq -c --arg r "$root" '[.services["restore-matrix"].volumes[] | select(.type == "bind") | [(.source | ltrimstr($r)), .read_only, .bind.selinux]] | sort')
[ "$r" = '[["/backups",true,"z"],["/data",true,"z"],["/matrix",true,"z"]]' ] || bad "restore-matrix binds: $r"
exit $fail
