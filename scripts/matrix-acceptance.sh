#!/usr/bin/env bash
# Matrix stack acceptance: a throwaway KyIdentity, `kymessages matrix-init` and
# docker-compose.matrix.yml on loopback, Element driven by Playwright. Proves that messages in
# encrypted rooms are stored encrypted, that the server is closed (no registration,
# password login or federation; unassigned KyIdentity users refused), and that a KyIdentity
# disable or unassign cuts open Element sessions within 30 seconds, with KyMessages' sweep
# making offboarding (including delete) stick in MAS. The operator console reports every
# component up on its pinned version and ends a live Element session within 30 seconds,
# audited. Health proves Synapse admin access through the console's service account. It
# also takes a server backup, loses the host and restores the whole stack from custodian
# shares, proving history, media, accounts, the server name and the signing key come back.
#
# Loopback without weakening shipped configs: a harness TLS proxy with a throwaway CA answers
# for the https hosts; MAS trusts that CA, the browser pins the proxy key. Everything runs in
# Compose project kymatrix-accept-<pid> with its own network and volumes; the exit trap runs
# `down -v` on that project only, removes its KyIdentity and KyMessages images and deletes the
# scratch directory.
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
app_image=kymatrix-accept-app:$$

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

# Compose interpolation. The network name is project-scoped, never kymessages-net.
export KY_NETWORK=$project-net
KY_MATRIX_UID=$(id -u) KY_MATRIX_GID=$(id -g)
export KY_MATRIX_UID KY_MATRIX_GID
KY_ADMIN_PASSWORD=$(openssl rand -hex 16) KY_SESSION_SECRET=$(openssl rand -hex 32)
export KY_ADMIN_PASSWORD KY_SESSION_SECRET
# Compose requires these on every command; the matrix-init and kyidentity steps set the real
# values before the app starts.
export KY_MATRIX_ADMIN_CLIENT_ID=pending-matrix-init KY_KYIDENTITY_HMAC_SECRET=pending-kyidentity
export KY_MATRIX_SERVER_NAME=kymatrix.test
export KY_MATRIX_HOST=https://matrix.kymatrix.test
export KY_MATRIX_AUTH_HOST=https://auth.kymatrix.test
export KY_MATRIX_CHAT_HOST=https://chat.kymatrix.test
export KY_ADMIN_HOST=https://admin.kymatrix.test KY_APP_URL=https://admin.kymatrix.test
export KY_KYIDENTITY_ISSUER=https://id.kymatrix.test
export KY_MATRIX_MAS_CLIENT_ID=kymatrix-mas
KYMATRIX_ACCEPT_ADMIN_PASS=$(openssl rand -hex 16)
export KYMATRIX_ACCEPT_ADMIN_PASS
export KYMATRIX_ACCEPT_KYID_IMAGE=$kyid_image KYMATRIX_ACCEPT_APP_IMAGE=$app_image

dc() {
	docker compose --progress quiet -p "$project" --project-directory "$scratch" \
		-f "$repo/docker-compose.yml" -f "$repo/docker-compose.matrix.yml" \
		-f "$scratch/overrides/compose.yml" -f "$scratch/overrides/app.yml" "$@"
}

current=setup t0=$SECONDS started=$SECONDS
step() { current=$1 t0=$SECONDS; echo "== $1"; }
pass() { printf 'PASS %-19s %4ss\n' "$current" "$((SECONDS - t0))" | tee -a "$summary"; }

