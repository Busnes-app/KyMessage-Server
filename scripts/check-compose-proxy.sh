#!/usr/bin/env bash
# The proxy overlay must publish nothing, name the network, carry its own subnet, require
# its secrets, and keep composing with the static-IP overlay. Uses throwaway values and
# ignores any local .env; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_SESSION_SECRET=check-only KY_CONTAINER_IP=10.91.0.20
unset KY_NETWORK_SUBNET KY_NETWORK
render() { docker compose --env-file /dev/null --project-directory "$root" "$@" config --format json; }
proxy=(-f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml")
fail=0
out=$(render "${proxy[@]}") || exit 1
[ "$(jq '.services.app.ports // [] | length' <<<"$out")" = 0 ] || { echo "proxy overlay publishes a port"; fail=1; }
[ "$(jq -r '.networks.default.name' <<<"$out")" = kymessages-net ] || { echo "network not named kymessages-net"; fail=1; }
[ "$(jq -r '.services.app.environment.KY_ENV' <<<"$out")" = production ] || { echo "KY_ENV not production"; fail=1; }
[ "$(jq -c '[.networks.default.ipam.config[].subnet]' <<<"$out")" = '["10.91.0.0/24"]' ] || { echo "overlay subnet is not 10.91.0.0/24"; fail=1; }
both=$(KY_NETWORK_SUBNET=10.91.0.0/24 render "${proxy[@]}" -f "$root/docker-compose.static-ip.yml") || { echo "proxy + static-ip does not compose"; exit 1; }
[ "$(jq -r '.services.app.networks.default.ipv4_address' <<<"$both")" = 10.91.0.20 ] || { echo "static IP lost with proxy overlay"; fail=1; }
[ "$(jq -c '[.networks.default.ipam.config[].subnet]' <<<"$both")" = '["10.91.0.0/24"]' ] || { echo "ipam subnet not merged to one entry"; fail=1; }
# Each required variable must fail the render, and for its own reason.
for v in KY_APP_URL KY_SESSION_SECRET; do
  err=$(env -u "$v" docker compose --env-file /dev/null --project-directory "$root" "${proxy[@]}" config 2>&1 >/dev/null) \
    && { echo "proxy overlay accepted a missing $v"; fail=1; continue; }
  grep -q "$v" <<<"$err" || { echo "missing $v failed for another reason: $err"; fail=1; }
done
exit $fail
