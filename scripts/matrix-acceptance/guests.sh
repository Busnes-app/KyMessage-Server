# shellcheck shell=bash disable=SC2154,SC2034
# Sourced by scripts/matrix-acceptance.sh step guests (after console: the admin is signed in as
# $admin_pass). Meeting guests on the shipped path, docs/KYCALENDAR.md "Guests": off refuses;
# on, a guest outside a window gets nothing; inside it they knock from their link, alice admits
# them from inside her call, they join it and both decode media; out of the call they are
# removed; their account cannot create a room, invite, search the directory or message anyone
# outside the room; a revoked link stops working; at the window end they are removed and
# deactivated; turning guests off refuses guests and ends live sessions; closing the meeting
# takes the links.
# shellcheck source=/dev/null
. "$here/spikes/guests-lib.sh"

# put_meeting ID EVENT BODY: signed as KyCalendar; prints the status, reply in $state/meeting.reply.
put_meeting() {
	local method=${4:-PUT} type=kycalendar.meeting.put ts sig
	[[ $method == DELETE ]] && type=kycalendar.meeting.close
	ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	sig=$(printf 'ky-sync-v1\n%s\n%s\n%s\n%s' "$ts" "$type" "$2" "$(printf '%s' "$3" | sha256sum | command cut -d' ' -f1)" |
		openssl dgst -sha256 -hmac "$(cat "$scratch/kycalendar/secret")" | awk '{print $NF}')
	hcurl -sS -o "$state/meeting.reply" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' -H "X-KySignOn-Signature: v1=$sig" \
		-H "X-KySignOn-Timestamp: $ts" -H "X-KySignOn-Event-Type: $type" -H "X-KySignOn-Event-ID: $2" --data-binary "$3" "$KY_APP_URL/api/meetings/$1"
}
utc() { date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ; }
guest_body() { # guest_body TITLE START END KEY...
	jq -nc --arg s "${kid[AQuinn]}" --arg i "$KY_KYIDENTITY_ISSUER" --arg t "$1" --arg a "$2" --arg b "$3" --arg bob "${kid[bob]}" \
		'{sub: $s, issuer: $i, title: $t, member_subs: [$bob], windows: [{start: $a, end: $b}], guests: [$ARGS.positional[] | {key: .}]}' --args "${@:4}"
}
guest_link() { jq -r --arg k "$1" '.guest_urls[$k] | sub("^https://admin\\.kymatrix\\.test/meet#"; "")' "$state/meeting.reply"; }
meet() { hcurl -sS -o "$state/meet.reply" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "$2" "$KY_APP_URL/api/meet/$1"; }
meet_status() { echo "$(meet status "$(jq -nc --arg t "$1" '{token: $t}')") $(jq -r '.state // .code' "$state/meet.reply")"; }
# pow: one solved challenge, as the guest's browser makes it.
pow() {
	hcurl -fsS "$KY_APP_URL/api/auth/pow-challenge" | python3 -I -c '
import base64, hashlib, json, sys
c = json.load(sys.stdin)
n = next(i for i in range(1, c["maxnumber"] + 1) if hashlib.sha256((c["salt"] + str(i)).encode()).hexdigest() == c["challenge"])
print(base64.b64encode(json.dumps({**{k: c[k] for k in ("algorithm", "salt", "challenge", "maxnumber", "expires_at", "signature")}, "number": n}).encode()).decode())'
}
join() { meet join "$(jq -nc --arg t "$1" --arg n "$2" --arg c "$(pow)" '{token: $t, name: $n, captcha: $c}')"; }
guest_audit() { app_api GET '/api/admin/audit?kind=matrix&limit=100' | jq -r --arg a "$1" '[.records[] | select(.action == $a)] | length'; }
# Accounts the console made: guest- and 20 hex digits (the spikes' accounts are named otherwise).
seats() { sql mas "SELECT coalesce(string_agg(username, ' '), '') FROM users WHERE username ~ '^guest-[0-9a-f]{20}$' AND deactivated_at IS NULL"; }
mas_guests() { seats | wc -w; }

mid=kymatrix-guests-$(openssl rand -hex 8) later=kymatrix-guests-later-$(openssl rand -hex 4)
k1=guestkey_$(openssl rand -hex 8) k2=guestkey_$(openssl rand -hex 8) k3=guestkey_$(openssl rand -hex 8)
now_start=$(utc '10 minutes ago') now_end=$(utc '50 minutes')

expect "$(put_meeting "$mid" "$mid-1" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k1")") $(jq -r .code "$state/meeting.reply")" \
	"422 guests_off" "guests are off by default: a PUT naming guests is refused"
