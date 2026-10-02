# KyMessages on Matrix — platform design

Date: 2026-10-01. Status: approved direction; each sub-project gets its own spec and plan.
Evidence: `docs/CHAT-PLATFORM-OPTIONS.md` (protocol comparison, Element's withdrawn Teams
bridge, and the Matrix + KyIdentity spike).

## Intent

KyMessages becomes a Ky-integrated Matrix deployment for small business teams: a self-hosted,
invite-only chat that signs in only through KyIdentity, encrypts by default, backs up to
KyRecovery and is run from the Ky admin console. Bridges to other networks are the reason to
build on Matrix. Teams interop is business-critical but no maintained Teams bridge exists, so
it is a separate later project (a self-built Teams bot), not part of this design.

## Decisions

1. **Shape — Ky control plane around stock Matrix.** Synapse, Matrix Authentication Service
   (MAS), PostgreSQL and Element Web run as unmodified upstream containers in KyMessages'
   Compose. The KyMessages Go server is the control plane: console, KyIdentity integration
   glue, offboarding sync, KyRecovery backups, health. No fork.
   **AGPL rule (owner-approved 2026-10-01):** Synapse, MAS and Element Web are used as
   unmodified official upstream images, pulled by KyMessages' Compose file (never
   repackaged into a Ky image), configured only through their config files and HTTP APIs.
   No code patches and no HTML/template overrides; branding is config-only. The console
   links to the exact upstream source release of each component. KyMessages' own code
   stays MIT. (Understanding, not legal advice.)
2. **Homeserver — Synapse + MAS + PostgreSQL.** The spike-validated stack. "SQLite, one
   instance" stops being the chat target; KyMessages' own console settings may stay in SQLite.
   Lighter homeservers (Tuwunel) may be trialled later.
3. **Retire the custom messaging stack** in its own phase: `chat-core/`, `mls-proof/`,
   `/api/messaging/*` and its tables, the messages capsule, `restore-messages`, suspended
   devices, resume, the 30-day expiry and the MLS research docs. Git history keeps them;
   MLS-in-Matrix (MSC2883) stays a watched future option.
4. **Sign-in — KyIdentity only, through MAS.** Local passwords and open registration off;
   only users assigned to the app in KyIdentity get in. MAS's compatibility (legacy) login is
   disabled: only native OIDC clients (current Element Web, Element X).
5. **Encryption — on by default.** New rooms are encrypted and cannot be made unencrypted
   where policy requires it. The label is exactly "End-to-end encrypted in Element (not
   independently audited)". **Finding (2026-10-01, resolved):** the spike's plaintext
   `m.room.message` needs a sender that does not encrypt; Element, through compat or native
   sign-in, stores only `m.room.encrypted`. Synapse stores what a client sends and does not
   enforce `m.room.encryption`, so a non-encrypting client or script can post plaintext into an
   encrypted room, and the server does not stop it. Docs say so plainly
   (`docs/CHAT-PLATFORM-OPTIONS.md` section 7).
6. **Offboarding — back-channel logout, webhook plus sweep.** KyIdentity's back-channel
   logout ends the user's MAS sessions within seconds; its signed directory webhook
   (`/api/sso/kyidentity/sync`) then locks (disable) or deactivates (delete) the user through
   the MAS admin API; reactivation unlocks. A sweep repairs failed MAS calls only; a webhook missed during a KyMessages outage is resumed by an operator in KyIdentity. Every action is
   audited. Detail: `2026-10-01-matrix-offboarding-design.md`.
   (Spike: KyIdentity disable alone leaves live sessions; Synapse deactivate is undone by MAS.)
7. **Backups — one server capsule.** Synapse and MAS database dumps (MAS first, seconds
   apart; Postgres snapshots do not span databases), plus MAS secrets (`secrets.encryption`,
   keys), the Synapse signing key and config secrets, through the existing KyRecovery pairing,
   schedule, local copies and drills. Media goes to the local backup directory as an encrypted
   incremental mirror plus monthly archives, outside the capsule. Detail:
   `2026-10-01-matrix-backups-design.md`. Users restore
   history with their own security keys; the server never sees plaintext. The people/messages
   capsule split is retired.
8. **Usernames.** Matrix localpart = KyIdentity username lowercased with characters Matrix
   disallows replaced by `_`; display name keeps the original; a collision refuses the second
   sign-in with a clear message. MAS links accounts by KyIdentity `sub`, so renames do not
   orphan accounts. Planning checks whether MAS templates can do the mapping.
9. **Domains — one subdomain per part.** e.g. `matrix.` (Synapse), `auth.` (MAS), `chat.`
   (Element Web), `admin.` (KyMessages console); user IDs stay `@alice:example.com` through
   `.well-known` delegation served by KyMessages. Each is a cloudflared hostname on
   `kymessages-net`; the per-app network model from `docs/Reverse_Proxy_Networking.md` holds.
10. **Clients — Element, branded by config.** Element Web gets the KyMessages name, logo and
    Busnes light/dark themes through `config.json`; Element X (mobile) stays Element-branded.
    Any Matrix client may connect.
11. **Console v1.** Users (from MAS: list, KyIdentity link, sessions, lock/unlock, end
    sessions); rooms (Synapse admin API: list, member count, encryption, shut down/delete);
    server health (Synapse, MAS, Element, PostgreSQL up, versions, network self-check);
    backups (server capsule, KyRecovery, schedule, drill); settings (branding pushed to
    Element config, KyIdentity status). Admin mutations need a fresh sign-in and are audited.
    Split into 5a (users, health, audit), 5b (rooms) and 5c (settings); 5a is specified in
    `2026-10-02-matrix-console-5a-design.md`, and its console has no lock/unlock (access is KyIdentity's).
12. **Review process** stays the suite's: security-audit run, PR security reviewer, task and
    branch reviews. Labels never claim an independent audit.

## Sub-projects (each: spec → plan → build, in order)

1. **Remove the custom messaging stack** (decision 3). "My account" becomes an account page.
2. **Matrix stack and sign-in** (1, 2, 4, 5, 8, 9, 10): Compose services, generated configs,
   `.well-known`, KyIdentity → MAS, encryption defaults, compatibility login off, the
   encryption gate, cloudflared hostname guide.
3. **Offboarding sync** (6).
4. **Server capsule backups** (7).
5. **Console** (11).

Later and separate: Teams bot; other bridges (mautrix, admin-enabled and labelled as
decrypting); a lighter homeserver trial.

## Risks carried

- The unexplained plaintext message (gate in sub-project 2).
- MFA enforcement relies on KyIdentity per-app policy; MAS ignores `acr` (untested).
- Teams: no bridge exists; partner tenants likely need their admin's consent (unverified).
- Operational weight rises (PostgreSQL + several services) versus the old single binary.
