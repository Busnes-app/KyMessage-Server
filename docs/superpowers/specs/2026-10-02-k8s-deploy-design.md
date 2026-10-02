# KyMessages on the K80/K81 cluster

Date: 2026-10-02. Deploys the shipped Matrix stack (`docker-compose.matrix.yml`) to the Ky
cluster. The manifests and runbook live in `busnes.app/ky-kubernetes/` (not versioned); this spec is their
design record. That directory's AGENTS.md binds every step (dry-run, Secrets out of band,
private Cloudflare snapshots, Retain PVs on K81).

## Intent

Yoshi runs KyMessages for real on the cluster: one real user (Yoshi), backed up to
KyRecovery from day one, reachable at public HTTPS names, with Compose's isolation preserved.
More users are added later with no redeploy.

## Decisions (owner-approved 2026-10-02)

1. Matrix server name `urlxl.com` (user IDs `@yoshi:urlxl.com`; permanent).
2. Hostnames: `msg.urlxl.com` (Element), `msg-matrix.urlxl.com` (Synapse), `msg-auth.urlxl.com`
   (MAS), `msg-admin.urlxl.com` (KyMessages console and app).
3. The apex `urlxl.com` (today a proxied CNAME to the invalid target `github.com/yoshiofthewire`,
   answering 530) points at the K8 tunnel; only `/.well-known/matrix/client` reaches KyMessages;
   a Cloudflare redirect rule sends every other path to `https://github.com/yoshiofthewire`.
   MX records are untouched.
4. One real user, no throwaway account. Paired to KyRecovery (`kyrecovery.urlxl.us`) at once.

## Section 1: workloads, images, storage

- Namespace `ky-stack`, node K81, five Deployments at one replica, `strategy: Recreate`:
  `kymessages` (app), `postgres`, `synapse`, `mas`, `element`. Upstream images and digests are
  exactly the pins in `docker-compose.matrix.yml`.
- The app image is unpublished: build from master at a recorded commit, import into K81
  containerd as `kymessages:<git sha>`, `imagePullPolicy: Never`; keep the archive (KyPost
  precedent).
- ClusterIP Services with fixed IPs `10.96.80.18`–`.23`, named as the generated configs
  expect: `kymessages`, `synapse`, `mas`, `mas-admin` (MAS pod, port 8081), `element`,
  `postgres`.
- Local Retain PVs under `/var/lib/ky-stack/` on K81: `kymessages` 4 GiB (app data),
  `kymessages-matrix` 1 GiB (`matrix-init` output), `kymessages-postgres` 8 GiB,
  `kymessages-media` 16 GiB, `kymessages-backups` 24 GiB (local copies). Node-bound, not
  redundant; KyRecovery is the off-node copy.
- Mounts mirror the Compose overlay: the app mounts `kymessages-matrix` read-only piece by piece
  (`subPath`), never `secrets/postgres_password`, with one read-write `element/config.json`.
  Synapse, MAS and Postgres run as `KY_MATRIX_UID` 10020; all containers drop every
  capability except what the Compose file grants (the app keeps the default it needs to write
  Element's config), no service-account token, read-only root filesystems where the image
  allows. The app's `terminationGracePeriodSeconds` is 1260 (Compose's 21-minute backup
  drain).
- `matrix-init` runs in-cluster as one-off Pods of the app image on the matrix PV as
  `KY_MATRIX_UID`. Pass 1 prints the KyIdentity registration values; the issued client secret
  goes in through stdin; pass 2 renders MAS's config. Secrets never leave the cluster.

## Section 2: isolation and routing

