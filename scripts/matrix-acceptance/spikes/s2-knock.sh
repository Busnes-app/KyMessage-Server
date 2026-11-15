# shellcheck shell=bash disable=SC2154,SC2034
# Sourced by scripts/matrix-acceptance.sh: its variables and helpers are in scope.
# Spike S2: in the pinned Element, with feature_ask_to_join on, does a member in an Element Call
# see a guest's knock and admit it without leaving the call? The guest is the one S1 signed in;
# alice is in the room from s3-confinement.sh and holds a call in it.
g=$(cat "$state/spike-guest") meet=$(cat "$state/spike-room")
GUEST_MXID=@$g:$KY_MATRIX_SERVER_NAME MEETING_ROOM=$meet e2e guestknock
expect "$(latest "@$g:$KY_MATRIX_SERVER_NAME" "$meet")" join "the guest admitted from inside the call is joined"
expect "$(sql synapse "SELECT sender FROM room_memberships WHERE user_id = '@$g:$KY_MATRIX_SERVER_NAME' AND room_id = '$meet' AND membership = 'invite'")" "$alice_mx" \
	"alice admitted it (the invite is hers)"
spike_note "S2 $(jq -c . "$state/guestknock.json")"
