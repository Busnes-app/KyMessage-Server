#!/usr/bin/env bash
# Matrix stack acceptance: a throwaway KyIdentity, `kymessages matrix-init` and
# docker-compose.matrix.yml on loopback, Element driven by Playwright. Proves that messages in
# encrypted rooms are stored encrypted and that the server is closed (no registration,
# password login or federation; unassigned KyIdentity users refused).
#
# Loopback without weakening shipped configs: a harness TLS proxy with a throwaway CA answers
# for the https hosts; MAS trusts that CA, the browser pins the proxy key. Everything runs in
# Compose project kymatrix-accept-<pid> with its own network and volumes; the exit trap runs
# `down -v` on that project only, removes its KyIdentity image and deletes the scratch directory.
#
# Env: KYIDENTITY_SRC (KyIdentity-server checkout, default ../KyIdentity-server),
# MATRIX_ACCEPT_REPRODUCE=1 (also reproduce the spike's compatibility-login sign-in; CI sets it),
# MATRIX_ACCEPT_ARTIFACTS (traces and logs on failure, default scripts/matrix-acceptance/artifacts).
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
here=$repo/scripts/matrix-acceptance
kyid_src=${KYIDENTITY_SRC:-$repo/../KyIdentity-server}
reproduce=${MATRIX_ACCEPT_REPRODUCE:-0}
artifacts=${MATRIX_ACCEPT_ARTIFACTS:-$here/artifacts}/kymatrix-accept-$$
project=kymatrix-accept-$$
kyid_image=$project-kyidentity:local

for tool in docker go node npm openssl curl jq; do
	command -v "$tool" >/dev/null || { echo "matrix-acceptance: $tool is required" >&2; exit 2; }
done
[[ -f $kyid_src/Dockerfile ]] || { echo "matrix-acceptance: no KyIdentity checkout at $kyid_src (set KYIDENTITY_SRC)" >&2; exit 2; }

scratch=$(mktemp -d -t kymatrix-accept.XXXXXX)
# Until cleanup is defined below, an early exit still removes the scratch dir.
trap 'rm -rf "$scratch"' EXIT
state=$scratch/state
summary=$scratch/summary

umask 077
mkdir -p "$state" "$scratch/tls" "$scratch/nginx-extra"
cp -r "$here/overrides" "$scratch/overrides"
chmod -R a+rX "$scratch/overrides" "$scratch/nginx-extra"

# Compose interpolation. The base file's app service is never started; its required values
# get throwaway placeholders. The network name is project-scoped, never kymessages-net.
export KY_NETWORK=$project-net
KY_MATRIX_UID=$(id -u) KY_MATRIX_GID=$(id -g)
export KY_MATRIX_UID KY_MATRIX_GID
export KY_ADMIN_PASSWORD=unused-by-the-harness
export KY_MATRIX_SERVER_NAME=kymatrix.test
export KY_MATRIX_HOST=https://matrix.kymatrix.test
export KY_MATRIX_AUTH_HOST=https://auth.kymatrix.test
export KY_MATRIX_CHAT_HOST=https://chat.kymatrix.test
export KY_ADMIN_HOST=https://admin.kymatrix.test
export KY_KYIDENTITY_ISSUER=https://id.kymatrix.test
export KY_MATRIX_MAS_CLIENT_ID=kymatrix-mas
KYMATRIX_ACCEPT_ADMIN_PASS=$(openssl rand -hex 16)
export KYMATRIX_ACCEPT_ADMIN_PASS
export KYMATRIX_ACCEPT_KYID_IMAGE=$kyid_image

dc() {
	docker compose --progress quiet -p "$project" --project-directory "$scratch" \
		-f "$repo/docker-compose.yml" -f "$repo/docker-compose.matrix.yml" \
		-f "$scratch/overrides/compose.yml" "$@"
}

