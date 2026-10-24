# Assurance case

[Product overview](../README.md) · [Product definition](PRODUCT.md) · [Chat setup](CHAT-SETUP.md) · [Verifying releases](VERIFYING-RELEASES.md)

This is the argument that the KyMessages server meets its security requirements: what it
protects, from whom, where the trust boundaries are, and why the design and the code hold at each
one. The detailed contracts live in the AGENTS.md file of each package and in the topic guides;
this page ties them together and points at the evidence. Where a claim names code, the path is
relative to the repository root.

**Scope.** This repository is the operator console and the glue around a Matrix stack (Synapse,
MAS, Element, LiveKit) that runs from official upstream images. Chat end-to-end encryption is
Element's and Matrix's, not this code's: **End-to-end encrypted in Element (not independently
audited)**. Synapse does not enforce encryption, so a client that does not encrypt can post
plaintext into an encrypted room ([Chat platform options](CHAT-PLATFORM-OPTIONS.md), section 7).

## What KyMessages must protect

1. **Only the right people administer the deployment.** Console sign-in, second factors, sessions
   and paired devices cannot be guessed, replayed or used beyond their account's current rights.
2. **Directory decisions reach Matrix.** When KyIdentity deactivates or deletes a person, their
   Matrix account is locked or deactivated and their sessions are ended; forged or stale
   directory events change nothing.
3. **Call grants go only to verified Matrix users** for the one room they asked about.
4. **Secrets held for others stay secret** (the KyRecovery token, TOTP secrets) from anyone who
   copies the database without the key file, and never reach a log.
5. **The server cannot be turned against its network** through federation or recovery URLs.
6. **Backups restore a complete deployment** and are sealed before they leave the host.

## Threat model

| Attacker | Can | Wants |
|---|---|---|
| Anonymous internet client | Send any request through the TLS proxy | Take over the console, guess passwords, exhaust resources |
| Signed-in member | Use their own session | Reach admin functions |
| Hostile web page in an admin's browser | Make the browser send requests | Act through the admin's session |
| A Matrix user on this or another server | Ask for a call grant with an OpenID token | Join a room they are not in, or make the server fetch internal URLs |
| Forged directory traffic | Send webhook or SCIM requests | Create, elevate or keep alive accounts |
| Someone with a copy of the data volume or a backup | Read the database file | Recover credentials |

Out of scope: an attacker who runs code on the host or holds the data volume **and** the key file,
a malicious administrator, the upstream Matrix components themselves, and clients that do not
encrypt.

## Trust boundaries

1. **Browser → TLS proxy → KyMessages.** `auth.ClientIP` honours `X-Forwarded-For` only from peers
   in `KY_TRUSTED_PROXIES` and ignores `X-Real-IP`; rate limits and session binding use it
   (`internal/auth`).
2. **KyIdentity → KyMessages.** OIDC sign-in with PKCE `S256` and verified ID tokens
   (`internal/sso/oauth.go`); the suite webhook verified by `ky-primitives/syncauth` (signed
   event headers, five-minute window) before any change (`internal/sso/kyidentity.go`); SCIM with a
   bearer token, acting only on SCIM-provisioned rows (`internal/scim`).
3. **KyMessages → MAS and Synapse.** Admin API clients that follow no redirects
   (`internal/matrixsync/mas.go`, `internal/synapseadmin`); the offboarding sweep locks, ends
   sessions or deactivates, and fails closed for an ambiguous or missing link.
4. **Matrix clients and other homeservers → call grants.** A grant needs a Matrix OpenID token
   that the user's homeserver confirms; other servers are reached only through a client that
   refuses blocked addresses after DNS resolution (`internal/api/federation_calls.go`). Grants
   last 15 seconds and name one room (`internal/matrixrtc`).
5. **KyMessages → KyRecovery.** Capsules are sealed with `ky-primitives/recoveryclient`; a private
   recovery URL is refused unless `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` is set
   (`internal/api/backup_handlers.go`).
6. **KyMessages → its data volume.** The KyRecovery token and TOTP secrets are sealed with
   AES-256-GCM under the deployment key before they reach the database.

## Secure design principles applied

- **Least privilege.** The app never runs as root (image `USER 10001`; Compose runs it as
  `KY_UID:KY_GID` with `cap_drop: [ALL]`, `no-new-privileges` and a read-only root), and
  `scripts/check-compose-proxy.sh` and `check-compose-matrix.sh` fail CI if that is lost. Backups
  read Postgres as the read-only `kybackup` role.
- **Fail-safe defaults.** Compose binds to loopback by default; media ports bind only
  `KY_MATRIX_MEDIA_BIND_IP`, which has no default. Unlinked or ambiguous Matrix users are locked.
  Privileged routes are registered wrapped in `s.requireAdmin` in one place.
- **Complete mediation.** Session authentication rejects inactive accounts on every request;
  managed sessions recheck issuer, subject, user and role.
- **Economy of mechanism.** OIDC and OAuth come from `coreos/go-oidc` and `golang.org/x/oauth2`,
  SCIM parsing from `elimity-com/scim`, passwords, webhook signatures and recovery from
  `ky-primitives`; the server's own crypto is one AES-256-GCM helper (`internal/crypto`).
