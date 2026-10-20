# Federation

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

### Federation

Closed by default. Settings > Federation has three levels:

| Level | Who can reach this server |
|---|---|
| Closed | nobody (as before; federation paths are 404) |
| Manual approval | servers you add by name |
| Curated list + approval | those, plus vetted public servers you tick (this release: matrix.org, mozilla.org, kde.org, gnome.org, fedora.im) |

There is no open federation level. Both servers must list each other.

1. One-time proxy change: route `https://<server name>/.well-known/matrix/server` to the app as
   well as the client file (cloudflared path `^/\.well-known/matrix/(client|server)$`,
   [docs/Reverse_Proxy_Networking.md](Reverse_Proxy_Networking.md)). Federation then
   reaches Synapse through the existing `KY_MATRIX_HOST` route on port 443; 8448 stays shut.
2. In Settings > Federation, choose a level. A wider level first lists what it exposes and
   needs a sign-in from the last 10 minutes; going back to Closed is one click.
3. Copy "Your server" and send it to the other operator; type their server name and press
   Add server. They do the same with yours.
4. Run the restart command the panel shows (`docker compose restart synapse`, or
   `KY_MATRIX_SYNAPSE_RESTART_HINT`): Synapse reads the list only at start. One restart covers
   several changes.
5. "Check connections" shows each server as Connected, Not allowed yet (one side has not listed
   the other, or Synapse has not restarted), Their federation is off, or Unreachable.

What changes when a server is allowed:
- Its servers see the metadata of rooms their users join (names, topics, membership,
  display names, timestamps). The chat label stays "End-to-end encrypted in Element (not
  independently audited)" with the caveat above (Synapse does not enforce encryption);
  encryption across servers is not covered by the acceptance run.
- Offboarding someone here does not remove what was already sent to other servers. Closing a
  room removes this server from it; other servers keep it. Deleting a room deletes only this
  server's copy.
- People on allowed servers can join calls in rooms they share with your users, through your
  LiveKit; only your users start calls. Removing a server or closing federation drops its
  callers within seconds, without waiting for the restart.
- Public servers: what members post in a room on a public server reaches every server in that
  room, allowed or not; Synapse's allow-list cannot prevent that. Presets suit direct messages
  and small rooms. Servers marked "Open sign-up" let strangers invite your members.

The app writes Synapse's `matrix/federation/federation.yaml` from its settings; edit federation
only in the console (hand edits are undone within a minute). Peers on private addresses (LAN
only) are refused, by Synapse's default and by the app.