current=setup t0=$SECONDS started=$SECONDS
step() { current=$1 t0=$SECONDS; echo "== $1"; }
pass() { printf 'PASS %-14s %4ss\n' "$current" "$((SECONDS - t0))" | tee -a "$summary"; }

cleanup() {
	local rc=$?
	if ((rc != 0)); then
		printf 'FAIL %-14s %4ss\n' "$current" "$((SECONDS - t0))" >>"$summary"
		mkdir -p "$artifacts"
		dc logs --no-color >"$artifacts/compose.log" 2>&1 || true
		echo "matrix-acceptance: logs and traces in $artifacts" >&2
	fi
	dc down -v --timeout 10 >/dev/null 2>&1 || echo "matrix-acceptance: down -v failed for project $project" >&2
	docker image rm "$kyid_image" >/dev/null 2>&1 || true
	echo "== summary (total $((SECONDS - started))s)"
	[[ -f $summary ]] && cat "$summary"
	rm -rf "$scratch"
	((rc == 0)) && echo "matrix-acceptance: PASS" || echo "matrix-acceptance: FAIL"
	exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

ok() { echo "  ok: $1"; }
# expect ACTUAL EXPECTED WHAT
expect() {
	[[ $1 == "$2" ]] || { echo "  FAILED: $3 (got '$1', want '$2')" >&2; return 1; }
	ok "$3"
}
sql() { dc exec -T postgres psql -U postgres -d "$1" -Atc "$2"; }
hcurl() { curl -sS --cacert "$scratch/tls/ca.crt" --connect-to "::$tls_addr" "$@"; }
# status METHOD URL [curl args...]: the HTTP status only.
status() { local m=$1 u=$2; shift 2; hcurl -o /dev/null -w '%{http_code}' -X "$m" "$@" "$u"; }
# answer METHOD URL [curl args...]: "<status> <errcode>"; the body stays in state/answer.json.
answer() {
	local m=$1 u=$2 code
	shift 2
	code=$(hcurl -o "$state/answer.json" -w '%{http_code}' -X "$m" "$@" "$u")
	echo "$code $(jq -r '.errcode // "-"' "$state/answer.json" 2>/dev/null || echo unparsable)"
}
# Readiness poll with a deadline (30 tries, 1s apart).
ready() { hcurl -fs -o /dev/null --retry 30 --retry-delay 1 --retry-all-errors "$1" 2>/dev/null || { echo "  FAILED: $1 not ready" >&2; return 1; }; }
kyid() { "$here/kyid-admin.sh" "$@"; }
e2e() { node "$here/e2e.mjs" "$1"; }
matrix_init() { "$scratch/kymessages" matrix-init -dir "$scratch/matrix"; }

# ---------------------------------------------------------------------------------------
step build
go build -C "$repo" -o "$scratch/kymessages" ./cmd/server
docker build -q -t "$kyid_image" "$kyid_src" >/dev/null
npm ci --prefix "$here" --no-audit --no-fund --silent
ok "kymessages, KyIdentity ($(git -C "$kyid_src" rev-parse --short HEAD 2>/dev/null || echo unknown)) and Playwright built"
pass

# ---------------------------------------------------------------------------------------
step matrix-init
# no_insecure WHAT PATH...: passes only on grep's "no match" (1); a match (0) or a missing or
# unreadable path (2) fails.
no_insecure() {
	local what=$1 rc=0
	shift
	grep -rniE 'insecure' "$@" || rc=$?
	((rc == 1)) || { echo "  FAILED: $what mention insecure or are unreadable (grep rc $rc)" >&2; return 1; }
	ok "$what contain no 'insecure'"
}
rendered=("$scratch/matrix/synapse/homeserver.yaml" "$scratch/matrix/mas/config.yaml" "$scratch/matrix/element/config.json")

# Shipped templates and overlay carry no insecure setting (Task 1 pins it; repeated here).
no_insecure "shipped templates and docker-compose.matrix.yml" "$repo/internal/matrixinit/templates" "$repo/docker-compose.matrix.yml"

# Throwaway CA and one leaf for the four https hosts.
(
	cd "$scratch/tls"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout ca.key -out ca.crt \
		-days 1 -subj "/CN=kymatrix-accept throwaway CA" 2>/dev/null
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout leaf.key -out leaf.csr \
		-subj "/CN=kymatrix.test" 2>/dev/null
	printf '%s\n' 'subjectAltName=DNS:kymatrix.test,DNS:*.kymatrix.test' 'basicConstraints=CA:FALSE' \
		'keyUsage=digitalSignature' 'extendedKeyUsage=serverAuth' >ext.cnf
	openssl x509 -req -in leaf.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out leaf.crt -days 1 \
		-extfile ext.cnf 2>/dev/null
	chmod 0644 ca.crt
	openssl x509 -pubkey -noout -in leaf.crt | openssl pkey -pubin -outform der |
		openssl dgst -sha256 -binary | base64 >spki
)
ok "throwaway CA and loopback certificate"

# First pass: the client secret does not exist until the client is registered with the
# redirect URI this prints. The second pass (kyidentity step) renders the real one.
KY_MATRIX_MAS_CLIENT_SECRET=pending-registration matrix_init >"$state/init1.out"
redirect_uri=$(awk '$1 == "redirect" && $2 == "URI" { print $3 }' "$state/init1.out")
expect "$redirect_uri" "$KY_MATRIX_AUTH_HOST/upstream/callback/$(cat "$scratch/matrix/secrets/upstream_provider_id")" \
	"matrix-init printed the redirect URI"
no_insecure "rendered configs (used unmodified)" "${rendered[@]}"
pass

# ---------------------------------------------------------------------------------------
step stack
if dc config | grep -q 'kymessages-net'; then
	echo "  FAILED: the harness would join kymessages-net" >&2
	false
fi
ok "network $KY_NETWORK, project-scoped"
dc up -d --quiet-pull --wait --wait-timeout 300 tls >/dev/null
tls_addr=$(dc port tls 443)
[[ $tls_addr == 127.0.0.1:* ]] || { echo "  FAILED: proxy not on loopback: $tls_addr" >&2; false; }
ready https://id.kymatrix.test/healthz
ready https://auth.kymatrix.test/.well-known/openid-configuration
published=$(docker ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}} {{.Ports}}' | grep -- '->' || true)
expect "$(grep -c . <<<"$published" || true)" 1 "only the harness proxy publishes a port ($published)"
ok "stack healthy behind https://*.kymatrix.test on $tls_addr"
pass

