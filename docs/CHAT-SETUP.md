# Chat setup

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

## Matrix chat

Chat is Matrix: Synapse, Matrix Authentication Service (MAS) and Element Web, with KyIdentity
as the only sign-in. Voice and video calls are enabled by default through stock Element Call
and LiveKit. HTTP services stay private behind the proxy; LiveKit publishes TCP 7881 and
UDP 7882 for direct media (routes in
[docs/Reverse_Proxy_Networking.md](Reverse_Proxy_Networking.md)). Registration is closed
and MAS's compatibility (password) login is not served. Federation is closed by default; an
operator can open it to named servers (see "Federation" below).

Encryption label: **End-to-end encrypted in Element (not independently audited)**. In the
acceptance run, every message Element sent to an encrypted room (direct and group) was stored
encrypted and its text appeared nowhere in the database, but Synapse does not enforce
encryption: a client or script that does not encrypt can post plaintext into an encrypted room,
and the server does not stop it. Members should use Element. Even with encryption, the server
sees room names and topics, membership, timestamps, display names and other metadata in
plaintext. Evidence: `docs/CHAT-PLATFORM-OPTIONS.md` section 7.

Setup, with the proxy overlay already working. `matrix-init` runs twice: KyIdentity shows the
client secret once, when you register the client the first run describes.

1. Export these for `matrix-init` (it reads the process environment, not `.env`). Hosts are
   https origins without a path: `KY_MATRIX_SERVER_NAME` (for example `example.com`),
   `KY_MATRIX_HOST`, `KY_MATRIX_AUTH_HOST`, `KY_MATRIX_CHAT_HOST`, `KY_ADMIN_HOST`,
   `KY_KYIDENTITY_ISSUER`, `KY_MATRIX_MAS_CLIENT_ID` (a name you choose; not secret), `KY_MATRIX_RTC_HOST` (the HTTPS LiveKit
   signalling origin, for example `https://sfu.example.com`) and `KY_MATRIX_MEDIA_IP` (the IPv4
   address clients can reach directly: public IP for remote callers, LAN IP for LAN-only use).
2. Run `./kymessages matrix-init` as the unprivileged user that will own the files
   (`-dir` defaults to `./matrix`; it refuses root, and an existing directory that group or
   others can read). It writes every config except MAS's, prints the KyIdentity registration
   values and `KY_MATRIX_UID`/`KY_MATRIX_GID`. Without a source build, run it from the image
   the stack runs (swap in your `KY_IMAGE` pin if you set one), in the directory holding the
   Compose files; the image's entrypoint is `/app/kymessages`, and each `-e NAME` passes the
   value exported in step 1:

   ```sh
   docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/w" -w /w \
     -e KY_MATRIX_SERVER_NAME -e KY_MATRIX_HOST -e KY_MATRIX_AUTH_HOST -e KY_MATRIX_CHAT_HOST \
     -e KY_MATRIX_RTC_HOST -e KY_MATRIX_MEDIA_IP -e KY_ADMIN_HOST -e KY_KYIDENTITY_ISSUER \
     -e KY_MATRIX_MAS_CLIENT_ID ghcr.io/busnes-app/kymessage-server:stable matrix-init
   ```

   Use the same command wherever this guide says to run or re-run `./kymessages matrix-init`.
   For TURN, mount the certificate and key too and pass `KY_MATRIX_TURN_*` as container paths.
   Back up `matrix/secrets` and
   `matrix/synapse/signing.key`; they are never regenerated. Upgrading a stack set up before
   `matrix/federation/federation.yaml` existed: re-run `matrix-init` first, or Compose refuses
   to start Synapse (it never creates that file), then `docker compose up -d` with your overlay
   chain. `restart` is not enough: it keeps Synapse's old command and the app's old mounts. The
   file is created closed; Settings owns it after.
