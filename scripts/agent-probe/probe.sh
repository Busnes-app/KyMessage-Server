#!/usr/bin/env bash
# Task 0 of docs/superpowers/plans/2026-10-14-agent-accounts.md: proves the agent-token facts
# against the pinned Synapse/MAS/Postgres images. Prints one PASS/FAIL/VALUE line per fact,
# with the exact requests and responses under it (access tokens redacted).
# Own Compose project agentprobe-<pid>, loopback ports only; the exit trap runs `down -v` on
# that project only and deletes the scratch dir (PROBE_KEEP=1 keeps the dir).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
project=agentprobe-$$
pins=$repo/docker-compose.matrix.yml
for tool in docker curl jq openssl python3; do
	command -v "$tool" >/dev/null || { echo "probe: $tool is required" >&2; exit 2; }
done
img() { grep -m1 -o "image: $1[^ ]*" "$pins" | cut -d' ' -f2; }
pg_image=$(img postgres:)
syn_image=$(img ghcr.io/element-hq/synapse:)
mas_image=$(img ghcr.io/element-hq/matrix-authentication-service:)

scratch=$(mktemp -d /tmp/agent-probe.XXXXXX)
dc() { docker compose --progress quiet -p "$project" --project-directory "$scratch" -f "$scratch/compose.yml" "$@"; }
cleanup() {
	local rc=$?
	if ((rc != 0)) && [[ -f $scratch/compose.yml ]]; then dc logs --no-color --tail 30 >&2 || true; fi
	if [[ -f $scratch/compose.yml ]]; then
		dc down -v --timeout 10 >/dev/null 2>&1 || { echo "CLEANUP FAIL: down -v for $project" >&2; rc=1; }
	fi
	if [[ ${PROBE_KEEP:-0} == 1 ]]; then echo "probe: kept $scratch" >&2; else rm -rf "$scratch"; fi
	exit "$rc"
}
trap cleanup EXIT
umask 077

pass() { echo "PASS  $*"; }
fail() { echo "FAIL  $*"; }
value() { echo "VALUE $*"; }
now_ms() { date +%s%3N; }
rnd() { python3 -I -c "import secrets,sys;print(sys.argv[1]+''.join(secrets.choice(sys.argv[2]) for _ in range(int(sys.argv[3]))))" "$@"; }
redact() { jq -c 'walk(if type == "object" and has("access_token") then .access_token = "<redacted>" else . end)' 2>/dev/null || cat; }

# http METHOD URL TOKEN [JSON]: sets st and body; ev prints them as evidence.
http() {
	local args=(-sS -o "$scratch/resp" -w '%{http_code}' -X "$1" -H "Authorization: Bearer $3")
	[[ -n ${4-} ]] && args+=(-H 'Content-Type: application/json' --data "$4")
	st=$(curl "${args[@]}" "$2" 2>/dev/null) || st=000
	body=$(cat "$scratch/resp" 2>/dev/null || true)
}
ev() {
	local r
	r=$(redact <<<"$body")
	printf '      %s %s%s -> %s %s\n' "$1" "$2" "${4:+ $4}" "$st" "${r:0:700}"
}

# poll LIMIT_S CMD...: runs CMD once a second until it succeeds; waited is the time taken (ms).
poll() {
	local limit=$1 t0
	shift
	t0=$(now_ms)
	until "$@"; do
		if (($(now_ms) - t0 >= limit * 1000)); then waited=$(($(now_ms) - t0)); return 1; fi
		sleep 1
	done
	waited=$(($(now_ms) - t0))
}

# --- configuration, shaped like internal/matrixinit/templates (no upstream provider) ---
server=probe.test
client_id=$(rnd 01 0123456789ABCDEFGHJKMNPQRSTVWXYZ 24)
client_secret=$(openssl rand -hex 32)
shared=$(openssl rand -hex 32)
syn_db=$(openssl rand -hex 16) mas_db=$(openssl rand -hex 16)
mkdir -p "$scratch/pg-init" "$scratch/synapse/media" "$scratch/mas"
chmod 755 "$scratch" "$scratch/pg-init"
cat >"$scratch/pg-init/init.sql" <<EOF
CREATE USER synapse PASSWORD '$syn_db';
CREATE DATABASE synapse OWNER synapse ENCODING 'UTF8' LC_COLLATE='C' LC_CTYPE='C' TEMPLATE template0;
CREATE USER mas PASSWORD '$mas_db';
CREATE DATABASE mas OWNER mas;
EOF
chmod 644 "$scratch/pg-init/init.sql"
printf 'ed25519 a_probe %s\n' "$(openssl rand 32 | base64 | tr -d '=\n')" >"$scratch/synapse/signing.key"
openssl genrsa -traditional -out "$scratch/mas/rsa.pem" 2048 2>/dev/null
openssl ecparam -name prime256v1 -genkey -noout -out "$scratch/mas/ec.pem"
cat >"$scratch/synapse/homeserver.yaml" <<EOF
server_name: $server
public_baseurl: http://synapse:8008/
listeners:
  - port: 8008
    type: http
    tls: false
    x_forwarded: true
    resources:
      - names: [client, openid]
        compress: false