export KYID_URL=https://id.kymatrix.test KYID_STATE=$state KYID_ADMIN_PASS=$KYMATRIX_ACCEPT_ADMIN_PASS
export ACCEPT_CACERT=$scratch/tls/ca.crt ACCEPT_CONNECT=$tls_addr
ACCEPT_SPKI=$(cat "$scratch/tls/spki")
export ACCEPT_DIR=$scratch ACCEPT_PORT=${tls_addr##*:} ACCEPT_SPKI ACCEPT_ARTIFACTS=$artifacts

# ---------------------------------------------------------------------------------------
step kyidentity
users=("Alice.Q@Ky" bob mallory nadia)
[[ $reproduce == 1 ]] && users+=(rita)
declare -A kid
for u in "${users[@]}"; do
	openssl rand -hex 16 >"$state/$u.pass"
	kid[$u]=$(kyid POST /api/admin/users "$(jq -n --arg u "$u" --arg p "$(cat "$state/$u.pass")" \
		'{username: $u, displayName: ($u | split("@")[0]), email: (($u | ascii_downcase | gsub("[^a-z0-9.]"; "-")) + "@kymatrix.test"), password: $p}')" | jq -re .user.id)
done
ok "users ${users[*]}"
kyid POST /api/admin/clients "$(jq -n --arg r "$redirect_uri" --arg c "$KY_MATRIX_MAS_CLIENT_ID" \
	'{clientId: $c, clientName: "KyMessages chat", clientType: "confidential", redirectUris: [$r], allowedScopes: ["openid", "profile", "email"]}')" >"$state/client.json"
app=$(kyid GET /api/admin/app-registry | jq -r --arg c "$KY_MATRIX_MAS_CLIENT_ID" '.records[] | select(.clientId == $c) | .id')
expect "$(kyid GET /api/admin/app-registry | jq -r --arg c "$KY_MATRIX_MAS_CLIENT_ID" '.records[] | select(.clientId == $c) | .accessMode')" \
	assigned_only "the MAS client admits assigned users only"
for u in "${users[@]}"; do
	[[ $u == mallory ]] && continue
	kyid PUT "/api/admin/app-registry/$app/assignments/users/${kid[$u]}" >/dev/null
done
ok "confidential client $KY_MATRIX_MAS_CLIENT_ID registered; everyone but mallory assigned"
KY_MATRIX_MAS_CLIENT_SECRET=$(jq -r .clientSecret "$state/client.json") matrix_init >"$state/init2.out"
grep -q 'kept      secrets/upstream_provider_id' "$state/init2.out"
ok "matrix-init re-run: secrets kept, client secret rendered"
no_insecure "re-rendered configs" "${rendered[@]}"
dc restart mas >/dev/null
ready https://auth.kymatrix.test/.well-known/openid-configuration
dc exec -T tls nginx -s reload 2>/dev/null
pass

# ---------------------------------------------------------------------------------------
# Shipped settings: compat login unrouted, nothing relaxed.
step prove
e2e prove
read -r dm group < <(jq -r '[.rooms.dm, .rooms.group] | @tsv' "$state/prove.json")
sent=$(jq '.messages | length' "$state/prove.json")
in_encrypted="room_id IN (SELECT room_id FROM current_state_events WHERE type = 'm.room.encryption')"
expect "$(sql synapse "SELECT count(*) FROM events WHERE type = 'm.room.message' AND $in_encrypted")" 0 \
	"no m.room.message in any encrypted room"
encrypted=$(sql synapse "SELECT count(*) FROM events WHERE type = 'm.room.encrypted' AND $in_encrypted")
((encrypted >= sent)) || { echo "  FAILED: $encrypted m.room.encrypted events for $sent messages" >&2; false; }
ok "$encrypted m.room.encrypted events for $sent messages sent"
for r in "$dm" "$group"; do
	expect "$(sql synapse "SELECT count(*) FROM current_state_events WHERE room_id = '$r' AND type = 'm.room.encryption'")" 1 "room $r is encrypted"
done
expect "$(sql synapse "SELECT count(*) FROM events WHERE type = 'm.room.message'")" 0 "no m.room.message anywhere"
dump=$(dc exec -T postgres pg_dump -U postgres synapse)
while read -r m; do
	if grep -qF "$m" <<<"$dump"; then echo "  FAILED: plaintext $m found in the Synapse database" >&2; false; fi
done < <(jq -r '.messages[]' "$state/prove.json")
ok "no message plaintext anywhere in a pg_dump of the Synapse database"
for u in '@alice.q_ky:kymatrix.test' '@bob:kymatrix.test'; do
	expect "$(sql synapse "SELECT count(DISTINCT keytype) FROM e2e_cross_signing_keys WHERE user_id = '$u'")" 3 "$u cross-signing keys uploaded"
	expect "$(sql synapse "SELECT count(*) > 0 FROM e2e_room_keys_versions WHERE user_id = '$u'")" t "$u key backup created"
	expect "$(sql synapse "SELECT count(*) FROM account_data WHERE user_id = '$u' AND account_data_type = 'm.secret_storage.default_key'")" 1 "$u recovery key (secret storage) set up"
done
expect "$(sql mas "SELECT string_agg(username, ',' ORDER BY username) FROM users")" 'alice.q_ky,bob' \
	"MAS accounts: mixed-case Alice.Q@Ky mapped to alice.q_ky"
pass

# ---------------------------------------------------------------------------------------
step closed
for q in '' '?kind=guest'; do
	expect "$(answer POST "https://matrix.kymatrix.test/_matrix/client/v3/register$q" -H 'Content-Type: application/json' -d '{}')" \
		'403 M_FORBIDDEN' "registration$q refused"
done
login=$(answer GET https://matrix.kymatrix.test/_matrix/client/v3/login)
if [[ $login != '404 M_UNRECOGNIZED' ]]; then
	# Served flows are acceptable only as a 200 that offers no password login.
	expect "${login%% *}" 200 "GET /login is the exact 404 M_UNRECOGNIZED or a 200 flow list"
	if jq -e '[.flows[].type] | index("m.login.password")' "$state/answer.json" >/dev/null; then
		echo "  FAILED: login offers m.login.password: $(cat "$state/answer.json")" >&2
		false
	fi
fi
ok "GET /login offers no m.login.password ($login)"
expect "$(answer POST https://matrix.kymatrix.test/_matrix/client/v3/login -H 'Content-Type: application/json' \
	-d "$(jq -n --arg p "$(cat "$state/bob.pass")" '{type: "m.login.password", identifier: {type: "m.id.user", user: "bob"}, password: $p}')")" \
	'404 M_UNRECOGNIZED' "password login with bob's real KyIdentity password refused"
for p in /_matrix/federation/v1/version /_matrix/key/v2/server; do
	expect "$(status GET "https://matrix.kymatrix.test$p")" 404 "federation path $p not served"
done
rc=0
dc exec -T synapse curl -s -o /dev/null --max-time 5 http://localhost:8448/ || rc=$?
expect "$rc" 7 "Synapse has no federation listener (curl to 8448: connection refused)"
e2e refused
expect "$(sql mas "SELECT count(*) FROM users WHERE username = 'mallory'")" 0 "no MAS account for mallory"
expect "$(sql mas "SELECT count(*) FROM upstream_oauth_links WHERE subject = '${kid[mallory]}'")" 0 "no upstream link for mallory"
expect "$(sql synapse "SELECT count(*) FROM users WHERE name LIKE '@mallory%'")" 0 "no Synapse user for mallory"
pass

# ---------------------------------------------------------------------------------------
# Runs last: it changes MAS's config and routing, which the steps above must not see.
if [[ $reproduce == 1 ]]; then
	step reproduce
	sed -i 's/^        - name: assets$/&\n        - name: compat/' "$scratch/matrix/mas/config.yaml"
	expect "$(grep -c -- '- name: compat' "$scratch/matrix/mas/config.yaml")" 1 "scratch MAS config serves compat"
	cp "$scratch/overrides/compat.conf" "$scratch/nginx-extra/compat.conf"
	chmod a+r "$scratch/nginx-extra/compat.conf"
	dc restart mas >/dev/null
	ready https://auth.kymatrix.test/.well-known/openid-configuration
	dc exec -T tls nginx -s reload 2>/dev/null
	ready https://matrix.kymatrix.test/_matrix/client/v3/login
	ok "compat login routed to MAS: $(hcurl https://matrix.kymatrix.test/_matrix/client/v3/login | jq -c '[.flows[].type]')"
	e2e compat
	room=$(jq -r .rooms.room "$state/compat.json")
	echo "  evidence: compat.json $(jq -c '{users, device, oidcKeys}' "$state/compat.json")"
	echo "  evidence: room encrypted: $(sql synapse "SELECT count(*) FROM current_state_events WHERE room_id = '$room' AND type = 'm.room.encryption'")"
	echo "  evidence: event types in the room: $(sql synapse "SELECT string_agg(type || '=' || n, ' ') FROM (SELECT type, count(*) n FROM events WHERE room_id = '$room' GROUP BY type ORDER BY type) t")"
	echo "  evidence: rita device keys: $(sql synapse "SELECT count(*) FROM e2e_device_keys_json WHERE user_id = '@rita:kymatrix.test'"), cross-signing keys: $(sql synapse "SELECT count(*) FROM e2e_cross_signing_keys WHERE user_id = '@rita:kymatrix.test'")"
	echo "  evidence: MAS sessions for rita: compat=$(sql mas "SELECT count(*) FROM compat_sessions s JOIN users u USING (user_id) WHERE u.username = 'rita'") oauth2=$(sql mas "SELECT count(*) FROM oauth2_sessions s JOIN users u USING (user_id) WHERE u.username = 'rita'")"
	# Each message is identified by its content, never by event type alone.
	stored_as() { sql synapse "SELECT coalesce(string_agg(e.type, ','), 'absent') FROM events e JOIN event_json j USING (event_id) WHERE e.room_id = '$room' AND j.json LIKE '%$1%'"; }
	element_msg=$(jq -r .element "$state/compat.json")
	raw_msg=$(jq -r .raw "$state/compat.json")
	element_as=$(stored_as "$element_msg")
	sealed=$(sql synapse "SELECT count(*) FROM events WHERE type = 'm.room.encrypted' AND room_id = '$room'")
	echo "  evidence: Element message stored in plaintext as: $element_as; m.room.encrypted events in the room: $sealed"
	if [[ $element_as != absent ]]; then
		echo "  FINDING: REPRODUCED: Element's compat-session message is stored in plaintext ($element_as) in encrypted room $room"
		echo "  FAILED: Element sent plaintext into an encrypted room after compat sign-in (see FINDING)" >&2
		false
	fi
	echo "  FINDING: NOT REPRODUCED through Element: its compat-session message is not in plaintext anywhere in the room"
	((sealed >= 1)) || { echo "  FAILED: Element's message never reached Synapse (no m.room.encrypted in $room)" >&2; false; }
	ok "Element's message reached Synapse as m.room.encrypted"
	# Synapse stores what a client sends; the encryption guard is the client.
	expect "$(stored_as "$raw_msg")" m.room.message "control: the raw API send is stored as a plaintext m.room.message in the encrypted room"
	# Same control with alice's native OIDC session: not specific to compat sessions.
	auth=(-H "Authorization: Bearer $(cat "$state/alice.token")" -H 'Content-Type: application/json')
	native=$(hcurl -f "${auth[@]}" -d '{"name":"kymatrix-native-raw"}' https://matrix.kymatrix.test/_matrix/client/v3/createRoom | jq -r .room_id)
	hcurl -f -X PUT "${auth[@]}" -d '{"msgtype":"m.text","body":"kymatrix-native-raw"}' \
		"https://matrix.kymatrix.test/_matrix/client/v3/rooms/$native/send/m.room.message/kymatrix-raw" >/dev/null
	expect "$(sql synapse "SELECT count(*) FROM current_state_events WHERE room_id = '$native' AND type = 'm.room.encryption'")" 1 \
		"control: a room created through the API is encrypted by Synapse's default"
	expect "$(sql synapse "SELECT count(*) FROM events WHERE room_id = '$native' AND type = 'm.room.message'")" 1 \
		"control: a native OIDC session's raw send is stored as plaintext m.room.message too"
	pass
fi

# ---------------------------------------------------------------------------------------
# Last: it narrows the KyIdentity client's scopes for everyone.
step no-username
kyid PUT "/api/admin/clients/$KY_MATRIX_MAS_CLIENT_ID" '{"allowedScopes": ["openid", "email"]}' >/dev/null
ok "KyIdentity client narrowed to openid email: ID tokens carry no preferred_username"
e2e noclaim
expect "$(sql mas "SELECT count(*) FROM users WHERE username = 'nadia'")" 0 "no MAS account for nadia"
expect "$(sql synapse "SELECT count(*) FROM users WHERE name LIKE '@nadia%'")" 0 "no Synapse user for nadia"
pass