3. In KyIdentity, register the printed confidential client (client ID, redirect URI, back-channel
   logout URI, scopes `openid profile email`) and assign the users who may chat. Unassigned
   users cannot sign in. A user's Matrix ID comes from their KyIdentity email once KyIdentity
   has verified it (the user redeemed an activation or reset link it mailed; configure
   KyIdentity's mail delivery, as a link handed over by hand does not verify): its local part,
   lowercased, with characters outside `[a-z0-9._=-]` replaced by `_`
   (`Alice.Q+Ky@example.com` becomes `@alice.q_ky`). A user without a verified email, or whose local
   part another chat user already has (`alice@` on two domains), cannot sign in. The ID is
   fixed at first sign-in. The back-channel logout URI is what ends a disabled user's open
   Element sessions; without it they keep working until their token expires.
4. Save the client secret KyIdentity shows to `matrix/secrets/kyidentity_client_secret` with
   mode 0600 (for example `install -m 600 /dev/null matrix/secrets/kyidentity_client_secret`,
   then paste it in with an editor). It lives only in that file, never in env or `.env`;
   `matrix-init` refuses it if group or others can read it.
5. Run `./kymessages matrix-init` again. It keeps every secret and now writes
   `matrix/mas/config.yaml`. Until then, `docker compose up` refuses MAS and the app with
   "bind source path does not exist". Whenever you re-run it on a running stack, apply the configs
   with `docker compose restart synapse mas element livekit app` (the app mounts each secret file, so
   it sees a replaced `kyidentity_client_secret` only after a restart). To rotate the Synapse or
   MAS database password, delete `matrix/secrets/synapse_db_password` or `mas_db_password`, re-run
   it and do what it prints: apply `postgres/role-passwords.sql` with `psql` on the running
   Postgres (its init script ran only on the empty volume), then restart `synapse mas app`.
6. Pair a `suite_webhook` system in KyIdentity (callback `https://<host>/api/sso/kyidentity/sync`)
   and link it to the MAS client's app. KyIdentity shows its signing secret once: that is
   `KY_KYIDENTITY_HMAC_SECRET`. On a console set up by `apply-setup` it must equal the setup's
   directory secret file ([INSTALLER_SETUP.md](INSTALLER_SETUP.md)). Its directory events
   are what lock and deactivate users in MAS.
   If MAS and KyMessages resolve only to private addresses (LAN-only), set
   `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true` on KyIdentity so it may call them.
7. Add to `.env`: `KY_MATRIX_UID` and `KY_MATRIX_GID` as printed (the app then runs as that
   user too, so it must own `./data` and `./backups`; keep `KY_UID`/`KY_GID` equal), plus `KY_MATRIX_SERVER_NAME`,
   `KY_MATRIX_HOST`, `KY_MATRIX_CHAT_HOST` (KyMessages serves discovery and the chat link
   from them), `KY_MATRIX_ADMIN_CLIENT_ID` (printed by `matrix-init`) and
   `KY_KYIDENTITY_HMAC_SECRET` and `KY_MATRIX_RTC_HOST`. With Matrix enabled, KyMessages refuses to start with missing
   admin settings (client ID, readable non-empty secret file) or no webhook secret. A wrong
   admin secret still starts, but every offboarding sweep then fails; the failure is logged
   once per failure streak (`[MATRIX] offboarding sweep failing`), so check the logs after
   start (console health comes later). Postgres, Synapse and MAS run as that user, the owner
   of `./matrix`, so its 0600 secrets stay unreadable to every other account; Compose
   refuses to start without these. If the UID or GID ever changes, `chown -R` `./matrix` and
   the `matrix-postgres` and `matrix-media` volumes (prefixed with the Compose project name)
   to the new owner.
8. Append `docker-compose.matrix.yml` to `COMPOSE_FILE`, after the proxy overlay, keeping the
   rest of the chain, then `docker compose up -d`.
9. In KyIdentity, open the system's deliveries, resume any held one, resync the system (how:
   "After any KyMessages outage" below) and confirm every assigned user shows delivered
   before members rely on chat. The sweep locks a
   MAS user KyMessages has no record of; it unlocks when that user's delivery lands.

