# shellcheck shell=bash disable=SC2154,SC2034
# Sourced by scripts/matrix-acceptance.sh: its variables and helpers are in scope.
# Shared by the meeting-guest spikes (s1-signin.sh, s2-knock.sh, s3-confinement.sh), sourced by
# scripts/matrix-acceptance.sh step guest-spikes after prove. Needs the harness's dc, hcurl, sql,
# expect, ok, state and scratch.

# masapi METHOD PATH [JSON]: MAS's admin API exactly as KyMessages reaches it (client credentials
# on the internal matrix-admin network), from a throwaway container of the pinned Synapse image.
masapi() {
	local img
	img=$(docker inspect -f '{{.Config.Image}}' "$(dc ps -q synapse)")
	docker run --rm --network "${project}_matrix-admin" --user "$(id -u):$(id -g)" \
		-e "CRED=$KY_MATRIX_ADMIN_CLIENT_ID:$(cat "$scratch/matrix/secrets/mas_admin_client_secret")" \
		-e "M=$1" -e "P=$2" -e "B=${3:-}" --entrypoint sh "$img" -c '
		r=$(curl -fsS -u "$CRED" -d grant_type=client_credentials -d scope=urn:mas:admin http://mas-admin:8081/oauth2/token) || exit 1
		t=$(printf %s "$r" | python3 -c "import json,sys; print(json.load(sys.stdin)[\"access_token\"])") || exit 1
		if [ -n "$B" ]; then
			curl -sS -X "$M" -H "Authorization: Bearer $t" -H "Content-Type: application/json" --data-binary "$B" "http://mas-admin:8081/api/admin/v1/$P"
		else
			curl -sS -X "$M" -H "Authorization: Bearer $t" "http://mas-admin:8081/api/admin/v1/$P"
		fi'
}

# session USER_ID SCOPE TTL: a MAS personal session's access token, as KyMessages mints agents'.
session() {
	masapi POST personal-sessions "$(jq -nc --arg u "$1" --arg s "$2" --argjson e "$3" \
		'{actor_user_id: $u, human_name: "kymatrix guest spike", scope: $s, expires_in: $e}')" | jq -re .data.attributes.access_token
}
mas_id() { masapi GET "users/by-username/$1" | jq -re .data.id; }

# new_guest LOCALPART TTL: an unlinked MAS user with no password (CreateAgentUser's mechanism)
# and a personal session on device $state/LOCALPART.device; prints the token.
new_guest() {
	local id dev
	id=$(masapi POST users "$(jq -nc --arg u "$1" '{username: $u}')" | jq -re .data.id)
	echo "$id" >"$state/$1.masid"
	dev=KYGUEST$(openssl rand -hex 5 | tr a-f A-F)
	echo "$dev" >"$state/$1.device"
	session "$id" "urn:matrix:client:api:* urn:matrix:client:device:$dev" "$2"
}

# mx TOKEN METHOD PATH [JSON]: the client-server API; prints "status errcode" (errcode - on success);
# the body is in $state/mx.reply.
mx() {
	local code
	code=$(hcurl -sS -o "$state/mx.reply" -w '%{http_code}' -X "$2" -H "Authorization: Bearer $1" \
		-H 'Content-Type: application/json' ${4:+--data-binary "$4"} "https://matrix.kymatrix.test/_matrix/client/v3/$3")
	echo "$code $(jq -r '.errcode // "-"' "$state/mx.reply" 2>/dev/null || echo -)"
}
mxr() { jq -r "$1" "$state/mx.reply"; }
latest() { sql synapse "SELECT membership FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '$1' AND m.room_id = '$2' ORDER BY e.stream_ordering DESC LIMIT 1"; }
alice_mx=@alice.q_ky:$KY_MATRIX_SERVER_NAME bob_mx=@bob:$KY_MATRIX_SERVER_NAME
spike_note() { echo "  evidence: $*" | tee -a "$summary"; }

# knock_room TOKEN NAME MARKED...: a room like a guest meeting room (room version 12, knock,
# history from joining, invite at 0 so any member admits a knock), with an
# app.kymessages.guest marker for each MARKED localpart, alice at 50 and invited.
knock_room() {
	local tok=$1 name=$2 init
	shift 2
	init=$(jq -nc '[{type: "m.room.join_rules", state_key: "", content: {join_rule: "knock"}},
		{type: "m.room.history_visibility", state_key: "", content: {history_visibility: "joined"}},
		{type: "m.room.guest_access", state_key: "", content: {guest_access: "forbidden"}}]
		+ [$ARGS.positional[] | {type: "app.kymessages.guest", state_key: ., content: {}}]' --args "$@")
	mx "$tok" POST createRoom "$(jq -nc --arg n "$name" --arg a "$alice_mx" --argjson i "$init" '{room_version: "12", preset: "private_chat", name: $n,
		initial_state: $i, invite: [$a], power_level_content_override: {users: {($a): 50}, invite: 0, kick: 50, state_default: 50,
		events: {"org.matrix.msc3401.call.member": 0, "m.room.power_levels": 100, "im.vector.modular.widgets": 100}}}')" >/dev/null
	mxr .room_id
}