cleanup() {
	local rc=$?
	if ((rc != 0)); then
		printf 'FAIL %-19s %4ss\n' "$current" "$((SECONDS - t0))" >>"$summary"
		mkdir -p "$artifacts"
		dc logs --no-color >"$artifacts/compose.log" 2>&1 || true
		echo "matrix-acceptance: logs and traces in $artifacts" >&2
	fi
	dc down -v --timeout 10 >/dev/null 2>&1 || echo "matrix-acceptance: down -v failed for project $project" >&2
	# The restore step hands ./data and ./backups to root, as an operator does; take them back.
	if [[ -n ${handed_to_root:-} ]]; then
		docker run --rm --network none --user 0:0 -v "$scratch/data:/data" -v "$scratch/backups:/backups" \
			--entrypoint chown "$app_image" -R "$(id -u):$(id -g)" /data /backups ||
			echo "matrix-acceptance: could not hand back $scratch/data and $scratch/backups" >&2
	fi
	docker image rm "$kyid_image" "$app_image" >/dev/null 2>&1 || true
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
# no_password_flow FILE: succeeds only for a JSON flow list without m.login.password; any jq
# failure (not JSON, empty, no .flows) counts as a failure.
no_password_flow() { jq -e '(.flows | type == "array") and ([.flows[].type] | index("m.login.password") | not)' "$1" >/dev/null 2>&1; }
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
e2e() { node "$here/e2e.mjs" "$@"; }
# eventually BOUND WANT WHAT CMD...: runs CMD 1s apart until it prints WANT; fails after BOUND
# seconds.
eventually() {
	local bound=$1 want=$2 what=$3 got start=$SECONDS
	shift 3
	until got=$("$@") && [[ $got == "$want" ]]; do
		((SECONDS - start < bound)) || { echo "  FAILED: $what within ${bound}s (got '$got', want '$want')" >&2; return 1; }
		sleep 1
	done
	ok "$what ($((SECONDS - start))s)"
}
matrix_init() { "$scratch/kymessages" matrix-init -dir "$scratch/matrix"; }

# ---------------------------------------------------------------------------------------
step build
go build -C "$repo" -o "$scratch/kymessages" ./cmd/server
docker build -q -t "$app_image" "$repo" >/dev/null
docker build -q -t "$kyid_image" "$kyid_src" >/dev/null
npm ci --prefix "$here" --no-audit --no-fund --silent
ok "kymessages (binary and image), KyIdentity ($(git -C "$kyid_src" rev-parse --short HEAD 2>/dev/null || echo unknown)) and Playwright built"
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

# First pass: KyIdentity issues the client secret only when the client is registered with
# the redirect URI this prints, so there is no MAS config yet. The kyidentity step saves the
# secret and runs the second pass.
client_secret_file=$scratch/matrix/secrets/kyidentity_client_secret
matrix_init >"$state/init1.out"
redirect_uri=$(awk '$1 == "redirect" && $2 == "URI" { print $3 }' "$state/init1.out")
expect "$redirect_uri" "$KY_MATRIX_AUTH_HOST/upstream/callback/$(cat "$scratch/matrix/secrets/upstream_provider_id")" \
	"matrix-init printed the redirect URI"
backchannel_uri=$(awk '$1 == "back-channel" && $2 == "logout" { print $4 }' "$state/init1.out")
expect "$backchannel_uri" "$KY_MATRIX_AUTH_HOST/upstream/backchannel-logout/$(cat "$scratch/matrix/secrets/upstream_provider_id")" \
	"matrix-init printed the back-channel logout URI"
KY_MATRIX_ADMIN_CLIENT_ID=$(cat "$scratch/matrix/secrets/mas_admin_client_id")
grep -qxF "  KY_MATRIX_ADMIN_CLIENT_ID=$KY_MATRIX_ADMIN_CLIENT_ID" "$state/init1.out" ||
	{ echo "  FAILED: matrix-init did not print the admin client ID it wrote" >&2; false; }
ok "admin client ID $KY_MATRIX_ADMIN_CLIENT_ID from secrets/mas_admin_client_id, as printed"
grep -qF "save the secret it shows to $client_secret_file (mode 0600), and run matrix-init again." "$state/init1.out" ||
	{ echo "  FAILED: first pass did not say where to save the client secret" >&2; false; }
[[ ! -e $scratch/matrix/mas/config.yaml ]] || { echo "  FAILED: first pass rendered a MAS config" >&2; false; }
ok "first pass: no MAS config, told where to save the client secret"
no_insecure "rendered configs (used unmodified)" "$scratch/matrix/synapse/homeserver.yaml" "$scratch/matrix/element/config.json"
pass

# ---------------------------------------------------------------------------------------
step stack
if dc config | grep -q 'kymessages-net'; then
	echo "  FAILED: the harness would join kymessages-net" >&2
	false
fi
ok "network $KY_NETWORK, project-scoped"
# The documented first-pass behaviour: Compose refuses MAS rather than start it unconfigured.
if dc up -d --quiet-pull mas >"$state/mas-up.out" 2>&1; then
	echo "  FAILED: Compose started MAS without mas/config.yaml" >&2
	false
fi
grep -q 'bind source path does not exist: .*/mas/config.yaml' "$state/mas-up.out" ||
	{ echo "  FAILED: MAS refused for another reason: $(cat "$state/mas-up.out")" >&2; false; }
[[ ! -e $scratch/matrix/mas/config.yaml ]] || { echo "  FAILED: Docker created mas/config.yaml" >&2; false; }
ok "Compose refuses MAS until the second pass (bind source path does not exist)"
dc up -d --quiet-pull --wait --wait-timeout 300 tls >/dev/null
tls_addr=$(dc port tls 443)
[[ $tls_addr == 127.0.0.1:* ]] || { echo "  FAILED: proxy not on loopback: $tls_addr" >&2; false; }
ready https://id.kymatrix.test/healthz
ok "KyIdentity up behind https://id.kymatrix.test on $tls_addr"
pass

export KYID_URL=https://id.kymatrix.test KYID_STATE=$state KYID_ADMIN_PASS=$KYMATRIX_ACCEPT_ADMIN_PASS
export ACCEPT_CACERT=$scratch/tls/ca.crt ACCEPT_CONNECT=$tls_addr
ACCEPT_SPKI=$(cat "$scratch/tls/spki")
export ACCEPT_DIR=$scratch ACCEPT_PORT=${tls_addr##*:} ACCEPT_SPKI ACCEPT_ARTIFACTS=$artifacts

# ---------------------------------------------------------------------------------------
step kyidentity
users=("Alice.Q@Ky" bob mallory nadia carol dave erin frank)
[[ $reproduce == 1 ]] && users+=(rita)
declare -A kid
for u in "${users[@]}"; do
	openssl rand -hex 16 >"$state/$u.pass"
	kid[$u]=$(kyid POST /api/admin/users "$(jq -n --arg u "$u" --arg p "$(cat "$state/$u.pass")" \
		'{username: $u, displayName: ($u | split("@")[0]), email: (($u | ascii_downcase | gsub("[^a-z0-9.]"; "-")) + "@kymatrix.test"), password: $p}')" | jq -re .user.id)
done
ok "users ${users[*]}"
kyid POST /api/admin/clients "$(jq -n --arg r "$redirect_uri" --arg b "$backchannel_uri" --arg c "$KY_MATRIX_MAS_CLIENT_ID" \
	'{clientId: $c, clientName: "KyMessages chat", clientType: "confidential", redirectUris: [$r], backchannelLogoutUri: $b, allowedScopes: ["openid", "profile", "email"]}')" >"$state/client.json"
expect "$(kyid GET /api/admin/clients | jq -r --arg c "$KY_MATRIX_MAS_CLIENT_ID" '.clients[] | select(.id == $c) | .backchannelLogoutUri')" \
	"$backchannel_uri" "the MAS client's back-channel logout goes to MAS"
# The directory webhook: a suite_webhook system linked into the MAS client's app record, so the
# users assigned to chat are the users KyIdentity delivers to KyMessages.
kyid POST /api/admin/systems "$(jq -n --arg u "$KY_APP_URL/api/sso/kyidentity/sync" \
	'{name: "KyMessages", systemType: "suite_webhook", callbackUrl: $u}')" >"$state/system.json"
system=$(jq -re .system.id "$state/system.json")
# Shown once; the operator's step is to give it to KyMessages.
KY_KYIDENTITY_HMAC_SECRET=$(jq -re .bearerToken "$state/system.json")
export KY_KYIDENTITY_HMAC_SECRET
link=$(kyid GET /api/admin/app-registry | jq -ce --arg c "$KY_MATRIX_MAS_CLIENT_ID" --arg s "$system" \
	'(.records[] | select(.clientId == $c)) as $t | (.records[] | select(.systemId == $s)) as $f
	| {app: $t.id, body: {sourceId: $f.id, targetRevision: $t.revision, sourceRevision: $f.revision}}')
app=$(jq -r .app <<<"$link")
kyid POST "/api/admin/app-registry/$app/link" "$(jq -c .body <<<"$link")" >/dev/null
record=$(kyid GET /api/admin/app-registry | jq -c --arg a "$app" '.records[] | select(.id == $a)')
expect "$(jq -r .systemId <<<"$record")" "$system" "the webhook system shares the MAS client's app record"
expect "$(jq -r .accessMode <<<"$record")" assigned_only "the MAS client admits assigned users only"
# The operator's step: KyIdentity showed the secret once; it goes in a 0600 file, never env.
# Before the app starts: Compose refuses the app until that file exists (it is in the capsule).
jq -re .clientSecret "$state/client.json" >"$client_secret_file"
chmod 600 "$client_secret_file"
matrix_init >"$state/init2.out"
grep -q 'kept      secrets/upstream_provider_id' "$state/init2.out"
grep -q 'rendered  mas/config.yaml' "$state/init2.out"
expect "$(grep -cF "client_secret: \"$(cat "$client_secret_file")\"" "$scratch/matrix/mas/config.yaml")" 1 \
	"matrix-init re-run: secrets kept, the saved client secret rendered into MAS"
no_insecure "re-rendered configs" "${rendered[@]}"
# KyMessages first, so it is listening when the assignments below are delivered.
dc up -d app >/dev/null
ready "$KY_APP_URL/.well-known/matrix/client"
ok "KyMessages up behind $KY_APP_URL with the webhook secret and MAS admin access"
for u in "${users[@]}"; do
	[[ $u == mallory ]] && continue
	kyid PUT "/api/admin/app-registry/$app/assignments/users/${kid[$u]}" >/dev/null
done
ok "confidential client $KY_MATRIX_MAS_CLIENT_ID registered; everyone but mallory assigned"
# Every assigned user is in KyMessages' directory before anyone signs in: the sweep locks a
# MAS user it has no record for.
delivered() { kyid GET "/api/admin/systems/$system/provisioning" | jq '[.users[] | select(.desired and .acknowledged)] | length'; }
eventually 60 "$((${#users[@]} - 1))" "KyMessages acknowledged every assigned user's webhook" delivered
# Element pulls in Synapse, MAS and Postgres; the base file's app service never starts.
dc up -d --quiet-pull --wait --wait-timeout 300 element >/dev/null
ready https://auth.kymatrix.test/.well-known/openid-configuration
ready https://matrix.kymatrix.test/_matrix/client/versions
published=$(docker ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}} {{.Ports}}' | grep -- '->' || true)
expect "$(grep -c . <<<"$published" || true)" 1 "only the harness proxy publishes a port ($published)"
ok "stack healthy behind https://*.kymatrix.test on $tls_addr"
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
	no_password_flow "$state/answer.json" || { echo "  FAILED: login flows missing or offer m.login.password: $(cat "$state/answer.json")" >&2; false; }
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
# Offboarding. The cut is KyIdentity's back-channel logout into MAS, timed against a live
# Element token; KyMessages' sweep (woken by the directory webhook) then locks, unlocks or
# deactivates the MAS user.
now_ms() { local t=${EPOCHREALTIME//[^0-9]/}; echo $((t / 1000)); }
# token_status USER: Synapse's status for USER's captured Element token.
token_status() { status GET https://matrix.kymatrix.test/_matrix/client/v3/account/whoami -H "Authorization: Bearer $(cat "$state/$1.token")"; }
# cut_within BOUND USER START_MS: polls until Synapse refuses USER's token (401) and prints the
# seconds since START_MS; fails past BOUND or on any other answer.
cut_within() {
	local bound=$1 user=$2 start=$3 code elapsed
	while code=$(token_status "$user") && elapsed=$(($(now_ms) - start)) && [[ $code == 200 ]]; do
		((elapsed <= bound * 1000)) || { echo "  FAILED: $user's session survived ${bound}s" >&2; return 1; }
		sleep 0.25
	done
	[[ $code == 401 ]] || { echo "  FAILED: whoami for $user answered '$code', not 401" >&2; return 1; }
	((elapsed <= bound * 1000)) || { echo "  FAILED: $user's session survived ${bound}s" >&2; return 1; }
	printf '%d.%d\n' $((elapsed / 1000)) $((elapsed % 1000 / 100))
}
# cut USER: disables USER in KyIdentity and records how long the captured token survived.
cut() {
	local start secs
	expect "$(token_status "$1")" 200 "$1's captured Element token is live"
	start=$(now_ms)
	kyid PUT "/api/admin/users/${kid[$1]}" '{"status": "disabled"}' >/dev/null
	secs=$(cut_within 30 "$1" "$start")
	ok "$1's open Element session refused ${secs}s after the disable (bound 30s)" | tee -a "$summary"
}
mas_user() { sql mas "SELECT $2 FROM users WHERE username = '$1'"; }

step offboard-cut
e2e room carol
e2e token carol
cut carol
e2e disabled carol
eventually 60 t "MAS locked carol" mas_user carol 'locked_at IS NOT NULL'
pass

# ---------------------------------------------------------------------------------------
step offboard-reactivate
kyid PUT "/api/admin/users/${kid[carol]}" '{"status": "active"}' >/dev/null
eventually 60 t "MAS unlocked carol" mas_user carol 'locked_at IS NULL'
e2e reread carol
pass

# ---------------------------------------------------------------------------------------
step offboard-delete
e2e room dave
kyid DELETE "/api/admin/users/${kid[dave]}" >/dev/null
eventually 60 t "MAS deactivated dave" mas_user dave 'deactivated_at IS NOT NULL'
expect "$(mas_user dave 'locked_at IS NULL')" t "dave deactivated, not just locked"
eventually 60 leave "dave left his rooms" sql synapse \
	"SELECT membership FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '@dave:$KY_MATRIX_SERVER_NAME' ORDER BY e.stream_ordering DESC LIMIT 1"
e2e reads dave
pass

# ---------------------------------------------------------------------------------------
# Deleting someone already locked still deactivates them.
step offboard-delete-locked
kyid PUT "/api/admin/users/${kid[carol]}" '{"status": "disabled"}' >/dev/null
eventually 60 t "MAS locked carol again" mas_user carol 'locked_at IS NOT NULL'
kyid DELETE "/api/admin/users/${kid[carol]}" >/dev/null
eventually 60 t "MAS deactivated the locked carol" mas_user carol 'deactivated_at IS NOT NULL'
eventually 60 leave "carol left her rooms" sql synapse \
	"SELECT membership FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '@carol:$KY_MATRIX_SERVER_NAME' ORDER BY e.stream_ordering DESC LIMIT 1"
pass

# ---------------------------------------------------------------------------------------
# Unassigning from the MAS client cuts like a disable and locks, without deactivating.
step offboard-unassign
e2e token frank
expect "$(token_status frank)" 200 "frank's captured Element token is live"
start=$(now_ms)
kyid DELETE "/api/admin/app-registry/$app/assignments/users/${kid[frank]}" >/dev/null
secs=$(cut_within 30 frank "$start")
ok "frank's open Element session refused ${secs}s after the unassign (bound 30s)" | tee -a "$summary"
eventually 60 t "MAS locked frank after the unassign" mas_user frank 'locked_at IS NOT NULL'
expect "$(mas_user frank 'deactivated_at IS NULL')" t "frank locked, not deactivated"
pass

# ---------------------------------------------------------------------------------------
step offboard-missed
e2e token erin
dc stop --timeout 10 app >/dev/null
cut erin
expect "$(mas_user erin 'locked_at IS NULL')" t "MAS has not locked erin while KyMessages is down"
erin_event() { kyid GET "/api/admin/systems/$system/provisioning" | jq -c '.users[] | select(.username == "erin") | .lastEvent'; }
# Proof the webhook was missed: KyIdentity tried to deliver erin's disable and failed.
missed() { erin_event | jq -r '.type == "user.updated" and .status != "delivered" and ((.error // "") != "" or .attempts > 0)'; }
eventually 30 true "KyIdentity's disable webhook for erin failed while KyMessages was down" missed
dc start app >/dev/null
ready "$KY_APP_URL/.well-known/matrix/client"
# The disable webhook failed while KyMessages was down, so its directory still says active and
# the start-up sweep rightly leaves erin unlocked; the lock needs that webhook redelivered.
# KyIdentity (internal/sync/delivery.go) treats a 5xx or transport error after the request left
# as an uncertain write: it fences erin's deliveries until an operator confirms KyMessages is
# quiescent and resumes the attempt, allowed once its 60s lease (recoverAfter) has passed; a
# resync queues behind the fence. Only a clean failure retries itself, 30s * 2^(failures - 1)
# apart (retryDelay), abandoned after 5, which a resync recovers.
event=$(erin_event)
echo "  KyIdentity's disable webhook for erin: $event"
next=$(jq -r 'select(.status == "pending") | .nextAttemptAt // empty' <<<"$event")
locked=(mas_user erin 'locked_at IS NOT NULL')
if [[ $(jq -r .error <<<"$event") == *"operator recovery required"* ]]; then
	attempt=$(kyid GET "/api/admin/systems/$system/deliveries" | jq -c --arg u "${kid[erin]}" '.[] | select(.userId == $u)')
	due=$(($(date -d "$(jq -r .recoverAfter <<<"$attempt")" +%s) - $(date +%s) + 1))
	((due <= 0)) || sleep "$due"
	kyid POST "/api/admin/systems/$system/deliveries/$(jq -r .token <<<"$attempt")/resume" '{"confirmedQuiescent": true}' >/dev/null
	eventually 60 t "MAS locked erin after the operator resumed the fenced webhook" "${locked[@]}" | tee -a "$summary"
elif [[ -n $next ]] && due=$(($(date -d "$next" +%s) - $(date +%s))) && ((due <= 240)); then
	eventually $((due + 60)) t "MAS locked erin after KyIdentity's retry (due in ${due}s)" "${locked[@]}" | tee -a "$summary"
elif [[ $(jq -r .status <<<"$event") == failed ]]; then
	kyid POST "/api/admin/systems/$system/resync" >/dev/null
	eventually 60 t "MAS locked erin after a resync of the system" "${locked[@]}" | tee -a "$summary"
else
	echo "  FAILED: erin's disable webhook is neither fenced, retrying nor abandoned: $event" >&2
	false
fi
pass

# ---------------------------------------------------------------------------------------
# Synapse is on default and matrix-db, not matrix-admin.
step admin-isolation
rc=0
dc run --rm --no-deps --entrypoint curl synapse -sS -m 5 -o /dev/null http://mas:8081/ 2>/dev/null || rc=$?
expect "$rc" 7 "synapse cannot connect to mas:8081 (connection refused)"
rc=0
dc run --rm --no-deps --entrypoint curl synapse -sS -m 5 -o /dev/null http://mas-admin:8081/ 2>/dev/null || rc=$?
expect "$rc" 6 "mas-admin does not resolve for synapse"
pass

# ---------------------------------------------------------------------------------------
jar=$state/app.cookies
# app_api METHOD PATH [JSON]: the KyMessages admin API as the browser calls it (CSRF header from the cookie).
app_api() {
	local csrf body=()
	csrf=$(awk '$6 == "ky_csrf" { print $7 }' "$jar" 2>/dev/null || true)
	[[ $# -ge 3 ]] && body=(-d "$3")
	hcurl -fsS -b "$jar" -c "$jar" -X "$1" -H "Origin: $KY_APP_URL" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" "${body[@]}" "$KY_APP_URL$2"
}
# app_login PASSWORD: a fresh password sign-in as the bootstrap admin.
app_login() { app_api POST /api/auth/login "$(jq -n --arg p "$1" '{username: "admin", password: $p}')" >/dev/null; }

# ---------------------------------------------------------------------------------------
# The operator console: Health reports every component on its pinned version, and ending a
# session there cuts a live Element token like offboarding does, audited.
step console
app_login "$KY_ADMIN_PASSWORD"
admin_pass=$(openssl rand -hex 16)
app_api POST /api/auth/change-password "$(jq -n --arg c "$KY_ADMIN_PASSWORD" --arg n "$admin_pass" '{current_password: $c, new_password: $n}')" >/dev/null
app_login "$admin_pass"
ok "operator signed in to the console and replaced the bootstrap password"
# Warm-up: the first load creates the console account inside the probe's 3s, which may time out.
app_api GET /api/admin/health >"$state/health-warmup.json"
app_api GET /api/admin/health >"$state/health.json"
expect "$(jq -r '[.components[].name] | join(",")' "$state/health.json")" kymessages,database,synapse,mas,element,postgres,synapse-admin "health checks every component"
expect "$(jq -r '[.components[] | select(.status != "up") | "\(.name): \(.error)"] | join("; ")' "$state/health.json")" "" "every component up"
# Synapse admin access, proven by the probe above: the console account exists, is MAS admin,
# unlocked and unlinked, and every session it used carried exactly the two scopes, expired
# within 5 minutes and was revoked.
expect "$(mas_user kymessages-console 'can_request_admin AND locked_at IS NULL AND deactivated_at IS NULL')" t "the console account exists, admin and unlocked"
expect "$(sql mas "SELECT count(*) FROM upstream_oauth_links l JOIN users u USING (user_id) WHERE u.username = 'kymessages-console'")" 0 "the console account has no KyIdentity link"
expect "$(sql mas "SELECT string_agg(DISTINCT array_to_string(s.scope_list, ' '), '|') FROM personal_sessions s JOIN users u ON u.user_id = s.actor_user_id WHERE u.username = 'kymessages-console'")" \
	'urn:matrix:client:api:* urn:synapse:admin:*' "console sessions carry exactly the client API and Synapse admin scopes"
expect "$(sql mas "SELECT count(*) FROM personal_sessions WHERE revoked_at IS NULL")" 0 "every console session was revoked"
expect "$(sql mas "SELECT bool_and(expires_at <= created_at + interval '5 minutes') FROM personal_access_tokens")" t "console tokens expire within 5 minutes"
# Only the image map: the resolved config holds secrets.
dc config --format json | jq '.services | map_values(.image)' >"$state/images.json"
for svc in synapse mas element postgres; do
	# The pin, read here from the resolved Compose file, independently of the Go generator.
	pin=$(jq -r --arg s "$svc" '.[$s] | split("@")[0] | split(":") | last | ltrimstr("v") | split("-")[0]' "$state/images.json")
	expect "$(jq -r --arg s "$svc" '.components[] | select(.name == $s) | "\(.version) \(.pinned) \(.mismatch)"' "$state/health.json")" \
		"$pin $pin false" "$svc runs the pinned $pin"
done
alice='Alice.Q@Ky'
e2e token "$alice"
expect "$(token_status "$alice")" 200 "alice's captured Element token is live"
device=$(hcurl -fsS -H "Authorization: Bearer $(cat "$state/$alice.token")" https://matrix.kymatrix.test/_matrix/client/v3/account/whoami | jq -re .device_id)
alice_id=$(app_api GET '/api/admin/matrix/users?search=alice.q_ky' |
	jq -re --arg m "@alice.q_ky:$KY_MATRIX_SERVER_NAME" '.users[] | select(.mxid == $m and .status == "active") | .id')
session=$(app_api GET "/api/admin/matrix/users/$alice_id/sessions" | jq -re --arg d "$device" \
	'[.sessions[] | select(.kind == "oauth2" and .device == $d)] | if length == 1 then .[0].id else error("want one session for \($d), got \(length)") end')
# Ending a session needs a sign-in from the last 10 minutes.
app_login "$admin_pass"
start=$(now_ms)
expect "$(app_api POST "/api/admin/matrix/sessions/oauth2/$session/finish" | jq -r .outcome)" ended "the console ended alice's Element session $session"
secs=$(cut_within 30 "$alice" "$start")
ok "alice's open Element session refused ${secs}s after the console ended it (bound 30s)" | tee -a "$summary"
expect "$(app_api POST "/api/admin/matrix/sessions/oauth2/$session/finish" | jq -r .outcome)" already_ended "ending it again succeeds"
expect "$(app_api GET '/api/admin/audit?kind=matrix&limit=20' | jq -r --arg s "$session" --arg m "@alice.q_ky:$KY_MATRIX_SERVER_NAME" \
	'[.records[] | select(.action == "matrix.session_end" and .target == $m and .actor == "admin" and (.details | contains($s))) | .outcome] | join(",")')" \
	already_ended,ended "the audit API shows both session-end rows, newest first"
pass

# ---------------------------------------------------------------------------------------
# Server backup: the operator's admin API and `kymessages deposit`, sealed to a throwaway
# suite key whose 2-of-3 shares only this harness holds.
kyb() { dc exec -T -e PGPASSWORD="$(cat "$scratch/matrix/secrets/kybackup_db_password")" postgres psql -h 127.0.0.1 -U kybackup -v ON_ERROR_STOP=1 "$@"; }

step backup
e2e media
dc exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql >/dev/null
ok "kybackup-role.sql re-applied on a running stack"
expect "$(kyb -d synapse -Atc 'SELECT count(*) > 0 FROM users')" t "kybackup reads synapse"
if kyb -d postgres -c 'SELECT 1' >/dev/null 2>&1; then echo "  FAILED: kybackup connected to database postgres" >&2; false; fi
ok "kybackup cannot connect to database postgres"
if kyb -d synapse -c 'CREATE TABLE kyb_probe ()' >/dev/null 2>&1; then echo "  FAILED: kybackup created a table in synapse" >&2; false; fi
ok "kybackup cannot create a table in synapse"
if kyb -d mas -c 'UPDATE users SET locked_at = now()' >/dev/null 2>&1; then echo "  FAILED: kybackup updated MAS users" >&2; false; fi
ok "kybackup cannot write MAS users"
app_login "$admin_pass"
# Off, so the scheduler cannot race the CLI.
app_api PUT /api/backup/schedule '{"interval_sec": 0}' >/dev/null
pub=$(go run -C "$repo" ./scripts/matrix-acceptance/suitekey "$state/shares")
app_api POST /api/backup/pin-key "$(jq -n --arg k "$pub" '{public_key: $k, threshold: 2, total_shares: 3}')" >/dev/null
ok "operator signed in and pinned a throwaway 2-of-3 suite key"
# The restore step's one-time-key check is vacuous unless the source holds some.
otks=$(sql synapse 'SELECT count(*) FROM e2e_one_time_keys_json')
((otks > 0)) || { echo "  FAILED: Synapse holds no one-time keys to exclude" >&2; false; }
ok "Synapse holds $otks one-time keys before the backup"
# The app reads ./matrix for the capsule, but not the superuser password.
[[ -s $scratch/matrix/secrets/postgres_password ]] || { echo "  FAILED: no superuser password to hide" >&2; false; }
dc exec -T app test -s /matrix/secrets/kybackup_db_password
if dc exec -T app test -e /matrix/secrets/postgres_password; then echo "  FAILED: the app sees the Postgres superuser password" >&2; false; fi
ok "the app sees the Matrix secrets but not the Postgres superuser password"
dc exec -T app /app/kymessages deposit >"$state/deposit.out"
ok "kymessages deposit sealed a capsule"
dc exec -T app /app/kymessages backup-drill | tee "$state/drill.out"
for want in 'Status:   PASSED' '[✓] Postgres Dump: matrix/dumps/mas.dump' '[✓] Postgres Dump: matrix/dumps/synapse.dump'; do
	grep -qF "$want" "$state/drill.out" || { echo "  FAILED: drill output lacks '$want'" >&2; false; }
done
ok "backup drill passed, both dumps checked"
app_api GET /api/backup/status >"$state/status.json"
jq -e '.capsule_size.bytes > 0 and .capsule_size.warning == false and .media_last_run.outcome == "success"' "$state/status.json" >/dev/null ||
	{ echo "  FAILED: backup status: $(jq -c '{capsule_size, media_last_run}' "$state/status.json")" >&2; false; }
ok "status: capsule $(jq -r '"\(.capsule_size.bytes) bytes, \(.capsule_size.percent)%"' "$state/status.json") of the limit, media run succeeded" | tee -a "$summary"
mid=$(sql synapse "SELECT media_id FROM local_media_repository WHERE user_id = '@bob:$KY_MATRIX_SERVER_NAME' ORDER BY created_ts DESC LIMIT 1")
rel="local_content/${mid:0:2}/${mid:2:2}/${mid:4}"
plain=$(dc exec -T synapse stat -c %s "/media/$rel")
expect "$(dc exec -T app stat -c %s "/app/backups/media/mirror/$rel")" $((plain + 28)) "bob's image mirrored as AES-GCM ciphertext"
media_sum=$(dc exec -T synapse sha256sum "/media/$rel" | awk '{print $1}')
mirror_sum=$(dc exec -T app sha256sum "/app/backups/media/mirror/$rel" | awk '{print $1}')
[[ $mirror_sum != "$media_sum" ]] || { echo "  FAILED: the mirror holds bob's image bytes unchanged" >&2; false; }
ok "the mirror's bytes differ from the media store's"
dc exec -T app test -f "/app/backups/media/full-$(date -u +%Y-%m).tar"
ok "this month's media archive exists"
cp "$scratch/matrix/synapse/signing.key" "$state/signing.key"
keyid=$(awk '{print $2}' "$state/signing.key")
pass

# ---------------------------------------------------------------------------------------
# Lose the KyMessages host (KyIdentity survives), then restore-matrix's usage text, step by
# step: kymessages restore from custodian shares, matrix-init, then restore-matrix.
step restore
dc cp app:/app/backups "$state/backups"
caps=("$state"/backups/*.kycap)
# Unmatched, the glob stays literal and still counts one.
[[ -f ${caps[0]} ]] || { echo "  FAILED: no sealed capsule in the backup copy" >&2; false; }
expect "${#caps[@]}" 1 "one sealed capsule"
dc rm -sfv app element synapse mas postgres synapse-media-owner >/dev/null
for v in matrix-postgres matrix-media app-data app-backups; do docker volume rm "${project}_$v" >/dev/null; done
expect "$(docker volume ls -q --filter "label=com.docker.compose.project=$project" | grep -cE '_(matrix-postgres|matrix-media|app-data|app-backups)$' || true)" 0 "Matrix and app volumes gone"
mv "$scratch/matrix" "$state/matrix.before"
# Usage step 1: restore as KY_MATRIX_UID (this user), shares on stdin, then move data/ and matrix/ in.
"$scratch/kymessages" restore -capsule "${caps[0]}" -to "$scratch/restored" -service KyMessages <"$state/shares" | tee "$state/restore.out"
mv "$scratch/restored/matrix" "$scratch/matrix"
mv "$scratch/restored/data" "$scratch/data"
cmp "$state/signing.key" "$scratch/matrix/synapse/signing.key"
ok "Synapse signing key restored"
[[ ! -e $scratch/matrix/secrets/postgres_password ]] || { echo "  FAILED: the capsule carried the superuser password" >&2; false; }
diff -r -x postgres_password "$state/matrix.before/secrets" "$scratch/matrix/secrets"
ok "matrix-init secrets restored, all but the superuser password"
# Usage step 2: matrix-init with the setup environment creates only the missing secret.
matrix_init >"$state/init3.out"
expect "$(awk '$1 == "created" { print $2 }' "$state/init3.out")" secrets/postgres_password "matrix-init created only the superuser password"
diff -r -x postgres_password "$state/matrix.before/secrets" "$scratch/matrix/secrets"
cmp "$state/signing.key" "$scratch/matrix/synapse/signing.key"
cmp -s "$state/matrix.before/secrets/postgres_password" "$scratch/matrix/secrets/postgres_password" &&
	{ echo "  FAILED: the new superuser password equals the lost one" >&2; false; }
ok "matrix-init kept every restored secret and the signing key"
expect "$(stat -c %a "$scratch/matrix/element/config.json")" 644 "Element config readable by its nginx again"
# Usage step 3: docker cp wrote the copy as this user, so ./backups/media is readable by it.
mv "$state/backups" "$scratch/backups"
# Usage step 4: the volumes are already gone; a fresh stack, database only.
dc up -d --quiet-pull --wait --wait-timeout 300 postgres >/dev/null
# Usage step 5.
dc run --rm -T restore-matrix | tee "$state/restore-matrix.out"
for want in 'Restored database mas' 'Restored database synapse'; do
	grep -qF "$want" "$state/restore-matrix.out" || { echo "  FAILED: restore-matrix did not say '$want'" >&2; false; }
done
grep -qE 'Restored [1-9][0-9]* media files' "$state/restore-matrix.out" || { echo "  FAILED: restore-matrix restored no media" >&2; false; }
ok "restore-matrix loaded both dumps as their owners and wrote media back"
expect "$(sql synapse 'SELECT count(*) FROM e2e_one_time_keys_json')" 0 "no one-time keys restored"
restored_sum() { dc run --rm --no-deps -T --entrypoint sha256sum synapse "/media/$rel" | awk '{print $1}'; }
expect "$(restored_sum)" "$media_sum" "bob's image restored byte for byte"
if dc run --rm -T restore-matrix >"$state/again.out" 2>&1; then echo "  FAILED: restore-matrix ran twice" >&2; false; fi
grep -q 'refused: database mas already holds' "$state/again.out" ||
	{ echo "  FAILED: second restore-matrix failed for another reason: $(cat "$state/again.out")" >&2; false; }
expect "$(restored_sum)" "$media_sum" "a second restore-matrix refused and changed nothing"
# Usage step 6 is `sudo chown -R root:root ./data ./backups`; CI has no sudo, so a throwaway
# root container with only those two binds does the chown. cleanup hands them back.
handed_to_root=1
docker run --rm --network none --user 0:0 -v "$scratch/data:/data" -v "$scratch/backups:/backups" \
	--entrypoint chown "$app_image" -R 0:0 /data /backups
ok "./data and ./backups chowned to root"
# The app now uses the shipped ./data and ./backups binds, as a deployment would.
export KYMATRIX_ACCEPT_APP_DATA=./data KYMATRIX_ACCEPT_APP_BACKUPS=./backups
dc up -d --quiet-pull --wait --wait-timeout 300 element >/dev/null
dc up -d app >/dev/null
ready "$KY_APP_URL/.well-known/matrix/client"
ready https://matrix.kymatrix.test/_matrix/client/versions
ok "restored stack up"
e2e restored
expect "$(mas_user bob 'locked_at IS NULL AND deactivated_at IS NULL')" t "bob's MAS account intact"
for r in "$dm" "$group"; do
	expect "$(sql synapse "SELECT membership FROM local_current_membership WHERE user_id = '@bob:$KY_MATRIX_SERVER_NAME' AND room_id = '$r'")" join "bob still in $r"
done
expect "$(hcurl -fsS -H "Authorization: Bearer $(cat "$state/alice.token")" https://matrix.kymatrix.test/_matrix/client/v3/account/whoami | jq -r .user_id)" \
	"@alice.q_ky:$KY_MATRIX_SERVER_NAME" "server name unchanged"
sql synapse "SELECT json FROM event_json j JOIN events e USING (event_id) WHERE e.sender = '@alice.q_ky:$KY_MATRIX_SERVER_NAME' ORDER BY e.stream_ordering DESC LIMIT 1" |
	grep -qF "\"ed25519:$keyid\"" || { echo "  FAILED: alice's newest event is not signed with $keyid" >&2; false; }
ok "new events signed with the restored key $keyid"
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
