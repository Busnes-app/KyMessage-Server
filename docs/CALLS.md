# Calls

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

### Voice and video calls

Calling is enabled in Element by default for DMs and group rooms. Members start or join a
call themselves; their browsers ask for microphone/camera permission. All callers must be
active KyIdentity users who have joined that Matrix room. No Matrix identity server is needed.

Route `KY_MATRIX_RTC_HOST` over HTTPS/WebSocket to `http://livekit:7880`; never expose LiveKit's
`/twirp/` administration routes. The existing console route carries `/api/matrix/rtc/get_token`
and `/api/matrix/rtc/sfu/get`, so Element can request grants with Matrix OpenID tokens. The app
checks local identity, directory/MAS status and joined-room membership; it never trusts a
caller-supplied server URL. See [network setup](Reverse_Proxy_Networking.md).

Forward **TCP 7881 and UDP 7882**, unchanged, to the Docker host at `KY_MATRIX_MEDIA_IP`.
If those ports are taken, export `KY_MATRIX_MEDIA_TCP_PORT` and `KY_MATRIX_MEDIA_UDP_PORT`
(1024-65535; not 7880, 5349, 3478 or 30000-30100, which LiveKit uses itself) for `matrix-init`
and set the same two in `.env`: LiveKit advertises the ports matrix-init wrote, and Compose
publishes `.env`'s, the same number on both sides, so they must match. Forward those instead.
`KY_MATRIX_MEDIA_BIND_IP` (required, no default) is the host interface Compose publishes them
on: the host's LAN address for LAN-only use, or `0.0.0.0` to accept media on every interface.
Cloudflared handles HTTPS/WebSocket signalling; its HTTP tunnel cannot carry these media ports.

For networks limited to TLS 443, export `KY_MATRIX_TURN_HOST` (DNS-only hostname resolving to
that public media IP), `KY_MATRIX_TURN_CERT_FILE` and `KY_MATRIX_TURN_KEY_FILE`, and re-run
`matrix-init`. Use a browser-trusted certificate covering that hostname; the input key must
be 0600 or stricter. Append `docker-compose.turn.yml` to your existing overlay chain, then
recreate LiveKit. Forward TCP443 to host 443 and UDP 3478 and 30000–30100 unchanged, alongside the
direct ports. LiveKit listens internally on 5349 and advertises external 443. The generated
private TLS copies enter encrypted backups. TURN permits only the configured media IP;
deployment egress rules must also restrict ports when that IP serves other applications.

After ACME renewal, run `matrix-init` with the renewed certificate/key inputs, and restart
LiveKit only after it succeeds. The whole private LiveKit directory is mounted read-only,
so replacements become visible; LiveKit reads its certificate at startup. Renewal keeps
API keys and invalid TLS input leaves generated files untouched.

Upgrade an existing stack by exporting the two new setup values, running `matrix-init` as its
original owner, adding `KY_MATRIX_RTC_HOST` to `.env`, configuring the routes/media ports,
and running `docker compose up -d --force-recreate app synapse element livekit` (preserve your
Compose overlay chain). The new write-once LiveKit keys/config are included in backups.
Update a moved host's media IP and re-run `matrix-init` before restarting LiveKit.

### Chat without calls

Calls are on by default. To run chat only (no LiveKit, no call buttons, no media ports):

1. Export `KY_MATRIX_CALLS=off` for `matrix-init` (then `KY_MATRIX_RTC_HOST` and
   `KY_MATRIX_MEDIA_IP` are not needed and are ignored; TURN is refused) and run it. Synapse
   then advertises no call service and Element shows no call buttons; restart `synapse
   element`, or recreate them on a running stack.
2. In `.env`, set `KY_MATRIX_CALLS=off` and append `docker-compose.chat-only.yml` to
   `COMPOSE_FILE` after `docker-compose.matrix.yml`. `KY_MATRIX_RTC_HOST` and
   `KY_MATRIX_MEDIA_BIND_IP` may then be left out. Leave out `docker-compose.turn.yml` and
   `docker-compose.notes.yml` (the meeting-notes bot joins calls; Compose refuses it here).
3. `docker compose up -d --remove-orphans`, the whole stack, not named services. On a stack
   that had calls, this stops and removes LiveKit (the overlay scales it to zero), which ends
   calls in progress and unpublishes its media ports; then close the media port forwards.

KyMessages then serves no call discovery in `/.well-known/matrix/client`, its
`/api/matrix/rtc` routes answer 404, and Health lists LiveKit as "Not deployed", counted
neither up nor down. With calls off nothing evicts call participants, so while a LiveKit still
answers (say `KY_MATRIX_CALLS=off` without the overlay, or only some services recreated)
Health lists it down instead: remove it with `docker compose rm -sf livekit`. To turn calls back on, undo both steps and re-run `matrix-init` with the
call settings above. `make matrix-acceptance` covers calls on, the default, and ends by
switching that stack to chat only as above with a call in progress.

The call reconciler runs every 5 seconds and evicts users disabled/unassigned/deleted in the
local directory, locked/deactivated in MAS, or removed from the Matrix room. LiveKit refreshes
connected users' tokens: cached grants can reconnect between passes, and a stopped or failing
control plane cannot evict media. Ending a MAS browser session alone does not end its active
call. The server records no call and restores none; the optional [meeting-notes bot](MEETING-NOTES.md)
is a participant that someone invites, and it says so in the call. LiveKit routes media; the existing text encryption
acceptance does not prove call encryption. `make matrix-acceptance` includes a separate
real audio/video check; production networking still needs deployment verification.