federation_domain_whitelist: []
database:
  name: psycopg2
  args: {user: synapse, password: "$syn_db", dbname: synapse, host: postgres, cp_min: 2, cp_max: 10}
media_store_path: /config/media
report_stats: false
macaroon_secret_key: "$(openssl rand -hex 32)"
form_secret: "$(openssl rand -hex 32)"
signing_key_path: /config/signing.key
trusted_key_servers: []
enable_registration: false
allow_guest_access: false
encryption_enabled_by_default_for_room_type: all
push:
  include_content: false
matrix_authentication_service:
  enabled: true
  endpoint: http://mas:8080/
  secret: "$shared"
experimental_features:
  msc4143_enabled: true
  msc4222_enabled: true
max_event_delay_duration: 24h
rc_message: {per_second: 0.5, burst_count: 30}
rc_delayed_event_mgmt: {per_second: 1, burst_count: 20}
matrix_rtc:
  transports:
    - type: livekit
      livekit_service_url: "http://admin.probe.test/api/matrix/rtc"
EOF
cat >"$scratch/mas/config.yaml" <<EOF
http:
  public_base: http://mas:8080/
  issuer: http://mas:8080/
  listeners:
    - name: web
      resources: [{name: discovery}, {name: human}, {name: oauth}, {name: graphql}, {name: assets}]
      binds: [{address: "[::]:8080"}]
    - name: admin
      resources: [{name: adminapi}, {name: oauth}]
      binds: [{address: "[::]:8081"}]
database: {host: postgres, username: mas, password: "$mas_db", database: mas}
secrets:
  encryption: "$(openssl rand -hex 32)"
  keys: [{key_file: /config/rsa.pem}, {key_file: /config/ec.pem}]
clients:
  - client_id: "$client_id"
    client_auth_method: client_secret_basic
    client_secret: "$client_secret"
policy:
  data:
    admin_clients: ["$client_id"]
passwords:
  enabled: false
account:
  password_registration_enabled: false
  password_change_allowed: false
  password_recovery_enabled: false
  email_change_allowed: false
  displayname_change_allowed: true
  account_deactivation_allowed: false
matrix:
  kind: synapse
  homeserver: $server
  secret: "$shared"
  endpoint: http://synapse:8008/
EOF
cat >"$scratch/compose.yml" <<EOF
services:
  postgres:
    image: $pg_image
    environment: {POSTGRES_PASSWORD: "$(openssl rand -hex 16)"}
    volumes: [pgdata:/var/lib/postgresql/data, ./pg-init:/docker-entrypoint-initdb.d:ro]
    healthcheck: {test: [CMD, pg_isready, -h, 127.0.0.1, -U, postgres], interval: 2s, retries: 60}
  mas:
    image: $mas_image
    user: "$(id -u):$(id -g)"
    command: [server, --config, /config/config.yaml]
    volumes: [./mas:/config:ro]
    ports: ["127.0.0.1::8081"]
    depends_on: {postgres: {condition: service_healthy}}
  synapse:
    image: $syn_image
    user: "$(id -u):$(id -g)"
    command: [run, --config-path, /config/homeserver.yaml]
    volumes: [./synapse:/config]
    ports: ["127.0.0.1::8008"]
    depends_on: {postgres: {condition: service_healthy}, mas: {condition: service_started}}
volumes:
  pgdata: {}
EOF

echo "== images: $pg_image | $syn_image | $mas_image"
dc up -d
syn=http://$(dc port synapse 8008)
mas=http://$(dc port mas 8081)
admin_token() {
	curl -fsS -u "$client_id:$client_secret" -d grant_type=client_credentials -d scope=urn:mas:admin "$mas/oauth2/token" 2>/dev/null | jq -r .access_token
}
up() { curl -fsS "$syn/health" >/dev/null 2>&1 && [[ -n $(admin_token) ]]; }
poll 240 up || { echo "probe: stack did not come up in 240s" >&2; exit 1; }
echo "== stack up in ${waited}ms; Synapse $syn, MAS admin $mas"