- Pods of the stack carry `ky.busnes.app/zone: matrix`. `allow-suite` in `foundation.yaml`
  excludes that label on both sides (the other seven apps keep exactly today's reach); a
  `kymessages-zone.yaml` allows only: cloudflared → app, Synapse, MAS 8080, Element; app →
  Postgres 5432, MAS 8080 and 8081, Synapse 8008, Element 8080; Synapse → Postgres, MAS 8080;
  MAS → Postgres, Synapse 8008 (user provisioning).
- Egress (`kymessages-egress.yaml`): app and MAS get public HTTPS (KyIdentity at
  `auth.urlxl.com`); the app also gets `192.168.1.91:443` (admin02 Nginx, KyRecovery).
  Synapse, Element and Postgres get none (federation off).
- `verify-network.sh` adds denials: another Ky app refused by Postgres and `mas-admin`;
  Synapse refused by MAS 8081.
- Tunnel `HLUSWCK8` routes, in order: `urlxl.com` path `^/\.well-known/matrix/client$` →
  `kymessages:8080`; `msg-matrix.urlxl.com` path `^/_synapse/(admin|mas)/` → 404;
  `msg-matrix.urlxl.com` → `synapse:8008`; `msg-auth.urlxl.com` → `mas:8080`; `msg.urlxl.com`
  → `element:8080`; `msg-admin.urlxl.com` → `kymessages:8080`. MAS 8081 is never routed.
- DNS: proxied CNAMEs to the tunnel for the four `msg-*` names and the apex. Redirect rule:
  host `urlxl.com` and path not starting `/.well-known/matrix/` → 301 to
  `https://github.com/yoshiofthewire`; needs the token's redirect-rule scope. Existing route,
  DNS and rule configs are saved privately before any change.
- KyMessages change: `KY_MATRIX_ELEMENT_RESTART_HINT` sets the Settings restart command
  (default `docker compose restart element`); the cluster sets
  `kubectl -n ky-stack rollout restart deployment/element`.

## Section 3: bootstrap, gate, rollback

- **Sequence** (server-side dry-run before each apply): merge the open PRs and the restart-hint
  change; build and import the image; create PV directories, apply storage, Services,
  `allow-suite` change, zone and egress policies; `verify-network.sh`; `matrix-init` pass 1;
  wizard; pass 2; start the Deployments; internal checks; routes and DNS; public checks.
- **Wizard** (interactive bash; secrets through hidden input straight into Secrets, never chat
  or the workspace). Operator steps: in KyIdentity, an OIDC client for MAS (pass 1's redirect
  and back-channel URIs), an OIDC client for the console, a `suite_webhook` system linked to
  MAS's app record (its secret becomes `KY_KYIDENTITY_HMAC_SECRET`), assign Yoshi; add the
  redirect-rule scope to the Cloudflare token; replace the bootstrap admin password before
  privileged use; in KyRecovery, generate a pairing code for service `KyMessages` (the claim pins `KY_APP_NAME`); claim it in
  the console (fresh sign-in) with `KY_BACKUP_ALLOW_PRIVATE_RECOVERY=1` (recorded on the
  pairing audit row); compare the pinned key's fingerprint with KyRecovery's ceremony page.
- **Gate** (all must pass):
  - Health: every component up on its pin.
  - `https://urlxl.com/.well-known/matrix/client` returns the client JSON; `https://urlxl.com/`
    redirects to GitHub; MX unchanged.
  - Yoshi signs in to Element at `msg.urlxl.com` through KyIdentity, sets up keys, creates an
    encrypted room and posts; Postgres (as `kybackup`) holds only `m.room.encrypted` there; a
    second device reads history.
  - Offboarding, reversibly: unassigning Yoshi from the app in KyIdentity refuses their live
    token within 30 s and MAS locks them; reassigning unlocks with history (KyIdentity refuses to
    disable its last administrator). Delete is proven by acceptance.
  - Sync status shows a webhook accepted and the sweep ok.
  - A rename appears in Element after `rollout restart deployment/element`.
  - A KyRecovery deposit succeeds with the digest matching; `backup-drill` passes; the local
    copy and media mirror exist; the daily schedule shows its next run.
  - Network denials hold; the other seven apps still pass `verify-network.sh` and their
    health paths.
- **Rollback:** scale the five Deployments to zero; restore the saved routes, DNS and rules
  (the apex returns to its current record); restore the saved `allow-suite`. PVs are Retain.
  A KyRecovery pairing is undone in two steps (console Unpair, KyRecovery revoke).

## Out of scope

Federation; more users (later, no redeploy); a second node or storage redundancy; publishing
the app image.

## Risks

- The server name `urlxl.com` is permanent; moving it later means new accounts.
- The apex now depends on the K8 tunnel and a redirect rule; the tunnel being down takes
  `urlxl.com` with it (today it already answers 530).
- The `allow-suite` change touches every app's policy; `verify-network.sh` and each app's
  health path are re-checked, and the old policy is kept for rollback.
- Single node: K81 loss stops chat until restore from KyRecovery.
