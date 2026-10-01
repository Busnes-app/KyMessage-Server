#!/usr/bin/env bash
# The proxy overlay must publish nothing, name the network, and keep composing with the
# static-IP overlay. Uses throwaway values; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_CONTAINER_IP=10.91.0.20 KY_NETWORK_SUBNET=10.91.0.0/24
render() { docker compose --project-directory "$root" "$@" config --format json; }
fail=0
out=$(render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml") || exit 1
[ "$(jq '.services.app.ports // [] | length' <<<"$out")" = 0 ] || { echo "proxy overlay publishes a port"; fail=1; }
[ "$(jq -r '.networks.default.name' <<<"$out")" = kymessages-net ] || { echo "network not named kymessages-net"; fail=1; }
[ "$(jq -r '.services.app.environment.KY_ENV' <<<"$out")" = production ] || { echo "KY_ENV not production"; fail=1; }
both=$(render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml" -f "$root/docker-compose.static-ip.yml") || { echo "proxy + static-ip does not compose"; exit 1; }
[ "$(jq -r '.services.app.networks.default.ipv4_address' <<<"$both")" = 10.91.0.20 ] || { echo "static IP lost with proxy overlay"; fail=1; }
[ "$(jq -c "[.networks.default.ipam.config[].subnet]" <<<"$both")" = "[\"10.91.0.0/24\"]" ] || { echo "ipam subnet not merged to one entry"; fail=1; }
unset KY_APP_URL
if render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml" >/dev/null 2>&1; then echo "proxy overlay accepted a missing KY_APP_URL"; fail=1; fi
exit $fail