# madmin METHOD PATH [JSON]: MAS admin API with a fresh urn:mas:admin token.
madmin() { http "$1" "$mas/api/admin/v1/$2" "$(admin_token)" "${3-}"; }
# new_user NAME: sets uid.
new_user() {
	madmin POST users "{\"username\":\"$1\"}"
	ev POST "users" "" "{\"username\":\"$1\"}"
	uid=$(jq -r .data.id <<<"$body")
}
# mint USER_ID SCOPE EXPIRES_JSON_FRAGMENT: personal session; EXPIRES may be empty.
mint() { madmin POST personal-sessions "{\"actor_user_id\":\"$1\",\"human_name\":\"KyMessages agent\",\"scope\":\"$2\"$3}"; }
console_token() {
	mint "$console_id" "urn:matrix:client:api:* urn:synapse:admin:*" ',"expires_in":300'
	jq -r .data.attributes.access_token <<<"$body"
}
devices() { http GET "$syn/_synapse/admin/v2/users/$(jq -rn --arg u "$1" '$u|@uri')/devices" "$(console_token)"; }
whoami() { http GET "$syn/_matrix/client/v3/account/whoami" "$1"; }
agent_scope() { echo "urn:matrix:client:api:* urn:matrix:client:device:$1"; }

echo "== MAS admin API spec"
http GET "$mas/api/spec.json" ""
printf '%s' "$body" >"$scratch/spec.json"
jq -r '.paths | keys[] | select(test("personal-sessions"))' "$scratch/spec.json" | sed 's/^/      spec path: /'
jq -c '.components.schemas | to_entries[] | select(.key | test("PersonalSession"; "i")) | {(.key): (.value.properties // .value | if type == "object" then keys else . end)}' "$scratch/spec.json" | sed 's/^/      spec schema: /'
jq -c '[.. | objects | select(has("expires_in")) | .expires_in] | unique' "$scratch/spec.json" | sed 's/^/      spec expires_in: /'
jq -c '.paths["/api/admin/v1/personal-sessions/{id}/regenerate"].post | {summary, description, requestBody, responses: (.responses | keys)}' "$scratch/spec.json" | sed 's/^/      spec regenerate: /'
echo

echo "== setup: console-like user and agent user"
new_user kymessages-console
console_id=$uid
new_user agent-probe
agent_id=$uid
agent_mxid="@agent-probe:$server"

echo "== F1 personal session with a device scope; expiry"
dev=$(rnd '' ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 16)
mint "$agent_id" "$(agent_scope "$dev")" ''
ev POST personal-sessions "" "(no expires_in, device $dev)"
if [[ $st == 201 || $st == 200 ]]; then
	value "F1a expires_in omitted: $st, expires_at=$(jq -c .data.attributes.expires_at <<<"$body")"
	sess=$(jq -r .data.id <<<"$body") tok=$(jq -r .data.attributes.access_token <<<"$body")
else fail "F1a expires_in omitted: $st"; fi
for e in null 31536000 315360000 2147483647 4294967295 4294967296 9223372036854775807; do
	d2=$(rnd '' ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 16)
	mint "$agent_id" "$(agent_scope "$d2")" ",\"expires_in\":$e"
	ev POST personal-sessions "" "(expires_in $e)"
	value "F1b expires_in=$e: $st, expires_at=$(jq -c '.data.attributes.expires_at // .errors' <<<"$body" 2>/dev/null)"
	if [[ $st == 20? ]]; then madmin POST "personal-sessions/$(jq -r .data.id <<<"$body")/revoke"; fi
done
[[ -n ${tok-} ]] || { fail "F1 no unexpiring session to continue with"; exit 1; }
madmin GET "personal-sessions/$sess"
ev GET "personal-sessions/$sess"
has_device() { devices "$agent_mxid"; [[ $st == 200 ]] && jq -e --arg d "$dev" '.devices[] | select(.device_id == $d)' <<<"$body" >/dev/null; }
if poll 20 has_device; then
	pass "F1c device $dev on Synapse before the token is used (after ${waited}ms)"
else
	ev GET "_synapse/admin/v2/users/$agent_mxid/devices"
	value "F1c device $dev NOT on Synapse within 20s of minting, before first use"