- **Separation of privilege.** Local accounts can add TOTP; a password installed by `init-admin`
  or an operator reset must be replaced before privileged use.
- **Open design.** Only published algorithms: AES-256-GCM, SHA-256, HMAC, Argon2id through
  `ky-primitives/password`, OIDC with PKCE, Matrix and MSC4195 call identifiers.
- **Psychological acceptability.** The encryption label says exactly what is and is not audited;
  `init-admin` reads the password from stdin, never argv.

## Common weaknesses and how they are countered

| Weakness | Counter | Where |
|---|---|---|
| Broken authentication, credential stuffing | Passwords of at least 12 characters, hashed by `ky-primitives/password`; 10 failures per client and 100 per account per 15 minutes; proof-of-work challenges, each spent once | `internal/auth/policy.go`, `internal/api/auth_handlers.go`, `internal/auth` |
| Session theft from storage | Only the SHA-256 of the session token is stored; cookies are `HttpOnly`, `SameSite=Lax`, and `__Host-` prefixed when secure and host-only | `internal/auth/session.go` |
| Device pairing takeover | 24-byte random QR secret, 90-second lifetime, sent only in request bodies, consumed atomically | `internal/devices/devices.go` |
| CSRF | Double-submit token compared in constant time on every state-changing request that carries a session cookie, except sign-in, MFA and the signed directory webhook; `SameSite=Lax` session cookie | `internal/auth/session.go` (`ValidateCSRF`), `internal/api/server.go` (`csrfExempt`) |
| Cross-origin data theft | Credentialed CORS only for the exact app origin; call-grant paths get `*` without credentials and a 16 KiB body cap; preflight from another origin refused | `internal/api/server.go` |
| Broken access control | Admin routes wrapped in `requireAdmin` and pinned by `TestPrivilegedEndpointsRequireAdmin`; unknown `/api/` paths are a JSON 404, never a false 200 (`TestUnknownAPIPathIsNotAcknowledged`) | `internal/api/server.go`, `internal/api/authz_test.go`, `internal/api/directory_webhook_test.go` |
| Cross-site scripting | CSP `script-src 'self'` and `frame-ancestors 'none'`, `X-Frame-Options: DENY`; no `dangerouslySetInnerHTML` in `web/src`; logos must be PNG and are re-encoded so no ancillary chunk survives (SVG refused) | `internal/api/server.go`, `internal/branding/branding.go` |
| SQL injection | `database/sql` with `?` parameters; the audit filter builds only placeholder clauses | `internal/store/sqlstore.go` |
| Forged directory events | `syncauth` signature and five-minute window before parsing; a payload cannot merge into another account (`username_conflict`) | `internal/sso/kyidentity.go` |
| Server-side request forgery | Federation lookups dial only public addresses after DNS resolution, https only; Matrix admin clients follow no redirects; the recovery URL is validated | `internal/api/federation_calls.go`, `internal/matrixsync/mas.go`, `internal/synapseadmin`, `internal/api/backup_handlers.go` |
| Offboarding gaps | A lock alone leaves MAS sessions alive, so every locked person also gets every session finished | `internal/matrixsync` |
| Vulnerable dependencies | `govulncheck` and `npm audit --audit-level=high` in CI; Dependabot; OSV ignore list with reasons; dependency releases ship daily | `.github/workflows/ci.yml`, `osv-scanner.toml` |
| Supply-chain tampering | Actions and base images pinned by digest; release images carry build provenance verified before `:stable` moves; the server binary rebuilds to the same bytes | [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md) |

## Residual risk

- **Chat encryption is Element's, unaudited and not enforced by Synapse.** See the scope note above.
- **The CSRF token is not bound to the session.** It is a random value compared with its own
  cookie. With `__Host-` cookies (HTTPS, no `KY_COOKIE_DOMAIN`) a sibling subdomain cannot plant
  it; when `KY_COOKIE_DOMAIN` is set, a page on another subdomain of that domain could, and
  `SameSite=Lax` does not stop same-site requests.
- **TOTP secrets are sealed without associated data**, so a sealed value copied to another user's
  row would still open.
- **Rate limits are process-local** (single-instance release target), and the per-client limiter
  evicts at its cap, which can shorten other clients' windows (`internal/api/server.go`).
- **Call encryption and production networking** must be verified on each deployment
  ([Calls](CALLS.md)).
- **No independent security audit** has been done.

## Evidence

- **Tests:** the security tests named above and the whole suite under the race detector, on SQLite
  and on PostgreSQL 17; Vitest; Chromium and Firefox regressions against the built server; the
  Matrix acceptance job (encrypted storage, closed server); backup acceptance and the smoke test,
  on every push and pull request.
- **Static analysis:** `go vet`, `govulncheck`, strict TypeScript (`tsc`), `shellcheck` on the
  scripts.
- **Dynamic analysis:** the race detector, the browser regressions and the Matrix acceptance run.
- **Releases:** attested images and a repeatable server binary ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

This case is updated when a trust boundary or a security requirement changes. A pull request that
moves one must update this page in the same change.
