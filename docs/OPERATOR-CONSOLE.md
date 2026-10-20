# Operator console

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

### Operator console

- **Overview** shows chat health, Matrix users, backups and (with Matrix) a KyIdentity sync
  card (ok, warning or failing), each failing on its own.
- **Users** lists Matrix users with their KyIdentity link. It ends one session or all of a
  person's sessions. There is no lock or unlock here: change access in KyIdentity.
- **Rooms** lists every room (name, members, encryption, public or invite-only, creator, state
  events, open or closed) with search and paging, and a room's members. It never shows
  messages. **Close** is final: members are removed, rejoin is blocked, history stays.
  **Delete permanently** purges the room's history after you type its name (or its ID when it
  has none); the room's media (attachments, avatars) stays in the media store. Both
  run as background jobs, need a sign-in from the last 10 minutes and are audited as
  `matrix.room_close` and `matrix.room_delete`.
- The console reaches Synapse's admin API as the service account `@kymessages-console`, which
  it creates in MAS on first use (a Rooms or Health load): no password, no KyIdentity link, not
  MAS admin. For each action it mints a 5-minute MAS session and revokes it afterwards. The
  offboarding sweep exempts exactly that unlinked account. A KyIdentity-linked account named
  `kymessages-console` is not exempt: the sweep judges it as a person and the console refuses
  it, so never give a KyIdentity user an email whose local part is `kymessages-console`. Locking the account in MAS cuts
  the console's room access.
- **Health** probes Synapse, MAS, Element, Postgres, the database and this app on each load and
  compares each running version with the Compose pin. It links components to their upstream
  source and shows the network check. Each load also proves Synapse admin access as the console
  account: it reads one room.
- **Audit** is the read-only log, filtered by kind (auth, backup, branding, matrix, scim).
- **Settings → Branding** sets the product name (1–64 characters; blank returns to
  `KY_APP_NAME`) and the logo (PNG only, at most 1 MiB and 1024×1024 pixels, re-encoded so
  no embedded text or metadata survives). Both show on the next page load of the console and
  its sign-in page, and the logo in Element too. Element shows a new name after it restarts
  (`docker compose restart element`, or the command in `KY_MATRIX_ELEMENT_RESTART_HINT`): it
  serves a copy of its config made when it starts, and Settings shows that command while
  Element serves the old name. The app changes only the
  `brand` key of `matrix/element/config.json`, in place, through one of the two read-write
  files it mounts under `./matrix`, and puts it back within a minute if `matrix-init` re-renders it. If it cannot
  write that file, the name is still saved and Settings shows what Element says and why.
  If a crash mid-write ever leaves that file truncated or invalid, Settings shows the error
  and a running Element is unaffected, but a restarted Element fails to load: re-run
  `./kymessages matrix-init`; the app re-applies the name within a minute, then restart
  Element.
  Changes need a sign-in from the last 10 minutes and are audited (`admin.brand_name`,
  `admin.brand_logo`). `KY_APP_NAME` stays the backup service name: capsules and
  KyRecovery pairing never see the console name.
- **Settings → KyIdentity sync** (with Matrix) shows the last directory webhook KyMessages
  accepted, deliveries refused since it started (and why: bad signature, stale clock,
  missing secret, bad headers, malformed body), and the last offboarding sweep, with what to
  fix. The
  Overview shows the same as a card. A change made while KyMessages was down waits in
  KyIdentity as an uncertain write; resume it there (Suite sync, the system's Deliveries).
- Ending a session needs a sign-in from the last 10 minutes; the console asks "Confirm it's you"
  and retries after you sign in again. Each ending is audited as `matrix.session_end`; the
  offboarding sweep's own endings show the actor `system`.
- Session IPs and devices are shown to admins only and are never logged or audited.

`make matrix-acceptance` proves encrypted
storage in Element, a closed server, offboarding and its sync status, room close and delete, branding reaching Element, and backup then restore of a lost host (needs
Docker, node and a KyIdentity checkout; see [AGENTS.md](../AGENTS.md)).
