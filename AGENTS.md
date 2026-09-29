# KyMessages repository

KyMessages builds on the inherited server base. Its source-built operator console
and API use the KyMessages identity; the encrypted-chat client stays isolated until
its release gates pass. The user-selected priority is small teams and encrypted text chat.

- `kymessages` is the binary and local image name; `KY_APP_NAME` defaults to
  `KyMessages`. `internal/config.AppVersion` is shared by CLI and capsule paths.
  Preserve explicit legacy service names for existing pairing pins/capsules.
- Compose is a loopback local preview by default, names `kymessages:local` and never
  pulls an upstream base image. Use the build overlay while images are unpublished;
  preserve existing overlay chains. The release target is SQLite, one instance.
- `make clean` removes generated artifacts only; never runtime data or backups.

- Continue the encrypted-chat first-release plan in `docs/FIRST-RELEASE-PLAN.md`
  through implementation and verification; commit each completed slice and before
  every unavoidable break. Report unmet release gates explicitly.

- Read [docs/PRODUCT.md](docs/PRODUCT.md) before messaging implementation or product
  scope changes; it records proposed defaults and acceptance gates, not shipped behavior.
- Read [docs/KYMESSAGES-PROTOCOL-RESEARCH.md](docs/KYMESSAGES-PROTOCOL-RESEARCH.md)
  before selecting MLS/media libraries or making federation compatibility claims.
- The isolated browser experiment lives in `mls-proof/`; selection evidence is in
  [docs/MLS-LIBRARY-RESEARCH.md](docs/MLS-LIBRARY-RESEARCH.md). Before changing library
  compatibility claims, read [docs/MLS-INTEROP-RESEARCH.md](docs/MLS-INTEROP-RESEARCH.md)
  for the failed extensibility gate and constrained OpenMLS exchange evidence. Its test results do not
  establish production approval or complete milestone 0. Keep it out of deployment.
- Root owns product definition and cross-domain documentation in `docs/`; children
  own the runtime domains indexed below. Keep product plans distinct from current
  scaffold capabilities and verify claims against code before publishing them.
- Before changing device, room or delivery behavior, read
  [docs/MESSAGING-API.md](docs/MESSAGING-API.md). The experimental opaque HTTP log
  coordinates declared epochs and roster changes. The isolated proof binds MLS
  credentials and validates transcripts over this API. Bounded KeyPackage
  publication/claims and an isolated clickable chat prototype are implemented;
  the isolated prototype exercises suite OIDC cookies and authenticated account
  binding. Production client integration remains open. The entry is
  `mls-proof/chat.html`; its OIDC mode uses a disposable local issuer with the real
  suite callback, never production accounts.

# DOX framework

- DOX is highly performant AGENTS.md hierarchy installed here
- Agent must follow DOX instructions across any edits

## Core Contract

- AGENTS.md files are binding work contracts for their subtrees
- Work products, source materials, instructions, records, assets, and durable docs must stay understandable from the nearest applicable AGENTS.md plus every parent AGENTS.md above it

## Read Before Editing

1. Read the root AGENTS.md
2. Identify every file or folder you expect to touch
3. Walk from the repository root to each target path
4. Read every AGENTS.md found along each route
5. If a parent AGENTS.md lists a child AGENTS.md whose scope contains the path, read that child and continue from there
6. Use the nearest AGENTS.md as the local contract and parent docs for repo-wide rules
7. If docs conflict, the closer doc controls local work details, but no child doc may weaken DOX

Do not rely on memory. Re-read the applicable DOX chain in the current session before editing.

## Update After Editing

Every meaningful change requires a DOX pass before the task is done.

Update the closest owning AGENTS.md when a change affects:

- purpose, scope, ownership, or responsibilities
- durable structure, contracts, workflows, or operating rules
- required inputs, outputs, permissions, constraints, side effects, or artifacts
- user preferences about behavior, communication, process, organization, or quality
- AGENTS.md creation, deletion, move, rename, or index contents

Update parent docs when parent-level structure, ownership, workflow, or child index changes. Update child docs when parent changes alter local rules. Remove stale or contradictory text immediately. Small edits that do not change behavior or contracts may leave docs unchanged, but the DOX pass still must happen.

