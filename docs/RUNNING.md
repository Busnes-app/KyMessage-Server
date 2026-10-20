# Running

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

## Container image

Compose runs the published image `ghcr.io/busnes-app/kymessage-server:stable`, the newest
release. Each release `vX.Y.Z` is also published as `:X.Y.Z` with an SBOM and a build
attestation, and only after CI passed on the tagged commit. `docker compose pull` moves to a
newer release. To stay on one release, pin it by digest in `KY_IMAGE` after verifying its
attestation; `docker-compose.yml` has the command.

Protect `.env` with mode 0600 and export `KY_ADMIN_PASSWORD` as above. The container
never runs as root: create `./data` and `./backups` as yourself and set `KY_UID` and `KY_GID`
in `.env` to your `id -u` and `id -g` (Compose refuses to start without them). It also drops
every capability and runs with a read-only root filesystem; only `./data`, `./backups` and a
`/tmp` tmpfs are writable. Then run `docker compose up -d`. The published port binds
`127.0.0.1` by default. Container names, binary and local image are KyMessages-specific; the
retained Go module path and token-sealing label are internal compatibility identifiers.

Upgrading from v0.4.2 or earlier: Compose now passes `KY_SCIM_ENABLED`, `KY_BACKUP_KEEP` and
`KY_BACKUP_DEPOSIT_INTERVAL` from `.env` only when set (unset keeps the defaults: SCIM on, 7,
24h). A `KY_SCIM_ENABLED` in `.env`, ignored before, now applies. On a console set up by
`apply-setup`, CLI `deposit` and `export-capsule` capsules from v0.4.2 or earlier lack its
installer credentials: seal a new one ([INSTALLER_SETUP.md](INSTALLER_SETUP.md)).

## Identity and recovery configuration

Deployment is still unverified. Prepare these settings for the eventual HTTPS
instance; the current operator-console preview is not a chat release (see `docs/superpowers/specs/2026-10-01-matrix-platform-design.md`):

| Setting | Purpose |
|---|---|
| `KY_APP_NAME` | Defaults to `KyMessages`; also the capsule service name, pinned at pairing |
| `KY_APP_URL` | Exact public origin used for OIDC, browser Origin checks |
| `KY_ADMIN_USERNAME` | Username of the local admin created on an empty database and `init-admin`'s default, default `admin`; 3-64 letters, digits, `.`, `_` or `-`. Set it before first start when a KyIdentity user is called `admin` (KyIdentity's own admin cannot be renamed): a KyIdentity user whose username a local account holds is refused, never merged into it. It has no effect on an existing database. After resolving a clash (renaming the local account or the KyIdentity user), press Resync Directory on the KyMessages system in KyIdentity; KyIdentity does not resend a refused new user on its own |
| `KY_ENV=production` | Requires a durable `KY_SESSION_SECRET` and an `https` `KY_APP_URL` (unless `KY_COOKIE_SECURE=false`) |
| `KY_COOKIE_SECURE` | Defaults to true for production or any `https` `KY_APP_URL`; also turns on HSTS for the `KY_APP_URL` host. With it on and `KY_COOKIE_DOMAIN` unset, auth cookies carry the `__Host-` prefix |
| `KY_SSO_ENABLED` | Default true; `false` turns KyIdentity sign-in off (its routes answer 404) |
| `KY_KYIDENTITY_ISSUER`, `KY_KYIDENTITY_CLIENT_ID`, `KY_KYIDENTITY_SECRET` | Suite KyIdentity OIDC client; register `https://<host>/api/sso/kyidentity/callback` as its redirect URI. The former `KY_KYSIGNON_*` names stop startup |
| `KY_MATRIX_RTC_HOST`, `KY_MATRIX_MEDIA_IP` | LiveKit HTTPS signalling origin and directly reachable media IPv4; required by `matrix-init`; set the host in `.env` too |
| `KY_MATRIX_TURN_HOST`, `KY_MATRIX_TURN_CERT_FILE`, `KY_MATRIX_TURN_KEY_FILE` | Optional complete TURN/TLS setup block for `matrix-init`; DNS-only hostname and trusted PEM files; enable the TURN overlay |
| `KY_MATRIX_MEDIA_BIND_IP` | Host interface for direct TCP 7881/UDP 7882, default `0.0.0.0`; does not change the advertised media IP |
| `KY_KYIDENTITY_HMAC_SECRET` | Signing secret KyIdentity shows once when you pair a `suite_webhook` system; set that system's callback URL to `https://<host>/api/sso/kyidentity/sync`; required with the Matrix stack |
| `KY_TRUSTED_PROXIES` | Only the reverse proxy's own addresses/CIDRs, not the whole container network |
| `KY_SCIM_TOKEN` | Stable provisioning credential when SCIM is used; no automatic SCIM-to-room mapping |
| `KY_BACKUP_DIR`, `KY_BACKUP_KEEP` | Optional local sealed copies; keep newest N (default 7) |
| `KY_BACKUP_MEDIA_FULL_KEEP` | Monthly full media archives kept, default 3; below 1 fails startup |
| `KY_BACKUP_DEPOSIT_INTERVAL` | Initial schedule, default `24h`; `0` disables, otherwise at least `15m`; admin UI overrides without restart |
| `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | Explicit LAN KyRecovery opt-in, off by default; HTTPS remains mandatory and loopback is refused |
| `KY_MATRIX_ELEMENT_RESTART_HINT` | Command Settings shows for restarting Element, default `docker compose restart element`; one line, at most 256 bytes |

Pin the suite public key manually or pair with KyRecovery, and compare its fingerprint
with the ceremony record. A pinned key needs at least one destination: local directory
or KyRecovery. One backup run seals once for both destinations and checks the remote
receipt digest. The backup screen shows destinations, schedule and the latest recorded attempt,
including scheduled/local-only failures and partial-success warnings. The last
successful remote receipt stays separate. Unpairing
keeps the key pin and local copies; separately revoke the product token at KyRecovery.
Never put custodian shares into the running server.

Only SQLite has a supported capsule backup/restore path. The backup is one server
capsule (accounts and settings, plus the Matrix stack when enabled). Restore invalidates stale grants. See the
[restore runbook](RESTORE.md) before relying on backups. If a prior test pairing
used the scaffold's `Busnes.app` service name, preserve that explicit `KY_APP_NAME`
for its existing token/capsules; changing the default does not change KyRecovery's pin.

## Container-local installer integration

KyQuickStart configures the console through `kymessages apply-setup`; protected inputs,
inspection, graceful activation and Matrix/recovery gates are in
[INSTALLER_SETUP.md](INSTALLER_SETUP.md).
