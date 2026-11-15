# shellcheck shell=bash disable=SC2154,SC2034
# Sourced by scripts/matrix-acceptance.sh: its variables and helpers are in scope.
# Spike S1: can stock Element Web (the pinned v1.12.30) take a session KyMessages minted through
# MAS, with no password and no upstream provider? A page on Element's origin (here Playwright, as
# the bootstrap page would) writes the session where Element restores sessions from and opens the
# room. Sourced by step guest-spikes after s3-confinement.sh, which made the guest and the room.
# S2 needs Element's knock UI, a labs flag: the scratch config gets it here, in place.
jq '.features = ((.features // {}) + {feature_ask_to_join: true})' "$element_cfg" >"$state/element.json"
cat "$state/element.json" >"$element_cfg"
dc restart element >/dev/null
eventually 90 true "Element serves feature_ask_to_join" sh -c "hcurl -fsS https://chat.kymatrix.test/config.json | jq -r '.features.feature_ask_to_join'"
g=$(cat "$state/spike-guest")
GUEST_MXID=@$g:$KY_MATRIX_SERVER_NAME GUEST_DEVICE=$(cat "$state/$g.device") GUEST_TOKEN_FILE=$state/$g.token MEETING_ROOM=$(cat "$state/spike-room") \
	e2e guestsignin
spike_note "S1 $(jq -c . "$state/guestsignin.json")"