app_login "$admin_pass"
expect "$(app_api GET /api/admin/meeting-guests | jq -c .)" '{"enabled":false,"live":0}' "the console reports guests off"
app_api POST /api/admin/meeting-guests/enable >/dev/null
expect "$(app_api GET /api/admin/meeting-guests | jq -r .enabled)" true "the administrator turned guests on"

code=$(put_meeting "$mid" "$mid-2" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k1" "$k2")")
expect "$code $(jq -r '.guest_urls | length' "$state/meeting.reply")" "200 2" "a PUT with two guest keys answers two links"
room=$(jq -r '.member_url | capture("#/room/(?<r>.+)$").r' "$state/meeting.reply")
l1=$(guest_link "$k1") l2=$(guest_link "$k2")
[[ ${#l1} == 43 && ${#l2} == 43 && $l1 != "$l2" ]] || { echo "  FAILED: guest links $l1 $l2" >&2; false; }
rule() { sql synapse "SELECT e.json::json->'content'->>'$2' FROM current_state_events c JOIN event_json e USING (event_id) WHERE c.room_id = '$room' AND c.type = '$1'"; }
expect "$(rule m.room.join_rules join_rule) $(rule m.room.power_levels invite)" "knock 0" "the meeting room is a waiting room any member may admit from"
expect "$(put_meeting "$mid" "$mid-3" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k2" "$k1")") $(jq -r '.guest_urls | length' "$state/meeting.reply")" \
	"200 0" "the same keys again issue no new links"

# Outside every window: nothing, and no account.
code=$(put_meeting "$later" "$later-1" "$(guest_body 'kymatrix later' "$(utc '2 hours')" "$(utc '3 hours')" "$k3")")
l3=$(guest_link "$k3")
expect "$code $(meet_status "$l3")" "200 200 waiting" "a link outside its window shows when it opens"
before=$(mas_guests)
expect "$(join "$l3" 'Early visitor') $(jq -r .code "$state/meet.reply")" "409 not_open" "joining outside the window is refused"
expect "$(mas_guests)" "$before" "and makes no account"
expect "$(meet_status "$(openssl rand -base64 32 | tr '+/' '-_' | command cut -c1-43)")" "404 not_found" "a guessed link is not found"

# Inside the window: the guest knocks, alice admits from her call, both decode media.
expect "$(join "$l1" 'Visitor')" 200 "the guest joins from its link"
next=$(jq -r .next "$state/meet.reply")
[[ $next == https://chat.kymatrix.test/kymessages-guest.html#* ]] || { echo "  FAILED: next $next" >&2; false; }
expect "$(hcurl -fsS https://chat.kymatrix.test/kymessages-guest.html | grep -c "script-src 'sha256-")" 1 "Element serves the bootstrap page with its script pinned by hash"
GUEST_NEXT=$next MEETING_ROOM=$room e2e guestmeeting
guest=$(jq -r .guest "$state/guestmeeting.json")
expect "$(sql synapse "SELECT e.json::json->'content'->>'displayname' FROM current_state_events c JOIN event_json e USING (event_id) WHERE c.room_id = '$room' AND c.type = 'm.room.member' AND c.state_key = '$guest'")" \
	"Visitor (guest)" "members see the guest's name with the suffix"
expect "$(sql synapse "SELECT m.sender FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '$guest' AND m.room_id = '$room' AND m.membership = 'invite' ORDER BY e.stream_ordering DESC LIMIT 1")" \
	"$alice_mx" "alice admitted the guest"
gtok=$(cat "$state/guest.token")
eventually 60 leave "out of the call, the guest is removed from the room and must knock again" latest "$guest" "$room"
expect "$(sql synapse "SELECT m.sender FROM room_memberships m JOIN events e USING (event_id) WHERE m.user_id = '$guest' AND m.room_id = '$room' AND m.membership = 'leave' ORDER BY e.stream_ordering DESC LIMIT 1")" \
	"@kymessages-console:$KY_MATRIX_SERVER_NAME" "the console removed it"
grant() { # grant ROOM: the call grant route's answer for the guest
	local openid
	openid=$(hcurl -fsS -X POST -H "Authorization: Bearer $gtok" -H 'Content-Type: application/json' -d '{}' \
		"https://matrix.kymatrix.test/_matrix/client/v3/user/$(uri "$guest")/openid/request_token" | jq -re .access_token)
	status POST "$KY_APP_URL/api/matrix/rtc/get_token" -H 'Content-Type: application/json' -d "$(jq -nc --arg r "$1" --arg u "$guest" --arg t "$openid" --arg s "$KY_MATRIX_SERVER_NAME" \
		'{room_id: $r, slot_id: "m.call#ROOM", openid_token: {access_token: $t, matrix_server_name: $s}, member: {id: "m1", claimed_user_id: $u, claimed_device_id: "D1"}}')"
}
expect "$(grant "$room")" 403 "a guest out of the room gets no call grant"
group=$(jq -r .rooms.group "$state/prove.json")
expect "$(grant "$group")" 403 "nor in any other room"

# Confinement of the real guest account.
expect "$(mx "$gtok" POST createRoom '{}')" "403 M_FORBIDDEN" "the guest cannot create a room"
expect "$(mx "$gtok" POST createRoom "$(jq -nc --arg b "$bob_mx" '{is_direct: true, invite: [$b], preset: "trusted_private_chat"}')")" "403 M_FORBIDDEN" "nor message anyone outside the meeting"
expect "$(mx "$gtok" POST user_directory/search '{"search_term":"bob"}') $(mxr '.results | length')" "200 - 0" "nor find anyone in the directory"
expect "$(mx "$gtok" POST "knock/$(uri "$group")" '{}')" "403 M_FORBIDDEN" "nor knock on another room"
expect "$(mx "$gtok" POST "knock/$(uri "$room")" '{}') $(latest "$guest" "$room")" "200 - knock" "it may knock on its meeting again"
alice_tok=$(session "$(mas_id alice.q_ky)" 'urn:matrix:client:api:*' 900)
expect "$(mx "$alice_tok" POST "rooms/$(uri "$room")/invite" "$(jq -nc --arg g "$guest" '{user_id: $g}')")" "200 -" "alice admits it again"
expect "$(mx "$gtok" POST "join/$(uri "$room")" '{}')" "200 -" "and it joins"
expect "$(mx "$gtok" POST "rooms/$(uri "$room")/invite" "$(jq -nc --arg b "$bob_mx" '{user_id: $b}')")" "403 M_FORBIDDEN" "the guest cannot invite"

# A revoked link stops working at once.
expect "$(put_meeting "$mid" "$mid-4" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k1")") $(meet_status "$l2")" \
	"200 404 not_found" "dropping a key revokes its link"

# At the window end the guest is removed and deactivated: a window ending in 40 seconds.
short_end=$(utc '40 seconds')
expect "$(put_meeting "$mid" "$mid-5" "$(guest_body 'kymatrix guests' "$now_start" "$short_end" "$k1")")" 200 "the window now ends in 40 seconds"
expect "$(join "$l1" 'Visitor')" 200 "the guest joins again (its earlier account is ended)"
eventually 30 "401 M_UNKNOWN_TOKEN" "the earlier account's token is refused" mx "$gtok" GET account/whoami
seat=$(seats)
expect "$(wc -w <<<"$seat")" 1 "one live guest account per link"
eventually 120 "1" "at the window end the account is deactivated" sql mas "SELECT count(*) FROM users WHERE username = '$seat' AND deactivated_at IS NOT NULL"
expect "$(meet_status "$l1")" "200 ended" "after its window the link shows the meeting ended"

# Guests off ends live sessions and refuses guests.
expect "$(put_meeting "$mid" "$mid-6" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k1")")" 200 "a new window"
expect "$(join "$l1" 'Visitor')" 200 "the guest joins once more"
live=$(seats)
app_api POST /api/admin/meeting-guests/disable >/dev/null
expect "$(sql mas "SELECT count(*) FROM users WHERE username = '$live' AND deactivated_at IS NOT NULL")" 1 "turning guests off ends the live guest at once"
expect "$(meet_status "$l1") $(put_meeting "$mid" "$mid-7" "$(guest_body 'kymatrix guests' "$now_start" "$now_end" "$k1")") $(jq -r .code "$state/meeting.reply")" \
	"404 not_found 422 guests_off" "and refuses guests"

# Closing the meeting takes its links (on again, so only the close refuses).
app_api POST /api/admin/meeting-guests/enable >/dev/null
expect "$(put_meeting "$mid" "$mid-close" '{}' DELETE) $(meet_status "$l1") $(put_meeting "$later" "$later-close" '{}' DELETE) $(meet_status "$l3")" \
	"200 404 not_found 200 404 not_found" "closing a meeting revokes its links"
app_api POST /api/admin/meeting-guests/disable >/dev/null

for a in guest.knock guest.admit guest.leave guest.expire guests.enable guests.disable; do
	n=$(guest_audit "$a")
	((n > 0)) || { echo "  FAILED: no $a audit row" >&2; false; }
	ok "audited $a ($n)"
done
audit=$(app_api GET '/api/admin/audit?kind=matrix&limit=100' | jq -c '[.records[] | select(.action | startswith("guest"))]')
for secret in "$l1" "$l2" "$k1" "$k2" Visitor 'kymatrix guests'; do
	if grep -qF "$secret" <<<"$audit"; then echo "  FAILED: a guest audit row holds '$secret'" >&2; false; fi
done
ok "guest audit rows hold IDs only: no link, key, name or title"
