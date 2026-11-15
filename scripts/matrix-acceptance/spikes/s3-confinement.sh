# shellcheck shell=bash disable=SC2154,SC2034,SC2016
# Sourced by scripts/matrix-acceptance.sh: its variables and helpers are in scope.
# Spike S3 (docs/superpowers/specs/2026-10-18-kymessages-meeting-rooms-design.md): does the
# confinement module (internal/matrixinit/templates/ky_guests.py.tmpl, loaded by the shipped
# homeserver.yaml) hold a real guest account on the pinned Synapse to its meeting room?
# Sourced by scripts/matrix-acceptance.sh step guest-spikes; guests-lib.sh first.
expect "$(dc exec -T synapse sh -c 'echo "$PYTHONPATH"; ls /config/ky_guests.py')" "/config
/config/ky_guests.py" "Synapse runs with the module on its path, from its read-only config dir"
synapse_log=$(dc logs --no-color synapse 2>&1)
grep -qF 'Loaded module <ky_guests.GuestConfinement' <<<"$synapse_log" || { echo "  FAILED: Synapse did not load ky_guests.GuestConfinement" >&2; false; }
expect "$(grep -c Traceback <<<"$synapse_log" || true)" 0 "Synapse loaded the confinement module with no error"

console_tok=$(session "$(mas_id kymessages-console)" 'urn:matrix:client:api:*' 900)
alice_tok=$(session "$(mas_id alice.q_ky)" 'urn:matrix:client:api:*' 900)
bob_tok=$(session "$(mas_id bob)" 'urn:matrix:client:api:*' 900)
g1=guest-s3$(openssl rand -hex 4) g2=guest-s1$(openssl rand -hex 4)
g1_mx=@$g1:$KY_MATRIX_SERVER_NAME g2_mx=@$g2:$KY_MATRIX_SERVER_NAME
g1_tok=$(new_guest "$g1" 900)
new_guest "$g2" 900 >"$state/$g2.token"
echo "$g2" >"$state/spike-guest"
expect "$(mx "$g1_tok" GET account/whoami) $(mxr .user_id) $(mxr .device_id)" "200 - $g1_mx $(cat "$state/$g1.device")" \
	"a MAS personal session on an unlinked, passwordless guest- user works on Synapse, on its own device"
expect "$(mx "$g1_tok" PUT "profile/$(uri "$g1_mx")/displayname" '{"displayname":"Visitor (guest)"}')" "200 -" "the guest's profile name carries the suffix"
mx "$(cat "$state/$g2.token")" PUT "profile/$(uri "$g2_mx")/displayname" '{"displayname":"Second visitor (guest)"}' >/dev/null

# The meeting room marks g1 and g2; "other" marks only another guest; alice's own room marks g1,
# but alice is not the console.
meet=$(knock_room "$console_tok" 'kymatrix guest meeting' "$g1" "$g2")
other=$(knock_room "$console_tok" 'kymatrix other meeting' guest-someoneelse)
echo "$meet" >"$state/spike-room"
expect "$(mx "$alice_tok" POST "join/$(uri "$meet")" '{}') $(mx "$alice_tok" POST "join/$(uri "$other")" '{}')" "200 - 200 -" "alice joins both rooms"
mine=$(mx "$alice_tok" POST createRoom "$(jq -nc --arg g "$g1" '{room_version: "12", preset: "private_chat",
	initial_state: [{type: "m.room.join_rules", state_key: "", content: {join_rule: "knock"}}, {type: "app.kymessages.guest", state_key: $g, content: {}}]}')" >/dev/null; mxr .room_id)

expect "$(mx "$g1_tok" POST createRoom '{}')" "403 M_FORBIDDEN" "a guest cannot create a room"
expect "$(mx "$g1_tok" POST createRoom "$(jq -nc --arg b "$bob_mx" '{is_direct: true, invite: [$b], preset: "trusted_private_chat"}')")" "403 M_FORBIDDEN" "a guest cannot start a DM"
expect "$(mx "$g1_tok" POST "knock/$(uri "$other")" '{}')" "403 M_FORBIDDEN" "a guest cannot knock on another meeting's room"
expect "$(mx "$g1_tok" POST "knock/$(uri "$mine")" '{}')" "403 M_FORBIDDEN" "a marker alice sent does not count: only the console's"
expect "$(mx "$g1_tok" POST "join/$(uri "$meet")" '{}')" "403 M_FORBIDDEN" "a guest cannot join its meeting uninvited"
expect "$(mx "$g1_tok" POST "knock/$(uri "$meet")" '{}') $(latest "$g1_mx" "$meet")" "200 - knock" "a guest knocks on its own meeting"
expect "$(sql synapse "SELECT e.json::json->'content'->>'displayname' FROM room_memberships m JOIN event_json e USING (event_id) WHERE m.user_id = '$g1_mx' AND m.room_id = '$meet' AND m.membership = 'knock'")" \
	"Visitor (guest)" "the knock shows the suffixed name"
expect "$(mx "$alice_tok" POST createRoom "$(jq -nc --arg g "$g1_mx" '{is_direct: true, invite: [$g], preset: "trusted_private_chat"}')")" "403 M_FORBIDDEN" \
	"a member cannot start a DM with a guest"