fi
first_use=$(now_ms)
whoami "$tok"
ev GET whoami
if [[ $st == 200 && $(jq -r .user_id <<<"$body") == "$agent_mxid" && $(jq -r .device_id <<<"$body") == "$dev" ]]; then
	pass "F1d whoami: user_id=$agent_mxid device_id=$dev"
else fail "F1d whoami: $st $body"; fi
http GET "$syn/_matrix/client/v3/sync?timeout=0" "$tok"
[[ $st == 200 ]] && pass "F1e /sync: 200, next_batch=$(jq -r .next_batch <<<"$body")" || { ev GET sync; fail "F1e /sync: $st"; }
if poll 20 has_device; then pass "F1f device $dev on Synapse after first use"; else fail "F1f device $dev not on Synapse after use"; fi
ev GET "_synapse/admin/v2/users/$agent_mxid/devices"

echo "== F3 last activity (token first used at t=0)"
devices "$agent_mxid"
value "F3a Synapse last_seen_ts $(($(now_ms) - first_use))ms after first use: $(jq -c --arg d "$dev" '.devices[] | select(.device_id == $d) | .last_seen_ts' <<<"$body") (first_use=$first_use)"
madmin GET "personal-sessions/$sess"
value "F3b MAS personal session attribute keys: $(jq -c '.data.attributes | keys' <<<"$body")"
value "F3b MAS last_active_at right after use: $(jq -c .data.attributes.last_active_at <<<"$body")"
mas_active() { madmin GET "personal-sessions/$sess"; [[ $(jq -r .data.attributes.last_active_at <<<"$body") != null ]]; }
if poll 300 mas_active; then
	pass "F3c MAS last_active_at set $(($(now_ms) - first_use))ms after first use: $(jq -c '.data.attributes | {last_active_at, last_active_ip}' <<<"$body")"
else fail "F3c MAS last_active_at still null $(($(now_ms) - first_use))ms after first use"; fi
# Freshness: keep using the token once a second; when do MAS and Synapse move?
before_mas=$(jq -r .data.attributes.last_active_at <<<"$body")
devices "$agent_mxid"
before_syn=$(jq -r --arg d "$dev" '.devices[] | select(.device_id == $d) | .last_seen_ts' <<<"$body")
mas_moved() { whoami "$tok"; madmin GET "personal-sessions/$sess"; [[ $(jq -r .data.attributes.last_active_at <<<"$body") != "$before_mas" ]]; }
if poll 400 mas_moved; then
	value "F3d MAS last_active_at moved from $before_mas to $(jq -r .data.attributes.last_active_at <<<"$body") after ${waited}ms of use once a second"
else value "F3d MAS last_active_at did not move in ${waited}ms of use once a second (still $before_mas)"; fi
syn_moved() { whoami "$tok"; devices "$agent_mxid"; [[ $(jq -r --arg d "$dev" '.devices[] | select(.device_id == $d) | .last_seen_ts' <<<"$body") != "$before_syn" ]]; }
if poll 120 syn_moved; then
	value "F3e Synapse last_seen_ts moved from $before_syn to $(jq -r --arg d "$dev" '.devices[] | select(.device_id == $d) | .last_seen_ts' <<<"$body") after ${waited}ms of use once a second"
else value "F3e Synapse last_seen_ts did not move in ${waited}ms (still $before_syn)"; fi

echo "== F2 regenerate"
regen_path=$(jq -r '.paths | keys[] | select(test("personal-sessions/.*regenerate"))' "$scratch/spec.json")
if [[ -z $regen_path ]]; then
	value "F2 no regenerate path in MAS spec.json"
else
	value "F2 spec path $regen_path, request schema: $(jq -c '.components.schemas.RegeneratePersonalSessionRequest' "$scratch/spec.json")"
	madmin POST "personal-sessions/$sess/regenerate" '{}'
	ev POST "personal-sessions/$sess/regenerate" "" '{}'
	if [[ $st == 20? ]]; then
		newtok=$(jq -r .data.attributes.access_token <<<"$body")
		value "F2 regenerate: $st, session id $(jq -r .data.id <<<"$body") (was $sess), scope=$(jq -r .data.attributes.scope <<<"$body"), expires_at=$(jq -c .data.attributes.expires_at <<<"$body")"
		old_dead() { whoami "$tok"; [[ $st != 200 ]]; }
		if poll 300 old_dead; then pass "F2 old token rejected ${waited}ms after regenerate: $st $(jq -c . <<<"$body")"; else fail "F2 old token still accepted ${waited}ms after regenerate"; fi
		whoami "$newtok"
		ev GET whoami "" "(new token)"
		[[ $(jq -r .device_id <<<"$body") == "$dev" ]] && pass "F2 new token keeps device $dev" || fail "F2 new token device $(jq -r .device_id <<<"$body") != $dev"
		tok=$newtok
		devices "$agent_mxid"
		value "F2 Synapse devices after regenerate: $(jq -c '[.devices[].device_id]' <<<"$body")"
	fi
