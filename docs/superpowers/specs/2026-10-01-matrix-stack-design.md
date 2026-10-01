# Sub-project 2: Matrix stack and KyIdentity sign-in

Date: 2026-10-01. Parent: `2026-10-01-matrix-platform-design.md` (decisions 1, 2, 4, 5, 8,
9, 10 and the AGPL rule). Evidence: `docs/CHAT-PLATFORM-OPTIONS.md` section 7 (spike).

## Intent

A KyMessages deployment can run a closed, encrypted-by-default Matrix chat for its team:
members sign in only through KyIdentity, use Element, and every message in an encrypted room
is stored encrypted — proven by a repeatable acceptance test, not assumed.

## Decisions (owner-approved 2026-10-01)

1. Configs come from a `kymessages matrix-init` command, not templates or manual editing.
2. The MAS client is registered in KyIdentity by hand, guided by `matrix-init`'s output.
   KyMessages holds no KyIdentity admin credential.
3. The spike's plaintext message is reproduced, explained, and then guarded by
   `scripts/matrix-acceptance.sh`; no E2EE label ships until it passes.
4. The acceptance test runs on every PR as its own CI job.

## Section 1: `kymessages matrix-init`

- **Inputs (env):** `KY_MATRIX_SERVER_NAME` (domain in user IDs), `KY_MATRIX_HOST`,
  `KY_MATRIX_AUTH_HOST`, `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`, `KY_KYIDENTITY_ISSUER`,
  `KY_MATRIX_MAS_CLIENT_ID`, `KY_MATRIX_MAS_CLIENT_SECRET`, output dir (default `./matrix`).
  Refuses missing values, non-https host URLs, an invalid server name.
- **Outputs:** Synapse `homeserver.yaml` + signing key, MAS `config.yaml`, Element
  `config.json`, a Postgres init script creating `synapse` and `mas` databases with separate
  users. Secrets (`crypto/rand`) written once at 0600 and never overwritten; re-runs reconcile
  only non-secret settings, so running twice is safe.
- **Prints** the KyIdentity registration values: redirect URI
  `https://<auth host>/upstream/callback/<provider id>`, scopes `openid profile email`,
  confidential client.
- **Synapse settings:** auth delegated to MAS; registration off; federation off;
  `encryption_enabled_by_default_for_room_type: all`; media in its own volume.
- **MAS settings:** KyIdentity sole upstream provider; local passwords and password
  registration off; localpart from `preferred_username` lowercased with Matrix-disallowed
  characters replaced by `_`, collisions refused (if MAS templates cannot express this, it is
  documented as an enforced rule and pinned by the acceptance test); compatibility login off
  or unrouted.
- **Element settings:** homeserver and auth on our hosts; branding by config only (KyMessages
  name, logo, Busnes light/dark themes). No code or template overrides (AGPL rule).

## Section 2: running and proving it

- **`docker-compose.matrix.yml`** overlay (added to `COMPOSE_FILE`, preserving existing
  overlays): `postgres`, `synapse`, `mas`, `element` as official upstream images pinned by tag
  and digest, never rebuilt. Each mounts only its own `matrix-init` files read-only. Separate
  volumes for Postgres data and Synapse media. All on `kymessages-net`, nothing published.
  Healthchecks; start order Postgres → MAS, Synapse → Element.
- **Routing:** cloudflared routes the four hostnames directly to `synapse:8008`, `mas:8080`,
  `element:80`, `kymessages:8080`. With the compatibility path off, no routing proxy.
- **`.well-known`:** KyMessages serves `/.well-known/matrix/client` so IDs stay
  `@alice:<server name>`. No `/.well-known/matrix/server` (federation off).
- **Member page:** "Chat isn't available yet." becomes an "Open chat" link to the chat host
  when the Matrix settings are configured.
- **`scripts/matrix-acceptance.sh`** (own CI job, every PR; throwaway KyIdentity; Playwright
  driving Element; tears down only its own Compose project):
  1. `matrix-init`, start the stack, register the MAS client in the throwaway KyIdentity,
     assign two test users.
  2. Reproduce: enable and route the compatibility login, show the plaintext message, and
     record the cause in `docs/CHAT-PLATFORM-OPTIONS.md` section 7.
  3. Prove: with shipped settings both users sign in by native OIDC, message in a DM and a
     group room; a database query finds no `m.room.message` events in encrypted rooms.
  4. Closed server: password login, registration and federation refused; an unassigned
     KyIdentity user refused.
- **Docs:** Matrix section in `docs/Reverse_Proxy_Networking.md` (four hostnames, cloudflared
  routes); README setup (`matrix-init`, KyIdentity registration, `COMPOSE_FILE`); root and
  `cmd/server` AGENTS.md.

## Out of scope

Offboarding sync (sub-project 3), server capsule backups (4), console pages including upstream
source links (5), bridges, Teams.

## Risks

- MAS username mapping may not support the exact transform; fallback is documented and tested.
- Image pulls make the CI job slower (~5–8 minutes, estimate).
- A real https deployment through cloudflared is still unverified (acceptance runs on loopback).