## Hierarchy

- Root AGENTS.md is the DOX rail: project-wide instructions, global preferences, durable workflow rules, and the top-level Child DOX Index
- Child AGENTS.md files own domain-specific instructions and their own Child DOX Index
- Each parent explains what its direct children cover and what stays owned by the parent
- The closer a doc is to the work, the more specific and practical it must be

## Child Doc Shape

- Create a child AGENTS.md when a folder becomes a durable boundary with its own purpose, rules, responsibilities, workflow, materials, or quality standards
- Work Guidance must reflect the current standards of the project or user instructions; if there are no specific standards or instructions yet, leave it empty
- Verification must reflect an existing check; if no verification framework exists yet, leave it empty and update it when one exists

Default section order:
- Purpose
- Ownership
- Local Contracts
- Work Guidance
- Verification
- Child DOX Index

## Style

- Keep docs concise, current, and operational
- Document stable contracts, not diary entries
- Put broad rules in parent docs and concrete details in child docs
- Prefer direct bullets with explicit names
- Do not duplicate rules across many files unless each scope needs a local version
- Delete stale notes instead of explaining history
- Trim obvious statements, repeated rules, misplaced detail, and warnings for risks that no longer exist

## Closeout

1. Re-check changed paths against the DOX chain
2. Update nearest owning docs and any affected parents or children
3. Refresh every affected Child DOX Index
4. Remove stale or contradictory text
5. Run existing verification when relevant
6. Report any docs intentionally left unchanged and why

## User Preferences
- Treat agent review of its own implementation as self-review. Separate reproduced
  defects, regression evidence and reviewer checks from independent cryptographic
  assessment; never use self-review to close that external release gate.
- Bootstrap passwords and passwords installed by `init-admin` must be replaced before privileged use. Operator resets atomically revoke sessions, MFA challenges and device pairings. Untouched existing accounts are not retroactively flagged.

When the user requests a durable behavior change, record it here or in the relevant child AGENTS.md

- Container network IP configuration belongs to Compose: the optional
  `docker-compose.static-ip.yml` overlay requires `KY_CONTAINER_IP` and `KY_NETWORK_SUBNET`.
  Preserve existing overlays when updating `COMPOSE_FILE`; the base keeps automatic addressing.

## Verification

CI (`.github/workflows/ci.yml`) runs on every push and pull request:
- `make lint` equivalent: gofmt, `go vet`, `go mod tidy`/`verify`
- `go test -race` with coverage on SQLite, and the same suite against PostgreSQL 17
- Frontend vitest suite, then typecheck/build plus a check that committed `web/dist` matches source (it is embedded in the binary)
- `govulncheck` and `npm audit --audit-level=high`
- `scripts/backup-acceptance.py` starts its own loopback process and disposable
  SQLite/local-copy directories. It exercises real scheduler ticks (~3 minutes),
  live schedule changes, local-destination failure/retry timing, pruning and
  shutdown. Time injection changes only its scratch database's last-attempt row.
  No live identity or recovery destination is contacted. CI's smoke job runs it.
- `scripts/smoke-test.sh`: runs the built binary and asserts CLI, auth, session, and SPA behavior
- Docker image build and container HTTP check
- Chromium regressions against the built server: production CSP/worker, themes, responsive layout and keyboard dialogs; these checks remain release gates.
- The isolated MLS browser proof runs its build, manual, HTTP/UI and OIDC suites
  on Chromium and Firefox in CI. It remains outside the deployment artifacts.
- Container builds use `npm ci` and exclude `mls-proof/`. CI builds/runs
  `kymessages:ci` but has no image publication/promotion jobs while release gates
  remain open. Keep the independent MLS review and deployed identity gates explicit.

Run the same checks locally with `make ci` (`tidy-check lint test-race test-web smoke`); add `make test-postgres` when a Postgres instance is available.

## Child DOX Index