expect "$(mx "$alice_tok" POST "rooms/$(uri "$other")/invite" "$(jq -nc --arg g "$g1_mx" '{user_id: $g}')")" "403 M_FORBIDDEN" \
	"a member cannot invite a guest into another meeting"
expect "$(mx "$alice_tok" POST "rooms/$(uri "$meet")/invite" "$(jq -nc --arg g "$g1_mx" '{user_id: $g}')") $(latest "$g1_mx" "$meet")" "200 - invite" \
	"a member admits the knock (an invite)"
expect "$(mx "$g1_tok" POST "join/$(uri "$meet")" '{}') $(latest "$g1_mx" "$meet")" "200 - join" "the admitted guest joins"
expect "$(mx "$g1_tok" PUT "rooms/$(uri "$meet")/send/m.room.message/s3-1" '{"msgtype":"m.text","body":"kymatrix guest spike"}')" "200 -" "the guest may talk in its meeting"
expect "$(mx "$g1_tok" POST "rooms/$(uri "$meet")/invite" "$(jq -nc --arg b "$bob_mx" '{user_id: $b}')")" "403 M_FORBIDDEN" "a guest cannot invite"
expect "$(mx "$g1_tok" POST "rooms/$(uri "$meet")/invite" '{"id_server":"id.invalid","id_access_token":"x","medium":"email","address":"x@kymatrix.test"}')" \
	"403 M_FORBIDDEN" "a guest cannot send a third-party invite"
expect "$(mx "$g1_tok" PUT "rooms/$(uri "$meet")/state/m.room.topic/" '{"topic":"x"}')" "403 M_FORBIDDEN" "a guest cannot set room state"
expect "$(mx "$g1_tok" PUT "rooms/$(uri "$meet")/state/m.room.member/$(uri "$g1_mx")" '{"membership":"join","displayname":"The boss"}')" \
	"403 M_FORBIDDEN" "a guest cannot drop the suffix from its membership"
mx "$g1_tok" PUT "profile/$(uri "$g1_mx")/displayname" '{"displayname":"The boss"}' >/dev/null
expect "$(sql synapse "SELECT e.json::json->'content'->>'displayname' FROM current_state_events c JOIN event_json e USING (event_id) WHERE c.room_id = '$meet' AND c.type = 'm.room.member' AND c.state_key = '$g1_mx'")" \
	"Visitor (guest)" "a profile rename does not reach the meeting room"
expect "$(mx "$g1_tok" PUT "directory/room/$(uri "#$g1:$KY_MATRIX_SERVER_NAME")" "$(jq -nc --arg r "$meet" '{room_id: $r}')")" "403 M_FORBIDDEN" "a guest cannot create a room alias"
expect "$(mx "$g1_tok" PUT "directory/list/room/$(uri "$meet")" '{"visibility":"public"}' | command cut -d' ' -f1)" 403 "a guest cannot publish a room"
expect "$(mx "$g1_tok" POST user_directory/search '{"search_term":"alice"}') $(mxr '.results | length')" "200 - 0" \
	"a guest's directory search finds nobody, not even alice in its room"
expect "$(mx "$g1_tok" POST user_directory/search '{"search_term":"bob"}') $(mxr '.results | length')" "200 - 0" "nor bob"
expect "$(mx "$alice_tok" POST user_directory/search '{"search_term":"bob"}' | command cut -d' ' -f1) $(mxr '.results | length > 0')" "200 true" "control: alice still finds bob"
up=$(hcurl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $g1_tok" -H 'Content-Type: text/plain' --data-binary 'x' \
	"https://matrix.kymatrix.test/_matrix/media/v3/upload?filename=x.txt")
[[ $up == 4* ]] || { echo "  FAILED: a guest's media upload got $up" >&2; false; }
ok "a guest cannot upload media ($up)"
expect "$(mx "$alice_tok" POST createRoom '{}' | command cut -d' ' -f1)" 200 "control: members still create rooms"
spike_note "S3 profile: a guest can still read any local user's profile by Matrix ID: $(mx "$g1_tok" GET "profile/$(uri "$bob_mx")") (limit_profile_requests_to_users_who_share_rooms is off)"

# The session ends at its expiry, and deactivating the account parts it from the room.
g3=guest-s3e$(openssl rand -hex 4)
g3_tok=$(new_guest "$g3" 30)
expect "$(mx "$g3_tok" GET account/whoami)" "200 -" "a 30-second guest session works"
t=$SECONDS
eventually 200 "401 M_UNKNOWN_TOKEN" "the guest session is refused after its expiry" mx "$g3_tok" GET account/whoami
spike_note "S3 expiry: a 30 s personal session was refused $((SECONDS - t + 0)) s after the check started (Synapse's token cache included)"
masapi POST "users/$(cat "$state/$g1.masid")/deactivate" '{"skip_erase": true}' >/dev/null
eventually 60 leave "deactivating the guest parts it from the meeting" latest "$g1_mx" "$meet"
eventually 60 "401 M_UNKNOWN_TOKEN" "the deactivated guest's token is refused" mx "$g1_tok" GET account/whoami