fi

echo "== F4 lock / unlock"
whoami "$tok"
[[ $st == 200 ]] || fail "F4 token not working before lock: $st"
# A second, never-used token: Synapse has no cached introspection for it.
fresh_dev=$(rnd '' ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 16)
mint "$agent_id" "$(agent_scope "$fresh_dev")" ''
fresh_tok=$(jq -r .data.attributes.access_token <<<"$body")
madmin POST "users/$agent_id/lock"
ev POST "users/$agent_id/lock"
whoami "$fresh_tok"
value "F4d locked: never-used token, first request right after lock: $st $(jq -c . <<<"$body")"
rejected() { whoami "$tok"; [[ $st != 200 ]]; }
accepted() { whoami "$tok"; [[ $st == 200 ]]; }
if poll 300 rejected; then
	pass "F4a locked: token rejected after ${waited}ms: $st $(jq -c . <<<"$body")"
else fail "F4a locked: token still accepted after ${waited}ms"; fi
http GET "$syn/_matrix/client/v3/sync?timeout=0" "$tok"
value "F4a /sync while locked: $st $(jq -c . <<<"$body")"
madmin GET "personal-sessions/$sess"
value "F4b session while locked: revoked_at=$(jq -c .data.attributes.revoked_at <<<"$body")"
madmin POST "users/$agent_id/unlock"
ev POST "users/$agent_id/unlock"
if poll 300 accepted; then pass "F4c unlock: token accepted again after ${waited}ms"; else fail "F4c unlock: token still rejected after ${waited}ms: $st $body"; fi

echo "== F5 revoke"
madmin POST "personal-sessions/$sess/revoke"
ev POST "personal-sessions/$sess/revoke"
if poll 300 rejected; then pass "F5 revoked: token rejected after ${waited}ms: $st $(jq -c . <<<"$body")"; else fail "F5 revoked: token still accepted after ${waited}ms"; fi
gone() { devices "$agent_mxid"; ! jq -e --arg d "$dev" '.devices[] | select(.device_id == $d)' <<<"$body" >/dev/null; }
if poll 30 gone; then value "F5 device $dev deleted from Synapse ${waited}ms after revoke"; else value "F5 device $dev still on Synapse 30s after revoke"; fi

echo "== F6 cross-signing bootstrap (fresh agent, no keys yet)"
new_user agent-bootstrap
b_id=$uid
b_mxid="@agent-bootstrap:$server"
b_dev=$(rnd '' ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 16)
mint "$b_id" "$(agent_scope "$b_dev")" ''
b_tok=$(jq -r .data.attributes.access_token <<<"$body")
python3 -m venv "$scratch/venv"
systemd-run --user --scope -q -p MemoryMax=4G -p MemorySwapMax=0 -- "$scratch/venv/bin/pip" install -q cryptography >/dev/null
PROBE_TOKEN=$b_tok "$scratch/venv/bin/python" -I "$here/xsign.py" "$syn" "$b_mxid" "$b_dev" >"$scratch/xsign.out"
sed 's/^/      /' "$scratch/xsign.out"
while read -r line; do
	s=$(jq -r .step <<<"$line") c=$(jq -r .status <<<"$line")
	value "F6 $s: $c $(jq -c '.body | if has("errcode") or has("flows") then . else keys end' <<<"$line")"
done <"$scratch/xsign.out"

echo "== F7 display name"
http PUT "$syn/_matrix/client/v3/profile/$(jq -rn --arg u "$b_mxid" '$u|@uri')/displayname" "$b_tok" '{"displayname":"Alice Admin"}'
ev PUT "profile/$b_mxid/displayname" "" '{"displayname":"Alice Admin"}'
put_st=$st
http GET "$syn/_matrix/client/v3/profile/$(jq -rn --arg u "$b_mxid" '$u|@uri')" "$b_tok"
value "F7 PUT displayname \"Alice Admin\": $put_st; profile now $(jq -c . <<<"$body")"

echo "== done"