- [mls-proof/AGENTS.md](mls-proof/AGENTS.md): Isolated MLS browser experiment, encrypted local persistence and lifecycle tests.
- [internal/config/AGENTS.md](internal/config/AGENTS.md): Configuration management and environment loader.
- [internal/store/AGENTS.md](internal/store/AGENTS.md): Pluggable database abstraction layer (SQLite & PostgreSQL).
- [internal/crypto/AGENTS.md](internal/crypto/AGENTS.md): Cryptographic primitives (AES-256-GCM, HMAC, SHA-256, randomness, PKCE).
- [internal/auth/AGENTS.md](internal/auth/AGENTS.md): Authentication, MFA (TOTP), recovery codes, sessions, and CAPTCHA.
- [internal/sso/AGENTS.md](internal/sso/AGENTS.md): Single Sign-On federation (KySignOn, OIDC, SAML 2.0).
- [internal/scim/AGENTS.md](internal/scim/AGENTS.md): SCIM 2.0 user and group provisioning engine.
- [internal/backup/AGENTS.md](internal/backup/AGENTS.md): Product-side adapters over `ky-primitives/recoveryclient`: payload collection, drill checks, settings and sealer glue.
- [internal/devices/AGENTS.md](internal/devices/AGENTS.md): 90-second ephemeral QR device pairing and push registration.
- [internal/testdb/AGENTS.md](internal/testdb/AGENTS.md): Test-only isolated database provisioning (SQLite or PostgreSQL).
- [internal/api/AGENTS.md](internal/api/AGENTS.md): HTTP REST API endpoints, routing, and middleware.
- [web/AGENTS.md](web/AGENTS.md): React 19 + TypeScript + Vite PWA frontend and KySecurity design system.

`cmd/server` owns the scheduler: `backupLoop` builds the `RunConfig` and client once and
returns with `scheduler disabled: ...` if that fails, because a run that never stamps its
attempt would log and audit the same failure every minute forever. It closes its `done` channel
only where it returns, between runs, and `runServer` cancels and waits on that channel after
`httpServer.Shutdown` and before the store closes, then waits on `api.Server.WaitDetached()` for
WebSocket handlers and the pair, pin-key, unpair and deposit handlers, which detach from their requests and so outlive
`Shutdown`. `api.Server.StopMessaging()` runs before HTTP shutdown to reject new stream
registrations and cancel upgraded WebSockets; they share the detached-handler drain.
`messagingMaintenanceLoop` sweeps expired ciphertext and expired device pairings every
minute with a 30-second operation deadline; startup pruning lives in `store.Open`. Its completion joins the
backup scheduler's completion before the same shutdown drain finishes. Nothing writes
into a closed store. Both waits run under one `backupWaitTimeout`
context (17m, the lib's 15m deposit ceiling plus sealing) -- a context, not a timer channel,
which delivers once and would leave the second wait unbounded; the HTTP drain is `shutdownTimeout`
(5s). `docker-compose.yml` grants a `stop_grace_period` above their sum, so the guarantee holds
in the shipped deployment instead of assuming a supervisor grace period;
`TestComposeGracePeriodCoversTheShutdownBudget` keeps the three in step. Past the deadline the
work is abandoned with a log line rather than killed silently.

`cmd/server/restore.go` delegates custodian handling and extraction to recoveryclient,
requires a regular nonempty `data/ky_server.db` and a valid 32-byte deployment key,
then opens the offline SQLite snapshot (migration/startup pruning), invalidates
restored grants and closes it before reporting success. Keep the target offline on
failure. Restored messaging rooms are permanently retired; users recover identity
with fresh suite authentication and create new independently verified rooms.
Never restore or rewind browser MLS state. Root owns this policy and `docs/RESTORE.md`.

The KyRecovery wire contract is `kyrecovery-server/zero_code_pairing_handoff_spec.md` (v2.0.0, sealed-capsule deposit); the product half is `ky-primitives/recoveryclient`, wired through `internal/backup` and `internal/api` so every server built on this base inherits it. Operator documents: `README.md` covers the source-built local preview and configuration; `docs/RESTORE.md` covers the tested SQLite restore policy. Deployment and production encrypted-chat integration remain release gates.