Upgrading from the Matrix setup before offboarding:

1. Re-run `./kymessages matrix-init`. It keeps every secret, adds the MAS admin client and
   its secret file, and prints `KY_MATRIX_ADMIN_CLIENT_ID` and the back-channel logout URI.
2. Add that back-channel logout URI to the existing KyIdentity client.
3. Pair and link the `suite_webhook` system (step 6) if you have not, and set
   `KY_MATRIX_ADMIN_CLIENT_ID` and `KY_KYIDENTITY_HMAC_SECRET` in `.env`.
4. `docker compose up -d`, then `docker compose restart synapse mas element` so they read
   the new configs.
5. Do step 9: confirm every assignee shows delivered before relying on chat.

After start:

- Add the cloudflared routes, then open the chat host. Signed-in members see an "Open chat"
  link in KyMessages.
- `curl https://<server name>/.well-known/matrix/client` must show your `KY_MATRIX_HOST`.

Offboarding, as measured by `make matrix-acceptance` (cut within 30 s, in practice 0.3-3 s):

- **Disable or unassign** in KyIdentity: open Element sessions end and sign-in is refused;
  KyMessages locks the MAS user (sessions are gone either way, history stays).
- **Re-enable**: MAS unlocks within seconds. Sign in again on a new device and restore keys from
  key backup with the security key to read earlier history; a device whose session ended
  stays ended.
- **Delete**: MAS deactivates the user without erasing; they leave their rooms and colleagues
  still read their messages. A deactivated user is never reactivated.
- Offboard in KyIdentity, not MAS. A lock applied by hand in MAS to someone active in
  KyIdentity is undone by the next sweep.
- MAS's admin API listens on `mas-admin:8081` on an internal network only KyMessages and MAS
  join; it is never routed.
- **After any KyMessages outage**, open the system in KyIdentity and check its deliveries. A
  disable sent while KyMessages was down is held as an uncertain write ("operator recovery
  required") and is not retried: resume it (allowed after 60 s), then resync the system. In
  KyIdentity's Suite sync page: the system's Deliveries, tick the confirmation once the old
  request has finished, Resume delivery (KyIdentity asks you to confirm it's you), then the
  system's Resync Directory.
  Until then that user has no sessions and cannot sign in, but stays unlocked in MAS. Other
  failed deliveries KyIdentity retries itself.

Backups:

- The scheduled server capsule includes Matrix: configs, secrets, signing key and `pg_dump`s of
  Synapse and MAS (one-time keys excluded). It leaves out the Postgres superuser password;
  restore recreates it with `matrix-init`.
- What this costs: the app joins `matrix-db` and reads `./matrix` to back it up. The configs
  there hold the `synapse` and `mas` database owner passwords and the MAS-Synapse shared
  secret, so a compromised app can write both databases and act as Synapse admin. It cannot
  decrypt end-to-end encrypted messages. Compose hides the superuser password from it.
- Media is mirrored, encrypted, to `KY_BACKUP_DIR/media` after each scheduled run or
  `kymessages deposit`, with a full archive each month. Copy that directory off the host.
  `deposit` exits non-zero if the media step fails after a good deposit; `backup-drill` exits
  non-zero when any check fails.
- The backup screen shows the capsule size, a warning from 75% of the 256 MiB limit and the
  last media run. "Run now" seals the capsule only.
- Restore: see [docs/RESTORE.md](RESTORE.md).

Upgrading a stack from before Matrix backups:

1. Re-run `./kymessages matrix-init` (adds `kybackup_db_password` and `postgres/kybackup-role.sql`).
2. Run the role file once:
   `docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql`.
3. `docker compose pull app` (or rebuild with the build overlay), keep `KY_BACKUP_DIR` set, and optionally set
   `KY_BACKUP_MEDIA_FULL_KEEP`.
4. `docker compose up -d`. The first scheduled run then backs up Matrix.
